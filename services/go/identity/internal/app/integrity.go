package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
)

// Play Integrity verification (ADR-0010 §5(b)): the boundary, and the policy
// applied to a decoded verdict.
//
// WHAT THIS IS, AND WHAT IT IS NOT. Decoding a Play Integrity token is a call
// to Google (decodeIntegrityToken) with credentials for the Google Cloud
// project linked to the app in Play Console. No such implementation exists in
// this repository, and none can be written or tested without those credentials.
// IntegrityVerifier is the port it will implement. Until one is configured,
// VerifyPlayIntegrity fails closed with ErrPlayIntegrityUnavailable: it never
// certifies a device, and stores nothing.
//
// What CAN be decided and tested offline is the policy: given a decoded
// verdict, is this the genuine app, on a device with integrity, licensed, and
// is the verdict bound to this request and fresh? IntegrityPolicy.Assess
// answers that, purely.
//
// A failed verdict is NOT an error. ADR-0010 §5(b): "A failed verdict does not
// necessarily reject — it downgrades the session to a restricted tier and
// raises a signal." Assess returns TierRestricted with the reasons. A token
// that does not decode at all (forged, malformed, wrong app) IS an error:
// there is no verdict to downgrade.

// ErrPlayIntegrityUnavailable is a verdict that could not be obtained: no
// verifier is configured, or the verifier could not answer. Nothing is
// certified or stored. Callers must not treat it as a pass.
var ErrPlayIntegrityUnavailable = errors.New("identity: Play Integrity verification is unavailable")

// ErrInvalidIntegrityPolicy is a policy SetIntegrityVerifier refuses.
var ErrInvalidIntegrityPolicy = errors.New("identity: invalid Play Integrity policy")

// ErrIntegrityTokenInvalid is what an IntegrityVerifier returns for a token
// that the decoding service rejects: forged, malformed, expired at the
// source, or issued for another app. Any other verifier error is treated as
// unavailability.
var ErrIntegrityTokenInvalid = errors.New("identity: the Play Integrity token was rejected")

// IntegrityVerifier decodes a Play Integrity token into its verdict. The
// production implementation calls Google's decodeIntegrityToken; it must
// return ErrIntegrityTokenInvalid (possibly wrapped) for a rejected token, and
// any other error for an outage.
type IntegrityVerifier interface {
	Decode(ctx context.Context, token string) (IntegrityVerdict, error)
}

// IntegrityVerdict is the subset of a decoded Play Integrity payload the
// policy reads. Field values are Google's, unmodified.
type IntegrityVerdict struct {
	// RequestPackageName is requestDetails.requestPackageName.
	RequestPackageName string
	// RequestHash is requestDetails.requestHash (standard requests): the
	// value the app bound the verdict to.
	RequestHash string
	// Timestamp is requestDetails.timestampMillis.
	Timestamp time.Time
	// AppRecognition is appIntegrity.appRecognitionVerdict.
	AppRecognition string
	// AppPackageName is appIntegrity.packageName.
	AppPackageName string
	// CertificateDigests is appIntegrity.certificateSha256Digest.
	CertificateDigests []string
	// DeviceRecognition is deviceIntegrity.deviceRecognitionVerdict.
	DeviceRecognition []string
	// AppLicensing is accountDetails.appLicensingVerdict.
	AppLicensing string
}

// The verdict values the policy requires (Google Play Integrity API).
const (
	AppPlayRecognized     = "PLAY_RECOGNIZED"
	DeviceMeetsIntegrity  = "MEETS_DEVICE_INTEGRITY"
	AppLicensingLicensed  = "LICENSED"
	maxIntegrityTokenSize = 16 << 10
)

// MaxIntegrityTokenBytes bounds a Play Integrity token as presented (the
// Integrity-Token header, ADR-0031 D31.2).
const MaxIntegrityTokenBytes = maxIntegrityTokenSize

// IntegrityTier is the outcome of an assessment.
type IntegrityTier string

const (
	// TierCertified is a verdict that passed every check.
	TierCertified IntegrityTier = "certified"
	// TierRestricted is a verdict that failed at least one check (ADR-0010's
	// restricted tier). The reasons say which.
	TierRestricted IntegrityTier = "restricted"
)

// IntegrityReason is why a verdict was restricted. The vocabulary is closed,
// so it can be counted and alerted on without carrying verdict data.
type IntegrityReason string

// The reasons, in the order Assess checks them.
const (
	ReasonPackageMismatch     IntegrityReason = "package_mismatch"
	ReasonRequestHashMismatch IntegrityReason = "request_hash_mismatch"
	ReasonStaleVerdict        IntegrityReason = "stale_verdict"
	ReasonFutureVerdict       IntegrityReason = "future_verdict"
	ReasonAppNotRecognized    IntegrityReason = "app_not_recognized"
	ReasonCertificateMismatch IntegrityReason = "certificate_mismatch"
	ReasonDeviceIntegrity     IntegrityReason = "device_integrity_missing"
	ReasonUnlicensed          IntegrityReason = "unlicensed"
)

// IntegrityAssessment is the policy's answer for one verdict.
type IntegrityAssessment struct {
	Tier    IntegrityTier
	Reasons []IntegrityReason
}

// IntegrityPolicy is what a genuine verdict must show.
type IntegrityPolicy struct {
	// PackageName is the Android application id (for example
	// com.example.app): the request's and the app's package must both equal it.
	PackageName string
	// CertificateDigests are the accepted base64url SHA-256 digests of the
	// app's signing certificate. The verdict must carry at least one.
	CertificateDigests []string
	// MaxAge bounds how old a verdict may be.
	MaxAge time.Duration
	// FutureSkew bounds how far in the future a verdict timestamp may be.
	FutureSkew time.Duration
	// DecodeTimeout bounds one Decode call, within (0, MaxDecodeTimeout].
	// Decoding is a network call to Google (ADR-0010 §12: "the one slow
	// operation"), so it must not consume the request's whole budget.
	DecodeTimeout time.Duration
}

// MaxDecodeTimeout is the longest DecodeTimeout a policy may set: well inside
// identity's 10-second handler budget.
const MaxDecodeTimeout = 5 * time.Second

var (
	packageNamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$`)
	digestPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// Validate refuses a policy that could not tell a genuine verdict from a
// forged one.
func (p IntegrityPolicy) Validate() error {
	switch {
	case !packageNamePattern.MatchString(p.PackageName):
		return fmt.Errorf("%w: Play Integrity package name must be an Android application id", ErrInvalidIntegrityPolicy)
	case len(p.CertificateDigests) == 0:
		return fmt.Errorf("%w: Play Integrity needs at least one signing-certificate digest", ErrInvalidIntegrityPolicy)
	case p.MaxAge <= 0 || p.MaxAge > 10*time.Minute:
		return fmt.Errorf("%w: Play Integrity verdict max age must be in (0, 10m]", ErrInvalidIntegrityPolicy)
	case p.FutureSkew < 0 || p.FutureSkew > time.Minute:
		return fmt.Errorf("%w: Play Integrity future skew must be in [0, 1m]", ErrInvalidIntegrityPolicy)
	case p.DecodeTimeout <= 0 || p.DecodeTimeout > MaxDecodeTimeout:
		return fmt.Errorf("%w: Play Integrity decode timeout must be in (0, %v]", ErrInvalidIntegrityPolicy, MaxDecodeTimeout)
	}
	for _, d := range p.CertificateDigests {
		if !digestPattern.MatchString(d) {
			return fmt.Errorf("%w: a Play Integrity certificate digest is not a base64url SHA-256", ErrInvalidIntegrityPolicy)
		}
	}
	return nil
}

// Assess applies the policy to a decoded verdict bound to requestHash. It is
// pure: no I/O, no clock but now. Every failed check is reported.
func (p IntegrityPolicy) Assess(v IntegrityVerdict, requestHash string, now time.Time) IntegrityAssessment {
	var reasons []IntegrityReason
	if v.RequestPackageName != p.PackageName || v.AppPackageName != p.PackageName {
		reasons = append(reasons, ReasonPackageMismatch)
	}
	if requestHash == "" || subtle.ConstantTimeCompare([]byte(v.RequestHash), []byte(requestHash)) != 1 {
		reasons = append(reasons, ReasonRequestHashMismatch)
	}
	switch {
	case v.Timestamp.IsZero() || now.Sub(v.Timestamp) > p.MaxAge:
		reasons = append(reasons, ReasonStaleVerdict)
	case v.Timestamp.Sub(now) > p.FutureSkew:
		reasons = append(reasons, ReasonFutureVerdict)
	}
	if v.AppRecognition != AppPlayRecognized {
		reasons = append(reasons, ReasonAppNotRecognized)
	}
	if !slices.ContainsFunc(v.CertificateDigests, func(d string) bool { return slices.Contains(p.CertificateDigests, d) }) {
		reasons = append(reasons, ReasonCertificateMismatch)
	}
	if !slices.Contains(v.DeviceRecognition, DeviceMeetsIntegrity) {
		reasons = append(reasons, ReasonDeviceIntegrity)
	}
	if v.AppLicensing != AppLicensingLicensed {
		reasons = append(reasons, ReasonUnlicensed)
	}
	if len(reasons) > 0 {
		return IntegrityAssessment{Tier: TierRestricted, Reasons: reasons}
	}
	return IntegrityAssessment{Tier: TierCertified}
}

// IntegrityBinding is what a verdict must be bound to.
type IntegrityBinding struct {
	// DeviceKeyThumbprint is the RFC 7638 thumbprint of the device's enrolled
	// key (ADR-0010 §5(c)). The attestation is recorded against it.
	DeviceKeyThumbprint string
	// RequestHash is the value the app was told to bind the verdict to.
	RequestHash string
}

var thumbprintPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// SetIntegrityVerifier configures Play Integrity verification. It is
// construction-time configuration, like SetOtpSender. The policy is
// validated; nil v leaves verification unavailable.
func (s *IdentityService) SetIntegrityVerifier(v IntegrityVerifier, p IntegrityPolicy) error {
	if v == nil {
		s.integrity = nil
		return nil
	}
	if err := p.Validate(); err != nil {
		return err
	}
	s.integrity = &integrityCheck{verifier: v, policy: p}
	return nil
}

type integrityCheck struct {
	verifier IntegrityVerifier
	policy   IntegrityPolicy
}

// IntegrityOutcome is what one VerifyPlayIntegrity call ended in. The
// vocabulary is closed, so it can be counted (ADR-0010 §9: "Play Integrity
// verdict distribution") without carrying any verdict data.
type IntegrityOutcome string

const (
	// IntegrityCertified is a verdict that passed every check.
	IntegrityCertified IntegrityOutcome = "certified"
	// IntegrityRestricted is a verdict that failed at least one check.
	IntegrityRestricted IntegrityOutcome = "restricted"
	// IntegrityRejected is a token the verifier rejected.
	IntegrityRejected IntegrityOutcome = "rejected"
	// IntegrityUnavailable is no verifier, an outage, or a timeout.
	IntegrityUnavailable IntegrityOutcome = "unavailable"
	// IntegrityInvalidRequest is a missing or malformed token or binding,
	// refused before the verifier is called.
	IntegrityInvalidRequest IntegrityOutcome = "invalid_request"
	// IntegrityNotPresented is an issuance or refresh request that carried no
	// integrity token (ADR-0031 D31.3). It is counted apart from
	// invalid_request so a client that does not attest is visible as such.
	IntegrityNotPresented IntegrityOutcome = "not_presented"
)

// SessionIntegrityTier is the Play Integrity tier a session records
// (ADR-0031 D31.4). It is a signal, not an authorisation (D31.5).
type SessionIntegrityTier string

const (
	// SessionCertified is a session whose latest verdict passed every check.
	SessionCertified SessionIntegrityTier = "certified"
	// SessionRestricted is a session whose latest verdict failed a check.
	SessionRestricted SessionIntegrityTier = "restricted"
	// SessionUnverified is a session with no verdict: no token was presented,
	// or no verifier could answer. When the restricted tier gets permissions
	// it is to be treated as restricted (ADR-0031 D31.3); it is recorded
	// apart so outages never inflate the failed-verdict signal.
	SessionUnverified SessionIntegrityTier = "unverified"
)

// valid reports whether t is a tier a repository may store; the zero value is
// stored as SessionUnverified.
func (t SessionIntegrityTier) valid() bool {
	switch t {
	case "", SessionCertified, SessionRestricted, SessionUnverified:
		return true
	}
	return false
}

// ErrInvalidIntegrityTier is a session carrying an unknown integrity tier.
var ErrInvalidIntegrityTier = errors.New("identity: unknown session integrity tier")

// IntegrityRequestHash is the requestHash a verdict presented with proof must
// carry (ADR-0031 D31.1): base64url, unpadded, of SHA-256 over the DPoP proof
// exactly as it appears in the request's DPoP header. The proof binds the
// device key, the request and single use; through this, so does the verdict.
func IntegrityRequestHash(proof string) string {
	sum := sha256.Sum256([]byte(proof))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// reassessIntegrity is D31.4 at refresh: with a token, the session's tier is
// re-assessed and set on session, for the caller's compare-and-swap to write.
// Without one, the tier is kept. A rejected token is ErrPlayIntegrityFailed.
func (s *IdentityService) reassessIntegrity(ctx context.Context, session *UserSession, token, proof, jkt string) error {
	if token == "" {
		return nil
	}
	tier, err := s.assessIntegrity(ctx, token, proof, jkt, SecurityEvent{SessionID: session.SessionID})
	if err != nil {
		return err
	}
	session.IntegrityTier = tier
	return nil
}

// integrityAssessor decides a new session's tier, after the OTP is verified
// and before the session is created.
type integrityAssessor func(context.Context) (SessionIntegrityTier, error)

// assessIntegrity is ADR-0031 D31.3's table for one issuance or refresh. The
// audit events it emits name the request or session that who carries.
func (s *IdentityService) assessIntegrity(ctx context.Context, token, proof, jkt string, who SecurityEvent) (SessionIntegrityTier, error) {
	if token == "" {
		s.observeIntegrity(IntegrityNotPresented, nil)
		return SessionUnverified, nil
	}
	a, err := s.VerifyPlayIntegrity(ctx, token, IntegrityBinding{DeviceKeyThumbprint: jkt, RequestHash: IntegrityRequestHash(proof)})
	switch {
	case errors.Is(err, ErrPlayIntegrityUnavailable):
		return SessionUnverified, nil
	case errors.Is(err, ErrPlayIntegrityFailed):
		who.Type, who.At = EventIntegrityRejected, s.now()
		s.audit(who)
		return "", err
	case err != nil:
		// Recording the attestation failed: fail closed.
		return "", err
	case a.Tier == TierCertified:
		return SessionCertified, nil
	default:
		who.Type, who.At = EventIntegrityRestricted, s.now()
		s.audit(who)
		return SessionRestricted, nil
	}
}

// SetIntegrityObserver sets the function told the outcome of every
// VerifyPlayIntegrity call, and the restriction reasons of a restricted one.
// It receives nothing from the token or the verdict beyond those. It must be
// safe for concurrent use. Nil removes it.
func (s *IdentityService) SetIntegrityObserver(observe func(IntegrityOutcome, []IntegrityReason)) {
	s.integrityObserver = observe
}

func (s *IdentityService) observeIntegrity(o IntegrityOutcome, reasons []IntegrityReason) {
	if s.integrityObserver != nil {
		s.integrityObserver(o, reasons)
	}
}

// decode calls the verifier under the policy's DecodeTimeout. The wait is
// bounded even for a verifier that ignores its context; its eventual answer
// is discarded (the channel is buffered, so its goroutine always finishes).
func (c *integrityCheck) decode(ctx context.Context, token string) (IntegrityVerdict, error) {
	dctx, cancel := context.WithTimeout(ctx, c.policy.DecodeTimeout)
	defer cancel()
	type result struct {
		v   IntegrityVerdict
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := c.verifier.Decode(dctx, token)
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-dctx.Done():
		return IntegrityVerdict{}, dctx.Err()
	}
}

// VerifyPlayIntegrity decodes token, assesses it against the configured
// policy and binding, and records the outcome against the device key.
//
// Without a verifier, or when the verifier cannot answer, it returns
// ErrPlayIntegrityUnavailable and records nothing. A token the verifier
// rejects returns ErrPlayIntegrityFailed and records nothing. Otherwise it
// returns the assessment, certified or restricted, and records it: the
// attestation never holds the raw token, and nothing is invented about the
// device.
func (s *IdentityService) VerifyPlayIntegrity(ctx context.Context, token string, b IntegrityBinding) (IntegrityAssessment, error) {
	if s.integrity == nil {
		s.observeIntegrity(IntegrityUnavailable, nil)
		return IntegrityAssessment{}, ErrPlayIntegrityUnavailable
	}
	if token == "" || len(token) > maxIntegrityTokenSize || !thumbprintPattern.MatchString(b.DeviceKeyThumbprint) || b.RequestHash == "" {
		s.observeIntegrity(IntegrityInvalidRequest, nil)
		return IntegrityAssessment{}, ErrPlayIntegrityFailed
	}
	verdict, err := s.integrity.decode(ctx, token)
	switch {
	case errors.Is(err, ErrIntegrityTokenInvalid):
		s.observeIntegrity(IntegrityRejected, nil)
		return IntegrityAssessment{}, ErrPlayIntegrityFailed
	case err != nil:
		s.observeIntegrity(IntegrityUnavailable, nil)
		return IntegrityAssessment{}, fmt.Errorf("%w: the verifier could not answer", ErrPlayIntegrityUnavailable)
	}
	now := s.now()
	a := s.integrity.policy.Assess(verdict, b.RequestHash, now)
	if err := s.repo.SaveDevice(ctx, &DeviceAttestation{
		DeviceID:        "dev_" + b.DeviceKeyThumbprint,
		AttestedAt:      now,
		IsPlayCertified: a.Tier == TierCertified,
	}); err != nil {
		return IntegrityAssessment{}, err
	}
	if a.Tier == TierCertified {
		s.observeIntegrity(IntegrityCertified, nil)
	} else {
		s.observeIntegrity(IntegrityRestricted, a.Reasons)
	}
	return a, nil
}
