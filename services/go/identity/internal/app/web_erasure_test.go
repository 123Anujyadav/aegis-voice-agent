package app

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"
)

// Web account deletion (web_erasure.go): codes bound to their purpose, the
// same answer with and without an account, the shared limits and the hourly
// budget, expiry and wrong codes, the retry after a lost answer, and
// concurrent confirms. Delivery is a recording fake, run synchronously.

const noAccountMSISDN = "+919876500077"

var requestIDPattern = regexp.MustCompile(`^req_[0-9a-f]{64}$`)

type recordingDeletionSender struct {
	mu    sync.Mutex
	sent  map[string]string // number -> last code
	calls int
	fail  error
}

func (r *recordingDeletionSender) SendDeletionCode(_ context.Context, msisdn, code string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.fail != nil {
		return r.fail
	}
	r.sent[msisdn] = code
	return nil
}

func (r *recordingDeletionSender) codeFor(t *testing.T, msisdn string) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	code, ok := r.sent[msisdn]
	if !ok {
		t.Fatalf("no deletion code was sent to %s", msisdn)
	}
	return code
}

type webRig struct {
	*erasureRig
	sender *recordingDeletionSender
}

// newWebRig: the erasure rig (subscriber A on two devices, B on one) with a
// deletion-code sender, a budget, and synchronous dispatch.
func newWebRig(ctx context.Context, t *testing.T, budget int) *webRig {
	t.Helper()
	e := newErasureRig(ctx, t)
	sender := &recordingDeletionSender{sent: map[string]string{}}
	e.svc.SetDeletionCodeSender(sender)
	e.svc.SetDeletionCodeBudget(budget)
	e.svc.deletionDispatch = func(send func()) { send() }
	return &webRig{erasureRig: e, sender: sender}
}

func (w *webRig) request(ctx context.Context, t *testing.T, msisdn string) string {
	t.Helper()
	id, err := w.svc.RequestDeletionCode(ctx, msisdn)
	if err != nil {
		t.Fatalf("RequestDeletionCode(%s): %v", msisdn, err)
	}
	return id
}

func TestWebDeletion_UnavailableWithoutASenderOrABudget(t *testing.T) {
	ctx := context.Background()
	for name, configure := range map[string]func(*IdentityService){
		"no sender": func(s *IdentityService) { s.SetDeletionCodeBudget(10) },
		"no budget": func(s *IdentityService) { s.SetDeletionCodeSender(&recordingDeletionSender{sent: map[string]string{}}) },
	} {
		e := newErasureRig(ctx, t)
		configure(e.svc)
		for _, n := range []string{rlMSISDN, noAccountMSISDN} {
			if _, err := e.svc.RequestDeletionCode(ctx, n); !errors.Is(err, ErrDeletionCodesUnavailable) {
				t.Errorf("%s, %s: err = %v, want ErrDeletionCodesUnavailable for every number", name, n, err)
			}
		}
		if err := e.svc.ConfirmDeletion(ctx, "req_x", "123456"); !errors.Is(err, ErrDeletionCodesUnavailable) {
			t.Errorf("%s: confirm err = %v", name, err)
		}
	}
}

// The account is erased on every device with a code only its number received;
// the erasure is recorded and audited with the request's random id.
func TestWebDeletion_ErasesTheAccountEverywhere(t *testing.T) {
	ctx := context.Background()
	w := newWebRig(ctx, t, 100)
	id := w.request(ctx, t, rlMSISDN)
	if !requestIDPattern.MatchString(id) {
		t.Fatalf("request id %q", id)
	}
	if err := w.svc.ConfirmDeletion(ctx, id, w.sender.codeFor(t, rlMSISDN)); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if w.sessionsOf(sid(rlMSISDN)) != 0 {
		t.Error("a session of the erased account survived")
	}
	if !w.sessionExists(ctx, t, w.sessB.SessionID) {
		t.Error("another subscriber's session was erased")
	}
	if n := len(w.repo.Erasures()); n != 1 {
		t.Errorf("%d erasure records, want 1", n)
	}
	if w.deletionsAudited() != 1 || w.audit.events[len(w.audit.events)-1].RequestID != id {
		t.Errorf("audit = %+v, want one deletion naming the request", w.audit.events)
	}
}

// A number without an account gets the same answer (a request id), the same
// refusals, and no message; nothing is erased or recorded.
func TestWebDeletion_ANumberWithoutAnAccountLooksTheSame(t *testing.T) {
	ctx := context.Background()
	w := newWebRig(ctx, t, 100)
	withAccount := w.request(ctx, t, rlMSISDN)
	without := w.request(ctx, t, noAccountMSISDN)
	if !requestIDPattern.MatchString(without) || len(without) != len(withAccount) {
		t.Fatalf("answers differ in shape: %q vs %q", without, withAccount)
	}
	if w.sender.calls != 1 {
		t.Fatalf("%d sends, want 1: only the number with an account gets a code", w.sender.calls)
	}
	stored, err := w.repo.GetOtp(ctx, without)
	if err != nil || stored.Purpose != OtpPurposeAccountDeletion {
		t.Fatalf("the challenge for a number without an account must exist like any other: %v %+v", err, stored)
	}
	for _, guess := range []string{"000000", "123456"} {
		if err := w.svc.ConfirmDeletion(ctx, without, guess); !errors.Is(err, ErrInvalidOtp) {
			t.Errorf("guess %s: err = %v, want ErrInvalidOtp (as for a wrong code on an account)", guess, err)
		}
	}
	if n := len(w.repo.Erasures()); n != 0 {
		t.Errorf("%d erasures for a number without an account", n)
	}
}

// A sign-in code cannot delete an account, and a deletion code cannot sign in.
func TestWebDeletion_CodesAreBoundToTheirPurpose(t *testing.T) {
	ctx := context.Background()
	w := newWebRig(ctx, t, 100)
	w.challenge(ctx, t, "req_signin") // a sign-in challenge for A, code testOtpCode
	if err := w.svc.ConfirmDeletion(ctx, "req_signin", testOtpCode); !errors.Is(err, ErrInvalidOtp) {
		t.Errorf("a sign-in code confirmed a deletion: %v", err)
	}
	id := w.request(ctx, t, rlMSISDN)
	k := deviceKey(t)
	_, _, err := w.svc.ValidateOtpWithDeviceProof(ctx, id, w.sender.codeFor(t, rlMSISDN), "POST", enrollProof(t, k, k, "purpose", "POST", w.clock.now()))
	if !errors.Is(err, ErrInvalidOtp) {
		t.Errorf("a deletion code signed in: %v", err)
	}
	if len(w.repo.Erasures()) != 0 || !w.sessionExists(ctx, t, w.sessA1.SessionID) {
		t.Error("something was erased by a code of the wrong purpose")
	}
}

// Wrong codes are refused and counted: three exhaust the challenge, after
// which even the right code fails. An expired code is refused as expired.
func TestWebDeletion_WrongAndExpiredCodes(t *testing.T) {
	ctx := context.Background()
	w := newWebRig(ctx, t, 100)
	id := w.request(ctx, t, rlMSISDN)
	right := w.sender.codeFor(t, rlMSISDN)
	wrong := "000000"
	if wrong == right {
		wrong = "111111"
	}
	for i := range maxOtpAttempts {
		if err := w.svc.ConfirmDeletion(ctx, id, wrong); !errors.Is(err, ErrInvalidOtp) {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	if err := w.svc.ConfirmDeletion(ctx, id, right); !errors.Is(err, ErrInvalidOtp) {
		t.Errorf("the right code after the attempts were exhausted: %v", err)
	}

	id2 := w.request(ctx, t, rlMSISDN)
	w.repo.mu.Lock()
	w.repo.otps[id2].ExpiresAt = time.Now().Add(-time.Second)
	w.repo.mu.Unlock()
	if err := w.svc.ConfirmDeletion(ctx, id2, w.sender.codeFor(t, rlMSISDN)); !errors.Is(err, ErrExpiredOtp) {
		t.Errorf("an expired code: %v, want ErrExpiredOtp", err)
	}
	if len(w.repo.Erasures()) != 0 || w.sessionsOf(sid(rlMSISDN)) != 2 {
		t.Error("a refused code erased something")
	}
}

// The number's request limit and failure lockout are SHARED with sign-in.
func TestWebDeletion_SharesTheNumbersLimitsWithSignIn(t *testing.T) {
	ctx := context.Background()
	w := newWebRig(ctx, t, 100)
	limit := DefaultOtpRatePolicy().MaxRequests
	for range limit - 1 {
		if _, err := w.svc.RequestOtp(ctx, noAccountMSISDN); err != nil {
			t.Fatal(err)
		}
	}
	w.request(ctx, t, noAccountMSISDN) // the last request in the window
	var rl *RateLimitError
	if _, err := w.svc.RequestDeletionCode(ctx, noAccountMSISDN); !errors.As(err, &rl) {
		t.Errorf("beyond the shared request limit: err = %v, want a rate limit", err)
	}
	if _, err := w.svc.RequestOtp(ctx, noAccountMSISDN); !errors.As(err, &rl) {
		t.Errorf("sign-in after deletion requests used the allowance: err = %v, want a rate limit", err)
	}

	// Five wrong deletion codes within the window (three exhaust one
	// challenge, two more on a second) lock the number, for sign-in too.
	wrong := 0
	for wrong < DefaultOtpRatePolicy().MaxFailures {
		id := w.request(ctx, t, rlMSISDN)
		for range maxOtpAttempts {
			if wrong == DefaultOtpRatePolicy().MaxFailures {
				break
			}
			if err := w.svc.ConfirmDeletion(ctx, id, "000000"); !errors.Is(err, ErrInvalidOtp) {
				t.Fatalf("wrong code %d: %v", wrong, err)
			}
			wrong++
		}
	}
	w.challenge(ctx, t, "req_after_lockout")
	k := deviceKey(t)
	if _, _, err := w.svc.ValidateOtpWithDeviceProof(ctx, "req_after_lockout", testOtpCode, "POST", enrollProof(t, k, k, "lock", "POST", w.clock.now())); !errors.As(err, &rl) {
		t.Errorf("sign-in after five wrong deletion codes: err = %v, want the lockout", err)
	}
}

// The hourly budget counts every request, with or without an account.
func TestWebDeletion_TheHourlyBudget(t *testing.T) {
	ctx := context.Background()
	w := newWebRig(ctx, t, 2)
	w.request(ctx, t, noAccountMSISDN)
	w.request(ctx, t, rlMSISDN)
	for _, n := range []string{otherMSISDN, rlMSISDN} {
		if _, err := w.svc.RequestDeletionCode(ctx, n); !errors.Is(err, ErrDeletionCodesUnavailable) {
			t.Errorf("%s over the budget: err = %v, want ErrDeletionCodesUnavailable", n, err)
		}
	}
	w.clock.advance(time.Hour)
	w.request(ctx, t, otherMSISDN) // a new hour
}

// A retry after a lost answer is told "done" with the same request and code;
// anything else is refused; nothing is erased or audited twice.
func TestWebDeletion_RetryAfterALostAnswer(t *testing.T) {
	ctx := context.Background()
	w := newWebRig(ctx, t, 100)
	id := w.request(ctx, t, rlMSISDN)
	code := w.sender.codeFor(t, rlMSISDN)
	if err := w.svc.ConfirmDeletion(ctx, id, code); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.ConfirmDeletion(ctx, id, code); err != nil {
		t.Errorf("the retry: %v, want success", err)
	}
	if err := w.svc.ConfirmDeletion(ctx, id, "000000"); !errors.Is(err, ErrInvalidOtp) {
		t.Errorf("a retry with another code: %v", err)
	}
	if len(w.repo.Erasures()) != 1 || w.deletionsAudited() != 1 {
		t.Errorf("%d erasures, %d audit events; a retry records nothing", len(w.repo.Erasures()), w.deletionsAudited())
	}
}

// confirmBarrierRepo makes concurrent confirms all pass the code check before any
// erases (ReserveOtpVerification sits just before the comparison).
type confirmBarrierRepo struct {
	*MemoryIdentityRepository
	wg *sync.WaitGroup
}

func (b *confirmBarrierRepo) ReserveOtpVerification(ctx context.Context, pseudonym string, at time.Time, p OtpRatePolicy) (uint64, error) {
	id, err := b.MemoryIdentityRepository.ReserveOtpVerification(ctx, pseudonym, at, p)
	b.wg.Done()
	b.wg.Wait()
	return id, err
}

// Concurrent confirms with the right code: all succeed, and exactly one
// erasure is recorded and audited.
func TestWebDeletion_ConcurrentConfirmsEraseOnce(t *testing.T) {
	ctx := context.Background()
	w := newWebRig(ctx, t, 100)
	id := w.request(ctx, t, rlMSISDN)
	code := w.sender.codeFor(t, rlMSISDN)
	const n = 3
	var barrier sync.WaitGroup
	barrier.Add(n)
	w.svc.repo = &confirmBarrierRepo{MemoryIdentityRepository: w.repo, wg: &barrier}

	errs := make([]error, n)
	var done sync.WaitGroup
	for i := range n {
		done.Add(1)
		go func() {
			defer done.Done()
			errs[i] = w.svc.ConfirmDeletion(ctx, id, code)
		}()
	}
	done.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("confirm %d: %v", i, err)
		}
	}
	if len(w.repo.Erasures()) != 1 || w.deletionsAudited() != 1 {
		t.Errorf("%d erasures, %d audit events, want 1 and 1", len(w.repo.Erasures()), w.deletionsAudited())
	}
}

// A code that could not be delivered is withdrawn, and the answer is still
// the same.
func TestWebDeletion_AnUndeliveredCodeIsWithdrawn(t *testing.T) {
	ctx := context.Background()
	w := newWebRig(ctx, t, 100)
	w.sender.fail = errors.New("provider down: +919876500001")
	id := w.request(ctx, t, rlMSISDN)
	if _, err := w.repo.GetOtp(ctx, id); !errors.Is(err, ErrInvalidOtp) {
		t.Errorf("an undelivered code's challenge survived: %v", err)
	}
}
