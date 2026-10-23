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

// Every provider here is a fake: nothing in these tests delivers a message,
// and no real SMS provider is exercised or claimed.

const (
	testNumber = "+919876543210"
	testCode   = "482915"
)

type fakeProvider struct {
	name    string
	err     error
	delay   time.Duration
	ignore  bool // ignore ctx while delaying (a misbehaving provider)
	mu      sync.Mutex
	calls   int
	gotCode string
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) SendOtp(ctx context.Context, msisdn, code string) error {
	f.mu.Lock()
	f.calls++
	f.gotCode = code
	f.mu.Unlock()
	if f.delay > 0 {
		if f.ignore {
			time.Sleep(f.delay)
		} else {
			select {
			case <-time.After(f.delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return f.err
}

func (f *fakeProvider) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

type observation struct {
	provider string
	outcome  Outcome
}

type recorder struct {
	mu  sync.Mutex
	got []observation
}

func (r *recorder) observe(p string, o Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, observation{p, o})
}

func newSender(t *testing.T, r *recorder, ps ...Provider) *Sender {
	t.Helper()
	s, err := New(Config{Providers: ps, AttemptTimeout: MinAttemptTimeout, Observe: r.observe})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func wantObservations(t *testing.T, r *recorder, want ...observation) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.got) != len(want) {
		t.Fatalf("observations %v, want %v", r.got, want)
	}
	for i := range want {
		if r.got[i] != want[i] {
			t.Fatalf("observations %v, want %v", r.got, want)
		}
	}
}

func TestSender_FirstProviderDelivers(t *testing.T) {
	r := &recorder{}
	primary, secondary := &fakeProvider{name: "primary"}, &fakeProvider{name: "secondary"}
	if err := newSender(t, r, primary, secondary).SendOtp(context.Background(), testNumber, testCode); err != nil {
		t.Fatal(err)
	}
	if primary.callCount() != 1 || secondary.callCount() != 0 || primary.gotCode != testCode {
		t.Fatalf("primary %d, secondary %d calls", primary.callCount(), secondary.callCount())
	}
	wantObservations(t, r, observation{"primary", OutcomeDelivered})
}

func TestSender_FailsOverOnErrorAndTimeout(t *testing.T) {
	r := &recorder{}
	failing := &fakeProvider{name: "primary", err: errors.New("provider 500: +919876543210 482915")}
	slow := &fakeProvider{name: "secondary", delay: 5 * time.Second}
	last := &fakeProvider{name: "tertiary"}
	start := time.Now()
	if err := newSender(t, r, failing, slow, last).SendOtp(context.Background(), testNumber, testCode); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*MinAttemptTimeout {
		t.Errorf("took %v; the slow provider was not bounded by the attempt timeout", elapsed)
	}
	if last.gotCode != testCode {
		t.Errorf("the fallback received %q, want the same code", last.gotCode)
	}
	wantObservations(t, r,
		observation{"primary", OutcomeFailed}, observation{"secondary", OutcomeTimeout}, observation{"tertiary", OutcomeDelivered})
}

// A provider that ignores its context cannot stall the sender past the
// attempt timeout.
func TestSender_BoundsAProviderThatIgnoresItsContext(t *testing.T) {
	r := &recorder{}
	stuck := &fakeProvider{name: "primary", delay: 3 * time.Second, ignore: true}
	backup := &fakeProvider{name: "secondary"}
	start := time.Now()
	if err := newSender(t, r, stuck, backup).SendOtp(context.Background(), testNumber, testCode); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*MinAttemptTimeout {
		t.Errorf("took %v, want about one attempt timeout", elapsed)
	}
	wantObservations(t, r, observation{"primary", OutcomeTimeout}, observation{"secondary", OutcomeDelivered})
}

// Every provider failing is ErrAllProvidersFailed, and no provider text
// (which may echo the number or the code) escapes.
func TestSender_AllFailWithoutLeakingProviderText(t *testing.T) {
	r := &recorder{}
	a := &fakeProvider{name: "primary", err: errors.New("rejected " + testNumber)}
	b := &fakeProvider{name: "secondary", err: errors.New("code " + testCode + " refused")}
	err := newSender(t, r, a, b).SendOtp(context.Background(), testNumber, testCode)
	if !errors.Is(err, ErrAllProvidersFailed) {
		t.Fatalf("err = %v, want ErrAllProvidersFailed", err)
	}
	if strings.Contains(err.Error(), testNumber) || strings.Contains(err.Error(), testCode) {
		t.Errorf("provider text leaked: %v", err)
	}
	wantObservations(t, r, observation{"primary", OutcomeFailed}, observation{"secondary", OutcomeFailed})
}

// When the caller's context ends, no further provider is tried.
func TestSender_StopsWhenTheCallerGivesUp(t *testing.T) {
	r := &recorder{}
	slow := &fakeProvider{name: "primary", delay: 5 * time.Second}
	never := &fakeProvider{name: "secondary"}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := newSender(t, r, slow, never).SendOtp(ctx, testNumber, testCode)
	if !errors.Is(err, context.DeadlineExceeded) || never.callCount() != 0 {
		t.Fatalf("(%v, fallback called %d times); want the caller's deadline and no fallback", err, never.callCount())
	}
	wantObservations(t, r, observation{"primary", OutcomeAbandoned})

	// Abandoned on the LAST provider: still the caller's error, not a
	// provider failure (nobody was waiting for the code).
	only := &fakeProvider{name: "primary", delay: 5 * time.Second}
	ctx1, cancel1 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel1()
	if err := newSender(t, &recorder{}, only).SendOtp(ctx1, testNumber, testCode); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrAllProvidersFailed) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}

	// Already cancelled: nothing is attempted or observed.
	r2 := &recorder{}
	done, cancel2 := context.WithCancel(context.Background())
	cancel2()
	first := &fakeProvider{name: "primary"}
	if err := newSender(t, r2, first).SendOtp(done, testNumber, testCode); !errors.Is(err, context.Canceled) || first.callCount() != 0 {
		t.Fatalf("(%v, %d calls)", err, first.callCount())
	}
	wantObservations(t, r2)
}

// Concurrent sends are independent (run under -race).
func TestSender_ConcurrentSends(t *testing.T) {
	r := &recorder{}
	p := &fakeProvider{name: "primary"}
	s := newSender(t, r, p)
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.SendOtp(context.Background(), testNumber, testCode); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if p.callCount() != n || len(r.got) != n {
		t.Fatalf("%d calls, %d observations; want %d each", p.callCount(), len(r.got), n)
	}
}

func TestNew_ValidatesConfiguration(t *testing.T) {
	p := func(name string) Provider { return &fakeProvider{name: name} }
	for name, cfg := range map[string]Config{
		"no providers":       {AttemptTimeout: time.Second},
		"too many providers": {Providers: []Provider{p("a"), p("b"), p("c"), p("d")}, AttemptTimeout: time.Second},
		"nil provider":       {Providers: []Provider{nil}, AttemptTimeout: time.Second},
		"duplicate names":    {Providers: []Provider{p("sms"), p("sms")}, AttemptTimeout: time.Second},
		"upper-case name":    {Providers: []Provider{p("Primary")}, AttemptTimeout: time.Second},
		"name with a number": {Providers: []Provider{p("+919876543210")}, AttemptTimeout: time.Second},
		"empty name":         {Providers: []Provider{p("")}, AttemptTimeout: time.Second},
		"timeout too short":  {Providers: []Provider{p("a")}, AttemptTimeout: 100 * time.Millisecond},
		"timeout too long":   {Providers: []Provider{p("a")}, AttemptTimeout: 9 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if s, err := New(cfg); s != nil || !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("(%v, %v); want ErrInvalidConfig", s, err)
			}
		})
	}
	// Observe is optional.
	if _, err := New(Config{Providers: []Provider{p("primary-sms")}, AttemptTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
}

// Through identity itself: a failover delivery creates a usable challenge;
// total failure is ERR_OTP_DELIVERY_FAILED with the challenge withdrawn.
func TestSender_ThroughIdentityRequestOtp(t *testing.T) {
	ctx := context.Background()
	key := []byte("test_secret_key_that_is_long_enough")

	r := &recorder{}
	got := &fakeProvider{name: "secondary"}
	svc := app.NewIdentityService(app.NewMemoryIdentityRepository(), key)
	svc.SetOtpSender(newSender(t, r, &fakeProvider{name: "primary", err: errors.New("down")}, got))
	reqID, err := svc.RequestOtp(ctx, testNumber)
	if err != nil || reqID == "" || got.gotCode == "" {
		t.Fatalf("(%q, %v); the fallback should have delivered (code %q)", reqID, err, got.gotCode)
	}
	if _, _, err := svc.ValidateOtp(ctx, reqID, got.gotCode); err != nil {
		t.Fatalf("the delivered code does not validate: %v", err)
	}

	repo := app.NewMemoryIdentityRepository()
	svc = app.NewIdentityService(repo, key)
	svc.SetOtpSender(newSender(t, &recorder{}, &fakeProvider{name: "primary", err: errors.New("down")}))
	if _, err := svc.RequestOtp(ctx, testNumber); !errors.Is(err, app.ErrOtpDeliveryFailed) {
		t.Fatalf("err = %v, want ErrOtpDeliveryFailed", err)
	}
}
