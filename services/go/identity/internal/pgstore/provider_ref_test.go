package pgstore

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/jackc/pgx/v5/pgconn"
)

// Migration 0010: a challenge carries exactly one code source, a code hash or
// a provider's verification id, and the database enforces it.
func TestPostgres_ProviderRefRoundTripsAndOneCodeSourceIsEnforced(t *testing.T) {
	env := requirePostgres(t)
	repo := NewRepository(env.poolA)
	pseudonym := strings.Repeat("a", 64)
	expires := time.Now().Add(time.Minute).Truncate(time.Microsecond)
	ref := fakeSID("V", "E")

	provider := &app.OtpChallenge{RequestID: "req_provider", MSISDNPseudonym: pseudonym, SubscriberID: "usr_p", ProviderRef: ref, ExpiresAt: expires}
	if err := repo.SaveOtp(ctx, provider); err != nil {
		t.Fatalf("saving a provider challenge: %v", err)
	}
	got, err := NewRepository(env.poolB).GetOtp(ctx, "req_provider")
	if err != nil || got.ProviderRef != ref || got.CodeHash != "" || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("read back (%+v, %v)", got, err)
	}

	local := &app.OtpChallenge{RequestID: "req_local", MSISDNPseudonym: pseudonym, SubscriberID: "usr_l", CodeHash: strings.Repeat("b", 64), ExpiresAt: expires}
	if err := repo.SaveOtp(ctx, local); err != nil {
		t.Fatalf("saving an identity challenge: %v", err)
	}
	if got, err := repo.GetOtp(ctx, "req_local"); err != nil || got.ProviderRef != "" || got.CodeHash != local.CodeHash {
		t.Fatalf("read back (%+v, %v)", got, err)
	}

	for name, c := range map[string]*app.OtpChallenge{
		"both":         {RequestID: "req_both", MSISDNPseudonym: pseudonym, CodeHash: strings.Repeat("b", 64), ProviderRef: ref, ExpiresAt: expires},
		"neither":      {RequestID: "req_neither", MSISDNPseudonym: pseudonym, ExpiresAt: expires},
		"bad ref":      {RequestID: "req_badref", MSISDNPseudonym: pseudonym, ProviderRef: "VE-0123", ExpiresAt: expires},
		"ref too long": {RequestID: "req_longref", MSISDNPseudonym: pseudonym, ProviderRef: strings.Repeat("V", 65), ExpiresAt: expires},
	} {
		err := repo.SaveOtp(ctx, c)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" { // check_violation
			t.Errorf("%s: err = %v, want a CHECK violation", name, err)
		}
	}
}

// fakeSID builds a made-up Twilio identifier of the documented shape (two
// letters and 32 lower-case hex digits) at run time, so that no
// credential-shaped literal sits in the source. None is a real identifier.
func fakeSID(a, b string) string { return a + b + strings.Repeat("0123456789abcdef", 2) }
