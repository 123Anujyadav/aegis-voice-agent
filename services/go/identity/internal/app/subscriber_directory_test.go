package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// ADR-0030 D30.5 offline preparation. Golden values were computed
// independently (Python hmac/hashlib), not by this package.
var (
	dirKey    = []byte("test_secret_key_that_is_long_enough")
	dirNumber = "+919876543210"
)

const (
	goldenLegacyID            = "usr_d260031284ff9d34bbddee88d1e62147"
	goldenSubscriberPseudonym = "8f43ea66662271c4019525fea7216a940d3430a954ebb7275537b54a493d4a5b"
	goldenMSISDNPseudonym     = "3bfdb927545054232211693780715f76149b8accdab479a576f237cbec39e8ea"
)

// The compatibility pin: the legacy derivation, and the id the service puts
// on a session, are exactly today's value. If this fails, every existing
// subscriber id would change.
func TestLegacySubscriberID_IsUnchanged(t *testing.T) {
	if got := LegacySubscriberID(dirKey, dirNumber); got != goldenLegacyID {
		t.Fatalf("LegacySubscriberID = %s, want %s", got, goldenLegacyID)
	}
	svc := withFixedOtp(NewIdentityService(NewMemoryIdentityRepository(), dirKey))
	session := validatedSessionFor(context.Background(), t, svc, dirNumber)
	if session.SubscriberID != goldenLegacyID {
		t.Fatalf("a verified session carries %s, want the legacy id %s", session.SubscriberID, goldenLegacyID)
	}
}

// The subscriber pseudonym is domain-separated: it differs from the MSISDN
// pseudonym and the legacy id under the same key.
func TestSubscriberPseudonym_IsDomainSeparated(t *testing.T) {
	p, err := SubscriberPseudonym(dirKey, dirNumber)
	if err != nil || p != goldenSubscriberPseudonym {
		t.Fatalf("(%s, %v), want %s", p, err, goldenSubscriberPseudonym)
	}
	m, _ := MSISDNPseudonym(dirKey, dirNumber)
	if m != goldenMSISDNPseudonym || m == p || strings.Contains(goldenLegacyID, p[:32]) {
		t.Fatalf("pseudonyms are not separated: subscriber %s, msisdn %s", p, m)
	}
	for _, bad := range []string{"", "9876543210", "+91 98765 43210", "+0123"} {
		if _, err := SubscriberPseudonym(dirKey, bad); !errors.Is(err, ErrInvalidMSISDN) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

// Stage 2: a first resolution adopts the legacy id; later ones return it
// unchanged and create nothing.
func TestDirectory_AdoptsTheLegacyID(t *testing.T) {
	d, err := NewMemorySubscriberDirectory(AdoptLegacyIDs, func() (string, error) {
		t.Fatal("legacy adoption must not mint a random id")
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := SubscriberPseudonym(dirKey, dirNumber)
	legacy := LegacySubscriberID(dirKey, dirNumber)
	id, created, err := d.Resolve(context.Background(), p, legacy)
	if err != nil || !created || id != goldenLegacyID {
		t.Fatalf("(%s, %v, %v); want the legacy id, created", id, created, err)
	}
	again, created, err := d.Resolve(context.Background(), p, legacy)
	if err != nil || created || again != id {
		t.Fatalf("second resolve (%s, %v, %v); want the same id, not created", again, created, err)
	}
}

// Stage 3: new numbers get random ids; an existing mapping (a legacy id
// adopted in stage 2) is never replaced.
func TestDirectory_MintsRandomIDsForNewNumbersOnly(t *testing.T) {
	d, _ := NewMemorySubscriberDirectory(MintRandomIDs, nil)
	ctx := context.Background()
	p1, _ := SubscriberPseudonym(dirKey, dirNumber)
	p2, _ := SubscriberPseudonym(dirKey, "+919812345678")
	id1, _, err := d.Resolve(ctx, p1, LegacySubscriberID(dirKey, dirNumber))
	if err != nil || id1 == goldenLegacyID || !validSubscriberID(id1) {
		t.Fatalf("(%s, %v); want a well-formed random id, not the legacy one", id1, err)
	}
	id2, _, _ := d.Resolve(ctx, p2, LegacySubscriberID(dirKey, "+919812345678"))
	if id2 == id1 {
		t.Fatal("two numbers share an id")
	}
	if again, created, _ := d.Resolve(ctx, p1, LegacySubscriberID(dirKey, dirNumber)); again != id1 || created {
		t.Fatalf("an existing mapping changed: %s -> %s", id1, again)
	}
}

// Concurrent first verifications of one number yield one id, created once.
func TestDirectory_ConcurrentFirstResolutionsAgree(t *testing.T) {
	for _, mode := range []SubscriberIDMode{AdoptLegacyIDs, MintRandomIDs} {
		d, _ := NewMemorySubscriberDirectory(mode, nil)
		p, _ := SubscriberPseudonym(dirKey, dirNumber)
		legacy := LegacySubscriberID(dirKey, dirNumber)
		const n = 32
		ids := make([]string, n)
		created := make([]bool, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				id, c, err := d.Resolve(context.Background(), p, legacy)
				if err != nil {
					t.Error(err)
				}
				ids[i], created[i] = id, c
			}(i)
		}
		wg.Wait()
		made := 0
		for i := range ids {
			if ids[i] != ids[0] {
				t.Fatalf("mode %d: concurrent resolutions disagree: %s vs %s", mode, ids[i], ids[0])
			}
			if created[i] {
				made++
			}
		}
		if made != 1 {
			t.Fatalf("mode %d: the mapping was created %d times, want once", mode, made)
		}
	}
}

// A colliding random id is retried; persistent collision fails closed and
// stores nothing.
func TestDirectory_CollisionsRetryThenFailClosed(t *testing.T) {
	ctx := context.Background()
	taken := "usr_00000000000000000000000000000001"
	fresh := "usr_00000000000000000000000000000002"
	seq := []string{taken, taken, fresh}
	calls := 0
	d, _ := NewMemorySubscriberDirectory(MintRandomIDs, func() (string, error) {
		id := seq[calls]
		calls++
		return id, nil
	})
	pa, _ := SubscriberPseudonym(dirKey, "+919800000001")
	pb, _ := SubscriberPseudonym(dirKey, "+919800000002")
	if id, _, err := d.Resolve(ctx, pa, LegacySubscriberID(dirKey, "+919800000001")); err != nil || id != taken {
		t.Fatalf("first: (%s, %v)", id, err)
	}
	if id, _, err := d.Resolve(ctx, pb, LegacySubscriberID(dirKey, "+919800000002")); err != nil || id != fresh {
		t.Fatalf("retry after a collision: (%s, %v), want %s", id, err, fresh)
	}

	always := func() (string, error) { return taken, nil }
	d2, _ := NewMemorySubscriberDirectory(MintRandomIDs, always)
	if _, _, err := d2.Resolve(ctx, pa, LegacySubscriberID(dirKey, "+919800000001")); err != nil {
		t.Fatal(err)
	}
	if id, created, err := d2.Resolve(ctx, pb, LegacySubscriberID(dirKey, "+919800000002")); !errors.Is(err, ErrSubscriberIDCollision) || id != "" || created {
		t.Fatalf("(%s, %v, %v); want ErrSubscriberIDCollision and nothing stored", id, created, err)
	}
	if _, ok := d2.byKey[pb]; ok {
		t.Fatal("a failed allocation stored a mapping")
	}
}

// Malformed inputs, a malformed minted id and an unknown mode are refused.
func TestDirectory_RefusesMalformedInput(t *testing.T) {
	if _, err := NewMemorySubscriberDirectory(0, nil); err == nil {
		t.Fatal("mode 0 accepted")
	}
	d, _ := NewMemorySubscriberDirectory(AdoptLegacyIDs, nil)
	p, _ := SubscriberPseudonym(dirKey, dirNumber)
	for name, tc := range map[string][2]string{
		"short pseudonym": {"abc", goldenLegacyID},
		"upper pseudonym": {strings.ToUpper(p), goldenLegacyID},
		"bad legacy id":   {p, "user_123"},
		"short legacy id": {p, "usr_abc"},
	} {
		if _, _, err := d.Resolve(context.Background(), tc[0], tc[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	bad, _ := NewMemorySubscriberDirectory(MintRandomIDs, func() (string, error) { return "not-an-id", nil })
	if _, _, err := bad.Resolve(context.Background(), p, goldenLegacyID); err == nil {
		t.Fatal("a malformed minted id was stored")
	}
	if id, err := NewRandomSubscriberID(); err != nil || !validSubscriberID(id) {
		t.Fatalf("NewRandomSubscriberID = (%s, %v)", id, err)
	}
}

// validatedSessionFor is an unbound session for number through the real OTP
// flow.
func validatedSessionFor(ctx context.Context, t *testing.T, svc *IdentityService, number string) *UserSession {
	t.Helper()
	reqID, err := svc.RequestOtp(ctx, number)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := svc.ValidateOtp(ctx, reqID, testOtpCode)
	if err != nil {
		t.Fatal(err)
	}
	return session
}
