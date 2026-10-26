package otpdelivery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Account-deletion codes through the same failover (otpdelivery.go, "Account-
// deletion codes"). Every provider is a fake: no message is delivered and no
// real provider is claimed.

// deletionProvider is a fake with a registered deletion template: it records
// which of its two methods was called, so a test can tell the messages apart.
type deletionProvider struct {
	fakeProvider
	delErr     error
	delDelay   time.Duration
	dmu        sync.Mutex
	delCalls   int
	delGotCode string
}

func (d *deletionProvider) SendDeletionCode(ctx context.Context, _, code string) error {
	d.dmu.Lock()
	d.delCalls++
	d.delGotCode = code
	err := d.delErr
	d.dmu.Unlock()
	if d.delDelay > 0 {
		select {
		case <-time.After(d.delDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (d *deletionProvider) deletionCalls() int { d.dmu.Lock(); defer d.dmu.Unlock(); return d.delCalls }

func (d *deletionProvider) deletionCode() string {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	return d.delGotCode
}

func deletionCapable(name string) *deletionProvider {
	return &deletionProvider{fakeProvider: fakeProvider{name: name}}
}

func newDeletionSender(t *testing.T, signIn, deletion *recorder, ps ...Provider) *Sender {
	t.Helper()
	s, err := New(Config{Providers: ps, AttemptTimeout: MinAttemptTimeout, Observe: signIn.observe, ObserveDeletion: deletion.observe})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// No provider with a deletion template: there is no deletion sender, so
// identity is never told deletion codes can be sent.
func TestDeletionCodes_ExistOnlyWhenAProviderCanDeliverThem(t *testing.T) {
	s := newDeletionSender(t, &recorder{}, &recorder{}, &fakeProvider{name: "primary"}, &fakeProvider{name: "secondary"})
	if d, ok := s.DeletionCodes(); ok || d != nil {
		t.Fatalf("DeletionCodes() = (%v, %v) with no deletion-capable provider", d, ok)
	}
	if h := s.DeletionHealth(); h != nil {
		t.Fatalf("DeletionHealth() = %v, want nil", h)
	}
	if err := s.SendOtp(context.Background(), testNumber, testCode); err != nil {
		t.Fatalf("sign-in delivery is unaffected: %v", err)
	}

	s = newDeletionSender(t, &recorder{}, &recorder{}, &fakeProvider{name: "primary"}, deletionCapable("secondary"))
	if _, ok := s.DeletionCodes(); !ok {
		t.Fatal("a deletion-capable provider is configured, but there is no deletion sender")
	}
}

// A deletion code goes only to deletion-capable providers, through their
// deletion method (the deletion template), and a sign-in code only through
// the sign-in method: the two messages never cross.
func TestDeletionCodes_NeverUseTheSignInMessageAndViceVersa(t *testing.T) {
	signInOnly := &fakeProvider{name: "primary"}
	both := deletionCapable("secondary")
	s := newDeletionSender(t, &recorder{}, &recorder{}, signInOnly, both)
	d, _ := s.DeletionCodes()

	if err := d.SendDeletionCode(context.Background(), testNumber, testCode); err != nil {
		t.Fatal(err)
	}
	if signInOnly.callCount() != 0 {
		t.Error("a provider without a deletion template was asked to send a deletion code")
	}
	if both.deletionCalls() != 1 || both.callCount() != 0 || both.deletionCode() != testCode {
		t.Errorf("the deletion code went through the wrong method: deletion %d, sign-in %d", both.deletionCalls(), both.callCount())
	}

	if err := s.SendOtp(context.Background(), testNumber, testCode); err != nil {
		t.Fatal(err)
	}
	if signInOnly.callCount() != 1 || both.deletionCalls() != 1 {
		t.Errorf("a sign-in code used the deletion template, or skipped the first provider")
	}
}

// Failover for deletion codes: an error, then a timeout, then delivery,
// observed on the deletion hook only. The same code reaches each provider
// tried (never two different codes).
func TestDeletionCodes_FailOverOnErrorAndTimeout(t *testing.T) {
	failing := deletionCapable("primary")
	failing.delErr = errors.New("template rejected")
	slow := deletionCapable("secondary")
	slow.delDelay = 5 * MinAttemptTimeout
	good := deletionCapable("tertiary")
	signIn, deletion := &recorder{}, &recorder{}
	s := newDeletionSender(t, signIn, deletion, failing, slow, good)
	d, _ := s.DeletionCodes()

	if err := d.SendDeletionCode(context.Background(), testNumber, testCode); err != nil {
		t.Fatalf("the third provider should have delivered: %v", err)
	}
	wantObservations(t, deletion,
		observation{"primary", OutcomeFailed}, observation{"secondary", OutcomeTimeout}, observation{"tertiary", OutcomeDelivered})
	wantObservations(t, signIn) // nothing on the sign-in hook
	for _, p := range []*deletionProvider{failing, slow, good} {
		if p.deletionCode() != testCode {
			t.Errorf("%s got code %q, want the one code", p.name, p.deletionCode())
		}
	}
}

// Every deletion-capable provider failing is ErrAllProvidersFailed, with no
// provider text; a caller that gives up stops the failover.
func TestDeletionCodes_AllFailWithoutLeakingAndStopWhenTheCallerGivesUp(t *testing.T) {
	a := deletionCapable("primary")
	a.delErr = errors.New("provider says: cannot reach " + testNumber + " with " + testCode)
	b := deletionCapable("secondary")
	b.delErr = errors.New("quota exceeded for " + testNumber)
	s := newDeletionSender(t, &recorder{}, &recorder{}, a, b)
	d, _ := s.DeletionCodes()
	err := d.SendDeletionCode(context.Background(), testNumber, testCode)
	if !errors.Is(err, ErrAllProvidersFailed) || strings.Contains(err.Error(), testNumber) || strings.Contains(err.Error(), testCode) {
		t.Fatalf("err = %v, want ErrAllProvidersFailed without provider text", err)
	}

	slow := deletionCapable("primary")
	slow.delDelay = MaxAttemptTimeout
	next := deletionCapable("secondary")
	deletion := &recorder{}
	s = newDeletionSender(t, &recorder{}, deletion, slow, next)
	d, _ = s.DeletionCodes()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := d.SendDeletionCode(ctx, testNumber, testCode); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
	if next.deletionCalls() != 0 {
		t.Error("a provider was tried after the caller gave up")
	}
	wantObservations(t, deletion, observation{"primary", OutcomeAbandoned})
}

// A rejected deletion template is held against the provider's deletion
// health only: its sign-in health is untouched.
func TestDeletionCodes_HealthIsSeparateFromSignIn(t *testing.T) {
	p := deletionCapable("primary")
	p.delErr = errors.New("template not approved")
	s := newDeletionSender(t, &recorder{}, &recorder{}, p)
	d, _ := s.DeletionCodes()
	_ = d.SendDeletionCode(context.Background(), testNumber, testCode)
	if err := s.SendOtp(context.Background(), testNumber, testCode); err != nil {
		t.Fatal(err)
	}
	if h := s.DeletionHealth(); len(h) != 1 || h[0].ConsecutiveFailures != 1 || h[0].LastOutcome != OutcomeFailed {
		t.Errorf("deletion health = %+v", h)
	}
	if h := s.Health(); len(h) != 1 || h[0].ConsecutiveFailures != 0 || h[0].LastOutcome != OutcomeDelivered {
		t.Errorf("sign-in health = %+v; a deletion failure must not count here", h)
	}
}

// Through identity's web deletion (app/web_erasure.go): the failover sender's
// deletion side delivers the code for a number with an account, and the code
// confirms the deletion; when every deletion-capable provider fails, the
// challenge is withdrawn.
func TestDeletionCodes_ThroughIdentityWebDeletion(t *testing.T) {
	ctx := context.Background()
	key := []byte("test_secret_key_that_is_long_enough")
	withAccount := func() (*app.IdentityService, *app.MemoryIdentityRepository) {
		repo := app.NewMemoryIdentityRepository()
		if err := repo.SaveSession(ctx, &app.UserSession{
			SessionID: "sess_" + strings.Repeat("a", 64), SubscriberID: app.LegacySubscriberID(key, testNumber),
			RefreshTokenHash: app.HashRefreshToken(key, strings.Repeat("b", 64)), Roles: []string{"subscriber"},
			CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		svc := app.NewIdentityService(repo, key)
		svc.SetDeletionCodeBudget(10)
		return svc, repo
	}

	svc, repo := withAccount()
	fallback := deletionCapable("secondary")
	down := deletionCapable("primary")
	down.delErr = errors.New("down")
	s := newDeletionSender(t, &recorder{}, &recorder{}, down, fallback)
	d, _ := s.DeletionCodes()
	svc.SetDeletionCodeSender(d)
	reqID, err := svc.RequestDeletionCode(ctx, testNumber)
	if err != nil {
		t.Fatal(err)
	}
	code := eventually(t, fallback.deletionCode) // the send is dispatched after the answer
	if cerr := svc.ConfirmDeletion(ctx, reqID, code); cerr != nil {
		t.Fatalf("the delivered deletion code did not confirm: %v", cerr)
	}
	if len(repo.Erasures()) != 1 {
		t.Fatalf("%d erasures, want 1", len(repo.Erasures()))
	}

	svc, repo = withAccount()
	allDown := deletionCapable("primary")
	allDown.delErr = errors.New("down")
	s = newDeletionSender(t, &recorder{}, &recorder{}, allDown)
	d, _ = s.DeletionCodes()
	svc.SetDeletionCodeSender(d)
	reqID, err = svc.RequestDeletionCode(ctx, testNumber)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, func() string {
		if _, gerr := repo.GetOtp(ctx, reqID); errors.Is(gerr, app.ErrInvalidOtp) {
			return "withdrawn"
		}
		return ""
	})
}

// eventually polls get until it returns a non-empty value, for up to 5 s.
func eventually(t *testing.T, get func() string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v := get(); v != "" {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the condition was never met")
	return ""
}
