package pgstore

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Account erasure at the repository boundary (app/erasure.go): the contract
// case runs on both backends; the PostgreSQL tests cover what only SQL holds.

func sessID(c byte) string { return "sess_" + strings.Repeat(string(c), 64) }
func usrID(c byte) string  { return "usr_" + strings.Repeat(string(c), 32) }
func reqID(c byte) string  { return "req_" + strings.Repeat(string(c), 64) }
func jktOf(c byte) string  { return "jkt_" + string(c) }
func eraseOf(subscriber, session, tokenHash, jkt string, expires time.Time) *app.SubscriberErasure {
	return &app.SubscriberErasure{
		SubscriberID: subscriber, RequestedAt: time.Now(), RequestingSessionID: session,
		RequestingRefreshTokenHash: tokenHash, DeviceKeyThumbprint: jkt, ReceiptExpiresAt: expires,
		Exceptions: app.ErasureExceptions,
	}
}

// seedErasure: subscriber a with two device-bound sessions (one with a
// superseded refresh token) and a pending challenge; subscriber b with one of
// each.
func seedErasure(t *testing.T, be backend) {
	t.Helper()
	future := time.Now().Add(24 * time.Hour)
	for _, s := range []struct {
		id         byte
		subscriber string
		jkt        string
	}{{'1', usrID('a'), jktOf('1')}, {'2', usrID('a'), jktOf('2')}, {'3', usrID('b'), jktOf('3')}} {
		sess := &app.UserSession{
			SessionID: sessID(s.id), SubscriberID: s.subscriber, RefreshTokenHash: th("rt_0"),
			DeviceID: "", Roles: []string{"subscriber"}, CreatedAt: time.Now(), ExpiresAt: future,
			DeviceKeyThumbprint: s.jkt,
		}
		if err := be.a.SaveSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	rotated, _ := be.a.GetSession(ctx, sessID('1'))
	rotated.RefreshTokenHash = th("rt_1")
	if err := be.a.ReplaceSession(ctx, rotated, th("rt_0")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id         byte
		subscriber string
		number     string
	}{{'a', usrID('a'), "+14155552671"}, {'b', usrID('b'), "+14155552672"}} {
		if err := be.a.SaveOtp(ctx, &app.OtpChallenge{
			RequestID: reqID(c.id), MSISDNPseudonym: pk(c.number), SubscriberID: c.subscriber, CodeHash: "h", ExpiresAt: future,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func testErasure(t *testing.T, be backend) {
	seedErasure(t, be)
	erasure, ok := be.a.(app.ErasureRepository)
	if !ok {
		t.Fatal("the repository does not implement app.ErasureRepository")
	}
	hour := time.Now().Add(time.Hour)
	// The requesting session is re-checked inside the erasure: a token it no
	// longer holds, a session of someone else, or no session erases nothing.
	for name, e := range map[string]*app.SubscriberErasure{
		"superseded token":   eraseOf(usrID('a'), sessID('1'), th("rt_0"), jktOf('1'), hour),
		"another subscriber": eraseOf(usrID('b'), sessID('1'), th("rt_1"), jktOf('1'), hour),
		"no such session":    eraseOf(usrID('a'), sessID('9'), th("rt_0"), jktOf('9'), hour),
	} {
		if err := erasure.EraseSubscriber(ctx, e); !errors.Is(err, app.ErrSessionConflict) {
			t.Errorf("%s: err = %v, want ErrSessionConflict", name, err)
		}
	}
	for _, id := range []string{sessID('1'), sessID('2'), sessID('3')} {
		if _, err := be.b.GetSession(ctx, id); err != nil {
			t.Fatalf("a refused erasure removed session %s: %v", id[:9], err)
		}
	}

	if err := erasure.EraseSubscriber(ctx, eraseOf(usrID('a'), sessID('1'), th("rt_1"), jktOf('1'), hour)); err != nil {
		t.Fatal(err)
	}
	// Erasing the same session again (a concurrent duplicate) changes nothing.
	if err := erasure.EraseSubscriber(ctx, eraseOf(usrID('a'), sessID('1'), th("rt_1"), jktOf('1'), hour)); !errors.Is(err, app.ErrSessionConflict) {
		t.Errorf("a second erasure of the same session: err = %v, want ErrSessionConflict", err)
	}
	for _, id := range []string{sessID('1'), sessID('2')} {
		if _, err := be.b.GetSession(ctx, id); !errors.Is(err, app.ErrInvalidToken) {
			t.Errorf("erased subscriber's session %s: err = %v, want gone", id[:9], err)
		}
	}
	if superseded, _ := be.b.SupersededRefreshToken(ctx, sessID('1'), th("rt_0")); superseded {
		t.Error("an erased session's superseded refresh-token hash survived")
	}
	if _, err := be.b.GetSession(ctx, sessID('3')); err != nil {
		t.Errorf("another subscriber's session: %v", err)
	}
	if _, err := be.b.GetOtp(ctx, reqID('a')); err == nil {
		t.Error("the erased subscriber's challenge survived")
	}
	if _, err := be.b.GetOtp(ctx, reqID('b')); err != nil {
		t.Errorf("another subscriber's challenge: %v", err)
	}

	reader := be.b.(app.ErasureRepository)
	if jkt, found, err := reader.ErasureReceipt(ctx, sessID('1')); err != nil || !found || jkt != jktOf('1') {
		t.Errorf("receipt = (%q, %v, %v), want the requesting key", jkt, found, err)
	}
	if _, found, _ := reader.ErasureReceipt(ctx, sessID('3')); found {
		t.Error("a receipt for a session that was never erased")
	}
	// A receipt past its expiry answers nothing, deleted or not.
	if err := erasure.EraseSubscriber(ctx, eraseOf(usrID('b'), sessID('3'), th("rt_0"), jktOf('3'), time.Now().Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := reader.ErasureReceipt(ctx, sessID('3')); found {
		t.Error("an expired receipt was found")
	}
}

// What only PostgreSQL holds: a failed erasure rolled back, attestations and
// delegations erased, the erasure recorded once and append-only, receipts
// housekept and records never.
func TestErasure_Postgres_RollbackAttestationsDelegationsRecordAndHousekeeping(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	be := backend{a: repo, b: NewRepository(env.poolB)}
	seedErasure(t, be)
	for _, jkt := range []string{jktOf('1'), jktOf('3')} {
		if err := repo.SaveDevice(ctx, &app.DeviceAttestation{DeviceID: "dev_" + jkt, AttestedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range [][2]string{{"usr_guardian", usrID('a')}, {usrID('a'), "usr_ward"}, {"usr_guardian", usrID('b')}} {
		if _, err := env.poolA.Exec(ctx, `INSERT INTO identity_guardian_delegations (guardian_id, subscriber_id, permissions, granted_at) VALUES ($1, $2, '{}', now())`, d[0], d[1]); err != nil {
			t.Fatal(err)
		}
	}

	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := env.poolB.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	held := func() [6]int {
		return [6]int{
			count(`SELECT count(*) FROM identity_sessions`),
			count(`SELECT count(*) FROM identity_refresh_tokens_superseded`),
			count(`SELECT count(*) FROM identity_otp_challenges`),
			count(`SELECT count(*) FROM identity_device_attestations`),
			count(`SELECT count(*) FROM identity_guardian_delegations`),
			count(`SELECT count(*) FROM identity_erasures`) + count(`SELECT count(*) FROM identity_erasure_receipts`),
		}
	}

	// ROLLBACK: the last statement (the receipt) fails its CHECK after every
	// deletion has run; the transaction leaves everything as it was.
	before := held()
	if err := repo.EraseSubscriber(ctx, eraseOf(usrID('a'), sessID('1'), th("rt_1"), "", time.Now().Add(time.Hour))); err == nil {
		t.Fatal("an erasure whose receipt cannot be written succeeded")
	}
	if after := held(); after != before {
		t.Fatalf("a failed erasure changed the store: %v -> %v (sessions, superseded, challenges, attestations, delegations, records+receipts)", before, after)
	}

	if err := repo.EraseSubscriber(ctx, eraseOf(usrID('a'), sessID('1'), th("rt_1"), jktOf('1'), time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := repo.EraseSubscriber(ctx, eraseOf(usrID('a'), sessID('1'), th("rt_1"), jktOf('1'), time.Now().Add(time.Hour))); !errors.Is(err, app.ErrSessionConflict) {
		t.Errorf("a duplicate erasure: err = %v, want ErrSessionConflict", err)
	}
	if n := count(`SELECT count(*) FROM identity_device_attestations WHERE device_id = $1`, "dev_"+jktOf('1')); n != 0 {
		t.Error("the erased subscriber's device attestation survived")
	}
	if n := count(`SELECT count(*) FROM identity_device_attestations WHERE device_id = $1`, "dev_"+jktOf('3')); n != 1 {
		t.Error("another subscriber's device attestation was erased")
	}
	if n := count(`SELECT count(*) FROM identity_guardian_delegations WHERE guardian_id = $1 OR subscriber_id = $1`, usrID('a')); n != 0 {
		t.Errorf("%d delegations involving the erased subscriber survived", n)
	}
	if n := count(`SELECT count(*) FROM identity_guardian_delegations WHERE subscriber_id = $1`, usrID('b')); n != 1 {
		t.Error("a delegation not involving the erased subscriber was erased")
	}
	if n := count(`SELECT count(*) FROM identity_refresh_tokens_superseded`); n != 0 {
		t.Errorf("%d superseded refresh-token hashes survived", n)
	}

	var subscriber string
	var exceptions []string
	if err := env.poolB.QueryRow(ctx, `SELECT subscriber_id, exceptions FROM identity_erasures`).Scan(&subscriber, &exceptions); err != nil {
		t.Fatal(err)
	}
	if subscriber != usrID('a') || len(exceptions) != len(app.ErasureExceptions) {
		t.Errorf("erasure record = (%s, %d exceptions)", subscriber, len(exceptions))
	}
	for _, sql := range []string{
		`UPDATE identity_erasures SET subscriber_id = subscriber_id`,
		`DELETE FROM identity_erasures`,
		`TRUNCATE identity_erasures`,
	} {
		_, err := env.poolA.Exec(ctx, sql)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "CSA02" {
			t.Errorf("%q: err = %v, want the append-only refusal (CSA02)", sql, err)
		}
	}
	// A record holds no number: only a subscriber id is accepted.
	if _, err := env.poolA.Exec(ctx, `INSERT INTO identity_erasures (subscriber_id, requested_at, exceptions) VALUES ('+14155552671', now(), '{}')`); err == nil {
		t.Error("an erasure record accepted a phone number")
	}

	// Housekeeping deletes a receipt past its expiry, never the record.
	if _, err := env.poolA.Exec(ctx, `UPDATE identity_erasure_receipts SET expires_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	res, err := housekeeper(t, env.poolA, time.Now(), 100).RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.ErasureReceipts != 1 {
		t.Errorf("housekeeping deleted %d receipts, want 1", res.ErasureReceipts)
	}
	if n := count(`SELECT count(*) FROM identity_erasures`); n != 1 {
		t.Errorf("%d erasure records after housekeeping, want 1 (LEGAL_HOLD)", n)
	}
}
