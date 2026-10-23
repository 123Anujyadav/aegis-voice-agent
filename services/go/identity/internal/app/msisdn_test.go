package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// RequestOtp took the MSISDN exactly as sent and stored it on the challenge,
// and ValidateOtp derives the subscriber id from it (usr_ + HMAC(MSISDN)). So
// "+919876543210", "919876543210" and "+91 98765 43210" were three different
// subscribers, and the database's UNIQUE(msisdn) — which compares strings —
// could not see that they were one number. Only the canonical E.164 form is
// accepted now; nothing is trimmed or rewritten.
//
// This is SYNTAX ONLY. It does not establish that a number is allocated,
// reachable, a mobile rather than a landline, or eligible for OTP delivery.

var canonicalMSISDNs = []string{
	"+14155552671",     // US
	"+442071838750",    // UK
	"+1234567",         // 7 digits, the minimum
	"+123456789012345", // 15 digits, the maximum
	"+919876543210",    // the form every existing test already uses
}

var nonCanonicalMSISDNs = map[string]string{
	"empty":                    "",
	"plus only":                "+",
	"missing plus":             "14155552671",
	"leading zero after plus":  "+04155552671",
	"too few digits (6)":       "+123456",
	"too many digits (16)":     "+1234567890123456",
	"leading space":            " +14155552671",
	"trailing space":           "+14155552671 ",
	"trailing newline":         "+14155552671\n",
	"embedded space":           "+44 2071838750",
	"hyphens":                  "+1-415-555-2671",
	"parentheses":              "+1(415)5552671",
	"double plus":              "++14155552671",
	"letters":                  "+1415555ABCD",
	"extra characters after":   "+14155552671x",
	"Arabic-Indic digits":      "+١٤١٥٥٥٥٢٦٧١",
	"fullwidth digits":         "+１４１５５５５２６７１",
	"Devanagari digits":        "+९१९८७६५४३२१०",
	"fullwidth plus":           "＋14155552671",
	"embedded NUL":             "+1415555\x002671",
	"synthetic schema example": "000TEST0000001",
}

// otpSpyRepo counts challenge writes and otherwise is the real repository.
type otpSpyRepo struct {
	*MemoryIdentityRepository
	saveOtp int
}

func (r *otpSpyRepo) SaveOtp(ctx context.Context, c *OtpChallenge) error {
	r.saveOtp++
	return r.MemoryIdentityRepository.SaveOtp(ctx, c)
}

// spyService wires a service whose code generator counts its calls. RequestOtp
// mints and hashes the code before anything is stored, so a zero count proves
// it returned before doing any OTP work at all.
func spyService(t *testing.T) (*IdentityService, *otpSpyRepo, *int) {
	t.Helper()
	repo := &otpSpyRepo{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := NewIdentityService(repo, []byte("test_secret_key"))
	codes := 0
	svc.newCode = func() string { codes++; return testOtpCode }
	return svc, repo, &codes
}

func TestMSISDN_SyntaxRule(t *testing.T) {
	t.Parallel()
	for _, m := range canonicalMSISDNs {
		if !isCanonicalMSISDN(m) {
			t.Errorf("canonical %q was rejected", m)
		}
	}
	for name, m := range nonCanonicalMSISDNs {
		if isCanonicalMSISDN(m) {
			t.Errorf("%s: %q was accepted", name, m)
		}
	}
}

// TestRequestOtp_RejectsANonCanonicalMSISDNBeforeAnyOtpWork is the regression:
// the old RequestOtp stored a challenge for every one of these.
func TestRequestOtp_RejectsANonCanonicalMSISDNBeforeAnyOtpWork(t *testing.T) {
	t.Parallel()
	for name, m := range nonCanonicalMSISDNs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc, repo, codes := spyService(t)

			reqID, err := svc.RequestOtp(context.Background(), m)

			if !errors.Is(err, ErrInvalidMSISDN) {
				t.Fatalf("RequestOtp(%q): err = %v, want ErrInvalidMSISDN", m, err)
			}
			if reqID != "" {
				t.Errorf("a request id was issued for a rejected MSISDN: %q", reqID)
			}
			if repo.saveOtp != 0 || len(repo.otps) != 0 {
				t.Errorf("a challenge was stored (%d writes, %d stored)", repo.saveOtp, len(repo.otps))
			}
			if *codes != 0 {
				t.Errorf("an OTP code was minted %d time(s) before the MSISDN was refused", *codes)
			}
			if len(repo.sessions) != 0 || len(repo.devices) != 0 {
				t.Error("unrelated state was modified")
			}
		})
	}
}

// TestRequestOtp_PassesACanonicalMSISDNThroughUnchanged pins the accepted path:
// the value is stored exactly as given and the subscriber id is derived from
// exactly that value, so nothing is silently rewritten.
func TestRequestOtp_PassesACanonicalMSISDNThroughUnchanged(t *testing.T) {
	t.Parallel()
	for _, m := range canonicalMSISDNs {
		svc, repo, codes := spyService(t)

		reqID, err := svc.RequestOtp(context.Background(), m)
		if err != nil {
			t.Fatalf("RequestOtp(%q): %v", m, err)
		}
		if repo.saveOtp != 1 || *codes != 1 {
			t.Fatalf("%q: %d challenge writes, %d codes minted; want 1 and 1", m, repo.saveOtp, *codes)
		}
		stored, err := repo.GetOtp(context.Background(), reqID)
		if err != nil {
			t.Fatalf("GetOtp: %v", err)
		}
		// Only the pseudonym of exactly this spelling is stored, and the
		// subscriber id derived from exactly this spelling.
		if want, _ := svc.msisdnPseudonym(m); stored.MSISDNPseudonym != want {
			t.Errorf("stored pseudonym is not the pseudonym of %q as given", m)
		}
		if stored.SubscriberID != svc.subscriberID(m) {
			t.Errorf("stored subscriber id is not derived from %q as given", m)
		}

		sess, _, err := svc.ValidateOtp(context.Background(), reqID, testOtpCode)
		if err != nil {
			t.Fatalf("ValidateOtp: %v", err)
		}
		if want := "usr_" + svc.hashString(m)[:32]; sess.SubscriberID != want {
			t.Errorf("subscriber id = %q, want the HMAC of the value as given", sess.SubscriberID)
		}
	}
}

// TestRequestOtpAPI_RejectsANonCanonicalMSISDN proves the endpoint answers with
// its existing 400 contract and stores nothing.
func TestRequestOtpAPI_RejectsANonCanonicalMSISDN(t *testing.T) {
	t.Parallel()
	for name, m := range nonCanonicalMSISDNs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc, repo, codes := spyService(t)
			rec, body := postRequestOtp(t, NewIdentityHandler(svc), m)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if body["code"] != "ERR_INVALID_INPUT" || body["detail"] != "Invalid MSISDN input" {
				t.Errorf("body = %v, want the existing ERR_INVALID_INPUT / Invalid MSISDN input", body)
			}
			if strings.Contains(rec.Body.String(), "E.164") {
				t.Error("the internal sentinel text reached the client")
			}
			if repo.saveOtp != 0 || *codes != 0 {
				t.Errorf("OTP work was done for a refused MSISDN (%d writes, %d codes)", repo.saveOtp, *codes)
			}
		})
	}
}
