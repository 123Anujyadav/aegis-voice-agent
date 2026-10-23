package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// docs/domain/10-identity.md requires "Change on failure and lockout, for
// abuse detection". Nothing was recorded at all before these events existed.

// recordingSecurityAuditor captures events, optionally refusing them.
type recordingSecurityAuditor struct {
	mu     sync.Mutex
	events []SecurityEvent
	err    error
}

func (a *recordingSecurityAuditor) Record(e SecurityEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return a.err
}

func (a *recordingSecurityAuditor) snapshot() []SecurityEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]SecurityEvent(nil), a.events...)
}

func countType(events []SecurityEvent, typ SecurityEventType) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func newAuditedSvc(t *testing.T) (*IdentityService, *MemoryIdentityRepository, *fakeClock, *recordingSecurityAuditor) {
	t.Helper()
	svc, repo, clock := newRateLimitedSvc(t)
	aud := &recordingSecurityAuditor{}
	svc.auditor = aud
	return svc, repo, clock, aud
}

func TestAudit_DefaultIsNoop(t *testing.T) {
	svc := NewIdentityService(NewMemoryIdentityRepository(), []byte("k"))
	if _, ok := svc.auditor.(NoopSecurityAuditor); !ok {
		t.Errorf("default auditor is %T, want NoopSecurityAuditor", svc.auditor)
	}
}

// 1 and 7: one wrong code, one failure event with the right type, time and
// challenge id.
func TestAudit_WrongCodeEmitsOneFailure(t *testing.T) {
	svc, repo, clock, aud := newAuditedSvc(t)
	seedChallengeFor(context.Background(), t, repo, svc, "req_a", time.Now().Add(time.Hour))
	at := clock.now()

	if _, _, err := svc.ValidateOtp(context.Background(), "req_a", "000000"); !errors.Is(err, ErrInvalidOtp) {
		t.Fatalf("err = %v, want ErrInvalidOtp", err)
	}
	ev := aud.snapshot()
	if len(ev) != 1 {
		t.Fatalf("%d events, want 1: %+v", len(ev), ev)
	}
	if ev[0].Type != EventOtpVerificationFailed || !ev[0].At.Equal(at) || ev[0].RequestID != "req_a" || !ev[0].LockedUntil.IsZero() {
		t.Errorf("event = %+v, want a failure at %v for req_a", ev[0], at)
	}
}

// 2: a correct code emits nothing here (success is audited at Subscriber level).
func TestAudit_CorrectCodeEmitsNothing(t *testing.T) {
	svc, repo, _, aud := newAuditedSvc(t)
	seedChallengeFor(context.Background(), t, repo, svc, "req_a", time.Now().Add(time.Hour))
	if _, _, err := svc.ValidateOtp(context.Background(), "req_a", testOtpCode); err != nil {
		t.Fatalf("ValidateOtp: %v", err)
	}
	if ev := aud.snapshot(); len(ev) != 0 {
		t.Errorf("a correct code emitted %+v", ev)
	}
}

// 3: nothing that is not a wrong code against a live challenge is reported as
// one.
func TestAudit_NoFalseFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("missing, expired, exhausted, malformed", func(t *testing.T) {
		svc, repo, _, aud := newAuditedSvc(t)
		seedChallengeFor(ctx, t, repo, svc, "req_old", time.Now().Add(-time.Minute))
		_ = repo.SaveOtp(ctx, &OtpChallenge{
			RequestID: "req_spent", MSISDNPseudonym: mp(rlMSISDN), SubscriberID: sid(rlMSISDN), CodeHash: svc.hashString(testOtpCode),
			Attempts: maxOtpAttempts, ExpiresAt: time.Now().Add(time.Hour),
		})
		_, _, _ = svc.ValidateOtp(ctx, "req_missing", "000000")
		_, _, _ = svc.ValidateOtp(ctx, "req_old", "000000")
		_, _, _ = svc.ValidateOtp(ctx, "req_spent", "000000")
		_, _ = svc.RequestOtp(ctx, "14155552671")
		if ev := aud.snapshot(); len(ev) != 0 {
			t.Errorf("non-failures were audited as failures: %+v", ev)
		}
	})

	t.Run("already consumed", func(t *testing.T) {
		svc, repo, _, aud := newAuditedSvc(t)
		seedChallengeFor(ctx, t, repo, svc, "req_a", time.Now().Add(time.Hour))
		if _, _, err := svc.ValidateOtp(ctx, "req_a", testOtpCode); err != nil {
			t.Fatal(err)
		}
		_, _, _ = svc.ValidateOtp(ctx, "req_a", "000000")
		if ev := aud.snapshot(); len(ev) != 0 {
			t.Errorf("a guess at a consumed challenge was audited: %+v", ev)
		}
	})

	// A wrong guess that read the challenge before a correct request consumed
	// it: not a failure against a live challenge, and its provisional failure
	// is withdrawn rather than counted.
	t.Run("consumed concurrently", func(t *testing.T) {
		repo := newOrderedOtpRepo("right", "wrong")
		svc := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
		aud := &recordingSecurityAuditor{}
		svc.auditor = aud
		seedChallengeFor(ctx, t, repo.MemoryIdentityRepository, svc, "req_a", time.Now().Add(time.Hour))

		var right, wrong validateResult
		rightDone := goValidate(asCaller(ctx, "right"), svc, "req_a", testOtpCode, &right)
		wrongDone := goValidate(asCaller(ctx, "wrong"), svc, "req_a", "000000", &wrong)
		repo.waitAllRead(t, 2)
		repo.release("right")
		await(t, rightDone, "the correct request")
		repo.release("wrong")
		await(t, wrongDone, "the wrong guess")

		if right.err != nil || !errors.Is(wrong.err, ErrInvalidOtp) {
			t.Fatalf("right=%v wrong=%v", right.err, wrong.err)
		}
		if ev := aud.snapshot(); len(ev) != 0 {
			t.Errorf("the losing guess was audited: %+v", ev)
		}
		repo.MemoryIdentityRepository.mu.RLock()
		st := repo.MemoryIdentityRepository.otpRates[mp(rlMSISDN)]
		left := len(st.failures)
		repo.MemoryIdentityRepository.mu.RUnlock()
		if left != 0 {
			t.Errorf("%d failures left counted, want 0", left)
		}
	})
}

// 4, 5, 7: the fifth failure emits exactly one lockout event; refusals during
// the lockout emit nothing more.
func TestAudit_LockoutIsReportedOnce(t *testing.T) {
	svc, repo, clock, aud := newAuditedSvc(t)
	ctx := context.Background()
	live := time.Now().Add(time.Hour)
	seedChallengeFor(ctx, t, repo, svc, "req_a", live)
	seedChallengeFor(ctx, t, repo, svc, "req_b", live)

	var fifthAt time.Time
	for _, id := range []string{"req_a", "req_a", "req_a", "req_b", "req_b"} {
		fifthAt = clock.now()
		_, _, _ = svc.ValidateOtp(ctx, id, "000000")
		clock.advance(time.Second)
	}
	ev := aud.snapshot()
	if countType(ev, EventOtpVerificationFailed) != 5 || countType(ev, EventOtpLockout) != 1 {
		t.Fatalf("events = %+v; want 5 failures and 1 lockout", ev)
	}
	lock := ev[len(ev)-1]
	if lock.Type != EventOtpLockout || !lock.At.Equal(fifthAt) || lock.RequestID != "req_b" ||
		!lock.LockedUntil.Equal(fifthAt.Add(15*time.Minute)) {
		t.Errorf("lockout event = %+v, want at %v, req_b, until %v", lock, fifthAt, fifthAt.Add(15*time.Minute))
	}

	// During the lockout: refused verifications and requests add nothing.
	_, _, _ = svc.ValidateOtp(ctx, "req_b", "000000")
	_, _, _ = svc.ValidateOtp(ctx, "req_b", testOtpCode)
	_, _ = svc.RequestOtp(ctx, rlMSISDN)
	if after := aud.snapshot(); len(after) != len(ev) {
		t.Errorf("refusals during the lockout emitted %d more event(s)", len(after)-len(ev))
	}
}

// 6: seven concurrent wrong guesses: five evaluated, so five failures and
// exactly one lockout, however the guesses interleave.
func TestAudit_ConcurrentGuessesReportOneLockout(t *testing.T) {
	names := []string{"g1", "g2", "g3", "g4", "g5", "g6", "g7"}
	repo := newOrderedOtpRepo(names...)
	svc := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
	clock := &fakeClock{t: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
	svc.now = clock.now
	aud := &recordingSecurityAuditor{}
	svc.auditor = aud
	live := time.Now().Add(time.Hour)
	for _, id := range []string{"req_1", "req_2", "req_3", "req_4"} {
		seedChallengeFor(context.Background(), t, repo.MemoryIdentityRepository, svc, id, live)
	}
	ids := []string{"req_1", "req_1", "req_2", "req_2", "req_3", "req_3", "req_4"}

	results := make([]validateResult, len(ids))
	done := make([]<-chan struct{}, len(ids))
	for i, id := range ids {
		done[i] = goValidate(asCaller(context.Background(), names[i]), svc, id, "000000", &results[i])
	}
	repo.waitAllRead(t, len(ids))
	for _, n := range names {
		repo.release(n)
	}
	for i := range ids {
		await(t, done[i], names[i])
	}

	// Every reservation shares one timestamp under this clock; only the
	// reservation id can tell which one locked the number.
	ev := aud.snapshot()
	if f, l := countType(ev, EventOtpVerificationFailed), countType(ev, EventOtpLockout); f != 5 || l != 1 {
		t.Errorf("events: %d failures, %d lockouts; want 5 and 1", f, l)
	}
}

// 8: no event carries the number, a code, a code hash, a token or the key.
func TestAudit_EventsCarryNoSecretsOrPersonalData(t *testing.T) {
	svc, repo, clock, aud := newAuditedSvc(t)
	ctx := context.Background()
	live := time.Now().Add(time.Hour)
	seedChallengeFor(ctx, t, repo, svc, "req_a", live)
	seedChallengeFor(ctx, t, repo, svc, "req_b", live)
	for _, id := range []string{"req_a", "req_a", "req_a", "req_b", "req_b"} {
		_, _, _ = svc.ValidateOtp(ctx, id, "000000")
		clock.advance(time.Second)
	}
	forbidden := map[string]string{
		"MSISDN":                 rlMSISDN,
		"MSISDN digits":          strings.TrimPrefix(rlMSISDN, "+"),
		"correct code":           testOtpCode,
		"wrong code":             "000000",
		"correct code hash":      svc.hashString(testOtpCode),
		"wrong code hash":        svc.hashString("000000"),
		"subscriber id material": svc.hashString(rlMSISDN)[:32],
		"secret key":             "test_secret_key",
	}
	for _, e := range aud.snapshot() {
		dump := fmt.Sprintf("%+v %#v", e, e)
		for what, v := range forbidden {
			if strings.Contains(dump, v) {
				t.Errorf("event %s contains the %s", e.Type, what)
			}
		}
	}
	if len(aud.snapshot()) == 0 {
		t.Fatal("no events were produced, so nothing was checked")
	}
}

// 10: a refusing sink changes no outcome. Rejections stay rejections, the
// lockout still applies, a correct code still authenticates, and every refusal
// is counted.
func TestAudit_SinkFailureChangesNoOutcome(t *testing.T) {
	svc, repo, clock, aud := newAuditedSvc(t)
	aud.err = errors.New("audit sink unavailable")
	ctx := context.Background()
	live := time.Now().Add(time.Hour)
	seedChallengeFor(ctx, t, repo, svc, "req_a", live)
	seedChallengeFor(ctx, t, repo, svc, "req_b", live)
	seedChallengeFor(ctx, t, repo, svc, "req_c", live)

	if _, _, err := svc.ValidateOtp(ctx, "req_a", "000000"); !errors.Is(err, ErrInvalidOtp) {
		t.Fatalf("wrong code with a failing sink: err = %v, want ErrInvalidOtp", err)
	}
	if svc.AuditFailures() != 1 {
		t.Errorf("AuditFailures = %d, want 1", svc.AuditFailures())
	}
	if len(repo.sessions) != 0 {
		t.Fatal("a failing sink produced a session")
	}
	if _, _, err := svc.ValidateOtp(ctx, "req_c", testOtpCode); err != nil {
		t.Errorf("a correct code was refused because the sink failed: %v", err)
	}

	for _, id := range []string{"req_a", "req_a", "req_b", "req_b", "req_b"} {
		_, _, _ = svc.ValidateOtp(ctx, id, "000000")
		clock.advance(time.Second)
	}
	// The reset after req_c's success means these five lock the number.
	if _, err := svc.RequestOtp(ctx, rlMSISDN); !errors.Is(err, ErrRateLimited) {
		t.Errorf("the lockout did not apply with a failing sink: %v", err)
	}
	if got := svc.AuditFailures(); got != 7 { // 1 + 5 failures + 1 lockout
		t.Errorf("AuditFailures = %d, want 7", got)
	}
}

// The documented store-failure rule: a code determined WRONG for a live
// challenge stays a counted, audited failure even when the per-challenge
// counter cannot be written, and the store error is still returned.
func TestAudit_WrongCodeWithAFailingAttemptWriteIsStillAudited(t *testing.T) {
	repo := &injectableRepo{MemoryIdentityRepository: NewMemoryIdentityRepository(), failSaveOtp: true}
	svc := withFixedOtp(NewIdentityService(repo, []byte("test_secret_key")))
	aud := &recordingSecurityAuditor{}
	svc.auditor = aud
	seedChallengeFor(context.Background(), t, repo.MemoryIdentityRepository, svc, "req_a", time.Now().Add(time.Hour))

	_, _, err := svc.ValidateOtp(context.Background(), "req_a", "000000")
	if !errors.Is(err, errInternalStore) {
		t.Fatalf("err = %v, want the store's error", err)
	}
	if ev := aud.snapshot(); countType(ev, EventOtpVerificationFailed) != 1 {
		t.Errorf("events = %+v, want one failure", ev)
	}
}
