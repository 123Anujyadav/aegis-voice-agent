package otpdelivery_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/httpapi"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/otpdelivery"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/routes"
)

// These tests run the real sign-in flow (app.IdentityService on the in-memory
// repository) with the development mock as its only provider. They make no
// network request: the mock delivers into an in-process outbox.

const (
	mockNumber      = "+919876543210"
	mockOtherNumber = "+919812345678"
)

var sixDigits = regexp.MustCompile(`^\d{6}$`)

type mockRig struct {
	svc    *app.IdentityService
	repo   *app.MemoryIdentityRepository
	outbox *otpdelivery.Outbox
	sender *otpdelivery.Sender
}

func newMockRig(t *testing.T, outboxFile string) *mockRig {
	t.Helper()
	repo := app.NewMemoryIdentityRepository()
	svc := app.NewIdentityService(repo, []byte("0123456789abcdef0123456789abcdef"))
	outbox := otpdelivery.NewOutbox(outboxFile)
	sender, err := otpdelivery.NewMockSender(outbox, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetOtpSender(sender)
	return &mockRig{svc: svc, repo: repo, outbox: outbox, sender: sender}
}

// request asks for a code for number and returns the request id and the code
// the mock received.
func (r *mockRig) request(t *testing.T, number string) (string, string) {
	t.Helper()
	reqID, err := r.svc.RequestOtp(context.Background(), number)
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}
	e, ok := r.outbox.Latest(number)
	if !ok {
		t.Fatal("the mock received no code")
	}
	return reqID, e.Code
}

func wrongCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func TestMock_GeneratesASixDigitCodeAndKeepsItOutOfTheResult(t *testing.T) {
	r := newMockRig(t, "")
	reqID, code := r.request(t, mockNumber)
	if !sixDigits.MatchString(code) {
		t.Fatalf("code %q is not six digits", code)
	}
	if strings.Contains(reqID, code) {
		t.Fatal("the request id carries the code")
	}
	e, _ := r.outbox.Latest(mockNumber)
	if got := e.ExpiresAt.Sub(e.SentAt); got != app.OtpLifetime {
		t.Errorf("outbox lifetime %v, want the challenge lifetime %v", got, app.OtpLifetime)
	}
	if h := r.sender.Health(); len(h) != 1 || h[0].Name != otpdelivery.MockProviderName || h[0].LastOutcome != otpdelivery.OutcomeDelivered {
		t.Errorf("health %+v, want one delivered mock", h)
	}
}

func TestMock_CorrectCodeSignsIn(t *testing.T) {
	r := newMockRig(t, "")
	reqID, code := r.request(t, mockNumber)
	session, _, err := r.svc.ValidateOtp(context.Background(), reqID, code)
	if err != nil {
		t.Fatalf("ValidateOtp: %v", err)
	}
	if session.SessionID == "" || session.RefreshToken == "" {
		t.Fatal("no session issued")
	}
}

func TestMock_WrongCodeIsRefused(t *testing.T) {
	r := newMockRig(t, "")
	reqID, code := r.request(t, mockNumber)
	if _, _, err := r.svc.ValidateOtp(context.Background(), reqID, wrongCode(code)); !errors.Is(err, app.ErrInvalidOtp) {
		t.Fatalf("wrong code: %v, want ErrInvalidOtp", err)
	}
	// The right code still works afterwards: one wrong guess is within the
	// attempt limit.
	if _, _, err := r.svc.ValidateOtp(context.Background(), reqID, code); err != nil {
		t.Fatalf("correct code after one wrong guess: %v", err)
	}
}

func TestMock_ExpiredCodeIsRefusedEvenWhenCorrect(t *testing.T) {
	r := newMockRig(t, "")
	reqID, code := r.request(t, mockNumber)
	ch, err := r.repo.GetOtp(context.Background(), reqID)
	if err != nil {
		t.Fatal(err)
	}
	ch.ExpiresAt = time.Now().Add(-time.Second)
	if err := r.repo.SaveOtp(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.svc.ValidateOtp(context.Background(), reqID, code); !errors.Is(err, app.ErrExpiredOtp) {
		t.Fatalf("expired: %v, want ErrExpiredOtp", err)
	}
}

func TestMock_UsedCodeCannotBeReused(t *testing.T) {
	r := newMockRig(t, "")
	reqID, code := r.request(t, mockNumber)
	if _, _, err := r.svc.ValidateOtp(context.Background(), reqID, code); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.svc.ValidateOtp(context.Background(), reqID, code); !errors.Is(err, app.ErrInvalidOtp) {
		t.Fatalf("reuse: %v, want ErrInvalidOtp", err)
	}
}

// Three wrong codes exhaust a challenge (app.maxOtpAttempts): the correct code
// is refused afterwards.
func TestMock_MaximumAttemptsExhaustTheChallenge(t *testing.T) {
	r := newMockRig(t, "")
	reqID, code := r.request(t, mockNumber)
	for i := 0; i < 3; i++ {
		if _, _, err := r.svc.ValidateOtp(context.Background(), reqID, wrongCode(code)); !errors.Is(err, app.ErrInvalidOtp) {
			t.Fatalf("wrong guess %d: %v", i+1, err)
		}
	}
	if _, _, err := r.svc.ValidateOtp(context.Background(), reqID, code); !errors.Is(err, app.ErrInvalidOtp) {
		t.Fatalf("correct code after three wrong guesses: %v, want ErrInvalidOtp", err)
	}
}

// identity has no minimum interval between requests for one number (no resend
// cooldown is specified; docs/phase19/OTP-PROVIDERS.md records the gap). A
// resend issues a fresh code on a new challenge, and each challenge accepts
// only its own code. Repeated requests are bounded by the rate limit below.
func TestMock_ResendIssuesAFreshChallengeThatAcceptsOnlyItsOwnCode(t *testing.T) {
	r := newMockRig(t, "")
	first, firstCode := r.request(t, mockNumber)
	second, secondCode := r.request(t, mockNumber)
	if first == second {
		t.Fatal("a resend reused the request id")
	}
	if latest, _ := r.outbox.Latest(mockNumber); latest.Code != secondCode {
		t.Fatal("the outbox does not hold the latest code")
	}
	if firstCode != secondCode {
		if _, _, err := r.svc.ValidateOtp(context.Background(), second, firstCode); !errors.Is(err, app.ErrInvalidOtp) {
			t.Fatalf("the first code on the second challenge: %v, want ErrInvalidOtp", err)
		}
	}
	if _, _, err := r.svc.ValidateOtp(context.Background(), second, secondCode); err != nil {
		t.Fatalf("the second code: %v", err)
	}
}

// Three requests per number in 15 minutes (app.DefaultOtpRatePolicy); the
// fourth is refused before any code is minted or sent. Another number is
// unaffected.
func TestMock_RequestRateLimitStopsTheFourthCode(t *testing.T) {
	r := newMockRig(t, "")
	p := app.DefaultOtpRatePolicy()
	for i := 0; i < p.MaxRequests; i++ {
		r.request(t, mockNumber)
	}
	before, _ := r.outbox.Latest(mockNumber)
	_, err := r.svc.RequestOtp(context.Background(), mockNumber)
	var limited *app.RateLimitError
	if !errors.As(err, &limited) || !errors.Is(err, app.ErrRateLimited) {
		t.Fatalf("request %d: %v, want a rate limit", p.MaxRequests+1, err)
	}
	if after, _ := r.outbox.Latest(mockNumber); after != before {
		t.Fatal("a rate-limited request still sent a code")
	}
	r.request(t, mockOtherNumber)
}

func TestMock_MalformedOrUnknownNumbersAndRequestsAreRefused(t *testing.T) {
	r := newMockRig(t, "")
	for _, n := range []string{"9876543210", "+91 98765 43210", "", "+0123456789"} {
		if _, err := r.svc.RequestOtp(context.Background(), n); !errors.Is(err, app.ErrInvalidMSISDN) {
			t.Errorf("RequestOtp(%q): %v, want ErrInvalidMSISDN", n, err)
		}
	}
	if r.outbox.Len() != 0 {
		t.Fatal("a refused number reached the mock")
	}
	if _, ok := r.outbox.Latest(mockNumber); ok {
		t.Fatal("the outbox has a code for a number that never asked")
	}
	if _, _, err := r.svc.ValidateOtp(context.Background(), "req_unknown", "123456"); !errors.Is(err, app.ErrInvalidOtp) {
		t.Fatalf("unknown request id: %v, want ErrInvalidOtp", err)
	}
}

// A mock that cannot deliver (here: its outbox file cannot be written) is a
// failed delivery: identity withdraws the challenge and says so, without the
// provider's error text.
func TestMock_DeliveryFailureWithdrawsTheChallenge(t *testing.T) {
	r := newMockRig(t, t.TempDir()) // a directory: appending to it fails
	_, err := r.svc.RequestOtp(context.Background(), mockNumber)
	if !errors.Is(err, app.ErrOtpDeliveryFailed) {
		t.Fatalf("RequestOtp: %v, want ErrOtpDeliveryFailed", err)
	}
	if r.outbox.Len() != 0 {
		t.Fatal("an undelivered code was kept")
	}
	if h := r.sender.Health(); h[0].LastOutcome != otpdelivery.OutcomeFailed {
		t.Errorf("health %+v, want failed", h)
	}
}

func TestMock_CancelledRequestDeliversNothing(t *testing.T) {
	outbox := otpdelivery.NewOutbox("")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := otpdelivery.NewMockProvider(outbox).SendOtp(ctx, mockNumber, "123456"); !errors.Is(err, context.Canceled) {
		t.Fatalf("SendOtp: %v, want context.Canceled", err)
	}
	if outbox.Len() != 0 {
		t.Fatal("a cancelled send was delivered")
	}
}

func TestMock_OutboxFileGetsOneOwnerOnlyLinePerCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "otp-outbox.txt")
	r := newMockRig(t, path)
	_, code := r.request(t, mockNumber)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) != 3 || fields[1] != mockNumber || fields[2] != code {
		t.Fatalf("outbox line %q", data)
	}
	if _, err := time.Parse(time.RFC3339, fields[0]); err != nil {
		t.Fatalf("expiry %q: %v", fields[0], err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("outbox file mode %v (%v), want 0600", fi.Mode().Perm(), err)
		}
	}
}

func TestMock_OutboxIsBounded(t *testing.T) {
	outbox := otpdelivery.NewOutbox("")
	p := otpdelivery.NewMockProvider(outbox)
	number := func(i int) string { return fmt.Sprintf("+91980000%04d", i) }
	for i := 0; i < otpdelivery.MaxOutboxEntries+10; i++ {
		if err := p.SendOtp(context.Background(), number(i), "123456"); err != nil {
			t.Fatal(err)
		}
	}
	if outbox.Len() != otpdelivery.MaxOutboxEntries {
		t.Fatalf("outbox holds %d, want %d", outbox.Len(), otpdelivery.MaxOutboxEntries)
	}
	if _, ok := outbox.Latest(number(0)); ok {
		t.Fatal("the oldest entry was not evicted")
	}
	if _, ok := outbox.Latest(number(otpdelivery.MaxOutboxEntries + 9)); !ok {
		t.Fatal("the newest entry is missing")
	}
}

// Through the HTTP API: the code reaches the mock and never the response, the
// response headers or the service's logs.
func TestMock_CodeNeverAppearsInTheAPIResponseOrLogs(t *testing.T) {
	r := newMockRig(t, "")
	var logs strings.Builder
	h := httpapi.New(r.svc, slog.New(slog.NewTextHandler(&logs, nil)), httpapi.NewMetrics(), nil)
	req := httptest.NewRequest(http.MethodPost, routes.OTPRequest, strings.NewReader(`{"msisdn":"`+mockNumber+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("OTP request: %d %s", rec.Code, rec.Body.String())
	}
	e, ok := r.outbox.Latest(mockNumber)
	if !ok {
		t.Fatal("the mock received no code")
	}
	body, _ := io.ReadAll(rec.Body)
	for where, text := range map[string]string{"body": string(body), "headers": headerText(rec.Header()), "logs": logs.String()} {
		if strings.Contains(text, e.Code) {
			t.Errorf("the code appears in the %s", where)
		}
		if strings.Contains(text, strings.TrimPrefix(mockNumber, "+")) {
			t.Errorf("the number appears in the %s", where)
		}
	}
}

func headerText(h http.Header) string {
	var b strings.Builder
	for k, vs := range h {
		b.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
	}
	return b.String()
}
