package otpdelivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Delivery-only adapters against local TLS stand-ins for MSG91 and Twilio.
// Nothing here contacts either provider.

const (
	indiaNumber = "+919876543210"
	adapterCode = "135790"
)

// sid builds a Twilio-shaped identifier at runtime: GitHub push protection
// rejects such literals even as fake fixtures.
func sid(prefix string) string { return prefix + strings.Repeat("0a", 16) }

type capture struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	status   int
	answer   string
	hold     bool
}

func (c *capture) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body) //nolint:errcheck // recorded as-is
	c.mu.Lock()
	c.requests = append(c.requests, r)
	c.bodies = append(c.bodies, string(b))
	status, answer, hold := c.status, c.answer, c.hold
	c.mu.Unlock()
	if hold {
		<-r.Context().Done()
		return
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, answer) //nolint:errcheck // test server
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func newCapture(t *testing.T, status int, answer string) (*capture, *httptest.Server) {
	t.Helper()
	c := &capture{status: status, answer: answer}
	srv := httptest.NewTLSServer(http.HandlerFunc(c.handler))
	t.Cleanup(srv.Close)
	return c, srv
}

func secretFile(t *testing.T, v string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func msg91Config(t *testing.T, srv *httptest.Server) MSG91Config {
	t.Helper()
	return MSG91Config{
		Name: "msg91", AuthKeyFile: secretFile(t, "msg91-test-auth-key"),
		SignInFlowID: "signinflow01", DeletionFlowID: "deleteflow02", CodeVariable: "otp",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}
}

func twilioConfig(t *testing.T, srv *httptest.Server) TwilioConfig {
	t.Helper()
	return TwilioConfig{
		Name:                "twilio-sms",
		AccountSIDFile:      secretFile(t, sid("AC")),
		APIKeySIDFile:       secretFile(t, sid("SK")),
		APIKeySecretFile:    secretFile(t, "twilio-test-secret"),
		MessagingServiceSID: sid("MG"),
		SignInTemplate:      "Your Lily AI sign-in code is {code}. Do not share it.",
		DeletionTemplate:    "Your Lily AI account deletion code is {code}.",
		BaseURL:             srv.URL, HTTPClient: srv.Client(),
	}
}

func TestMSG91_SendsTheCodeThroughTheRegisteredFlow(t *testing.T) {
	c, srv := newCapture(t, http.StatusOK, `{"type":"success","message":"x"}`)
	p, err := NewMSG91(msg91Config(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SendOtp(context.Background(), indiaNumber, adapterCode); err != nil {
		t.Fatalf("SendOtp: %v", err)
	}
	r := c.requests[0]
	if r.Method != http.MethodPost || r.URL.Path != "/api/v5/flow" || r.Header.Get("authkey") != "msg91-test-auth-key" {
		t.Errorf("request %s %s authkey=%q", r.Method, r.URL.Path, r.Header.Get("authkey"))
	}
	var body struct {
		TemplateID string              `json:"template_id"`
		ShortURL   string              `json:"short_url"`
		Recipients []map[string]string `json:"recipients"`
	}
	if err := json.Unmarshal([]byte(c.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body.TemplateID != "signinflow01" || body.ShortURL != "0" || len(body.Recipients) != 1 ||
		body.Recipients[0]["mobiles"] != "919876543210" || body.Recipients[0]["otp"] != adapterCode {
		t.Errorf("body = %s", c.bodies[0])
	}
	d, ok := p.(DeletionCodeProvider)
	if !ok {
		t.Fatal("a configured deletion flow did not make a DeletionCodeProvider")
	}
	if err := d.SendDeletionCode(context.Background(), indiaNumber, adapterCode); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.bodies[1], `"deleteflow02"`) {
		t.Errorf("the deletion code used %s", c.bodies[1])
	}
}

func TestTwilio_SendsTheRegisteredTemplateThroughTheMessagingService(t *testing.T) {
	c, srv := newCapture(t, http.StatusCreated, `{"sid":"x"}`)
	p, err := NewTwilio(twilioConfig(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	if sendErr := p.SendOtp(context.Background(), indiaNumber, adapterCode); sendErr != nil {
		t.Fatalf("SendOtp: %v", sendErr)
	}
	r := c.requests[0]
	user, pass, ok := r.BasicAuth()
	if r.URL.Path != "/2010-04-01/Accounts/"+sid("AC")+"/Messages.json" || !ok || user != sid("SK") || pass != "twilio-test-secret" {
		t.Errorf("request %s auth ok=%v user=%q", r.URL.Path, ok, user)
	}
	form, err := url.ParseQuery(c.bodies[0])
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("To") != indiaNumber || form.Get("MessagingServiceSid") != sid("MG") ||
		form.Get("Body") != "Your Lily AI sign-in code is 135790. Do not share it." || form.Has("From") {
		t.Errorf("form = %v", form)
	}
	if err := p.(DeletionCodeProvider).SendDeletionCode(context.Background(), indiaNumber, adapterCode); err != nil { //nolint:forcetypeassert // configured with a deletion template
		t.Fatal(err)
	}
	form2, _ := url.ParseQuery(c.bodies[1]) //nolint:errcheck // checked through the field
	if form2.Get("Body") != "Your Lily AI account deletion code is 135790." {
		t.Errorf("deletion body = %q", form2.Get("Body"))
	}
}

func TestAdapters_OnlyDomesticIndianMobilesAndSixDigitCodes(t *testing.T) {
	mc, msrv := newCapture(t, http.StatusOK, `{"type":"success"}`)
	tc, tsrv := newCapture(t, http.StatusCreated, `{}`)
	m, err := NewMSG91(msg91Config(t, msrv))
	if err != nil {
		t.Fatal(err)
	}
	tw, err := NewTwilio(twilioConfig(t, tsrv))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Provider{m, tw} {
		for _, n := range []string{"+14155550100", "+447700900123", "+91987654321", "+9198765432100", "+915123456789", "+91987654321x", "919876543210"} {
			if err := p.SendOtp(context.Background(), n, adapterCode); !errors.Is(err, ErrNotDomestic) {
				t.Errorf("%s: %q = %v, want ErrNotDomestic", p.Name(), n, err)
			}
		}
		for _, code := range []string{"12345", "1234567", "١٣٥٧٩٠", "12345a", ""} {
			if err := p.SendOtp(context.Background(), indiaNumber, code); !errors.Is(err, ErrBadCode) {
				t.Errorf("%s: code %q = %v, want ErrBadCode", p.Name(), code, err)
			}
		}
	}
	if mc.count() != 0 || tc.count() != 0 {
		t.Errorf("refused messages reached a provider: msg91 %d, twilio %d", mc.count(), tc.count())
	}
}

func TestAdapters_ErrorsAreFixedAndNeverEchoTheMessage(t *testing.T) {
	echo := `{"type":"error","message":"invalid number +919876543210 code 135790"}`
	for _, tc := range []struct {
		name   string
		status int
		answer string
		want   error
	}{
		{"msg91 200 error", http.StatusOK, echo, ErrRejected},
		{"msg91 401", http.StatusUnauthorized, echo, ErrRejected},
		{"msg91 429", http.StatusTooManyRequests, echo, ErrProviderUnavailable},
		{"msg91 500", http.StatusInternalServerError, echo, ErrProviderUnavailable},
		{"twilio 400", http.StatusBadRequest, echo, ErrRejected},
		{"twilio 503", http.StatusServiceUnavailable, echo, ErrProviderUnavailable},
		{"twilio 200 is not 201", http.StatusOK, echo, ErrRejected},
	} {
		_, srv := newCapture(t, tc.status, tc.answer)
		var p Provider
		var err error
		if strings.HasPrefix(tc.name, "msg91") {
			p, err = NewMSG91(msg91Config(t, srv))
		} else {
			p, err = NewTwilio(twilioConfig(t, srv))
		}
		if err != nil {
			t.Fatal(err)
		}
		err = p.SendOtp(context.Background(), indiaNumber, adapterCode)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
		for _, secret := range []string{"9876543210", adapterCode, "msg91-test-auth-key", "twilio-test-secret", "invalid number"} {
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error contains %q", tc.name, secret)
			}
		}
	}
}

func TestAdapters_HonourTheCallersContext(t *testing.T) {
	c, srv := newCapture(t, http.StatusOK, `{"type":"success"}`)
	c.hold = true
	p, err := NewMSG91(msg91Config(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = p.SendOtp(ctx, indiaNumber, adapterCode)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Errorf("a held provider: %v after %v, want the context's deadline promptly", err, time.Since(start))
	}
}

func TestAdapters_RefuseRedirectsAndForeignHosts(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	cfg := msg91Config(t, redirector)
	p, err := NewMSG91(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SendOtp(context.Background(), indiaNumber, adapterCode); err == nil {
		t.Error("a redirect was followed or ignored")
	}
	if hits.Load() != 0 {
		t.Errorf("the redirect target received %d requests (and the credential)", hits.Load())
	}
	for _, base := range []string{"http://control.msg91.com", "https://control.msg91.com.evil.test", "https://u:p@control.msg91.com", "https://example.com"} {
		cfg.BaseURL = base
		if _, err := NewMSG91(cfg); !errors.Is(err, ErrProviderConfig) {
			t.Errorf("%q accepted", base)
		}
	}
	if b, err := endpoint("", TwilioAPIHost); err != nil || b != "https://api.twilio.com" {
		t.Errorf("default Twilio endpoint = %q, %v", b, err)
	}
}

func TestAdapters_ConfigurationIsCheckedAtConstruction(t *testing.T) {
	_, srv := newCapture(t, http.StatusOK, `{}`)
	for name, mutate := range map[string]func(*TwilioConfig){
		"no placeholder":    func(c *TwilioConfig) { c.SignInTemplate = "Your code." },
		"two placeholders":  func(c *TwilioConfig) { c.SignInTemplate = "{code} {code}" },
		"bad deletion":      func(c *TwilioConfig) { c.DeletionTemplate = "no code here" },
		"relative secret":   func(c *TwilioConfig) { c.APIKeySecretFile = "secret" },
		"empty secret":      func(c *TwilioConfig) { c.APIKeySecretFile = secretFile(t, "") },
		"wrong account sid": func(c *TwilioConfig) { c.AccountSIDFile = secretFile(t, "not-a-sid") },
		"wrong service sid": func(c *TwilioConfig) { c.MessagingServiceSID = "PN123" },
		"name not a label":  func(c *TwilioConfig) { c.Name = "Twilio SMS" },
	} {
		cfg := twilioConfig(t, srv)
		mutate(&cfg)
		if _, err := NewTwilio(cfg); !errors.Is(err, ErrProviderConfig) {
			t.Errorf("%s: %v, want ErrProviderConfig", name, err)
		}
	}
	for name, mutate := range map[string]func(*MSG91Config){
		"bad flow":     func(c *MSG91Config) { c.SignInFlowID = "../x" },
		"bad variable": func(c *MSG91Config) { c.CodeVariable = "o-t-p" },
		"missing key":  func(c *MSG91Config) { c.AuthKeyFile = "" },
	} {
		cfg := msg91Config(t, srv)
		mutate(&cfg)
		if _, err := NewMSG91(cfg); !errors.Is(err, ErrProviderConfig) {
			t.Errorf("%s: %v, want ErrProviderConfig", name, err)
		}
	}
	cfg := msg91Config(t, srv)
	cfg.DeletionFlowID = ""
	p, err := NewMSG91(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(DeletionCodeProvider); ok {
		t.Error("a provider without a deletion flow can be asked for deletion codes")
	}
}

func TestAdapters_FailOverInsideTheSender(t *testing.T) {
	down, dsrv := newCapture(t, http.StatusInternalServerError, `{}`)
	up, usrv := newCapture(t, http.StatusCreated, `{}`)
	m, err := NewMSG91(msg91Config(t, dsrv))
	if err != nil {
		t.Fatal(err)
	}
	tw, err := NewTwilio(twilioConfig(t, usrv))
	if err != nil {
		t.Fatal(err)
	}
	var outcomes []string
	var mu sync.Mutex
	s, err := New(Config{Providers: []Provider{m, tw}, AttemptTimeout: 2 * time.Second, Observe: func(p string, o Outcome) {
		mu.Lock()
		defer mu.Unlock()
		outcomes = append(outcomes, p+":"+string(o))
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendOtp(context.Background(), indiaNumber, adapterCode); err != nil {
		t.Fatalf("SendOtp: %v", err)
	}
	if down.count() != 1 || up.count() != 1 {
		t.Errorf("attempts: msg91 %d, twilio %d", down.count(), up.count())
	}
	if strings.Join(outcomes, ",") != "msg91:failed,twilio-sms:delivered" {
		t.Errorf("outcomes = %v", outcomes)
	}
}
