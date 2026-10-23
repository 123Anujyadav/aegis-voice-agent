package pgstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// The repository and replay-store contract, written once and run against every
// backend. Each case gets two handles, a and b. For the in-memory backend they
// are the same object; for PostgreSQL they are independent pools on one
// database, so every "concurrent" case below is then a cross-instance case.

type backend struct {
	a, b   app.IdentityRepository
	ra, rb dpop.ReplayStore
}

var (
	t0     = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	policy = app.OtpRatePolicy{MaxRequests: 3, MaxFailures: 5, Window: 15 * time.Minute, Lockout: 15 * time.Minute}
	ctx    = context.Background()
)

// pick alternates between the two handles.
func (be backend) pick(i int) app.IdentityRepository {
	if i%2 == 0 {
		return be.a
	}
	return be.b
}

func (be backend) pickReplay(i int) dpop.ReplayStore {
	if i%2 == 0 {
		return be.ra
	}
	return be.rb
}

// race runs n calls of fn concurrently, released together.
func race(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn(i)
		}()
	}
	close(start)
	wg.Wait()
}

// contractKey is a test-only HMAC key: refresh-token values at the repository
// boundary are app.HashRefreshToken outputs, never raw tokens.
var contractKey = []byte("contract-test-hmac-key")

// th is the stored form of a test token label.
func th(label string) string { return app.HashRefreshToken(contractKey, label) }

// pk is the stored form of a test phone number: its MSISDN pseudonym. No raw
// number is ever handed to a repository.
func pk(raw string) string {
	p, err := app.MSISDNPseudonym(contractKey, raw)
	if err != nil {
		panic(fmt.Sprintf("test fixture %q is not canonical E.164: %v", raw, err))
	}
	return p
}

var contract = []struct {
	name string
	fn   func(t *testing.T, be backend)
}{
	{"OtpRoundTripAndCopies", testOtpRoundTrip},
	{"ConsumeOtpHasExactlyOneWinner", testConsumeOnce},
	{"FailedAttemptsCountAndNeverResurrect", testFailedAttempts},
	{"RequestLimit", testRequestLimit},
	{"VerificationReservationsAndLockout", testVerificationLock},
	{"SessionRoundTripExpiryAndRevocation", testSessionRoundTrip},
	{"ReplaceSessionIsACompareAndSwap", testReplaceCAS},
	{"RevocationCannotBeUndone", testRevocationSticky},
	{"EnrolledKeyIsImmutable", testKeyImmutable},
	{"DevicesAndDelegations", testDevicesAndDelegations},
	{"ReplayReservations", testReplay},
	{"RefreshTokensAreStoredOnlyAsHashes", testRefreshTokensOnlyAsHashes},
	{"PhoneNumbersAreKeyedOnlyByPseudonym", testPhoneNumbersOnlyAsPseudonyms},
}

func runContract(t *testing.T, open func(t *testing.T) backend) {
	for _, c := range contract {
		t.Run(c.name, func(t *testing.T) { c.fn(t, open(t)) })
	}
}

func memoryBackend(*testing.T) backend {
	m := app.NewMemoryIdentityRepository()
	r := dpop.NewMemoryReplayStore()
	return backend{a: m, b: m, ra: r, rb: r}
}

// TestContract_Memory runs the contract against the in-memory implementations,
// which is what makes the suite a statement about the CONTRACT rather than
// about one backend.
func TestContract_Memory(t *testing.T) { runContract(t, memoryBackend) }

func testOtpRoundTrip(t *testing.T, be backend) {
	c := &app.OtpChallenge{RequestID: "req_1", MSISDNPseudonym: pk("+14155552671"), SubscriberID: "usr_1", CodeHash: "h1", Attempts: 1, ExpiresAt: t0.Add(5 * time.Minute)}
	if err := be.a.SaveOtp(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := be.b.GetOtp(ctx, "req_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.MSISDNPseudonym != c.MSISDNPseudonym || got.SubscriberID != c.SubscriberID || got.CodeHash != c.CodeHash || got.Attempts != 1 || !got.ExpiresAt.Equal(c.ExpiresAt) {
		t.Errorf("round trip: %+v, want %+v", got, c)
	}
	got.Attempts = 99
	c.Attempts = 98
	if again, _ := be.b.GetOtp(ctx, "req_1"); again.Attempts != 1 {
		t.Error("a caller's copy reached stored state")
	}
	if err := be.b.SaveOtp(ctx, &app.OtpChallenge{RequestID: "req_1", MSISDNPseudonym: c.MSISDNPseudonym, SubscriberID: c.SubscriberID, CodeHash: "h2", ExpiresAt: c.ExpiresAt}); err != nil {
		t.Fatal(err)
	}
	if again, _ := be.a.GetOtp(ctx, "req_1"); again.CodeHash != "h2" || again.Attempts != 0 {
		t.Errorf("overwrite: %+v", again)
	}
	if _, err := be.a.GetOtp(ctx, "req_missing"); !errors.Is(err, app.ErrInvalidOtp) {
		t.Errorf("missing: err = %v, want ErrInvalidOtp", err)
	}
	if err := be.a.DeleteOtp(ctx, "req_missing"); err != nil {
		t.Errorf("deleting a missing challenge: %v", err)
	}
	if err := be.b.DeleteOtp(ctx, "req_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := be.a.GetOtp(ctx, "req_1"); !errors.Is(err, app.ErrInvalidOtp) {
		t.Errorf("after delete: err = %v", err)
	}
}

func testConsumeOnce(t *testing.T, be backend) {
	_ = be.a.SaveOtp(ctx, &app.OtpChallenge{RequestID: "req_c", MSISDNPseudonym: pk("+14155552671"), CodeHash: "h", ExpiresAt: t0})
	var mu sync.Mutex
	wins, errs := 0, 0
	race(16, func(i int) {
		ok, err := be.pick(i).ConsumeOtp(ctx, "req_c")
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs++
		}
		if ok {
			wins++
		}
	})
	if wins != 1 || errs != 0 {
		t.Fatalf("%d consumers won (%d errors), want exactly 1", wins, errs)
	}
	if ok, err := be.a.ConsumeOtp(ctx, "req_c"); ok || err != nil {
		t.Errorf("after consumption: (%v, %v), want (false, nil)", ok, err)
	}
}

func testFailedAttempts(t *testing.T, be backend) {
	if _, err := be.a.RecordFailedOtpAttempt(ctx, "req_none"); !errors.Is(err, app.ErrInvalidOtp) {
		t.Errorf("missing challenge: err = %v, want ErrInvalidOtp", err)
	}
	_ = be.a.SaveOtp(ctx, &app.OtpChallenge{RequestID: "req_f", MSISDNPseudonym: pk("+14155552671"), CodeHash: "h", ExpiresAt: t0})
	const n = 20
	seen := make([]int, n)
	race(n, func(i int) {
		v, err := be.pick(i).RecordFailedOtpAttempt(ctx, "req_f")
		if err != nil {
			t.Errorf("increment %d: %v", i, err)
		}
		seen[i] = v
	})
	got := map[int]bool{}
	for _, v := range seen {
		got[v] = true
	}
	for v := 1; v <= n; v++ {
		if !got[v] {
			t.Errorf("no increment returned %d: increments were lost or duplicated (%v)", v, seen)
		}
	}
	if c, _ := be.b.GetOtp(ctx, "req_f"); c == nil || c.Attempts != n {
		t.Errorf("stored attempts = %+v, want %d", c, n)
	}
	if ok, _ := be.a.ConsumeOtp(ctx, "req_f"); !ok {
		t.Fatal("consume")
	}
	if _, err := be.b.RecordFailedOtpAttempt(ctx, "req_f"); !errors.Is(err, app.ErrInvalidOtp) {
		t.Errorf("after consumption: err = %v, want ErrInvalidOtp", err)
	}
	if _, err := be.a.GetOtp(ctx, "req_f"); !errors.Is(err, app.ErrInvalidOtp) {
		t.Error("a failed attempt resurrected a consumed challenge")
	}
}

func wantRetryAt(t *testing.T, err error, want time.Time) {
	t.Helper()
	var rl *app.RateLimitError
	if !errors.As(err, &rl) || !errors.Is(err, app.ErrRateLimited) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if !rl.RetryAt.Equal(want) {
		t.Errorf("RetryAt = %v, want %v", rl.RetryAt, want)
	}
}

func testRequestLimit(t *testing.T, be backend) {
	m := pk("+14155550001")
	for i := 0; i < 3; i++ {
		if err := be.pick(i).ReserveOtpRequest(ctx, m, t0.Add(time.Duration(i)*time.Minute), policy); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	wantRetryAt(t, be.a.ReserveOtpRequest(ctx, m, t0.Add(3*time.Minute), policy), t0.Add(15*time.Minute))
	if err := be.b.ReserveOtpRequest(ctx, m, t0.Add(15*time.Minute), policy); err != nil {
		t.Errorf("after the oldest left the window: %v", err)
	}
	if err := be.a.ReserveOtpRequest(ctx, pk("+14155550002"), t0, policy); err != nil {
		t.Errorf("another number: %v", err)
	}

	c := pk("+14155550003")
	var mu sync.Mutex
	ok, limited := 0, 0
	race(12, func(i int) {
		err := be.pick(i).ReserveOtpRequest(ctx, c, t0, policy)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil:
			ok++
		case errors.Is(err, app.ErrRateLimited):
			limited++
		default:
			t.Errorf("request: %v", err)
		}
	})
	if ok != policy.MaxRequests || limited != 12-policy.MaxRequests {
		t.Errorf("concurrent requests: %d admitted, %d limited; want %d admitted", ok, limited, policy.MaxRequests)
	}
}

func testVerificationLock(t *testing.T, be backend) {
	m := pk("+14155550011")
	ids := map[uint64]bool{}
	var last uint64
	for i := 0; i < 5; i++ {
		id, err := be.pick(i).ReserveOtpVerification(ctx, m, t0.Add(time.Duration(i)*time.Minute), policy)
		if err != nil || id == 0 || ids[id] {
			t.Fatalf("reservation %d: (%d, %v), want a fresh non-zero id", i, id, err)
		}
		ids[id] = true
		last = id
	}
	lockEnd := t0.Add(4*time.Minute + policy.Lockout)
	for id := range ids {
		got, err := be.b.ConfirmOtpFailure(ctx, m, id)
		if err != nil {
			t.Fatal(err)
		}
		if id == last && !got.Equal(lockEnd) {
			t.Errorf("the locking reservation reports %v, want %v", got, lockEnd)
		}
		if id != last && !got.IsZero() {
			t.Errorf("reservation %d reports a lockout it did not set", id)
		}
	}
	_, err := be.a.ReserveOtpVerification(ctx, m, t0.Add(5*time.Minute), policy)
	wantRetryAt(t, err, lockEnd)

	// Releasing a reservation that did not lock leaves the lock.
	var other uint64
	for id := range ids {
		if id != last {
			other = id
			break
		}
	}
	if err := be.b.ReleaseOtpVerification(ctx, m, other); err != nil {
		t.Fatal(err)
	}
	if got, _ := be.a.ConfirmOtpFailure(ctx, m, last); !got.Equal(lockEnd) {
		t.Error("releasing another reservation withdrew the lockout")
	}
	// Releasing the locking reservation withdraws the lock with it.
	if err := be.a.ReleaseOtpVerification(ctx, m, last); err != nil {
		t.Fatal(err)
	}
	if got, _ := be.b.ConfirmOtpFailure(ctx, m, last); !got.IsZero() {
		t.Error("the released reservation still reports a lockout")
	}
	if _, err := be.b.ReserveOtpVerification(ctx, m, t0.Add(6*time.Minute), policy); err != nil {
		t.Errorf("after the lock was withdrawn: %v", err)
	}
	if err := be.a.ReleaseOtpVerification(ctx, pk("+14155550099"), 12345); err != nil {
		t.Errorf("releasing for a number with no state: %v", err)
	}
	// A success clears failures and lockout.
	if err := be.a.ResetOtpFailures(ctx, m); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := be.pick(i).ReserveOtpVerification(ctx, m, t0.Add(7*time.Minute), policy); err != nil {
			t.Fatalf("after reset, reservation %d: %v", i, err)
		}
	}
	if err := be.b.ResetOtpFailures(ctx, pk("+14155550098")); err != nil {
		t.Errorf("resetting a number with no state: %v", err)
	}

	// Concurrent reservations: exactly MaxFailures admitted, exactly one locks.
	c := pk("+14155550012")
	var mu sync.Mutex
	var admitted []uint64
	race(12, func(i int) {
		id, err := be.pick(i).ReserveOtpVerification(ctx, c, t0, policy)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil:
			admitted = append(admitted, id)
		case !errors.Is(err, app.ErrRateLimited):
			t.Errorf("reservation: %v", err)
		}
	})
	if len(admitted) != policy.MaxFailures {
		t.Fatalf("%d reservations admitted, want %d", len(admitted), policy.MaxFailures)
	}
	lockers := 0
	for _, id := range admitted {
		if got, _ := be.pick(int(id)).ConfirmOtpFailure(ctx, c, id); !got.IsZero() {
			lockers++
		}
	}
	if lockers != 1 {
		t.Errorf("%d reservations report the lockout, want exactly 1", lockers)
	}
}

func session(id string) *app.UserSession {
	return &app.UserSession{
		SessionID: id, SubscriberID: "usr_" + id, RefreshTokenHash: th("rt_0"),
		DeviceID: "dev_1", Roles: []string{"subscriber"},
		CreatedAt: t0, ExpiresAt: t0.Add(24 * time.Hour),
	}
}

func sameSession(t *testing.T, got, want *app.UserSession) {
	t.Helper()
	if got.SessionID != want.SessionID || got.SubscriberID != want.SubscriberID ||
		got.RefreshTokenHash != want.RefreshTokenHash || got.DeviceID != want.DeviceID || !reflect.DeepEqual(got.Roles, want.Roles) ||
		!got.CreatedAt.Equal(want.CreatedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) ||
		got.IsRevoked != want.IsRevoked || got.DeviceKeyThumbprint != want.DeviceKeyThumbprint {
		t.Errorf("session = %+v\n      want %+v", got, want)
	}
}

func testSessionRoundTrip(t *testing.T, be backend) {
	s := session("s1")
	s.DeviceKeyThumbprint = "jkt_1"
	if err := be.a.SaveSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := be.b.GetSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	sameSession(t, got, s)
	got.Roles[0] = "admin"
	if again, _ := be.a.GetSession(ctx, "s1"); again.Roles[0] != "subscriber" {
		t.Error("a caller's copy reached stored roles")
	}
	for name, roles := range map[string][]string{"nil": nil, "empty": {}} {
		s := session("roles_" + name)
		s.Roles = roles
		_ = be.a.SaveSession(ctx, s)
		// nil and empty are not distinguished by the contract (the memory
		// repository's copy turns empty into nil); only "no roles" must hold.
		if got, _ := be.b.GetSession(ctx, s.SessionID); len(got.Roles) != 0 {
			t.Errorf("%s roles round-tripped as %#v", name, got.Roles)
		}
	}
	expired := session("s_expired")
	expired.ExpiresAt = t0.Add(-time.Hour)
	_ = be.a.SaveSession(ctx, expired)
	if got, _ := be.b.GetSession(ctx, "s_expired"); !got.ExpiresAt.Equal(expired.ExpiresAt) {
		t.Error("expiry did not persist")
	}
	if _, err := be.a.GetSession(ctx, "s_missing"); !errors.Is(err, app.ErrInvalidToken) {
		t.Errorf("missing: err = %v, want ErrInvalidToken", err)
	}
	if err := be.b.RevokeSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := be.a.GetSession(ctx, "s1"); !got.IsRevoked {
		t.Error("revocation did not persist")
	}
	if err := be.a.RevokeSession(ctx, "s_missing"); err != nil {
		t.Errorf("revoking a missing session: %v", err)
	}
}

func testReplaceCAS(t *testing.T, be backend) {
	s := session("cas")
	_ = be.a.SaveSession(ctx, s)
	next := func(token string) *app.UserSession { n := session("cas"); n.RefreshTokenHash = token; return n }

	if err := be.a.ReplaceSession(ctx, &app.UserSession{SessionID: "cas_missing", RefreshTokenHash: th("x")}, th("rt_0")); !errors.Is(err, app.ErrSessionConflict) {
		t.Errorf("missing: err = %v", err)
	}
	if err := be.b.ReplaceSession(ctx, next(th("rt_x")), th("rt_wrong")); !errors.Is(err, app.ErrSessionConflict) {
		t.Errorf("wrong token: err = %v", err)
	}
	if err := be.a.ReplaceSession(ctx, next(th("rt_1")), th("rt_0")); err != nil {
		t.Fatalf("matching token: %v", err)
	}
	if got, _ := be.b.GetSession(ctx, "cas"); got.RefreshTokenHash != th("rt_1") {
		t.Error("the swap was not stored")
	}

	var mu sync.Mutex
	var winners []string
	race(8, func(i int) {
		tok := th(fmt.Sprintf("rt_2_%d", i))
		err := be.pick(i).ReplaceSession(ctx, next(tok), th("rt_1"))
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil:
			winners = append(winners, tok)
		case !errors.Is(err, app.ErrSessionConflict):
			t.Errorf("swap: %v", err)
		}
	})
	if len(winners) != 1 {
		t.Fatalf("%d concurrent swaps won, want exactly 1", len(winners))
	}
	if got, _ := be.a.GetSession(ctx, "cas"); got.RefreshTokenHash != winners[0] {
		t.Error("the store does not hold the winner's token")
	}
}

func testRevocationSticky(t *testing.T, be backend) {
	s := session("rev")
	_ = be.a.SaveSession(ctx, s)
	stale, _ := be.a.GetSession(ctx, "rev")
	if err := be.b.RevokeSession(ctx, "rev"); err != nil {
		t.Fatal(err)
	}
	stale.RefreshTokenHash = th("rt_after")
	if err := be.a.ReplaceSession(ctx, stale, th("rt_0")); !errors.Is(err, app.ErrSessionConflict) {
		t.Errorf("stale swap of a revoked session: err = %v, want ErrSessionConflict", err)
	}
	stale.RefreshTokenHash = th("rt_0")
	if err := be.a.SaveSession(ctx, stale); err != nil {
		t.Fatalf("stale save: %v", err)
	}
	got, _ := be.b.GetSession(ctx, "rev")
	if !got.IsRevoked {
		t.Error("a stale write un-revoked the session")
	}
	if got.RefreshTokenHash != th("rt_0") {
		t.Error("a refused swap changed the refresh token")
	}
	// Even a swap that would itself leave the session revoked is refused: a
	// revoked session's refresh token never rotates.
	revokedNext := session("rev")
	revokedNext.RefreshTokenHash, revokedNext.IsRevoked = th("rt_rotated"), true
	if err := be.b.ReplaceSession(ctx, revokedNext, th("rt_0")); !errors.Is(err, app.ErrSessionConflict) {
		t.Errorf("swap of a revoked session: err = %v, want ErrSessionConflict", err)
	}
	if got, _ := be.a.GetSession(ctx, "rev"); got.RefreshTokenHash != th("rt_0") {
		t.Error("a revoked session's refresh token rotated")
	}
}

func testKeyImmutable(t *testing.T, be backend) {
	s := session("key")
	_ = be.a.SaveSession(ctx, s)
	enrolled := session("key")
	enrolled.DeviceKeyThumbprint = "jkt_X"
	if err := be.b.SaveSession(ctx, enrolled); err != nil {
		t.Fatalf("enrolling an unenrolled session: %v", err)
	}
	for name, key := range map[string]string{"replaced": "jkt_Y", "cleared": ""} {
		w := session("key")
		w.DeviceKeyThumbprint = key
		if err := be.pick(len(name)).SaveSession(ctx, w); !errors.Is(err, app.ErrDeviceKeyAlreadyEnrolled) {
			t.Errorf("SaveSession %s: err = %v", name, err)
		}
		w.RefreshTokenHash = th("rt_new")
		if err := be.pick(len(name)+1).ReplaceSession(ctx, w, th("rt_0")); !errors.Is(err, app.ErrDeviceKeyAlreadyEnrolled) {
			t.Errorf("ReplaceSession %s: err = %v", name, err)
		}
	}
	got, _ := be.a.GetSession(ctx, "key")
	if got.DeviceKeyThumbprint != "jkt_X" || got.RefreshTokenHash != th("rt_0") {
		t.Errorf("a refused write changed the session: %+v", got)
	}
	same := session("key")
	same.DeviceKeyThumbprint, same.RefreshTokenHash = "jkt_X", th("rt_1")
	if err := be.b.ReplaceSession(ctx, same, th("rt_0")); err != nil {
		t.Errorf("a rotation keeping the key: %v", err)
	}
}

func testDevicesAndDelegations(t *testing.T, be backend) {
	d := &app.DeviceAttestation{DeviceID: "dev_1", Model: "m", OsVersion: "v", IntegrityToken: "tok", AttestedAt: t0}
	if err := be.a.SaveDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := be.b.SaveDevice(ctx, d); err != nil {
		t.Errorf("replacing a device: %v", err)
	}
	if _, err := be.a.GetGuardianDelegation(ctx, "g", "s"); !errors.Is(err, app.ErrGuardianForbidden) {
		t.Errorf("missing delegation: err = %v, want ErrGuardianForbidden", err)
	}
}

func testReplay(t *testing.T, be backend) {
	exp := t0.Add(time.Minute)
	if err := be.ra.Reserve(ctx, "jkt_a", "j1", exp, t0); err != nil {
		t.Fatal(err)
	}
	if err := be.rb.Reserve(ctx, "jkt_a", "j1", exp, t0.Add(30*time.Second)); !errors.Is(err, dpop.ErrReplayed) {
		t.Errorf("duplicate: err = %v, want ErrReplayed", err)
	}
	if err := be.rb.Reserve(ctx, "jkt_b", "j1", exp, t0); err != nil {
		t.Errorf("same jti, other key: %v", err)
	}
	if err := be.ra.Reserve(ctx, "jkt_a", "j1", exp.Add(time.Minute), exp); err != nil {
		t.Errorf("once expired (expires_at <= now) the reservation stops blocking: %v", err)
	}
	if err := be.rb.Reserve(ctx, "jkt_a", "j1", exp.Add(time.Minute), exp); !errors.Is(err, dpop.ErrReplayed) {
		t.Errorf("the renewed reservation does not block: err = %v", err)
	}

	var mu sync.Mutex
	wins := 0
	race(16, func(i int) {
		err := be.pickReplay(i).Reserve(ctx, "jkt_c", "jc", exp, t0)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, dpop.ErrReplayed):
			t.Errorf("reserve: %v", err)
		}
	})
	if wins != 1 {
		t.Errorf("%d concurrent reservations succeeded, want exactly 1", wins)
	}
}

// Neither backend accepts a session carrying a raw refresh token or a
// non-canonical hash, on either write path, and neither returns a raw token.
func testRefreshTokensOnlyAsHashes(t *testing.T, be backend) {
	if err := be.a.SaveSession(ctx, session("hashed")); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*app.UserSession){
		"raw token beside the hash": func(s *app.UserSession) { s.RefreshToken = "raw-refresh-token" },
		"raw token in the hash":     func(s *app.UserSession) { s.RefreshTokenHash = "raw-refresh-token" },
		"upper-case hash":           func(s *app.UserSession) { s.RefreshTokenHash = strings.ToUpper(th("rt_0")) },
		"no hash":                   func(s *app.UserSession) { s.RefreshTokenHash = "" },
	}
	for name, mutate := range bad {
		s := session("hashed")
		mutate(s)
		if err := be.b.SaveSession(ctx, s); !errors.Is(err, app.ErrRefreshTokenNotHashed) {
			t.Errorf("SaveSession, %s: err = %v", name, err)
		}
		if err := be.a.ReplaceSession(ctx, s, th("rt_0")); !errors.Is(err, app.ErrRefreshTokenNotHashed) {
			t.Errorf("ReplaceSession, %s: err = %v", name, err)
		}
		fresh := session("never_" + strings.ReplaceAll(name, " ", "_"))
		mutate(fresh)
		_ = be.b.SaveSession(ctx, fresh)
		if _, err := be.a.GetSession(ctx, fresh.SessionID); !errors.Is(err, app.ErrInvalidToken) {
			t.Errorf("%s: a refused session was stored", name)
		}
	}
	got, err := be.b.GetSession(ctx, "hashed")
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != "" || got.RefreshTokenHash != th("rt_0") {
		t.Errorf("read back: raw %q, hash %q", got.RefreshToken, got.RefreshTokenHash)
	}
}

// Neither backend accepts a raw (or non-canonical) phone number as the key of a
// challenge or of rate-limit state, on any method, and nothing is stored.
func testPhoneNumbersOnlyAsPseudonyms(t *testing.T, be backend) {
	for _, key := range []string{"+14155552671", "14155552671", "", strings.ToUpper(pk("+14155552671"))} {
		if err := be.a.SaveOtp(ctx, &app.OtpChallenge{RequestID: "req_raw", MSISDNPseudonym: key, ExpiresAt: t0}); !errors.Is(err, app.ErrMSISDNNotPseudonymised) {
			t.Errorf("SaveOtp(%q): %v", key, err)
		}
		if err := be.b.ReserveOtpRequest(ctx, key, t0, policy); !errors.Is(err, app.ErrMSISDNNotPseudonymised) {
			t.Errorf("ReserveOtpRequest(%q): %v", key, err)
		}
		if _, err := be.a.ReserveOtpVerification(ctx, key, t0, policy); !errors.Is(err, app.ErrMSISDNNotPseudonymised) {
			t.Errorf("ReserveOtpVerification(%q): %v", key, err)
		}
		if err := be.b.ReleaseOtpVerification(ctx, key, 1); !errors.Is(err, app.ErrMSISDNNotPseudonymised) {
			t.Errorf("ReleaseOtpVerification(%q): %v", key, err)
		}
		if _, err := be.a.ConfirmOtpFailure(ctx, key, 1); !errors.Is(err, app.ErrMSISDNNotPseudonymised) {
			t.Errorf("ConfirmOtpFailure(%q): %v", key, err)
		}
		if err := be.b.ResetOtpFailures(ctx, key); !errors.Is(err, app.ErrMSISDNNotPseudonymised) {
			t.Errorf("ResetOtpFailures(%q): %v", key, err)
		}
	}
	if _, err := be.b.GetOtp(ctx, "req_raw"); !errors.Is(err, app.ErrInvalidOtp) {
		t.Error("a refused challenge was stored")
	}
	// The pseudonym itself works, and is what comes back.
	if err := be.a.ReserveOtpRequest(ctx, pk("+14155552671"), t0, policy); err != nil {
		t.Errorf("a pseudonym was refused: %v", err)
	}
}
