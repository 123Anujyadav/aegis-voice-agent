package pgstore

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
)

// Accounts and authentication methods (migration 0011; app/accounts.go). The
// contract case runs on both backends; on PostgreSQL its concurrent calls
// alternate between two pools, so they are cross-instance calls. The
// PostgreSQL tests below cover the constraints and housekeeping.

func amID(c byte) string  { return "am_" + strings.Repeat(string(c), 32) }
func pseud(c byte) string { return strings.Repeat(string(c), 64) }

func accountsOf(t *testing.T, be backend) (a, b app.AccountRepository) {
	t.Helper()
	ra, okA := be.a.(app.AccountRepository)
	rb, okB := be.b.(app.AccountRepository)
	if !okA || !okB {
		t.Fatal("the repository does not implement app.AccountRepository")
	}
	return ra, rb
}

func newAccount(id string) *app.Account {
	return &app.Account{ID: id, Status: app.AccountActive, CreatedAt: t0}
}

func googleMethod(methodID, accountID, keyID, pseudonym string) *app.AuthMethod {
	return &app.AuthMethod{
		MethodID: methodID, AccountID: accountID, Kind: app.AuthMethodGoogle,
		KeyID: keyID, LookupPseudonym: pseudonym, CreatedAt: t0, LastUsedAt: t0,
	}
}

func testAccounts(t *testing.T, be backend) {
	a, b := accountsOf(t, be)

	// Create, and read back through the other handle.
	if err := a.CreateAccountWithMethod(ctx, newAccount(usrID('1')), googleMethod(amID('1'), usrID('1'), "k1", pseud('1'))); err != nil {
		t.Fatal(err)
	}
	if got, gerr := b.GetAccount(ctx, usrID('1')); gerr != nil || got.Status != app.AccountActive || !got.CreatedAt.Equal(t0) || !got.ErasedAt.IsZero() {
		t.Fatalf("GetAccount = (%+v, %v)", got, gerr)
	}
	if _, err := b.GetAccount(ctx, usrID('9')); !errors.Is(err, app.ErrAccountNotFound) {
		t.Errorf("unknown account: err = %v", err)
	}

	// An attached identity cannot join a second account; nothing is stored.
	if err := b.CreateAccountWithMethod(ctx, newAccount(usrID('2')), googleMethod(amID('2'), usrID('2'), "k1", pseud('1'))); !errors.Is(err, app.ErrAuthMethodInUse) {
		t.Errorf("same identity, second account: err = %v, want ErrAuthMethodInUse", err)
	}
	if _, err := a.GetAccount(ctx, usrID('2')); !errors.Is(err, app.ErrAccountNotFound) {
		t.Error("a refused creation stored the account")
	}
	// An account id is never issued twice, nor one a session already holds.
	if err := b.CreateAccountWithMethod(ctx, newAccount(usrID('1')), googleMethod(amID('3'), usrID('1'), "k1", pseud('3'))); !errors.Is(err, app.ErrAccountIDInUse) {
		t.Errorf("same account id: err = %v, want ErrAccountIDInUse", err)
	}
	held := &app.UserSession{
		SessionID: sessID('c'), SubscriberID: usrID('c'), RefreshTokenHash: th("rt_0"),
		Roles: []string{"subscriber"}, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := be.a.SaveSession(ctx, held); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateAccountWithMethod(ctx, newAccount(usrID('c')), googleMethod(amID('c'), usrID('c'), "k1", pseud('c'))); !errors.Is(err, app.ErrAccountIDInUse) {
		t.Errorf("an id a number-derived session holds: err = %v, want ErrAccountIDInUse", err)
	}
	// Records are validated before anything is written.
	for name, m := range map[string]*app.AuthMethod{
		"bad kind":      {MethodID: amID('4'), AccountID: usrID('4'), Kind: "email", KeyID: "k1", LookupPseudonym: pseud('4'), CreatedAt: t0, LastUsedAt: t0},
		"bad pseudonym": googleMethod(amID('4'), usrID('4'), "k1", "ABC"),
		"bad key id":    googleMethod(amID('4'), usrID('4'), "K 1", pseud('4')),
		"other account": googleMethod(amID('4'), usrID('5'), "k1", pseud('4')),
		"bad method id": googleMethod("am_x", usrID('4'), "k1", pseud('4')),
	} {
		if err := a.CreateAccountWithMethod(ctx, newAccount(usrID('4')), m); !errors.Is(err, app.ErrInvalidAccountRecord) {
			t.Errorf("%s: err = %v, want ErrInvalidAccountRecord", name, err)
		}
	}

	// Lookup matches (key id, pseudonym) pairs, never a pseudonym under
	// another key.
	if m, ferr := b.FindAuthMethod(ctx, app.AuthMethodGoogle, []app.KeyedPseudonym{{KeyID: "k2", Pseudonym: pseud('9')}, {KeyID: "k1", Pseudonym: pseud('1')}}); ferr != nil || m.MethodID != amID('1') || m.AccountID != usrID('1') {
		t.Fatalf("FindAuthMethod = (%+v, %v)", m, ferr)
	}
	if _, err := a.FindAuthMethod(ctx, app.AuthMethodGoogle, []app.KeyedPseudonym{{KeyID: "k2", Pseudonym: pseud('1')}}); !errors.Is(err, app.ErrAuthMethodNotFound) {
		t.Errorf("the pseudonym under another key id: err = %v, want not found", err)
	}
	if _, err := a.FindAuthMethod(ctx, app.AuthMethodPasskey, []app.KeyedPseudonym{{KeyID: "k1", Pseudonym: pseud('1')}}); !errors.Is(err, app.ErrAuthMethodNotFound) {
		t.Errorf("another kind: err = %v, want not found", err)
	}
	if _, err := a.FindAuthMethod(ctx, app.AuthMethodGoogle, nil); !errors.Is(err, app.ErrInvalidAccountRecord) {
		t.Errorf("no lookups: err = %v", err)
	}

	// Re-keying is a compare-and-swap on the key id.
	if ok, err := a.RekeyAuthMethod(ctx, amID('1'), "k1", app.KeyedPseudonym{KeyID: "k2", Pseudonym: pseud('2')}); err != nil || !ok {
		t.Fatalf("rekey: (%v, %v)", ok, err)
	}
	if ok, err := b.RekeyAuthMethod(ctx, amID('1'), "k1", app.KeyedPseudonym{KeyID: "k3", Pseudonym: pseud('5')}); err != nil || ok {
		t.Errorf("a stale rekey: (%v, %v), want false", ok, err)
	}
	if m, err := b.FindAuthMethod(ctx, app.AuthMethodGoogle, []app.KeyedPseudonym{{KeyID: "k2", Pseudonym: pseud('2')}}); err != nil || m.MethodID != amID('1') {
		t.Errorf("after rekey: (%+v, %v)", m, err)
	}
	if ids, err := a.AuthMethodKeyIDs(ctx, app.AuthMethodGoogle); err != nil || strings.Join(ids, ",") != "k2" {
		t.Errorf("key ids = (%v, %v), want [k2]", ids, err)
	}
	if err := a.CreateAccountWithMethod(ctx, newAccount(usrID('6')), googleMethod(amID('6'), usrID('6'), "k2", pseud('6'))); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RekeyAuthMethod(ctx, amID('6'), "k2", app.KeyedPseudonym{KeyID: "k2", Pseudonym: pseud('2')}); !errors.Is(err, app.ErrAuthMethodInUse) {
		t.Errorf("rekey onto an attached pseudonym: err = %v, want ErrAuthMethodInUse", err)
	}

	// Last use only moves forward.
	later := t0.Add(time.Hour)
	if err := b.TouchAuthMethod(ctx, amID('1'), later); err != nil {
		t.Fatal(err)
	}
	if err := a.TouchAuthMethod(ctx, amID('1'), t0); err != nil {
		t.Fatal(err)
	}
	if m, _ := a.FindAuthMethod(ctx, app.AuthMethodGoogle, []app.KeyedPseudonym{{KeyID: "k2", Pseudonym: pseud('2')}}); m == nil || !m.LastUsedAt.Equal(later) {
		t.Errorf("last used = %v, want %v", m, later)
	}

	// Concurrent first attachments of one identity: exactly one wins.
	var wins, inUse atomic.Int64
	race(20, func(i int) {
		repo := a
		if i%2 == 1 {
			repo = b
		}
		id := "usr_" + strings.Repeat("e", 30) + string("0123456789abcdef"[i%16]) + string("0123456789abcdef"[i/16])
		mid := "am_" + strings.Repeat("e", 30) + string("0123456789abcdef"[i%16]) + string("0123456789abcdef"[i/16])
		err := repo.CreateAccountWithMethod(ctx, newAccount(id), googleMethod(mid, id, "k2", pseud('7')))
		switch {
		case err == nil:
			wins.Add(1)
		case errors.Is(err, app.ErrAuthMethodInUse):
			inUse.Add(1)
		default:
			t.Errorf("create %d: %v", i, err)
		}
	})
	if wins.Load() != 1 || inUse.Load() != 19 {
		t.Errorf("%d created and %d refused, want 1 and 19", wins.Load(), inUse.Load())
	}

	// Erasure deletes the methods and tombstones the id (INV-ID-7 option A).
	jkt := jktOf('8')
	owner := &app.UserSession{
		SessionID: sessID('8'), SubscriberID: usrID('1'), RefreshTokenHash: th("rt_0"),
		Roles: []string{"subscriber"}, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		DeviceKeyThumbprint: jkt,
	}
	if err := be.a.SaveSession(ctx, owner); err != nil {
		t.Fatal(err)
	}
	erasure, ok := be.a.(app.ErasureRepository)
	if !ok {
		t.Fatal("no app.ErasureRepository")
	}
	if err := erasure.EraseSubscriber(ctx, eraseOf(usrID('1'), sessID('8'), th("rt_0"), jkt, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if got, err := b.GetAccount(ctx, usrID('1')); err != nil || got.Status != app.AccountErased || got.ErasedAt.IsZero() {
		t.Errorf("after erasure: (%+v, %v), want a tombstone", got, err)
	}
	if _, err := b.FindAuthMethod(ctx, app.AuthMethodGoogle, []app.KeyedPseudonym{{KeyID: "k2", Pseudonym: pseud('2')}}); !errors.Is(err, app.ErrAuthMethodNotFound) {
		t.Errorf("an erased account's method: err = %v, want gone", err)
	}
	if err := a.CreateAccountWithMethod(ctx, newAccount(usrID('1')), googleMethod(amID('d'), usrID('1'), "k2", pseud('d'))); !errors.Is(err, app.ErrAccountIDInUse) {
		t.Errorf("a tombstoned id was reused: err = %v", err)
	}
	// The same person may sign up again, as a new account.
	if err := a.CreateAccountWithMethod(ctx, newAccount(usrID('f')), googleMethod(amID('f'), usrID('f'), "k2", pseud('2'))); err != nil {
		t.Errorf("re-signup after erasure: %v", err)
	}
	// A number-derived subscriber's erasure touches no account.
	if got, err := b.GetAccount(ctx, usrID('6')); err != nil || got.Status != app.AccountActive {
		t.Errorf("another account: (%+v, %v)", got, err)
	}
}

func testAccountCreationBudget(t *testing.T, be backend) {
	a, b := accountsOf(t, be)
	hour := t0.Truncate(time.Hour)
	var within atomic.Int64
	race(20, func(i int) {
		repo := a
		if i%2 == 1 {
			repo = b
		}
		ok, err := repo.ReserveAccountCreation(ctx, hour, 5)
		if err != nil {
			t.Errorf("reserve %d: %v", i, err)
		}
		if ok {
			within.Add(1)
		}
	})
	if within.Load() != 5 {
		t.Fatalf("%d of 20 concurrent creations within a cap of 5", within.Load())
	}
	ist := time.FixedZone("IST", 5*3600+1800)
	if ok, err := a.ReserveAccountCreation(ctx, hour.Add(time.Hour).In(ist), 5); err != nil || !ok {
		t.Errorf("the next hour: (%v, %v)", ok, err)
	}
	for _, limit := range []int{0, -1} {
		if ok, err := b.ReserveAccountCreation(ctx, hour.Add(-time.Hour), limit); err != nil || ok {
			t.Errorf("limit %d: (%v, %v), want false", limit, ok, err)
		}
	}
}

func testGoogleTokenSingleUse(t *testing.T, be backend) {
	a, b := accountsOf(t, be)
	exp := time.Now().Add(time.Hour)
	var first atomic.Int64
	race(20, func(i int) {
		repo := a
		if i%2 == 1 {
			repo = b
		}
		ok, err := repo.ReserveGoogleToken(ctx, pseud('a'), exp)
		if err != nil {
			t.Errorf("reserve %d: %v", i, err)
		}
		if ok {
			first.Add(1)
		}
	})
	if first.Load() != 1 {
		t.Fatalf("%d of 20 concurrent redemptions of one token succeeded, want 1", first.Load())
	}
	if ok, err := b.ReserveGoogleToken(ctx, pseud('b'), exp); err != nil || !ok {
		t.Errorf("another token: (%v, %v)", ok, err)
	}
	for name, h := range map[string]string{"short": "abc", "upper": strings.ToUpper(pseud('c')), "empty": ""} {
		if _, err := a.ReserveGoogleToken(ctx, h, exp); !errors.Is(err, app.ErrInvalidAccountRecord) {
			t.Errorf("%s hash: err = %v", name, err)
		}
	}
}

// The schema refuses what the repository would never write.
func TestAccounts_Postgres_Constraints(t *testing.T) {
	env := requirePostgres(t)
	if _, err := env.poolA.Exec(ctx, `INSERT INTO identity_accounts (account_id, status, created_at) VALUES ($1, 'active', $2)`, usrID('1'), t0); err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{
		"bad account id":      `INSERT INTO identity_accounts (account_id, status, created_at) VALUES ('usr_1', 'active', now())`,
		"bad status":          `INSERT INTO identity_accounts (account_id, status, created_at) VALUES ('` + usrID('2') + `', 'locked', now())`,
		"erased without time": `INSERT INTO identity_accounts (account_id, status, created_at) VALUES ('` + usrID('3') + `', 'erased', now())`,
		"active with time":    `INSERT INTO identity_accounts (account_id, status, created_at, erased_at) VALUES ('` + usrID('4') + `', 'active', now(), now())`,
		"bad kind":            `INSERT INTO identity_auth_methods VALUES ('` + amID('1') + `', '` + usrID('1') + `', 'email', 'k1', '` + pseud('1') + `', now(), now())`,
		"bad pseudonym":       `INSERT INTO identity_auth_methods VALUES ('` + amID('1') + `', '` + usrID('1') + `', 'google', 'k1', 'ABC', now(), now())`,
		"bad key id":          `INSERT INTO identity_auth_methods VALUES ('` + amID('1') + `', '` + usrID('1') + `', 'google', 'K1', '` + pseud('1') + `', now(), now())`,
		"bad method id":       `INSERT INTO identity_auth_methods VALUES ('am_1', '` + usrID('1') + `', 'google', 'k1', '` + pseud('1') + `', now(), now())`,
		"unknown account":     `INSERT INTO identity_auth_methods VALUES ('` + amID('2') + `', '` + usrID('9') + `', 'google', 'k1', '` + pseud('2') + `', now(), now())`,
		"negative budget":     `INSERT INTO identity_account_creation_budget VALUES (now(), -1)`,
		"bad token hash":      `INSERT INTO identity_google_token_replay VALUES ('abc', now())`,
	} {
		if _, err := env.poolA.Exec(ctx, sql); err == nil {
			t.Errorf("%s: stored", name)
		}
	}
	// A revoked method frees its pseudonym; an active one does not.
	if _, err := env.poolA.Exec(ctx, `INSERT INTO identity_auth_methods VALUES ($1, $2, 'google', 'k1', $3, now(), now(), now())`, amID('5'), usrID('1'), pseud('5')); err != nil {
		t.Fatal(err)
	}
	if _, err := env.poolA.Exec(ctx, `INSERT INTO identity_auth_methods VALUES ($1, $2, 'google', 'k1', $3, now(), now())`, amID('6'), usrID('1'), pseud('5')); err != nil {
		t.Errorf("a revoked method's pseudonym could not be reused: %v", err)
	}
	if _, err := env.poolA.Exec(ctx, `INSERT INTO identity_auth_methods VALUES ($1, $2, 'google', 'k1', $3, now(), now())`, amID('7'), usrID('1'), pseud('5')); err == nil {
		t.Error("two active methods share a pseudonym")
	}
}

// Housekeeping deletes old creation-budget hours and expired Google token
// records, and keeps the current hour and live records.
func TestAccounts_Postgres_Housekeeping(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	now := time.Now()
	current := now.UTC().Truncate(time.Hour)
	for _, h := range []time.Time{current.Add(-5 * time.Hour), current.Add(-3 * time.Hour), current} {
		if _, err := repo.ReserveAccountCreation(ctx, h, 10); err != nil {
			t.Fatal(err)
		}
	}
	for c, exp := range map[byte]time.Time{'1': now.Add(-2 * time.Hour), '2': now.Add(-3 * time.Hour), '3': now.Add(time.Hour)} {
		if _, err := repo.ReserveGoogleToken(ctx, pseud(c), exp); err != nil {
			t.Fatal(err)
		}
	}
	res, err := housekeeper(t, env.poolA, now, 100).RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.AccountBudgetHours != 2 || res.GoogleTokens != 2 {
		t.Errorf("housekeeping deleted %d budget hours and %d token records, want 2 and 2", res.AccountBudgetHours, res.GoogleTokens)
	}
	if res.Total() < res.AccountBudgetHours+res.GoogleTokens {
		t.Errorf("Total %d does not include them", res.Total())
	}
	if within, _ := repo.ReserveAccountCreation(ctx, current, 1); within {
		t.Error("the current hour's count was deleted")
	}
	if ok, _ := repo.ReserveGoogleToken(ctx, pseud('3'), now.Add(time.Hour)); ok {
		t.Error("a live token record was deleted")
	}
}
