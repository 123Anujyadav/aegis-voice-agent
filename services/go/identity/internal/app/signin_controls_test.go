package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The F-1 sign-in delivery controls (signin_controls.go): a tester allowlist
// checked before any work, and a global hourly cap reserved atomically just
// before the send, both failing closed.

// countingSender counts sends, safely under concurrency.
type countingSender struct {
	mu    sync.Mutex
	sent  map[string]int
	total atomic.Int64
}

func newCountingSender() *countingSender { return &countingSender{sent: map[string]int{}} }

func (c *countingSender) SendOtp(_ context.Context, number, _ string) error {
	c.mu.Lock()
	c.sent[number]++
	c.mu.Unlock()
	c.total.Add(1)
	return nil
}

// testers returns n distinct allowlisted test numbers.
func testers(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("+9190000%05d", i)
	}
	return out
}

// midHour is a fixed instant 30 minutes into an hour.
var midHour = time.Date(2026, 11, 12, 10, 30, 0, 0, time.UTC)

// controlledSvc is a service with a counting sender, the controls set to
// allowed and hourlyCap, and a controllable clock at midHour.
func controlledSvc(t *testing.T, repo IdentityRepository, allowed []string, hourlyCap int) (*IdentityService, *countingSender, *fakeClock) {
	t.Helper()
	svc := NewIdentityService(repo, testRefreshKey)
	clock := &fakeClock{t: midHour}
	svc.now = clock.now
	sender := newCountingSender()
	svc.SetOtpSender(sender)
	if err := svc.SetSignInDeliveryControls(allowed, hourlyCap); err != nil {
		t.Fatal(err)
	}
	return svc, sender, clock
}

func (m *MemoryIdentityRepository) signInCount(hour time.Time) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.signInDeliveries[hour.UTC().Truncate(time.Hour)]
}

func TestSignInControls_AnAllowlistedNumberGetsACode(t *testing.T) {
	m := NewMemoryIdentityRepository()
	allowed := testers(2)
	svc, sender, _ := controlledSvc(t, m, allowed, 5)
	reqID, err := svc.RequestOtp(context.Background(), allowed[1])
	if err != nil || reqID == "" {
		t.Fatalf("(%q, %v)", reqID, err)
	}
	if sender.sent[allowed[1]] != 1 {
		t.Errorf("sent %v", sender.sent)
	}
	if _, err := m.GetOtp(context.Background(), reqID); err != nil {
		t.Errorf("no challenge: %v", err)
	}
	if got := m.signInCount(midHour); got != 1 {
		t.Errorf("budget count %d, want 1", got)
	}
}

func TestSignInControls_ANumberOffTheListSpendsNothing(t *testing.T) {
	m := NewMemoryIdentityRepository()
	svc, sender, _ := controlledSvc(t, m, testers(3), 5)
	const outsider = "+919876543210"
	for i := 0; i < 5; i++ { // more than the per-number allowance: none is spent
		reqID, err := svc.RequestOtp(context.Background(), outsider)
		if !errors.Is(err, ErrOtpDeliveryUnavailable) || reqID != "" {
			t.Fatalf("(%q, %v), want ErrOtpDeliveryUnavailable", reqID, err)
		}
		if strings.Contains(err.Error(), "9876543210") {
			t.Errorf("the error carries the number: %v", err)
		}
	}
	if len(m.otps) != 0 {
		t.Errorf("%d challenges stored", len(m.otps))
	}
	if st := m.otpRates[mp(outsider)]; st != nil {
		t.Errorf("the number's allowance was touched: %+v", st)
	}
	if got := m.signInCount(midHour); got != 0 {
		t.Errorf("budget count %d, want 0", got)
	}
	if sender.total.Load() != 0 {
		t.Errorf("%d sends", sender.total.Load())
	}
	// A malformed number is still ErrInvalidMSISDN, checked first.
	if _, err := svc.RequestOtp(context.Background(), "919876543210"); !errors.Is(err, ErrInvalidMSISDN) {
		t.Errorf("malformed: %v", err)
	}
}

func TestSignInControls_TheCapIsExactAndARefusalWithdrawsTheChallenge(t *testing.T) {
	m := NewMemoryIdentityRepository()
	allowed := testers(6)
	const hourlyCap = 4
	svc, sender, _ := controlledSvc(t, m, allowed, hourlyCap)
	for i := 0; i < hourlyCap; i++ {
		if _, err := svc.RequestOtp(context.Background(), allowed[i]); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	for _, n := range allowed[hourlyCap:] {
		reqID, err := svc.RequestOtp(context.Background(), n)
		if !errors.Is(err, ErrOtpDeliveryUnavailable) || reqID != "" {
			t.Fatalf("over the cap: (%q, %v), want ErrOtpDeliveryUnavailable", reqID, err)
		}
		// The per-number checks ran first, so the request counted against
		// the number, as a failed delivery does.
		if st := m.otpRates[mp(n)]; st == nil || len(st.requests) != 1 {
			t.Errorf("the refused number's allowance: %+v", st)
		}
	}
	if sender.total.Load() != hourlyCap {
		t.Errorf("%d sends, want %d", sender.total.Load(), hourlyCap)
	}
	if len(m.otps) != hourlyCap {
		t.Errorf("%d live challenges, want %d: a refused one was not withdrawn", len(m.otps), hourlyCap)
	}
	if got := m.signInCount(midHour); got != hourlyCap {
		t.Errorf("budget count %d, want %d: a refusal must count nothing", got, hourlyCap)
	}
}

func TestSignInControls_TheCapResetsAtTheNextHour(t *testing.T) {
	m := NewMemoryIdentityRepository()
	allowed := testers(4)
	svc, sender, clock := controlledSvc(t, m, allowed, 1)
	if _, err := svc.RequestOtp(context.Background(), allowed[0]); err != nil {
		t.Fatal(err)
	}
	clock.advance(29*time.Minute + 59*time.Second) // 10:59:59, the same hour
	if _, err := svc.RequestOtp(context.Background(), allowed[1]); !errors.Is(err, ErrOtpDeliveryUnavailable) {
		t.Fatalf("the same hour: %v", err)
	}
	clock.advance(time.Second) // 11:00:00
	if _, err := svc.RequestOtp(context.Background(), allowed[2]); err != nil {
		t.Fatalf("the next hour: %v", err)
	}
	if _, err := svc.RequestOtp(context.Background(), allowed[3]); !errors.Is(err, ErrOtpDeliveryUnavailable) {
		t.Fatalf("the next hour, over its cap: %v", err)
	}
	if sender.total.Load() != 2 {
		t.Errorf("%d sends, want 2", sender.total.Load())
	}
}

// Many requests at once, across many numbers: exactly the cap is sent.
func TestSignInControls_ConcurrentRequestsNeverExceedTheCap(t *testing.T) {
	const (
		goroutines = 50
		hourlyCap  = 10
		rounds     = 20
	)
	allowed := testers(25)
	for round := 0; round < rounds; round++ {
		m := NewMemoryIdentityRepository()
		svc, sender, _ := controlledSvc(t, m, allowed, hourlyCap)
		var ok, refused atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := svc.RequestOtp(context.Background(), allowed[i%len(allowed)])
				switch {
				case err == nil:
					ok.Add(1)
				case errors.Is(err, ErrOtpDeliveryUnavailable):
					refused.Add(1)
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if sender.total.Load() != hourlyCap || ok.Load() != hourlyCap || refused.Load() != goroutines-hourlyCap {
			t.Fatalf("round %d: %d sent, %d ok, %d refused; want %d, %d, %d", round,
				sender.total.Load(), ok.Load(), refused.Load(), hourlyCap, hourlyCap, goroutines-hourlyCap)
		}
		if len(m.otps) != hourlyCap {
			t.Fatalf("round %d: %d live challenges, want %d", round, len(m.otps), hourlyCap)
		}
	}
}

// failingBudgetRepo's budget store fails, as a database might.
type failingBudgetRepo struct {
	*MemoryIdentityRepository
	calls atomic.Int64
}

func (r *failingBudgetRepo) ReserveSignInDelivery(context.Context, time.Time, int) (bool, error) {
	r.calls.Add(1)
	return true, errors.New("pq: connection to 10.0.0.5 refused for +919000000000")
}

func TestSignInControls_AReservationErrorFailsClosed(t *testing.T) {
	repo := &failingBudgetRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	allowed := testers(1)
	svc, sender, _ := controlledSvc(t, repo, allowed, 100)
	reqID, err := svc.RequestOtp(context.Background(), allowed[0])
	if !errors.Is(err, ErrOtpDeliveryUnavailable) || reqID != "" {
		t.Fatalf("(%q, %v), want ErrOtpDeliveryUnavailable", reqID, err)
	}
	if strings.Contains(err.Error(), "10.0.0.5") || strings.Contains(err.Error(), "9000000000") {
		t.Errorf("the store's text leaked: %v", err)
	}
	if repo.calls.Load() != 1 || sender.total.Load() != 0 {
		t.Errorf("%d reservations, %d sends; want 1 and 0", repo.calls.Load(), sender.total.Load())
	}
	if len(repo.otps) != 0 {
		t.Error("the challenge was not withdrawn")
	}
}

// Without the controls nothing changes: any number, no budget touched.
func TestSignInControls_NotConfiguredLeavesRequestOtpUnchanged(t *testing.T) {
	m := NewMemoryIdentityRepository()
	svc := NewIdentityService(m, testRefreshKey)
	sender := newCountingSender()
	svc.SetOtpSender(sender)
	if _, _, ok := svc.SignInDeliveryControls(); ok {
		t.Fatal("controls configured by default")
	}
	for _, n := range []string{"+919876543210", "+14155552671", "+447700900123"} {
		if _, err := svc.RequestOtp(context.Background(), n); err != nil {
			t.Fatalf("%v", err)
		}
	}
	if sender.total.Load() != 3 || len(m.signInDeliveries) != 0 {
		t.Errorf("%d sends, %d budget hours", sender.total.Load(), len(m.signInDeliveries))
	}
	// Without a sender, the controls reserve nothing either: there is no send.
	quiet := NewIdentityService(m, testRefreshKey)
	if err := quiet.SetSignInDeliveryControls(testers(1), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := quiet.RequestOtp(context.Background(), testers(1)[0]); err != nil {
		t.Fatal(err)
	}
	if len(m.signInDeliveries) != 0 {
		t.Error("a request with no sender reserved a delivery")
	}
}

func TestSetSignInDeliveryControls_Validation(t *testing.T) {
	good := testers(3)
	secret := []string{"+919876543210", "919876543211", "+91 98765 43212"}
	for name, tc := range map[string]struct {
		allowed   []string
		hourlyCap int
		mention   string
	}{
		"nil list":          {nil, 5, "no number"},
		"empty list":        {[]string{}, 5, "no number"},
		"non-canonical":     {[]string{good[0], secret[1]}, 5, "entry 2"},
		"spaces":            {[]string{secret[2]}, 5, "entry 1"},
		"empty entry":       {[]string{good[0], ""}, 5, "entry 2"},
		"duplicate":         {[]string{secret[0], good[1], secret[0]}, 5, "entries 1 and 3"},
		"cap 0":             {good, 0, "hourly cap"},
		"cap -1":            {good, -1, "hourly cap"},
		"cap 10001":         {good, MaxSignInHourlyCap + 1, "hourly cap"},
		"cap 0 and no list": {nil, 0, "hourly cap"},
	} {
		t.Run(name, func(t *testing.T) {
			svc := NewIdentityService(NewMemoryIdentityRepository(), testRefreshKey)
			err := svc.SetSignInDeliveryControls(tc.allowed, tc.hourlyCap)
			if !errors.Is(err, ErrInvalidSignInDeliveryControls) || !strings.Contains(err.Error(), tc.mention) {
				t.Fatalf("err = %v, want ErrInvalidSignInDeliveryControls mentioning %q", err, tc.mention)
			}
			for _, n := range append(append([]string{}, secret...), good...) {
				if digits := strings.TrimPrefix(strings.ReplaceAll(n, " ", ""), "+"); strings.Contains(err.Error(), digits[2:]) {
					t.Errorf("the error carries a number: %v", err)
				}
			}
			if _, _, ok := svc.SignInDeliveryControls(); ok {
				t.Error("a refused configuration was applied")
			}
		})
	}
	// A refused change leaves a previous configuration in place.
	svc := NewIdentityService(NewMemoryIdentityRepository(), testRefreshKey)
	if err := svc.SetSignInDeliveryControls(good, MaxSignInHourlyCap); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSignInDeliveryControls(nil, 1); err == nil {
		t.Fatal("accepted an empty list")
	}
	if n, c, ok := svc.SignInDeliveryControls(); !ok || n != 3 || c != MaxSignInHourlyCap {
		t.Errorf("controls after a refused change: (%d, %d, %v)", n, c, ok)
	}
	if err := svc.SetSignInDeliveryControls(good[:1], 1); err != nil {
		t.Errorf("the smallest valid configuration: %v", err)
	}
}

// The allowlist is held as pseudonyms only.
func TestSignInControls_TheAllowlistIsHeldAsPseudonyms(t *testing.T) {
	svc := NewIdentityService(NewMemoryIdentityRepository(), testRefreshKey)
	allowed := testers(3)
	if err := svc.SetSignInDeliveryControls(allowed, 2); err != nil {
		t.Fatal(err)
	}
	if got := len(svc.signInControls.allowed); got != 3 {
		t.Fatalf("%d entries", got)
	}
	for k := range svc.signInControls.allowed {
		if !ValidMSISDNPseudonym(k) {
			t.Errorf("an entry is not a pseudonym: %q", k)
		}
		for _, n := range allowed {
			if strings.Contains(k, n[3:]) {
				t.Errorf("an entry carries a number")
			}
		}
	}
	if printed := fmt.Sprintf("%+v", *svc.signInControls); strings.Contains(printed, allowed[0][3:]) {
		t.Error("the controls' printed form carries a number")
	}
}

// The memory repository's reservation: atomic, exact, per hour, and a refusal
// counts nothing.
func TestMemoryReserveSignInDelivery(t *testing.T) {
	m := NewMemoryIdentityRepository()
	hour := midHour.Truncate(time.Hour)
	var ok atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			h := hour
			if i%2 == 1 {
				h = hour.Add(time.Hour)
			}
			within, err := m.ReserveSignInDelivery(context.Background(), h, 37)
			if err != nil {
				t.Error(err)
			}
			if within && h.Equal(hour) {
				ok.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 37 || m.signInCount(hour) != 37 || m.signInCount(hour.Add(time.Hour)) != 37 {
		t.Fatalf("%d within; counts %d and %d; want 37 each", ok.Load(), m.signInCount(hour), m.signInCount(hour.Add(time.Hour)))
	}
	// A refusal counted nothing: one more is allowed only under a higher limit.
	if within, _ := m.ReserveSignInDelivery(context.Background(), hour, 38); !within {
		t.Error("the refused calls were counted")
	}
	if within, _ := m.ReserveSignInDelivery(context.Background(), hour, 0); within {
		t.Error("a limit of 0 allowed a delivery")
	}
	// The same hour in another location is the same hour.
	ist := time.FixedZone("IST", 5*3600+1800)
	if within, _ := m.ReserveSignInDelivery(context.Background(), hour.In(ist), 39); !within || m.signInCount(hour) != 39 {
		t.Error("the hour key depends on the location")
	}
}
