package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/twilioverify"
)

// Twilio Verify selection. No test here reaches Twilio: the verifier is built
// against a loopback test server, or not built at all.

// Made-up identifiers in Twilio's documented shapes (fakeSID).
var (
	tAccount = fakeSID("A", "C")
	tKey     = fakeSID("S", "K")
	tService = fakeSID("V", "A")
)

const (
	tSecret = "testSecretNotReal0123456789abcdef"
)

func twilioEnv(secretFile string) map[string]string {
	return map[string]string{
		OtpProviderVariable:            OtpProviderTwilio,
		TwilioAccountSIDVariable:       tAccount,
		TwilioAPIKeySIDVariable:        tKey,
		TwilioAPIKeySecretFileVariable: secretFile,
		TwilioVerifyServiceSIDVariable: tService,
	}
}

func TestResolveOtpDelivery_Twilio(t *testing.T) {
	for _, env := range []string{"development", "staging", "production"} {
		e := twilioEnv("/run/secrets/twilio")
		e[environmentVariable] = env
		got, err := resolveOtpDelivery(lookupFrom(e))
		if err != nil {
			t.Fatalf("%s: %v", env, err)
		}
		want := TwilioConfig{AccountSID: tAccount, APIKeySID: tKey, APIKeySecretFile: "/run/secrets/twilio", VerifyServiceSID: tService}
		if got.Provider != OtpProviderTwilio || got.Twilio != want {
			t.Fatalf("%s: %+v", env, got)
		}
	}
}

func TestResolveOtpDelivery_TwilioRequiresEveryVariable(t *testing.T) {
	for _, missing := range TwilioVariables {
		e := twilioEnv("/run/secrets/twilio")
		delete(e, missing)
		_, err := resolveOtpDelivery(lookupFrom(e))
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s: %v, want an error naming it", missing, err)
		}
	}
}

// A Twilio variable without twilio selected is refused: credentials must not
// sit unused in a pod, and a typo in the provider must not fall back to the
// mock or to nothing silently.
func TestResolveOtpDelivery_StrayTwilioVariablesAreRefused(t *testing.T) {
	for _, provider := range []string{"", "mock", "none"} {
		for _, name := range TwilioVariables {
			e := map[string]string{environmentVariable: "development", name: "x"}
			if provider != "" {
				e[OtpProviderVariable] = provider
			}
			_, err := resolveOtpDelivery(lookupFrom(e))
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), name) {
				t.Errorf("provider %q with %s: %v", provider, name, err)
			}
			if err != nil && strings.Contains(err.Error(), "=x") {
				t.Errorf("the value is echoed: %v", err)
			}
		}
	}
}

func TestOtpVerifier_BuildsOnlyForTwilio(t *testing.T) {
	never := func(twilioverify.Config) (*twilioverify.Client, error) {
		t.Error("a Twilio client was built for another provider")
		return nil, errors.New("unexpected")
	}
	for _, d := range []OtpDelivery{{}, {Provider: OtpProviderMock}} {
		if v, err := d.otpVerifier(never); v != nil || err != nil {
			t.Errorf("%+v: (%v, %v)", d, v, err)
		}
	}
}

// The secret is read from its file (a trailing newline is tolerated), every
// value is validated, and errors never carry a value or a path.
func TestOtpVerifier_ReadsTheSecretFileAndValidates(t *testing.T) {
	good := writeFile(t, "twilio-secret", []byte(tSecret+"\n"))
	d := OtpDelivery{Provider: OtpProviderTwilio, Twilio: TwilioConfig{
		AccountSID: tAccount, APIKeySID: tKey, APIKeySecretFile: good, VerifyServiceSID: tService,
	}}
	var got twilioverify.Config
	v, err := d.otpVerifier(func(c twilioverify.Config) (*twilioverify.Client, error) {
		got = c
		return twilioverify.New(c)
	})
	if err != nil || v == nil {
		t.Fatalf("(%v, %v)", v, err)
	}
	if got.APIKeySecret != tSecret || got.BaseURL != "" || got.HTTPClient != nil {
		t.Errorf("the client is not built from the file with Twilio's defaults: base %q", got.BaseURL)
	}

	for name, mutate := range map[string]func(*OtpDelivery){
		"missing secret file": func(d *OtpDelivery) { d.Twilio.APIKeySecretFile = good + ".absent" },
		"empty secret file":   func(d *OtpDelivery) { d.Twilio.APIKeySecretFile = writeFile(t, "empty", nil) },
		"malformed account":   func(d *OtpDelivery) { d.Twilio.AccountSID = "AC123" },
		"key SID swapped":     func(d *OtpDelivery) { d.Twilio.APIKeySID = tService },
	} {
		t.Run(name, func(t *testing.T) {
			bad := d
			mutate(&bad)
			_, err := bad.otpVerifier(twilioverify.New)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err %v, want ErrInvalidConfig", err)
			}
			for _, s := range []string{tSecret, good, "AC123"} {
				if strings.Contains(err.Error(), s) {
					t.Errorf("the error carries %q", s)
				}
			}
		})
	}
}

// The adapter maps Twilio's answers to identity's contract: approved is true;
// a wrong code or a verification Twilio no longer has is a counted wrong
// code (false, nil); anything else is the provider's failure.
func TestTwilioVerifier_MapsOutcomes(t *testing.T) {
	var status atomic.Int32
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()
	c, err := twilioverify.New(twilioverify.Config{
		AccountSID: tAccount, APIKeySID: tKey, APIKeySecret: tSecret, ServiceSID: tService,
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	v := twilioVerifier{c}
	ref := fakeSID("V", "E")
	resource := func(s string) string {
		return `{"sid":"` + ref + `","service_sid":"` + tService + `","account_sid":"` + tAccount + `","status":"` + s + `"}`
	}
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		code    string
		want    bool
		wantErr bool
	}{
		{"approved", 200, resource("approved"), "123456", true, false},
		{"wrong code", 200, resource("pending"), "123456", false, false},
		{"gone at Twilio", 404, `{"code":20404}`, "123456", false, false},
		{"not digits", 200, resource("approved"), "abcdef", false, false},
		{"rate limited", 429, `{"code":60202}`, "123456", false, true},
		{"server error", 500, ``, "123456", false, true},
		{"rejected", 400, `{"code":60200}`, "123456", false, true},
	} {
		status.Store(int32(tc.status))
		body.Store(tc.body)
		ok, err := v.CheckVerification(context.Background(), ref, tc.code)
		if ok != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: (%v, %v), want (%v, error %v)", tc.name, ok, err, tc.want, tc.wantErr)
		}
	}
	status.Store(201)
	body.Store(resource("pending"))
	if got, err := v.StartVerification(context.Background(), "+919876543210"); err != nil || got != ref {
		t.Errorf("start: (%q, %v)", got, err)
	}
}

// fakeSID builds a made-up Twilio identifier of the documented shape (two
// letters and 32 lower-case hex digits) at run time, so that no
// credential-shaped literal sits in the source. None is a real identifier.
func fakeSID(a, b string) string { return a + b + strings.Repeat("0123456789abcdef", 2) }
