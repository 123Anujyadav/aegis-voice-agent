package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ValidateOtp read the challenge (a copy), checked attempts, expiry and code
// against that copy, and only then deleted it — with a DeleteOtp that reports
// success whether or not the challenge was still there. A one-time code was
// therefore only one-time when requests arrived one at a time.

type callerKey struct{}

func asCaller(parent context.Context, name string) context.Context {
	return context.WithValue(parent, callerKey{}, name)
}

// orderedOtpRepo holds every GetOtp caller after its read until the test
// releases that caller by name, so the interleaving is chosen, not hoped for.
type orderedOtpRepo struct {
	*MemoryIdentityRepository
	arrived chan string
	mu      sync.Mutex
	gates   map[string]chan struct{}
}

func newOrderedOtpRepo(callers ...string) *orderedOtpRepo {
	r := &orderedOtpRepo{
		MemoryIdentityRepository: NewMemoryIdentityRepository(),
		arrived:                  make(chan string, len(callers)),
		gates:                    map[string]chan struct{}{},
	}
	for _, c := range callers {
		r.gates[c] = make(chan struct{})
	}
	return r
}

func (r *orderedOtpRepo) GetOtp(ctx context.Context, id string) (*OtpChallenge, error) {
	ch, err := r.MemoryIdentityRepository.GetOtp(ctx, id)
	name, _ := ctx.Value(callerKey{}).(string)
	r.mu.Lock()
	gate, held := r.gates[name]
	r.mu.Unlock()
	if held {
		r.arrived <- name
		<-gate
	}
	return ch, err
}

func (r *orderedOtpRepo) waitAllRead(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-r.arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("deadlock guard: not every request reached GetOtp")
		}
	}
}

func (r *orderedOtpRepo) release(name string) { close(r.gates[name]) }

func (r *orderedOtpRepo) sessionCount() int {
	r.MemoryIdentityRepository.mu.RLock()
	defer r.MemoryIdentityRepository.mu.RUnlock()
	return len(r.MemoryIdentityRepository.sessions)
}

type validateResult struct {
	session *UserSession
	token   string
	err     error
}

// goValidate runs ValidateOtp in a goroutine. The returned channel is closed
// after *out is written, so reading *out after receiving from it is ordered.
func goValidate(ctx context.Context, svc *IdentityService, reqID, code string, out *validateResult) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s, tok, err := svc.ValidateOtp(ctx, reqID, code)
		*out = validateResult{s, tok, err}
	}()
	return done
}

func await(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("deadlock guard: %s never finished", what)
	}
}

// TestValidateOtp_ConcurrentCorrectCodesConsumeOnce is the regression: two
// requests with the right code, both having read the challenge before either
// consumed it.
func TestValidateOtp_ConcurrentCorrectCodesConsumeOnce(t *testing.T) {
	repo := newOrderedOtpRepo("a", "b")
	svc := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}

	var a, b validateResult
	aDone := goValidate(asCaller(context.Background(), "a"), svc, reqID, testOtpCode, &a)
	bDone := goValidate(asCaller(context.Background(), "b"), svc, reqID, testOtpCode, &b)
	repo.waitAllRead(t, 2)
	repo.release("a")
	repo.release("b")
	await(t, aDone, "request a")
	await(t, bDone, "request b")

	successes := 0
	var loser validateResult
	for _, r := range []validateResult{a, b} {
		if r.err == nil {
			successes++
		} else {
			loser = r
		}
	}
	if successes != 1 {
		t.Fatalf("%d concurrent validations of one single-use code succeeded, want exactly 1 "+
			"(%d sessions created)", successes, repo.sessionCount())
	}
	if got := repo.sessionCount(); got != 1 {
		t.Errorf("%d sessions created from one code, want 1", got)
	}
	if !errors.Is(loser.err, ErrInvalidOtp) {
		t.Errorf("losing validation: err = %v, want ErrInvalidOtp", loser.err)
	}
	if loser.session != nil || loser.token != "" {
		t.Error("the losing validation returned a session or token")
	}
}

// TestValidateOtp_ConsumedChallengeCannotBeResurrected covers the other
// interleaving: a wrong guess that read the challenge before a correct request
// consumed it must not write the challenge back afterwards. A blind SaveOtp of
// its stale copy did exactly that, making a used code valid again.
func TestValidateOtp_ConsumedChallengeCannotBeResurrected(t *testing.T) {
	repo := newOrderedOtpRepo("right", "wrong")
	svc := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}

	var right, wrong validateResult
	rightDone := goValidate(asCaller(context.Background(), "right"), svc, reqID, testOtpCode, &right)
	wrongDone := goValidate(asCaller(context.Background(), "wrong"), svc, reqID, "000000", &wrong)
	repo.waitAllRead(t, 2)

	// The correct request consumes the challenge and finishes first ...
	repo.release("right")
	await(t, rightDone, "the correct request")
	// ... and only then does the stale wrong guess get to write.
	repo.release("wrong")
	await(t, wrongDone, "the wrong guess")

	if right.err != nil {
		t.Fatalf("the correct validation failed: %v", right.err)
	}
	if !errors.Is(wrong.err, ErrInvalidOtp) {
		t.Errorf("wrong guess: err = %v, want ErrInvalidOtp", wrong.err)
	}
	if _, err := repo.MemoryIdentityRepository.GetOtp(context.Background(), reqID); !errors.Is(err, ErrInvalidOtp) {
		t.Error("the consumed challenge exists again after the stale wrong guess wrote")
	}
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode); err == nil {
		t.Error("the consumed code was accepted a second time")
	}
	if got := repo.sessionCount(); got != 1 {
		t.Errorf("%d sessions created from one code, want 1", got)
	}
}

// TestValidateOtp_ConcurrentWrongGuessesAreAllCounted: three wrong guesses that
// all read the challenge before any wrote used to persist Attempts=1 three
// times. Every one is counted now, so the limit of three still holds.
func TestValidateOtp_ConcurrentWrongGuessesAreAllCounted(t *testing.T) {
	repo := newOrderedOtpRepo("g1", "g2", "g3")
	svc := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
	reqID, err := svc.RequestOtp(context.Background(), "+919876543210")
	if err != nil {
		t.Fatalf("RequestOtp: %v", err)
	}

	var r1, r2, r3 validateResult
	done := []<-chan struct{}{
		goValidate(asCaller(context.Background(), "g1"), svc, reqID, "000001", &r1),
		goValidate(asCaller(context.Background(), "g2"), svc, reqID, "000002", &r2),
		goValidate(asCaller(context.Background(), "g3"), svc, reqID, "000003", &r3),
	}
	repo.waitAllRead(t, 3)
	for _, g := range []string{"g1", "g2", "g3"} {
		repo.release(g)
	}
	for i, d := range done {
		await(t, d, "wrong guess "+string(rune('1'+i)))
	}

	// Three wrong guesses exhaust the challenge: it is gone, and the correct
	// code no longer authenticates.
	if _, err := repo.MemoryIdentityRepository.GetOtp(context.Background(), reqID); !errors.Is(err, ErrInvalidOtp) {
		t.Error("three concurrent wrong guesses did not exhaust the challenge; increments were lost")
	}
	if _, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode); err == nil {
		t.Error("the correct code authenticated after the attempt budget was spent")
	}
	if repo.sessionCount() != 0 {
		t.Error("a session was created")
	}
}

// TestValidateOtp_SequentialContracts pins the ordinary single-request paths
// alongside the concurrency fixes.
func TestValidateOtp_SequentialContracts(t *testing.T) {
	newSvc := func() (*IdentityService, *MemoryIdentityRepository) {
		m := NewMemoryIdentityRepository()
		return withFixedOtp(NewIdentityService(m, []byte("test_secret_key"))), m
	}
	ctx := context.Background()

	t.Run("valid code succeeds once and cannot be reused", func(t *testing.T) {
		svc, m := newSvc()
		reqID, _ := svc.RequestOtp(ctx, "+919876543210")
		if _, _, err := svc.ValidateOtp(ctx, reqID, testOtpCode); err != nil {
			t.Fatalf("first validation: %v", err)
		}
		if _, _, err := svc.ValidateOtp(ctx, reqID, testOtpCode); !errors.Is(err, ErrInvalidOtp) {
			t.Errorf("reuse: err = %v, want ErrInvalidOtp", err)
		}
		if len(m.sessions) != 1 {
			t.Errorf("%d sessions, want 1", len(m.sessions))
		}
	})

	t.Run("wrong code creates no session and leaves the challenge usable", func(t *testing.T) {
		svc, m := newSvc()
		reqID, _ := svc.RequestOtp(ctx, "+919876543210")
		if _, _, err := svc.ValidateOtp(ctx, reqID, "000000"); !errors.Is(err, ErrInvalidOtp) {
			t.Errorf("wrong code: err = %v, want ErrInvalidOtp", err)
		}
		if len(m.sessions) != 0 {
			t.Error("a wrong code created a session")
		}
		if ch, err := m.GetOtp(ctx, reqID); err != nil || ch.Attempts != 1 {
			t.Errorf("challenge after one wrong guess: %+v, %v; want stored with Attempts=1", ch, err)
		}
		if _, _, err := svc.ValidateOtp(ctx, reqID, testOtpCode); err != nil {
			t.Errorf("the correct code was refused after one wrong guess: %v", err)
		}
	})

	t.Run("missing challenge creates no session", func(t *testing.T) {
		svc, m := newSvc()
		if _, _, err := svc.ValidateOtp(ctx, "req_missing", testOtpCode); !errors.Is(err, ErrInvalidOtp) {
			t.Errorf("err = %v, want ErrInvalidOtp", err)
		}
		if len(m.sessions) != 0 {
			t.Error("a session was created")
		}
	})

	t.Run("expired challenge creates no session", func(t *testing.T) {
		svc, m := newSvc()
		_ = m.SaveOtp(ctx, &OtpChallenge{
			RequestID: "req_old", MSISDNPseudonym: mp("+919876543210"), SubscriberID: sid("+919876543210"),
			CodeHash: svc.hashString(testOtpCode), ExpiresAt: time.Now().Add(-time.Minute),
		})
		if _, _, err := svc.ValidateOtp(ctx, "req_old", testOtpCode); !errors.Is(err, ErrExpiredOtp) {
			t.Errorf("err = %v, want ErrExpiredOtp", err)
		}
		if len(m.sessions) != 0 {
			t.Error("a session was created")
		}
	})
}

// TestRepo_OtpAtomicOperations pins the repository contract both fixes rest on.
func TestRepo_OtpAtomicOperations(t *testing.T) {
	ctx := context.Background()
	seed := func(m *MemoryIdentityRepository) {
		_ = m.SaveOtp(ctx, &OtpChallenge{RequestID: "req_1", MSISDNPseudonym: mp("+919876543210"), ExpiresAt: time.Now().Add(time.Minute)})
	}

	m := NewMemoryIdentityRepository()
	seed(m)
	if ok, err := m.ConsumeOtp(ctx, "req_1"); !ok || err != nil {
		t.Fatalf("first ConsumeOtp = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := m.ConsumeOtp(ctx, "req_1"); ok || err != nil {
		t.Errorf("second ConsumeOtp = (%v, %v), want (false, nil)", ok, err)
	}

	m = NewMemoryIdentityRepository()
	seed(m)
	for want := 1; want <= 3; want++ {
		if got, err := m.RecordFailedOtpAttempt(ctx, "req_1"); got != want || err != nil {
			t.Errorf("RecordFailedOtpAttempt #%d = (%d, %v), want (%d, nil)", want, got, err, want)
		}
	}
	if ok, _ := m.ConsumeOtp(ctx, "req_1"); !ok {
		t.Fatal("ConsumeOtp failed")
	}
	if _, err := m.RecordFailedOtpAttempt(ctx, "req_1"); !errors.Is(err, ErrInvalidOtp) {
		t.Errorf("attempt on a consumed challenge: err = %v, want ErrInvalidOtp", err)
	}
	if _, err := m.GetOtp(ctx, "req_1"); !errors.Is(err, ErrInvalidOtp) {
		t.Error("recording an attempt re-created a consumed challenge")
	}
}
