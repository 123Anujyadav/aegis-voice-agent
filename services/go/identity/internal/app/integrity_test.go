package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// Play Integrity (ADR-0010 §5(b)): fail closed without a verifier, never
// invent a verdict, and apply the policy exactly. The verifier here is a fake:
// no real Google verification is exercised, and none is claimed.

var (
	integrityNow  = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	goodDigest    = strings.Repeat("A", 43)
	otherDigest   = strings.Repeat("B", 43)
	testJKT       = strings.Repeat("k", 43)
	goodRequestHs = "req-hash-1"
)

func goodPolicy() IntegrityPolicy {
	return IntegrityPolicy{
		PackageName:        "com.example.callscreen",
		CertificateDigests: []string{goodDigest},
		MaxAge:             2 * time.Minute,
		FutureSkew:         30 * time.Second,
		DecodeTimeout:      time.Second,
	}
}

func goodVerdict() IntegrityVerdict {
	return IntegrityVerdict{
		RequestPackageName: "com.example.callscreen",
		RequestHash:        goodRequestHs,
		Timestamp:          integrityNow.Add(-10 * time.Second),
		AppRecognition:     AppPlayRecognized,
		AppPackageName:     "com.example.callscreen",
		CertificateDigests: []string{goodDigest},
		DeviceRecognition:  []string{"MEETS_BASIC_INTEGRITY", DeviceMeetsIntegrity},
		AppLicensing:       AppLicensingLicensed,
	}
}

type fakeVerifier struct {
	verdict IntegrityVerdict
	err     error
	calls   int
}

func (f *fakeVerifier) Decode(context.Context, string) (IntegrityVerdict, error) {
	f.calls++
	return f.verdict, f.err
}

// deviceSpy records every SaveDevice.
type deviceSpy struct {
	*MemoryIdentityRepository
	saved []DeviceAttestation
}

func (d *deviceSpy) SaveDevice(ctx context.Context, a *DeviceAttestation) error {
	d.saved = append(d.saved, *a)
	return d.MemoryIdentityRepository.SaveDevice(ctx, a)
}

func integrityService(t *testing.T, v IntegrityVerifier) (*IdentityService, *deviceSpy) {
	t.Helper()
	repo := &deviceSpy{MemoryIdentityRepository: NewMemoryIdentityRepository()}
	svc := NewIdentityService(repo, []byte("test_secret_key_that_is_long_enough"))
	svc.now = func() time.Time { return integrityNow }
	if v != nil {
		if err := svc.SetIntegrityVerifier(v, goodPolicy()); err != nil {
			t.Fatal(err)
		}
	}
	return svc, repo
}

func binding() IntegrityBinding {
	return IntegrityBinding{DeviceKeyThumbprint: testJKT, RequestHash: goodRequestHs}
}

// Without a verifier, the token the old stub certified is not certified, and
// nothing is recorded.
func TestIntegrity_WithoutAVerifierNothingIsCertified(t *testing.T) {
	svc, repo := integrityService(t, nil)
	a, err := svc.VerifyPlayIntegrity(context.Background(), "aaaaaaaaaa", binding())
	if !errors.Is(err, ErrPlayIntegrityUnavailable) || a.Tier != "" || len(repo.saved) != 0 {
		t.Fatalf("(%+v, %v, %d saved); want ErrPlayIntegrityUnavailable and nothing saved", a, err, len(repo.saved))
	}
	// Removing a configured verifier makes it unavailable again.
	svc, _ = integrityService(t, &fakeVerifier{verdict: goodVerdict()})
	if err := svc.SetIntegrityVerifier(nil, IntegrityPolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyPlayIntegrity(context.Background(), "tok", binding()); !errors.Is(err, ErrPlayIntegrityUnavailable) {
		t.Fatalf("after removing the verifier: %v", err)
	}
}

// A verdict that passes every check is certified and recorded against the
// device key, with nothing invented and no raw token kept.
func TestIntegrity_AGenuineVerdictIsCertified(t *testing.T) {
	v := &fakeVerifier{verdict: goodVerdict()}
	svc, repo := integrityService(t, v)
	a, err := svc.VerifyPlayIntegrity(context.Background(), "opaque-integrity-token", binding())
	if err != nil || a.Tier != TierCertified || len(a.Reasons) != 0 {
		t.Fatalf("(%+v, %v); want certified", a, err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("%d attestations saved, want 1", len(repo.saved))
	}
	got := repo.saved[0]
	want := DeviceAttestation{DeviceID: "dev_" + testJKT, AttestedAt: integrityNow, IsPlayCertified: true}
	if got != want {
		t.Errorf("saved %+v, want %+v (no model, OS or raw token)", got, want)
	}
}

// Each failed check downgrades to restricted with exactly its reason, and is
// recorded as not certified. A failed verdict is not an error (ADR-0010).
func TestIntegrity_EachFailedCheckRestricts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*IntegrityVerdict)
		want   IntegrityReason
	}{
		{"request package", func(v *IntegrityVerdict) { v.RequestPackageName = "com.evil.app" }, ReasonPackageMismatch},
		{"app package", func(v *IntegrityVerdict) { v.AppPackageName = "com.evil.app" }, ReasonPackageMismatch},
		{"request hash", func(v *IntegrityVerdict) { v.RequestHash = "another-request" }, ReasonRequestHashMismatch},
		{"stale", func(v *IntegrityVerdict) { v.Timestamp = integrityNow.Add(-3 * time.Minute) }, ReasonStaleVerdict},
		{"no timestamp", func(v *IntegrityVerdict) { v.Timestamp = time.Time{} }, ReasonStaleVerdict},
		{"future", func(v *IntegrityVerdict) { v.Timestamp = integrityNow.Add(time.Minute) }, ReasonFutureVerdict},
		{"unrecognized app", func(v *IntegrityVerdict) { v.AppRecognition = "UNRECOGNIZED_VERSION" }, ReasonAppNotRecognized},
		{"unevaluated app", func(v *IntegrityVerdict) { v.AppRecognition = "UNEVALUATED" }, ReasonAppNotRecognized},
		{"wrong certificate", func(v *IntegrityVerdict) { v.CertificateDigests = []string{otherDigest} }, ReasonCertificateMismatch},
		{"no certificate", func(v *IntegrityVerdict) { v.CertificateDigests = nil }, ReasonCertificateMismatch},
		{"basic integrity only", func(v *IntegrityVerdict) { v.DeviceRecognition = []string{"MEETS_BASIC_INTEGRITY"} }, ReasonDeviceIntegrity},
		{"no device verdict", func(v *IntegrityVerdict) { v.DeviceRecognition = nil }, ReasonDeviceIntegrity},
		{"unlicensed", func(v *IntegrityVerdict) { v.AppLicensing = "UNLICENSED" }, ReasonUnlicensed},
		{"licensing unevaluated", func(v *IntegrityVerdict) { v.AppLicensing = "UNEVALUATED" }, ReasonUnlicensed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict := goodVerdict()
			tc.mutate(&verdict)
			svc, repo := integrityService(t, &fakeVerifier{verdict: verdict})
			a, err := svc.VerifyPlayIntegrity(context.Background(), "tok", binding())
			if err != nil || a.Tier != TierRestricted || !slices.Equal(a.Reasons, []IntegrityReason{tc.want}) {
				t.Fatalf("(%+v, %v); want restricted for %s only", a, err, tc.want)
			}
			if len(repo.saved) != 1 || repo.saved[0].IsPlayCertified {
				t.Fatalf("saved %+v; want one attestation, not certified", repo.saved)
			}
		})
	}
}

// Every failed check is reported, not just the first.
func TestIntegrity_AllFailuresAreReported(t *testing.T) {
	v := IntegrityVerdict{} // nothing at all
	a := goodPolicy().Assess(v, goodRequestHs, integrityNow)
	want := []IntegrityReason{
		ReasonPackageMismatch, ReasonRequestHashMismatch, ReasonStaleVerdict, ReasonAppNotRecognized,
		ReasonCertificateMismatch, ReasonDeviceIntegrity, ReasonUnlicensed,
	}
	if a.Tier != TierRestricted || !slices.Equal(a.Reasons, want) {
		t.Fatalf("%+v; want %v", a, want)
	}
	// An empty expected request hash can never match, even an empty verdict hash.
	if a := goodPolicy().Assess(IntegrityVerdict{}, "", integrityNow); !slices.Contains(a.Reasons, ReasonRequestHashMismatch) {
		t.Fatalf("an empty binding matched: %+v", a)
	}
}

// A rejected token is a failure and an unavailable verifier is unavailability;
// neither records anything or certifies anything.
func TestIntegrity_RejectedTokensAndOutagesRecordNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want error
	}{
		"rejected":         {ErrIntegrityTokenInvalid, ErrPlayIntegrityFailed},
		"rejected wrapped": {fmt.Errorf("google: %w", ErrIntegrityTokenInvalid), ErrPlayIntegrityFailed},
		"outage":           {errors.New("decodeIntegrityToken: 503 at 10.0.0.9"), ErrPlayIntegrityUnavailable},
		"timeout":          {context.DeadlineExceeded, ErrPlayIntegrityUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			svc, repo := integrityService(t, &fakeVerifier{verdict: goodVerdict(), err: tc.err})
			a, err := svc.VerifyPlayIntegrity(context.Background(), "tok", binding())
			if !errors.Is(err, tc.want) || a.Tier != "" || len(repo.saved) != 0 {
				t.Fatalf("(%+v, %v, %d saved); want %v, nothing saved", a, err, len(repo.saved), tc.want)
			}
			if strings.Contains(err.Error(), "10.0.0.9") {
				t.Errorf("the verifier's error text leaked: %v", err)
			}
		})
	}
}

// The token and the binding are checked before the verifier is called.
func TestIntegrity_TokenAndBindingAreValidatedFirst(t *testing.T) {
	for name, tc := range map[string]struct {
		token string
		b     IntegrityBinding
	}{
		"empty token":       {"", binding()},
		"oversized token":   {strings.Repeat("t", maxIntegrityTokenSize+1), binding()},
		"no thumbprint":     {"tok", IntegrityBinding{RequestHash: goodRequestHs}},
		"short thumbprint":  {"tok", IntegrityBinding{DeviceKeyThumbprint: "abc", RequestHash: goodRequestHs}},
		"padded thumbprint": {"tok", IntegrityBinding{DeviceKeyThumbprint: " " + testJKT[1:], RequestHash: goodRequestHs}},
		"no request hash":   {"tok", IntegrityBinding{DeviceKeyThumbprint: testJKT}},
	} {
		t.Run(name, func(t *testing.T) {
			v := &fakeVerifier{verdict: goodVerdict()}
			svc, repo := integrityService(t, v)
			if _, err := svc.VerifyPlayIntegrity(context.Background(), tc.token, tc.b); !errors.Is(err, ErrPlayIntegrityFailed) {
				t.Fatalf("err = %v, want ErrPlayIntegrityFailed", err)
			}
			if v.calls != 0 || len(repo.saved) != 0 {
				t.Errorf("verifier called %d times, %d saved; want neither", v.calls, len(repo.saved))
			}
		})
	}
}

// A policy that could not distinguish a genuine verdict is refused.
func TestIntegrityPolicy_Validate(t *testing.T) {
	if err := goodPolicy().Validate(); err != nil {
		t.Fatalf("the good policy: %v", err)
	}
	for name, mutate := range map[string]func(*IntegrityPolicy){
		"no package":       func(p *IntegrityPolicy) { p.PackageName = "" },
		"single label":     func(p *IntegrityPolicy) { p.PackageName = "callscreen" },
		"padded package":   func(p *IntegrityPolicy) { p.PackageName = " com.example.app" },
		"no digests":       func(p *IntegrityPolicy) { p.CertificateDigests = nil },
		"bad digest":       func(p *IntegrityPolicy) { p.CertificateDigests = []string{"not-a-digest"} },
		"padded digest":    func(p *IntegrityPolicy) { p.CertificateDigests = []string{goodDigest + "="} },
		"zero max age":     func(p *IntegrityPolicy) { p.MaxAge = 0 },
		"long max age":     func(p *IntegrityPolicy) { p.MaxAge = time.Hour },
		"negative skew":    func(p *IntegrityPolicy) { p.FutureSkew = -time.Second },
		"long future skew": func(p *IntegrityPolicy) { p.FutureSkew = 2 * time.Minute },
	} {
		t.Run(name, func(t *testing.T) {
			p := goodPolicy()
			mutate(&p)
			if err := p.Validate(); !errors.Is(err, ErrInvalidIntegrityPolicy) {
				t.Fatalf("err = %v, want ErrInvalidIntegrityPolicy", err)
			}
			svc := NewIdentityService(NewMemoryIdentityRepository(), []byte("test_secret_key_that_is_long_enough"))
			if err := svc.SetIntegrityVerifier(&fakeVerifier{}, p); !errors.Is(err, ErrInvalidIntegrityPolicy) {
				t.Fatalf("SetIntegrityVerifier accepted the policy: %v", err)
			}
			if _, err := svc.VerifyPlayIntegrity(context.Background(), "tok", binding()); !errors.Is(err, ErrPlayIntegrityUnavailable) {
				t.Fatalf("a refused policy left verification enabled: %v", err)
			}
		})
	}
}
