package pgstore

import (
	"errors"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Web account deletion at the repository boundary (app/web_erasure.go): the
// contract case runs on both backends; the PostgreSQL tests cover the
// migration's default, concurrent confirms, and housekeeping.

func deletionChallenge(id byte, subscriber, number string, purpose app.OtpPurpose) *app.OtpChallenge {
	return &app.OtpChallenge{
		RequestID: reqID(id), Purpose: purpose, MSISDNPseudonym: pk(number), SubscriberID: subscriber,
		CodeHash: th("code-" + string(id)), ExpiresAt: time.Now().Add(5 * time.Minute),
	}
}

func webErasureOf(subscriber string) *app.SubscriberErasure {
	return &app.SubscriberErasure{
		SubscriberID: subscriber, RequestedAt: time.Now(), ReceiptExpiresAt: time.Now().Add(5 * time.Minute),
		Exceptions: app.ErasureExceptions,
	}
}

func testWebErasure(t *testing.T, be backend) {
	seedErasure(t, be) // a: sessions 1, 2 and challenge a; b: session 3 and challenge b
	web, ok := be.a.(app.WebErasureRepository)
	if !ok {
		t.Fatal("the repository does not implement app.WebErasureRepository")
	}
	reader := be.b.(app.WebErasureRepository)

	// Purpose round-trips; the zero value is sign-in.
	for _, c := range []*app.OtpChallenge{
		deletionChallenge('d', usrID('a'), "+14155552671", app.OtpPurposeAccountDeletion),
		deletionChallenge('s', usrID('a'), "+14155552671", app.OtpPurposeSignIn),
	} {
		if err := be.a.SaveOtp(ctx, c); err != nil {
			t.Fatal(err)
		}
		got, err := be.b.GetOtp(ctx, c.RequestID)
		if err != nil || got.Purpose != c.Purpose {
			t.Fatalf("purpose of %s = %q (%v), want %q", c.RequestID[:5], got.Purpose, err, c.Purpose)
		}
	}

	for subscriber, want := range map[string]bool{usrID('a'): true, usrID('b'): true, usrID('c'): false} {
		if got, err := reader.HasAccount(ctx, subscriber); err != nil || got != want {
			t.Errorf("HasAccount(%s) = %v, %v; want %v", subscriber[:5], got, err, want)
		}
	}

	hour := time.Now().UTC().Truncate(time.Hour)
	for i, want := range []bool{true, true, false} {
		if got, err := web.ReserveDeletionCodeRequest(ctx, hour, 2); err != nil || got != want {
			t.Errorf("request %d in the hour: within = %v (%v), want %v", i+1, got, err, want)
		}
	}
	if got, _ := web.ReserveDeletionCodeRequest(ctx, hour.Add(time.Hour), 2); !got {
		t.Error("a new hour starts a new count")
	}

	// Only an account-deletion challenge of the same subscriber is consumed.
	for name, c := range map[string]struct {
		id         byte
		subscriber string
	}{"a sign-in challenge": {'s', usrID('a')}, "another subscriber": {'d', usrID('b')}, "no challenge": {'z', usrID('a')}} {
		if err := web.EraseSubscriberByDeletionCode(ctx, webErasureOf(c.subscriber), reqID(c.id), th("x")); !errors.Is(err, app.ErrSessionConflict) {
			t.Errorf("%s: err = %v, want ErrSessionConflict", name, err)
		}
	}
	if _, err := be.b.GetSession(ctx, sessID('1')); err != nil {
		t.Fatalf("a refused web erasure removed a session: %v", err)
	}

	if err := web.EraseSubscriberByDeletionCode(ctx, webErasureOf(usrID('a')), reqID('d'), th("code-d")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{sessID('1'), sessID('2')} {
		if _, err := be.b.GetSession(ctx, id); !errors.Is(err, app.ErrInvalidToken) {
			t.Errorf("session %s survived the web erasure", id[:9])
		}
	}
	if _, err := be.b.GetOtp(ctx, reqID('d')); err == nil {
		t.Error("the deletion challenge survived")
	}
	if _, err := be.b.GetSession(ctx, sessID('3')); err != nil {
		t.Errorf("another subscriber's session: %v", err)
	}
	if hash, found, err := reader.DeletionCodeReceipt(ctx, reqID('d')); err != nil || !found || hash != th("code-d") {
		t.Errorf("receipt = (%q, %v, %v)", hash, found, err)
	}
	if err := web.EraseSubscriberByDeletionCode(ctx, webErasureOf(usrID('a')), reqID('d'), th("code-d")); !errors.Is(err, app.ErrSessionConflict) {
		t.Errorf("a second confirm: err = %v, want ErrSessionConflict", err)
	}
}

// Rows written before 0009 are sign-in challenges.
func TestWebErasure_Postgres_ExistingChallengesAreSignIn(t *testing.T) {
	env := requirePostgres(t)
	if _, err := env.poolA.Exec(ctx, `
		INSERT INTO identity_otp_challenges (request_id, msisdn_pseudonym, subscriber_id, code_hash, attempts, expires_at)
		VALUES ($1, $2, $3, 'h', 0, now() + interval '5 minutes')`, reqID('o'), pk("+14155552671"), usrID('a')); err != nil {
		t.Fatal(err)
	}
	got, err := NewRepository(env.poolB).GetOtp(ctx, reqID('o'))
	if err != nil || got.Purpose != app.OtpPurposeSignIn {
		t.Fatalf("an existing challenge: purpose %q (%v), want sign-in", got.Purpose, err)
	}
	if _, err := env.poolA.Exec(ctx, `UPDATE identity_otp_challenges SET purpose = 'something_else'`); err == nil {
		t.Error("the purpose CHECK accepted an unknown purpose")
	}
}

// A confirm is mid-transaction (it has consumed the challenge and holds the
// row) when a concurrent confirm for the same code runs: the second waits,
// then finds the challenge gone and changes nothing; its caller is answered
// from the first's receipt.
func TestWebErasure_Postgres_ConcurrentConfirmsConsumeOnce(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	seedErasure(t, backend{a: repo, b: NewRepository(env.poolB)})
	if err := repo.SaveOtp(ctx, deletionChallenge('d', usrID('a'), "+14155552671", app.OtpPurposeAccountDeletion)); err != nil {
		t.Fatal(err)
	}

	first, err := env.poolB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Rollback(ctx) })
	if _, err := first.Exec(ctx, `DELETE FROM identity_otp_challenges WHERE request_id = $1`, reqID('d')); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- repo.EraseSubscriberByDeletionCode(ctx, webErasureOf(usrID('a')), reqID('d'), th("code-d"))
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var waiting int
		if err := env.poolA.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the second confirm did not wait for the first: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the second confirm never waited")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, app.ErrSessionConflict) {
		t.Fatalf("the second confirm: %v, want ErrSessionConflict", err)
	}
	var records int
	if err := env.poolA.QueryRow(ctx, `SELECT count(*) FROM identity_erasures`).Scan(&records); err != nil || records != 0 {
		t.Fatalf("%d erasure records from a confirm that found the code consumed (%v)", records, err)
	}
}

// Housekeeping deletes expired deletion receipts and old budget hours, and
// keeps the current hour.
func TestWebErasure_Postgres_Housekeeping(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	now := time.Now()
	if _, err := env.poolA.Exec(ctx, `
		INSERT INTO identity_deletion_code_receipts (request_id, code_hash, expires_at) VALUES ($1, 'h', $2), ($3, 'h', $4)`,
		reqID('1'), now.Add(-3*time.Hour), reqID('2'), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	current := now.UTC().Truncate(time.Hour)
	for _, h := range []time.Time{current.Add(-5 * time.Hour), current} {
		if _, err := repo.ReserveDeletionCodeRequest(ctx, h, 10); err != nil {
			t.Fatal(err)
		}
	}
	res, err := housekeeper(t, env.poolA, now, 100).RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletionReceipts != 1 || res.DeletionBudgetHours != 1 {
		t.Errorf("housekeeping deleted %d receipts and %d budget hours, want 1 and 1", res.DeletionReceipts, res.DeletionBudgetHours)
	}
	if _, found, _ := repo.DeletionCodeReceipt(ctx, reqID('2')); !found {
		t.Error("a live receipt was deleted")
	}
	if within, _ := repo.ReserveDeletionCodeRequest(ctx, current, 1); within { // 2 > 1 only if the first count was kept
		t.Error("the current hour's count was deleted")
	}
}
