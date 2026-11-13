package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The Google account directory (account_directory.go) over the in-memory
// repository: find-or-create, one account per identity under concurrency, key
// rotation, the keyring completeness guard, id collisions and erasure.

const testIssuer = "https://accounts.google.com"

func newTestDirectory(t *testing.T, repo *MemoryIdentityRepository, keyring string) *GoogleAccountDirectory {
	t.Helper()
	d, err := NewGoogleAccountDirectory(repo, mustKeyring(t, keyring))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestGoogleDirectory_CreateThenLookup(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryIdentityRepository()
	d := newTestDirectory(t, repo, "k1 "+testKeyHex(0x11))

	if _, found, err := d.Lookup(ctx, testIssuer, "111"); err != nil || found {
		t.Fatalf("an unknown identity: (%v, %v)", found, err)
	}
	id, created, err := d.Create(ctx, testIssuer, "111")
	if err != nil || !created || !validSubscriberID(id) {
		t.Fatalf("Create = (%q, %v, %v)", id, created, err)
	}
	got, found, err := d.Lookup(ctx, testIssuer, "111")
	if err != nil || !found || got != id {
		t.Errorf("Lookup = (%q, %v, %v), want %q", got, found, err, id)
	}
	// A different identity, or the same subject at another issuer, is another
	// account.
	other, _, _ := d.Create(ctx, testIssuer, "222")
	elsewhere, _, _ := d.Create(ctx, "https://issuer.example", "111")
	if other == id || elsewhere == id || other == elsewhere {
		t.Error("distinct identities share an account")
	}
	// Creating an identity that is already attached returns its account.
	again, created, err := d.Create(ctx, testIssuer, "111")
	if err != nil || created || again != id {
		t.Errorf("Create again = (%q, %v, %v), want the existing account", again, created, err)
	}
	// The account is stored, active, with a random id that is not derived
	// from the subject.
	acct, err := repo.GetAccount(ctx, id)
	if err != nil || acct.Status != AccountActive {
		t.Errorf("stored account: (%+v, %v)", acct, err)
	}
}

// Fifty concurrent first sign-ins of one identity create exactly one account,
// and every caller gets it.
func TestGoogleDirectory_ConcurrentFirstSignInsShareOneAccount(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryIdentityRepository()
	d := newTestDirectory(t, repo, "k1 "+testKeyHex(0x11))
	var (
		mu      sync.Mutex
		ids     = map[string]int{}
		created int
		wg      sync.WaitGroup
	)
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, c, err := d.Create(ctx, testIssuer, "999")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("Create: %v", err)
				return
			}
			ids[id]++
			if c {
				created++
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(ids) != 1 || created != 1 {
		t.Errorf("%d distinct accounts, %d created; want 1 and 1", len(ids), created)
	}
}

// Rotation: a method stored under the previous key is found and moved to the
// current key; once moved, the old key can be removed.
func TestGoogleDirectory_KeyRotation(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryIdentityRepository()
	old := newTestDirectory(t, repo, "k1 "+testKeyHex(0x11))
	id := mustCreateAccount(ctx, t, old, "111")

	rotated := newTestDirectory(t, repo, "k2 "+testKeyHex(0x22)+"\nk1 "+testKeyHex(0x11))
	got, found, err := rotated.Lookup(ctx, testIssuer, "111")
	if err != nil || !found || got != id {
		t.Fatalf("lookup during rotation: (%q, %v, %v)", got, found, err)
	}
	if ids, _ := repo.AuthMethodKeyIDs(ctx, AuthMethodGoogle); len(ids) != 1 || ids[0] != "k2" {
		t.Errorf("after use, key ids = %v, want [k2]", ids)
	}

	retired := newTestDirectory(t, repo, "k2 "+testKeyHex(0x22))
	if got, found, err := retired.Lookup(ctx, testIssuer, "111"); err != nil || !found || got != id {
		t.Errorf("after the old key is removed: (%q, %v, %v)", got, found, err)
	}
}

// Fail closed: while a stored method uses a key the keyring lacks, no account
// is created (that identity could not be found, and would get a second one).
func TestGoogleDirectory_RefusesCreationWithAnIncompleteKeyring(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryIdentityRepository()
	if _, _, err := newTestDirectory(t, repo, "k1 "+testKeyHex(0x11)).Create(ctx, testIssuer, "111"); err != nil {
		t.Fatal(err)
	}
	missingK1 := newTestDirectory(t, repo, "k9 "+testKeyHex(0x99))
	if _, found, _ := missingK1.Lookup(ctx, testIssuer, "111"); found {
		t.Fatal("found under a key the keyring does not hold")
	}
	for _, sub := range []string{"111", "222"} {
		if _, _, err := missingK1.Create(ctx, testIssuer, sub); !errors.Is(err, ErrPseudonymKeyringIncomplete) {
			t.Errorf("subject %s: err = %v, want ErrPseudonymKeyringIncomplete", sub, err)
		}
	}
}

// Random ids: one that is already held is skipped, and a run of collisions
// fails closed rather than reusing an id.
func TestGoogleDirectory_IDCollisions(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryIdentityRepository()
	held := "usr_" + "0123456789abcdef0123456789abcdef"
	if err := repo.SaveSession(ctx, &UserSession{
		SessionID: "sess_held", SubscriberID: held, RefreshTokenHash: HashRefreshToken([]byte("k"), "held"), Roles: []string{"subscriber"},
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	d := newTestDirectory(t, repo, "k1 "+testKeyHex(0x11))
	fresh := "usr_" + "fedcba9876543210fedcba9876543210"
	calls := 0
	d.mintAccount = func() (string, error) {
		calls++
		if calls == 1 {
			return held, nil
		}
		return fresh, nil
	}
	if id, created, err := d.Create(ctx, testIssuer, "111"); err != nil || !created || id != fresh {
		t.Errorf("after one collision: (%q, %v, %v), want %q", id, created, err, fresh)
	}
	d.mintAccount = func() (string, error) { return held, nil }
	if _, _, err := d.Create(ctx, testIssuer, "222"); !errors.Is(err, ErrSubscriberIDCollision) {
		t.Errorf("every attempt collides: err = %v, want ErrSubscriberIDCollision", err)
	}
}

// Erasure (INV-ID-7 option A): the account id is tombstoned and never reused;
// the identity is detached, so the person may sign up again as a new account.
func TestGoogleDirectory_ErasureTombstonesTheAccount(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryIdentityRepository()
	d := newTestDirectory(t, repo, "k1 "+testKeyHex(0x11))
	id := mustCreateAccount(ctx, t, d, "111")
	if err := repo.SaveSession(ctx, &UserSession{
		SessionID: "sess_a", SubscriberID: id, RefreshTokenHash: HashRefreshToken([]byte("k"), "rt"), Roles: []string{"subscriber"},
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), DeviceKeyThumbprint: "jkt_a",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.EraseSubscriber(ctx, &SubscriberErasure{
		SubscriberID: id, RequestedAt: time.Now(), RequestingSessionID: "sess_a",
		RequestingRefreshTokenHash: HashRefreshToken([]byte("k"), "rt"), DeviceKeyThumbprint: "jkt_a",
		ReceiptExpiresAt: time.Now().Add(time.Hour), Exceptions: ErasureExceptions,
	}); err != nil {
		t.Fatal(err)
	}
	if acct, gerr := repo.GetAccount(ctx, id); gerr != nil || acct.Status != AccountErased || acct.ErasedAt.IsZero() {
		t.Fatalf("after erasure: (%+v, %v), want a tombstone", acct, gerr)
	}
	if _, found, _ := d.Lookup(ctx, testIssuer, "111"); found {
		t.Error("an erased account is still found")
	}
	d.mintAccount = func() (string, error) { return id, nil }
	if _, _, err := d.Create(ctx, testIssuer, "111"); !errors.Is(err, ErrSubscriberIDCollision) {
		t.Errorf("a tombstoned id was reused: err = %v", err)
	}
	d.mintAccount = NewRandomSubscriberID
	again, created, err := d.Create(ctx, testIssuer, "111")
	if err != nil || !created || again == id {
		t.Errorf("re-signup: (%q, %v, %v), want a new account", again, created, err)
	}
}

func mustCreateAccount(ctx context.Context, t *testing.T, d *GoogleAccountDirectory, subject string) string {
	t.Helper()
	id, _, err := d.Create(ctx, testIssuer, subject)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGoogleDirectory_NeedsARepositoryAndAKeyring(t *testing.T) {
	if _, err := NewGoogleAccountDirectory(nil, mustKeyring(t, "k1 "+testKeyHex(0x11))); !errors.Is(err, ErrAccountsUnavailable) {
		t.Errorf("no repository: %v", err)
	}
	if _, err := NewGoogleAccountDirectory(NewMemoryIdentityRepository(), nil); !errors.Is(err, ErrAccountsUnavailable) {
		t.Errorf("no keyring: %v", err)
	}
}
