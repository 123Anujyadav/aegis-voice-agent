// Package app is the identity service's domain: OTP challenges, sessions,
// device-key enrolment, proof-bound refresh and access-token issuance.
package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/middleware"
)

var (
	// ErrInvalidOtp is a wrong code, or a challenge that does not exist.
	//
	// Its message used to read "invalid or expired OTP code", which was accurate
	// only because expiry returned this same error. The domain spec,
	// docs/domain/10-identity.md, requires the opposite: "Expiry and wrong-code are DISTINCT outcomes —
	// collapsing them makes an expired code look like user error." The message no
	// longer mentions expiry because this error no longer represents it.
	//
	// A MISSING challenge deliberately still returns this rather than a
	// "no such request" error: distinguishing them would tell an unauthenticated
	// caller which request ids exist.
	ErrInvalidOtp = errors.New("identity: invalid OTP code")

	// ErrExpiredOtp is a challenge that exists but whose window has passed.
	//
	// Distinct from ErrInvalidOtp so a client can tell a user to request a new
	// code rather than re-read the one they have. It carries no attempt cost:
	// the expiry check runs before the code is compared, so an expired challenge
	// never consumes an attempt regardless of what was submitted.
	ErrExpiredOtp = errors.New("identity: OTP challenge has expired")

	// ErrInvalidMSISDN is an MSISDN that is not in canonical E.164 form. See
	// isCanonicalMSISDN for exactly what that means and what it does not.
	ErrInvalidMSISDN = errors.New("identity: MSISDN is not canonical E.164 (+ then 7 to 15 digits, first digit 1-9)")

	// ErrSessionConflict is returned by IdentityRepository.ReplaceSession when
	// the stored session is no longer the one the caller read: it was rotated,
	// revoked or removed in between. It is a repository-level signal, not a
	// client-facing error; RefreshToken answers it with ErrInvalidToken.
	ErrSessionConflict = errors.New("identity: session changed since it was read")

	// ErrRateLimited is an MSISDN that has used its OTP request allowance, or
	// has reached the failed-verification limit and is locked out. See
	// OtpRatePolicy. It carries no detail about which limit applied.
	ErrRateLimited = errors.New("identity: too many OTP requests or failed verifications for this number")

	// ErrDeviceKeyAlreadyEnrolled is a write that would replace a session's
	// enrolled device key. Enrolment binds once; a new key is a new
	// enrolment, through OTP, into a new session.
	ErrDeviceKeyAlreadyEnrolled = errors.New("identity: session already has an enrolled device key")
	// ErrEnrollmentUnavailable is device-key enrolment requested of a service
	// with no proof verifier configured. It fails closed rather than creating
	// an unenrolled session the caller believes is bound.
	ErrEnrollmentUnavailable = errors.New("identity: device-key enrolment is not configured")
	// ErrInvalidDeviceProof is an enrolment proof that did not verify, or was
	// replayed. No OTP state is touched when it is returned.
	ErrInvalidDeviceProof = errors.New("identity: invalid device-key proof")
	// ErrTokenIssuanceUnavailable is a signed access token requested of a
	// service with no signer configured. It is returned before any proof or
	// OTP state is touched, and there is no unsigned fallback.
	ErrTokenIssuanceUnavailable = errors.New("identity: access-token signing is not configured")
	// ErrAccessTokenIssuance is a signing failure AFTER the enrolled session
	// was created. That session is revoked rather than left live without a
	// token, and the caller is given neither the session nor a token.
	ErrAccessTokenIssuance = errors.New("identity: access token could not be issued")
	// ErrDeviceProofRequired is a device-bound session refreshed through
	// RefreshToken, which carries no proof. Such a session is refreshed only by
	// RefreshTokenWithDeviceProof; the refusal comes after the reuse check and
	// without rotating, so the presented refresh token stays valid.
	ErrDeviceProofRequired = errors.New("identity: refreshing a device-bound session requires a device-key proof")
	// ErrDeviceRefreshUnavailable is a proof-bound refresh requested of a
	// service with no refresh proof verifier configured. Nothing is read,
	// verified or rotated.
	ErrDeviceRefreshUnavailable = errors.New("identity: device-bound refresh is not configured")
	// ErrSessionNotDeviceBound is a proof-bound refresh of a session with no
	// enrolled key. There is no binding to prove, so no bound token can be
	// issued; nothing is rotated and the proof is not spent.
	ErrSessionNotDeviceBound = errors.New("identity: session has no enrolled device key")
	// ErrRefreshTokenNotHashed is a session write that carries a raw refresh
	// token, or a refresh-token hash that is not canonical. Repositories refuse
	// it, so a raw token cannot be persisted by mistake.
	ErrRefreshTokenNotHashed = errors.New("identity: a session may be stored only with a canonical refresh-token hash")
	// ErrMSISDNNotPseudonymised is challenge or rate-limit state keyed by
	// anything other than a canonical MSISDNPseudonym — a raw phone number
	// included. Repositories refuse it, so a raw number cannot be persisted.
	ErrMSISDNNotPseudonymised = errors.New("identity: OTP and rate-limit state may be keyed only by an MSISDN pseudonym")
	// ErrOtpDeliveryFailed is a code the configured OtpSender could not hand
	// on. The challenge is removed: a code nobody received must not stay live.
	ErrOtpDeliveryFailed = errors.New("identity: the one-time code could not be delivered")

	// ErrInvalidToken is an access or refresh token that does not verify or
	// names no live session.
	ErrInvalidToken = errors.New("identity: invalid access or refresh token")
	// ErrTokenReused is a refresh token presented after it was rotated away:
	// the sign of a stolen token, so the whole session family is revoked.
	ErrTokenReused = errors.New("identity: refresh token reuse detected - revoking family session")
	// ErrPlayIntegrityFailed is a device attestation that did not verify.
	ErrPlayIntegrityFailed = errors.New("identity: Google Play Integrity attestation failed")
	// ErrGuardianForbidden is a guardian with no delegation for the subscriber.
	ErrGuardianForbidden = errors.New("identity: guardian access denied for target subscriber")
)

// DeviceAttestation is a device's integrity verdict as identity recorded it.
type DeviceAttestation struct {
	DeviceID        string    `json:"deviceId"`
	Model           string    `json:"model"`
	OsVersion       string    `json:"osVersion"`
	IntegrityToken  string    `json:"integrityToken"`
	AttestedAt      time.Time `json:"attestedAt"`
	IsPlayCertified bool      `json:"isPlayCertified"`
}

// UserSession is one signed-in subscriber device: the refresh-token family
// and, once enrolled, the device key its tokens are bound to.
type UserSession struct {
	SessionID    string `json:"sessionId"`
	SubscriberID string `json:"subscriberId"`
	// There is no MSISDN on a session. Nothing read it, and it is a personal
	// identifier: SubscriberID is the session's (keyed, non-reversible) link to
	// the number.
	//
	// RefreshToken is the RAW refresh token, and is NEVER persisted. It is set
	// only on the copy of a newly created session that the service returns to
	// its caller, so the caller can deliver it to the client. Every repository
	// refuses a session that carries it, and no repository returns it: a
	// session read back from storage has it empty.
	RefreshToken string `json:"refreshToken"`
	// RefreshTokenHash is the only stored form of the refresh token:
	// HashRefreshToken(identity HMAC key, raw token). It is never serialised.
	RefreshTokenHash string    `json:"-"`
	DeviceID         string    `json:"deviceId"`
	Roles            []string  `json:"roles"`
	CreatedAt        time.Time `json:"createdAt"`
	ExpiresAt        time.Time `json:"expiresAt"`
	IsRevoked        bool      `json:"isRevoked"`
	// DeviceKeyThumbprint is the RFC 7638 thumbprint of the device key this
	// session was enrolled with, set only from a VERIFIED enrolment proof
	// (ValidateOtpWithDeviceProof). Empty means not enrolled. Once set it never
	// changes: the repository refuses a write that would replace it.
	DeviceKeyThumbprint string `json:"deviceKeyThumbprint,omitempty"`
}

// refreshTokenDomain separates refresh-token hashes from everything else the
// same HMAC key hashes (OTP codes, subscriber ids), so no value hashed for one
// purpose can stand in for another.
const refreshTokenDomain = "callscreen.identity.refresh-token.v1\x00"

// HashRefreshToken is the persisted form of a refresh token:
// HMAC-SHA-256 under the identity HMAC key, over a domain-separated input,
// as 64 lowercase hex characters. Keyed, so a copy of the database alone
// cannot be used to test guesses; never an unkeyed SHA-256.
func HashRefreshToken(key []byte, raw string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(refreshTokenDomain))
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

// msisdnDomain separates MSISDN pseudonyms from every other value the identity
// HMAC key produces: refresh-token hashes, OTP code hashes and subscriber ids.
const msisdnDomain = "callscreen.identity.msisdn.v1\x00"

// MSISDNPseudonym is the persisted, lookup form of a phone number:
// HMAC-SHA-256 under the identity HMAC key, over a domain-separated input, as
// 64 lowercase hex characters. It is computed ONLY from the canonical E.164
// spelling (isCanonicalMSISDN) and refuses anything else, so one number has
// exactly one pseudonym and no alternate spelling can earn a fresh rate-limit
// allowance. It is keyed, not anonymous: anyone holding the key can test a
// guessed number against it, so it is treated as sensitive.
func MSISDNPseudonym(key []byte, msisdn string) (string, error) {
	if !isCanonicalMSISDN(msisdn) {
		return "", ErrInvalidMSISDN
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msisdnDomain))
	h.Write([]byte(msisdn))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ValidMSISDNPseudonym reports whether p is in MSISDNPseudonym's canonical form.
// A raw number never is: it starts with '+'.
func ValidMSISDNPseudonym(p string) bool { return isLowerHex64(p) }

// ValidRefreshTokenHash reports whether h is in the canonical form
// HashRefreshToken produces: exactly 64 lowercase hex characters.
func ValidRefreshTokenHash(h string) bool { return isLowerHex64(h) }

// isLowerHex64 is the canonical form of every HMAC-SHA-256 value persisted here.
func isLowerHex64(h string) bool {
	if len(h) != 2*sha256.Size {
		return false
	}
	for i := 0; i < len(h); i++ {
		if c := h[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// CheckStorableSession is the rule every repository applies before storing a
// session: no raw refresh token, and a canonical refresh-token hash.
func CheckStorableSession(s *UserSession) error {
	if s.RefreshToken != "" || !ValidRefreshTokenHash(s.RefreshTokenHash) {
		return ErrRefreshTokenNotHashed
	}
	return nil
}

// equalHash compares two hashes in constant time.
func equalHash(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

// GuardianDelegation is a guardian's granted access to a subscriber.
type GuardianDelegation struct {
	GuardianID   string    `json:"guardianId"`
	SubscriberID string    `json:"subscriberId"`
	Permissions  []string  `json:"permissions"`
	GrantedAt    time.Time `json:"grantedAt"`
}

// OtpChallenge is one issued one-time code, stored only as its HMAC.
type OtpChallenge struct {
	RequestID string `json:"requestId"`
	// MSISDNPseudonym keys the number's rate-limit and lockout state
	// (MSISDNPseudonym). The raw number is never stored.
	MSISDNPseudonym string `json:"-"`
	// SubscriberID is derived from the raw number when the challenge is
	// created, the only moment the number is in hand, and becomes the session's.
	SubscriberID string    `json:"subscriberId"`
	CodeHash     string    `json:"codeHash"`
	Attempts     int       `json:"attempts"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// IdentityRepository is identity's persistence port: challenges, sessions,
// devices, delegations and per-number rate-limit state.
type IdentityRepository interface {
	SaveOtp(ctx context.Context, challenge *OtpChallenge) error
	GetOtp(ctx context.Context, requestID string) (*OtpChallenge, error)
	DeleteOtp(ctx context.Context, requestID string) error
	// ConsumeOtp removes the challenge and reports whether THIS call removed it,
	// atomically: of any number of concurrent calls for one challenge, exactly
	// one gets consumed == true. A challenge already gone is (false, nil); any
	// error is the store's own. It is what makes an OTP single-use under
	// concurrency — DeleteOtp succeeds whether or not anything was there.
	ConsumeOtp(ctx context.Context, requestID string) (consumed bool, err error)
	// RecordFailedOtpAttempt increments the stored challenge's attempt counter
	// and returns the new count, atomically. If the challenge no longer exists
	// it stores nothing and returns ErrInvalidOtp — so a wrong guess can never
	// write back a challenge that a correct guess has already consumed, and
	// concurrent wrong guesses cannot lose each other's increments.
	RecordFailedOtpAttempt(ctx context.Context, requestID string) (attempts int, err error)

	// Per-MSISDN rate limiting, keyed by the number's MSISDNPseudonym — never
	// the raw number: every rate method, and SaveOtp, refuses a key that is not
	// a canonical pseudonym with ErrMSISDNNotPseudonymised. Each method is ONE
	// atomic step on the number's
	// state, so concurrent requests cannot both pass a limit by reading the same
	// count. now is passed in rather than read here, so the policy's clock is
	// the service's and tests can control it.
	//
	// ReserveOtpRequest records an OTP request for msisdn, or refuses with
	// ErrRateLimited when the number is locked out or already has
	// p.MaxRequests requests within p.Window. A refusal is a *RateLimitError
	// carrying when it lifts.
	ReserveOtpRequest(ctx context.Context, msisdnPseudonym string, now time.Time, p OtpRatePolicy) error
	// ReserveOtpVerification records a PROVISIONAL failed verification for
	// msisdn before a code is compared, or refuses with ErrRateLimited when
	// the number is locked out or already has p.MaxFailures within p.Window.
	// The reservation that reaches p.MaxFailures locks the number until
	// now+p.Lockout. Reserving before comparing is what bounds the number of
	// codes ever EVALUATED, concurrent requests included. A refusal is a
	// *RateLimitError carrying when it lifts.
	//
	// It returns an id unique to this reservation. Timestamps cannot serve:
	// concurrent reservations can share one (and do, under a coarse clock), and
	// "which reservation locked the number" must have exactly one answer.
	ReserveOtpVerification(ctx context.Context, msisdnPseudonym string, now time.Time, p OtpRatePolicy) (reservationID uint64, err error)
	// ReleaseOtpVerification withdraws a reservation whose outcome was not a
	// counted failure (a store failure, or a challenge consumed concurrently),
	// and the lockout with it if that reservation is the one that set it.
	ReleaseOtpVerification(ctx context.Context, msisdnPseudonym string, reservationID uint64) error
	// ConfirmOtpFailure records that the reservation was a genuine wrong code.
	// It returns the lockout end if, and only if, THIS reservation set a lock
	// that still stands — atomically, so one lockout is reported exactly once
	// and a lock already cleared by a concurrent success is not reported.
	ConfirmOtpFailure(ctx context.Context, msisdnPseudonym string, reservationID uint64) (lockedUntil time.Time, err error)
	// ResetOtpFailures clears msisdn's failures and lockout after a successful
	// verification.
	ResetOtpFailures(ctx context.Context, msisdnPseudonym string) error
	// SaveSession and ReplaceSession refuse, with ErrRefreshTokenNotHashed and
	// before anything else, a session that fails CheckStorableSession: one
	// carrying a raw RefreshToken, or a RefreshTokenHash that is not canonical.
	// Refresh tokens are stored, compared and returned ONLY as
	// HashRefreshToken values; GetSession never returns a raw token.
	SaveSession(ctx context.Context, session *UserSession) error
	GetSession(ctx context.Context, sessionID string) (*UserSession, error)
	// ReplaceSession stores session only if, ATOMICALLY with the write, the
	// stored session with that id still exists, is not revoked, and still holds
	// expectedRefreshTokenHash. Otherwise it stores nothing and returns
	// ErrSessionConflict. Any other error is the store's own.
	//
	// It is what makes refresh-token rotation single-use: a read-compare-
	// SaveSession sequence let two refreshes of one token both succeed, and
	// let a rotation overwrite a revocation that landed after the read.
	ReplaceSession(ctx context.Context, session *UserSession, expectedRefreshTokenHash string) error
	RevokeSession(ctx context.Context, sessionID string) error
	SaveDevice(ctx context.Context, device *DeviceAttestation) error
	GetGuardianDelegation(ctx context.Context, guardianID, subscriberID string) (*GuardianDelegation, error)
}

// MemoryIdentityRepository is an in-memory IdentityRepository.
//
// # It copies on the way in and on the way out
//
// Every Save* stores a copy of what it was given, and every Get* returns a copy
// of what it holds. A caller therefore cannot reach persisted state through a
// pointer it happens to be holding: the only way to change what is stored is to
// call a Save* method.
//
// THIS IS NOT A MICRO-OPTIMISATION QUESTION, IT IS A CORRECTNESS ONE. When this
// type returned its stored pointers, `challenge.Attempts++` in ValidateOtp
// mutated the persisted record by aliasing, and the OTP attempt limit appeared
// to work without anything ever writing it back. IdentityRepository is an
// interface: a SQL- or Redis-backed implementation deserialises a fresh struct
// on every read, so the same service code would have discarded every increment
// and left a six-digit code open to unlimited guessing. Behaviour that differs
// between two implementations of one interface is a defect in whichever
// implementation is quieter about it.
//
// The copies are deep with respect to the reference-typed fields that exist:
// UserSession.Roles and GuardianDelegation.Permissions are slices, and a
// struct copy alone would still share their backing arrays.
type MemoryIdentityRepository struct {
	mu          sync.RWMutex
	otps        map[string]*OtpChallenge
	sessions    map[string]*UserSession
	devices     map[string]*DeviceAttestation
	delegations map[string]*GuardianDelegation
	otpRates    map[string]*otpRateState
	// nextReservation numbers verification reservations; guarded by mu.
	nextReservation uint64
}

// otpRateState is one MSISDN's rate-limit state: the times of its OTP requests
// and of its counted failed verifications (both pruned to the policy window on
// every use), and the end of any lockout.
type otpRateState struct {
	requests    []time.Time
	failures    []otpFailure
	lockedUntil time.Time
	// lockedBy is the id of the reservation that triggered the lockout, so
	// releasing that reservation (it turned out not to be a failure) withdraws
	// the lock, and only that reservation's failure reports it.
	lockedBy uint64
}

// otpFailure is one counted, or provisionally counted, failed verification.
type otpFailure struct {
	at time.Time
	id uint64
}

func failureTimes(fs []otpFailure) []time.Time {
	out := make([]time.Time, len(fs))
	for i, f := range fs {
		out[i] = f.at
	}
	return out
}

func failuresWithinWindow(fs []otpFailure, now time.Time, window time.Duration) []otpFailure {
	kept := fs[:0]
	for _, f := range fs {
		if now.Sub(f.at) < window {
			kept = append(kept, f)
		}
	}
	return kept
}

// RateLimitError is ErrRateLimited with the moment the restriction lifts. It
// unwraps to ErrRateLimited, so errors.Is(err, ErrRateLimited) holds for it and
// callers that only need the fact keep working. RetryAt is computed in the same
// locked step that refused the request, never from a later read.
type RateLimitError struct {
	RetryAt time.Time
}

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// slotFreesAt is when a full rolling window next admits one more event: the
// oldest kept time plus the window. The minimum is taken rather than the first
// element because concurrent callers read their clock before taking the lock,
// so append order need not be time order.
func slotFreesAt(times []time.Time, window time.Duration) time.Time {
	oldest := times[0]
	for _, t := range times[1:] {
		if t.Before(oldest) {
			oldest = t
		}
	}
	return oldest.Add(window)
}

// limitedUntil returns the refusal for a number: the LATEST of its lockout end
// and, if its counted window is full, the moment that window frees a slot. A
// client told to retry at that time is past every restriction that applied.
func limitedUntil(st *otpRateState, now time.Time, times []time.Time, limit int, window time.Duration) error {
	var retryAt time.Time
	if now.Before(st.lockedUntil) {
		retryAt = st.lockedUntil
	}
	if len(times) >= limit {
		if at := slotFreesAt(times, window); at.After(retryAt) {
			retryAt = at
		}
	}
	return &RateLimitError{RetryAt: retryAt}
}

// withinWindow keeps only the times less than window before now.
func withinWindow(times []time.Time, now time.Time, window time.Duration) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	return kept
}

// cloneOtp copies a challenge. All fields are value types.
func cloneOtp(c *OtpChallenge) *OtpChallenge {
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}

// cloneSession copies a session, including its Roles slice.
func cloneSession(s *UserSession) *UserSession {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Roles != nil {
		cp.Roles = append([]string(nil), s.Roles...)
	}
	return &cp
}

// cloneDevice copies an attestation. All fields are value types.
func cloneDevice(d *DeviceAttestation) *DeviceAttestation {
	if d == nil {
		return nil
	}
	cp := *d
	return &cp
}

// cloneDelegation copies a delegation, including its Permissions slice.
func cloneDelegation(g *GuardianDelegation) *GuardianDelegation {
	if g == nil {
		return nil
	}
	cp := *g
	if g.Permissions != nil {
		cp.Permissions = append([]string(nil), g.Permissions...)
	}
	return &cp
}

// NewMemoryIdentityRepository returns an empty in-memory repository.
func NewMemoryIdentityRepository() *MemoryIdentityRepository {
	return &MemoryIdentityRepository{
		otps:        make(map[string]*OtpChallenge),
		sessions:    make(map[string]*UserSession),
		devices:     make(map[string]*DeviceAttestation),
		delegations: make(map[string]*GuardianDelegation),
		otpRates:    make(map[string]*otpRateState),
	}
}

// rateStateLocked returns msisdn's state, creating it. Caller holds m.mu.
func (m *MemoryIdentityRepository) rateStateLocked(msisdn string) *otpRateState {
	st, ok := m.otpRates[msisdn]
	if !ok {
		st = &otpRateState{}
		m.otpRates[msisdn] = st
	}
	return st
}

// The four rate-limit operations each run entirely under the write lock: the
// check and the update are one step. That is their whole atomicity — one
// process's memory. Replicas each holding their own repository would each
// enforce the limit separately.

// ReserveOtpRequest implements IdentityRepository.
func (m *MemoryIdentityRepository) ReserveOtpRequest(ctx context.Context, msisdn string, now time.Time, p OtpRatePolicy) error {
	if !ValidMSISDNPseudonym(msisdn) {
		return ErrMSISDNNotPseudonymised
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.rateStateLocked(msisdn)
	st.requests = withinWindow(st.requests, now, p.Window)
	if now.Before(st.lockedUntil) || len(st.requests) >= p.MaxRequests {
		return limitedUntil(st, now, st.requests, p.MaxRequests, p.Window)
	}
	st.requests = append(st.requests, now)
	return nil
}

// ReserveOtpVerification implements IdentityRepository.
func (m *MemoryIdentityRepository) ReserveOtpVerification(ctx context.Context, msisdn string, now time.Time, p OtpRatePolicy) (uint64, error) {
	if !ValidMSISDNPseudonym(msisdn) {
		return 0, ErrMSISDNNotPseudonymised
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.rateStateLocked(msisdn)
	st.failures = failuresWithinWindow(st.failures, now, p.Window)
	if now.Before(st.lockedUntil) || len(st.failures) >= p.MaxFailures {
		return 0, limitedUntil(st, now, failureTimes(st.failures), p.MaxFailures, p.Window)
	}
	m.nextReservation++
	id := m.nextReservation
	st.failures = append(st.failures, otpFailure{at: now, id: id})
	if len(st.failures) >= p.MaxFailures {
		st.lockedUntil = now.Add(p.Lockout)
		st.lockedBy = id
	}
	return id, nil
}

// ReleaseOtpVerification implements IdentityRepository.
func (m *MemoryIdentityRepository) ReleaseOtpVerification(ctx context.Context, msisdn string, reservationID uint64) error {
	if !ValidMSISDNPseudonym(msisdn) {
		return ErrMSISDNNotPseudonymised
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.otpRates[msisdn]
	if !ok {
		return nil
	}
	for i, f := range st.failures {
		if f.id == reservationID {
			st.failures = append(st.failures[:i], st.failures[i+1:]...)
			// A released reservation was not a failure, so a lockout that it
			// triggered is withdrawn with it.
			if st.lockedBy == reservationID {
				st.lockedUntil, st.lockedBy = time.Time{}, 0
			}
			return nil
		}
	}
	return nil
}

// ConfirmOtpFailure implements IdentityRepository.
func (m *MemoryIdentityRepository) ConfirmOtpFailure(ctx context.Context, msisdn string, reservationID uint64) (time.Time, error) {
	if !ValidMSISDNPseudonym(msisdn) {
		return time.Time{}, ErrMSISDNNotPseudonymised
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.otpRates[msisdn]
	if !ok || reservationID == 0 || st.lockedBy != reservationID || st.lockedUntil.IsZero() {
		return time.Time{}, nil
	}
	return st.lockedUntil, nil
}

// ResetOtpFailures implements IdentityRepository.
func (m *MemoryIdentityRepository) ResetOtpFailures(ctx context.Context, msisdn string) error {
	if !ValidMSISDNPseudonym(msisdn) {
		return ErrMSISDNNotPseudonymised
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.otpRates[msisdn]; ok {
		st.failures = nil
		st.lockedUntil, st.lockedBy = time.Time{}, 0
	}
	return nil
}

// SaveOtp implements IdentityRepository.
func (m *MemoryIdentityRepository) SaveOtp(ctx context.Context, challenge *OtpChallenge) error {
	if !ValidMSISDNPseudonym(challenge.MSISDNPseudonym) {
		return ErrMSISDNNotPseudonymised
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.otps[challenge.RequestID] = cloneOtp(challenge)
	return nil
}

// GetOtp implements IdentityRepository.
func (m *MemoryIdentityRepository) GetOtp(ctx context.Context, requestID string) (*OtpChallenge, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	otp, exists := m.otps[requestID]
	if !exists {
		return nil, ErrInvalidOtp
	}
	return cloneOtp(otp), nil
}

// DeleteOtp implements IdentityRepository.
func (m *MemoryIdentityRepository) DeleteOtp(ctx context.Context, requestID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.otps, requestID)
	return nil
}

// ConsumeOtp checks for and deletes the challenge under one write lock. That is
// the whole of its atomicity: one process's memory, not a distributed store.
func (m *MemoryIdentityRepository) ConsumeOtp(ctx context.Context, requestID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.otps[requestID]; !exists {
		return false, nil
	}
	delete(m.otps, requestID)
	return true, nil
}

// RecordFailedOtpAttempt increments the STORED counter under the write lock,
// never re-inserting a challenge that is gone.
func (m *MemoryIdentityRepository) RecordFailedOtpAttempt(ctx context.Context, requestID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, exists := m.otps[requestID]
	if !exists {
		return 0, ErrInvalidOtp
	}
	stored.Attempts++
	return stored.Attempts, nil
}

// replacesEnrolledKey reports whether writing next over stored would change a
// device key already enrolled on it.
func replacesEnrolledKey(stored, next *UserSession) bool {
	return stored != nil && stored.DeviceKeyThumbprint != "" &&
		next.DeviceKeyThumbprint != stored.DeviceKeyThumbprint
}

// SaveSession implements IdentityRepository.
func (m *MemoryIdentityRepository) SaveSession(ctx context.Context, session *UserSession) error {
	if err := CheckStorableSession(session); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := m.sessions[session.SessionID]
	if replacesEnrolledKey(stored, session) {
		return ErrDeviceKeyAlreadyEnrolled
	}
	cp := cloneSession(session)
	// REVOCATION IS ONE-WAY. A blind overwrite from a copy read before the
	// revocation must not un-revoke the session; the PostgreSQL repository
	// keeps the same rule (is_revoked = old OR new).
	if stored != nil && stored.IsRevoked {
		cp.IsRevoked = true
	}
	m.sessions[session.SessionID] = cp
	return nil
}

// GetSession implements IdentityRepository.
func (m *MemoryIdentityRepository) GetSession(ctx context.Context, sessionID string) (*UserSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, exists := m.sessions[sessionID]
	if !exists {
		return nil, ErrInvalidToken
	}
	return cloneSession(session), nil
}

// ReplaceSession is the compare-and-swap: the check and the write happen under
// one write lock, so no other call on this repository can interleave between
// them. That is the whole of its atomicity — it is one process's memory.
func (m *MemoryIdentityRepository) ReplaceSession(ctx context.Context, session *UserSession, expectedRefreshTokenHash string) error {
	if err := CheckStorableSession(session); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, exists := m.sessions[session.SessionID]
	if !exists || stored.IsRevoked || !equalHash(stored.RefreshTokenHash, expectedRefreshTokenHash) {
		return ErrSessionConflict
	}
	if replacesEnrolledKey(stored, session) {
		return ErrDeviceKeyAlreadyEnrolled
	}
	m.sessions[session.SessionID] = cloneSession(session)
	return nil
}

// RevokeSession mutates the repository's OWN stored value, under the write lock.
//
// That is not the aliasing this type now prevents: the pointer being written
// here never left the repository, because Get* returns copies. Revocation is a
// repository operation, so it is correct for it to write in place rather than
// requiring the caller to read, mutate and save back.
func (m *MemoryIdentityRepository) RevokeSession(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, exists := m.sessions[sessionID]; exists {
		s.IsRevoked = true
	}
	return nil
}

// SaveDevice implements IdentityRepository.
func (m *MemoryIdentityRepository) SaveDevice(ctx context.Context, device *DeviceAttestation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[device.DeviceID] = cloneDevice(device)
	return nil
}

// GetGuardianDelegation implements IdentityRepository.
func (m *MemoryIdentityRepository) GetGuardianDelegation(ctx context.Context, guardianID, subscriberID string) (*GuardianDelegation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := guardianID + ":" + subscriberID
	del, exists := m.delegations[key]
	if !exists {
		return nil, ErrGuardianForbidden
	}
	return cloneDelegation(del), nil
}

// SecurityEventType names an identity security audit event.
type SecurityEventType string

const (
	// EventOtpVerificationFailed records a code determined to be WRONG for a
	// challenge that existed, was not exhausted and had not expired.
	EventOtpVerificationFailed SecurityEventType = "identity.otp.verification_failed"
	// EventOtpLockout records a failed verification that moved the number into
	// a lockout.
	// Emitted once per lockout, not for each request refused during it.
	EventOtpLockout SecurityEventType = "identity.otp.lockout"
	// EventRefreshTokenReused records a refresh token presented after it was
	// rotated away: the exfiltration alarm (ADR-0010 §6, §9). The session
	// family is revoked; the event is emitted whether or not the revocation
	// succeeded, because the reuse happened either way.
	EventRefreshTokenReused SecurityEventType = "identity.refresh.token_reused"
)

// SecurityEvent is one identity security audit record, as required by
// docs/domain/10-identity.md ("Audit Requirements — Change on failure and
// lockout, for abuse detection").
//
// DELIBERATELY WITHOUT ANY MSISDN-DERIVED VALUE. The only keyed derivation this
// package has is hashString(MSISDN), and that IS the subscriber id (usr_ +
// its first 32 hex chars) — while the domain requires a VerificationChallenge
// to "reference no Subscriber" and not reveal whether one exists. No approved
// audit pseudonym exists yet, so none is invented here. RequestID is the
// challenge's own random identifier: it identifies the aggregate the audit is
// about and references nobody.
//
// Never added here: the MSISDN, the code, its hash, any token or key.
type SecurityEvent struct {
	Type      SecurityEventType
	At        time.Time
	RequestID string
	// LockedUntil is set on EventOtpLockout only.
	LockedUntil time.Time
	// SessionID is set on EventRefreshTokenReused only: the revoked session's
	// own random identifier (sess_…), which references nobody.
	SessionID string
}

// SecurityAuditor receives identity security audit events.
//
// Synchronous and expected to be fast. An error does not change the outcome it
// describes — a rejected code stays rejected and no session is created on its
// account — and is counted (see IdentityService.AuditFailures), the same
// contract governance.Auditor states. This is NOT a durable, exactly-once
// pipeline: an event is emitted after the state change it describes, not
// atomically with it.
type SecurityAuditor interface {
	Record(SecurityEvent) error
}

// NoopSecurityAuditor discards events. The default, so behaviour is unchanged
// until a sink is configured.
type NoopSecurityAuditor struct{}

// Record discards the event.
func (NoopSecurityAuditor) Record(SecurityEvent) error { return nil }

// OtpRatePolicy bounds OTP use per MSISDN: at most MaxRequests OTP requests
// and MaxFailures failed verifications within any rolling Window, and a
// Lockout once MaxFailures is reached. During a lockout both requests and
// verifications are refused.
type OtpRatePolicy struct {
	MaxRequests int
	MaxFailures int
	Window      time.Duration
	Lockout     time.Duration
}

// defaultOtpRatePolicy is the selected INITIAL policy: three requests and five
// failed verifications per number in any 15 minutes, then a 15-minute lockout.
// These are starting defaults, not production-tuned values. ADR-0010 (a) asks
// for exponential backoff and a per-device limit as well; neither is here.
var defaultOtpRatePolicy = OtpRatePolicy{
	MaxRequests: 3,
	MaxFailures: 5,
	Window:      15 * time.Minute,
	Lockout:     15 * time.Minute,
}

// DefaultOtpRatePolicy is the policy every IdentityService enforces (there is
// no setter). Housekeeping derives its retention windows from it, so rows are
// kept for as long as this policy can count them.
func DefaultOtpRatePolicy() OtpRatePolicy { return defaultOtpRatePolicy }

// IdentityService is identity's application core: one-time codes, device-key
// enrolment, signed access-token issuance and refresh-token rotation.
type IdentityService struct {
	repo      IdentityRepository
	secretKey []byte

	// ratePolicy and now drive per-MSISDN rate limiting. now is injected for
	// the same reason newCode is: so windows can be tested without waiting.
	ratePolicy OtpRatePolicy
	now        func() time.Time

	// auditor receives security audit events; auditFailures counts the ones
	// it refused, so a failing sink is visible rather than silent.
	auditor       SecurityAuditor
	auditFailures atomic.Uint64

	// deviceProof verifies device-key enrolment proofs. Nil — the default —
	// means enrolment is not configured, and ValidateOtpWithDeviceProof
	// refuses. It is set only by an explicit SetDeviceProofVerifier.
	deviceProof *dpop.Verifier

	// accessTokens signs device-bound access tokens. Nil — the default — means
	// signing is not configured, and nothing issues an access token. It is set
	// only by an explicit SetAccessTokenSigner; this package never builds one,
	// never reads a key, and has no issuer, audience or key id of its own.
	accessTokens *accesstoken.Signer

	// refreshProof verifies device-key proofs for refresh. It is a separate
	// verifier from deviceProof because a proof names one URL (htu): each is
	// configured with its own endpoint's canonical URL. Nil — the default —
	// means proof-bound refresh is not configured and is refused.
	refreshProof *dpop.Verifier

	// otpSender delivers one-time codes. Nil — the default, and the only value
	// production code sets today — means no delivery exists; see SetOtpSender.
	otpSender OtpSender
	// integrity is Play Integrity verification (integrity.go); nil means
	// unavailable, and VerifyPlayIntegrity then fails closed.
	integrity *integrityCheck
	// integrityObserver is told every VerifyPlayIntegrity outcome; nil is none.
	integrityObserver func(IntegrityOutcome, []IntegrityReason)

	// newCode mints the one-time code. Injected rather than called directly so
	// a test can make the value predictable WITHOUT the production path ever
	// gaining a way to do so: the field is unexported and has no setter, so only
	// this package can replace it. RequestOtp does not return the code and the
	// HTTP layer does not carry it, so there is otherwise no way for a caller —
	// or a test — to learn what was generated.
	newCode func() string
}

// NewIdentityService builds the service on repo, with secretKey as the HMAC
// key for codes, refresh tokens and number pseudonyms.
func NewIdentityService(repo IdentityRepository, secretKey []byte) *IdentityService {
	return &IdentityService{
		repo:       repo,
		secretKey:  secretKey,
		newCode:    secureOtpCode,
		ratePolicy: defaultOtpRatePolicy,
		now:        time.Now,
		auditor:    NoopSecurityAuditor{},
	}
}

// OtpLifetime is how long a challenge's code is valid. The HTTP API reports it
// to the caller, so it is defined once.
const OtpLifetime = 5 * time.Minute

// otpDigits is the code length. Six is the existing shape (the previous
// hardcoded value was "482910") and matches the 300-second expiry the HTTP
// layer advertises.
const otpDigits = 6

// secureOtpCode returns a uniformly random six-digit code.
//
// It draws with crypto/rand.Int over exactly 10^6 rather than reading bytes
// and taking a remainder: the modulo of a byte range that is not a multiple of 10^6 is
// biased, and a biased OTP shrinks the search space an attacker must cover.
//
// A panic on entropy failure follows the reasoning packages/go/runtime/ids.go
// already records for the same situation: an error here means the OS entropy
// source has failed, and every code minted afterwards would be predictable.
// Returning a code anyway would be worse than not starting.
func secureOtpCode() string {
	upper := big.NewInt(1)
	for i := 0; i < otpDigits; i++ {
		upper.Mul(upper, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, upper)
	if err != nil {
		panic(fmt.Sprintf("identity: entropy source unavailable: %v", err))
	}
	return fmt.Sprintf("%0*d", otpDigits, n)
}

// isCanonicalMSISDN reports whether s is an E.164 number in its one canonical
// spelling: "+", a first digit 1-9, then digits only, 7 to 15 digits in all —
// the ^\+[1-9][0-9]{6,14}$ rule, checked byte by byte so that non-ASCII digits,
// whitespace and separators can never match.
//
// SYNTAX ONLY. It does not establish that the number is allocated, reachable, a
// mobile rather than a landline (docs/domain/10-identity.md also requires that),
// valid in any particular country, or still held by the same person.
func isCanonicalMSISDN(s string) bool {
	if len(s) < 8 || len(s) > 16 || s[0] != '+' || s[1] < '1' || s[1] > '9' {
		return false
	}
	for i := 2; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// SetDeviceProofVerifier enables device-key enrolment with v. There is no
// default verifier, because its canonical URL is a deployment fact.
func (s *IdentityService) SetDeviceProofVerifier(v *dpop.Verifier) { s.deviceProof = v }

// SetAccessTokenSigner configures access-token signing. The signer, its key and
// its issuer, audience and key id are the caller's; accesstoken.NewSigner has
// already validated them.
func (s *IdentityService) SetAccessTokenSigner(signer *accesstoken.Signer) { s.accessTokens = signer }

// SetRefreshProofVerifier enables proof-bound refresh with v, which must be
// configured with the refresh endpoint's canonical URL — never the enrolment
// endpoint's, and never one taken from a request. There is no default.
func (s *IdentityService) SetRefreshProofVerifier(v *dpop.Verifier) { s.refreshProof = v }

// OtpSender delivers a one-time code to a phone number: the one place the code
// and the raw number leave the service. No implementation exists in this
// repository (there is no SMS provider); tests supply fakes.
type OtpSender interface {
	SendOtp(ctx context.Context, msisdn, code string) error
}

// SetOtpSender configures OTP delivery. It is construction-time configuration,
// not safe to change while requests are being served.
func (s *IdentityService) SetOtpSender(o OtpSender) { s.otpSender = o }

// CanDeliverOtp reports whether an OtpSender is configured. Without one,
// RequestOtp still creates a challenge, but nothing can deliver its code — so
// a caller that would tell a user "a code is on its way" must check this first.
func (s *IdentityService) CanDeliverOtp() bool { return s.otpSender != nil }

// SetSecurityAuditor configures where security audit events go. It is
// construction-time configuration, not safe to change while requests are
// being served. Nil restores the default, which discards events.
func (s *IdentityService) SetSecurityAuditor(a SecurityAuditor) {
	if a == nil {
		a = NoopSecurityAuditor{}
	}
	s.auditor = a
}

// AuditFailures reports how many security audit events the auditor refused.
func (s *IdentityService) AuditFailures() uint64 { return s.auditFailures.Load() }

func (s *IdentityService) audit(e SecurityEvent) {
	if err := s.auditor.Record(e); err != nil {
		s.auditFailures.Add(1)
	}
}

// confirmFailure finalises a reservation as a genuine wrong code and emits its
// audit events: always the failure, and the lockout only when THIS failure is
// the one that locked the number (ConfirmOtpFailure answers that atomically,
// so a lockout is reported once however many guesses race).
func (s *IdentityService) confirmFailure(ctx context.Context, msisdnPseudonym, requestID string, reservationID uint64, at time.Time) error {
	lockedUntil, err := s.repo.ConfirmOtpFailure(ctx, msisdnPseudonym, reservationID)
	s.audit(SecurityEvent{Type: EventOtpVerificationFailed, At: at, RequestID: requestID})
	if err != nil {
		return err
	}
	if !lockedUntil.IsZero() {
		s.audit(SecurityEvent{Type: EventOtpLockout, At: at, RequestID: requestID, LockedUntil: lockedUntil})
	}
	return nil
}

// RequestOtp issues a one-time code for msisdn, which must be canonical, and
// returns the challenge's request ID. Rate limits apply per number.
func (s *IdentityService) RequestOtp(ctx context.Context, msisdn string) (string, error) {
	// ONLY THE CANONICAL SPELLING IS ACCEPTED, and it is checked before any OTP
	// work. The value is stored on the challenge and ValidateOtp derives the
	// subscriber id from it (usr_ + HMAC(MSISDN)), so "+919876543210" and
	// "919876543210" used to become two subscribers — and the database's
	// UNIQUE(msisdn), which compares strings, could not tell them apart.
	// Nothing is trimmed or rewritten: a non-canonical value is refused, not
	// repaired, so one number has exactly one stored form.
	if !isCanonicalMSISDN(msisdn) {
		return "", ErrInvalidMSISDN
	}

	// PER-NUMBER RATE LIMIT, before any OTP work. The per-challenge attempt
	// limit alone was bypassable: each new challenge brought three fresh
	// guesses. The key is the canonical MSISDN validated just above, so one
	// number has one allowance; a malformed value never reaches here and so
	// consumes none. A store failure is returned, never treated as allowed.
	//
	// THE NUMBER ITSELF GOES NO FURTHER THAN THIS FUNCTION. Everything stored
	// is keyed by its pseudonym, and the subscriber id is derived here, once.
	pseudonym, err := s.msisdnPseudonym(msisdn)
	if err != nil {
		return "", err
	}
	if err := s.repo.ReserveOtpRequest(ctx, pseudonym, s.now(), s.ratePolicy); err != nil {
		return "", err
	}

	// RequestID is the lookup key for this OTP challenge (see ValidateOtp ->
	// repo.GetOtp), so it must be unique AND unguessable.
	//
	// It was fmt.Sprintf("req_%d", time.Now().UnixNano()). That is neither.
	// UnixNano reports whatever resolution the platform clock has, not
	// nanoseconds: measured here, 1600 concurrent generations produced 2
	// distinct values, one of them reused 1599 times. Two callers requesting an
	// OTP at the same moment would share a challenge id, so one would overwrite
	// the other's challenge — or validate against it.
	reqID := "req_" + s.generateSecureToken()

	// WAS `code := "482910"` — the SAME six digits for every request and every
	// phone number, so anyone who had ever seen one OTP could verify any MSISDN.
	// The attempt limit below was irrelevant against it: one guess sufficed.
	//
	// Only the HASH is stored, and the code is never logged, never returned from
	// this function and never placed in the HTTP response. That is deliberate and
	// it has a consequence, recorded rather than worked around: NOTHING DELIVERS
	// THIS CODE. ADR-0010 (a) specifies SMS OTP at enrolment; no SMS path exists
	// (see packages/go/integrations, whose provider clients place no calls), so
	// in production today no caller can learn the code and the flow cannot be
	// completed. Returning the code to make the flow "work" would publish the
	// credential to every unauthenticated caller of this endpoint.
	code := s.newCode()
	hash := s.hashString(code)

	challenge := &OtpChallenge{
		RequestID:       reqID,
		MSISDNPseudonym: pseudonym,
		SubscriberID:    s.subscriberID(msisdn),
		CodeHash:        hash,
		Attempts:        0,
		ExpiresAt:       time.Now().Add(OtpLifetime),
	}

	if err := s.repo.SaveOtp(ctx, challenge); err != nil {
		return "", err
	}

	// DELIVERY, when configured: the raw number and the code go to the sender
	// and nowhere else. A code that could not be handed on is withdrawn — a
	// live challenge nobody received is a guessing target with no legitimate
	// user. The request still counts against the number's allowance, so a
	// failing provider cannot be used to request without limit. The sender's
	// error is not returned: its text is the provider's and may echo the number.
	if s.otpSender != nil {
		if err := s.otpSender.SendOtp(ctx, msisdn, code); err != nil {
			_ = s.repo.DeleteOtp(ctx, reqID) //nolint:errcheck // best effort: an undelivered code expires anyway, and the request still counts
			return "", ErrOtpDeliveryFailed
		}
	}
	return reqID, nil
}

// maxOtpAttempts is how many WRONG codes a challenge tolerates before it is
// exhausted.
//
// Three, taken from the behaviour that was already here rather than chosen: the
// counter is incremented only on a wrong code, and the challenge was deleted at
// `Attempts >= 3`. So three wrong guesses are allowed and the third exhausts the
// challenge — including the case where the third guess is CORRECT, which still
// succeeds, because the guard below runs before the increment for this attempt.
const maxOtpAttempts = 3

// ValidateOtp verifies an OTP and creates a session that is NOT bound to any
// device key.
//
// THE ACCESS TOKEN IS ALWAYS EMPTY. It was the unsigned "Bearer_JWT.<hex json>"
// format, carrying the plaintext MSISDN and a hardcoded tenant, that anyone
// could forge. Every access token this service issues is signed and carries the
// cnf.jkt of an enrolled device key; an unenrolled session has no key to bind,
// so it gets no token rather than a weaker one. The return value is kept so
// existing callers compile; an empty string is not a credential.
func (s *IdentityService) ValidateOtp(ctx context.Context, requestID, code string) (*UserSession, string, error) {
	session, err := s.validateOtp(ctx, requestID, code, "")
	if err != nil {
		return nil, "", err
	}
	return session, "", nil
}

// ValidateOtpWithDeviceProof is device-key enrolment: an OTP validation that
// also proves possession of the device's EC P-256 key, binding the resulting
// session to that key's thumbprint.
//
// The proof is verified, and its identifier reserved against replay, BEFORE
// any OTP state is read or changed: an invalid or replayed proof costs no
// attempt, counts no failure and consumes no challenge. The method argument is
// the HTTP method of the request the proof was made for; the URL it must name is the
// verifier's configured canonical URL, never one taken from the request.
//
// The binding is written in the same single SaveSession that creates the
// session, so a session either exists enrolled or does not exist.
//
// It returns the session and a signed access token whose cnf.jkt is the
// session's STORED binding (see issueBoundAccessToken). Both the verifier and
// the signer must be configured; either missing is refused before anything is
// verified or consumed. If signing fails after the session was created, that
// session is revoked and neither it nor a token is returned: the OTP and the
// proof are both spent, so the client starts again with a new OTP.
func (s *IdentityService) ValidateOtpWithDeviceProof(ctx context.Context, requestID, code, method, proof string) (*UserSession, string, error) {
	if s.deviceProof == nil {
		return nil, "", ErrEnrollmentUnavailable
	}
	if s.accessTokens == nil {
		return nil, "", ErrTokenIssuanceUnavailable
	}
	verified, err := s.deviceProof.Verify(ctx, proof, method)
	switch {
	case errors.Is(err, dpop.ErrInvalidProof):
		return nil, "", fmt.Errorf("%w: %w", ErrInvalidDeviceProof, err)
	case err != nil:
		// The replay store could not answer: fail closed, as the store's
		// error rather than as a rejected proof.
		return nil, "", err
	}
	session, err := s.validateOtp(ctx, requestID, code, verified.JKT)
	if err != nil {
		return nil, "", err
	}
	token, err := s.issueBoundAccessToken(ctx, session.SessionID)
	if err != nil {
		// The session exists, enrolled, but its holder would have no access
		// token. Revoke it rather than hand back a refresh token for a login
		// that did not complete. If even that fails, both errors are reported:
		// the session is then live in the store, but its refresh token was
		// never disclosed.
		if rerr := s.repo.RevokeSession(ctx, session.SessionID); rerr != nil {
			return nil, "", fmt.Errorf("%w: %w; revoking the session also failed: %w", ErrAccessTokenIssuance, err, rerr)
		}
		return nil, "", fmt.Errorf("%w: %w", ErrAccessTokenIssuance, err)
	}
	return session, token, nil
}

// issueBoundAccessToken signs an access token for the STORED session. Subject,
// roles and cnf.jkt are read from the repository, never from a caller: there is
// no parameter through which a thumbprint could arrive, and a session object a
// caller has modified does not change what is signed. A session that is
// missing, revoked, expired or not enrolled gets no token.
func (s *IdentityService) issueBoundAccessToken(ctx context.Context, sessionID string) (string, error) {
	if s.accessTokens == nil {
		return "", ErrTokenIssuanceUnavailable
	}
	stored, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if stored.IsRevoked || time.Now().After(stored.ExpiresAt) || stored.DeviceKeyThumbprint == "" {
		return "", ErrAccessTokenIssuance
	}
	return s.accessTokens.Issue(stored.SubscriberID, stored.Roles, stored.DeviceKeyThumbprint)
}

// wrongCode handles a code that is WRONG for a live, unexhausted, unexpired
// challenge (validateOtp has already reserved the per-number verification).
// It always returns a non-nil error: ErrInvalidOtp, or a store error.
func (s *IdentityService) wrongCode(ctx context.Context, challenge *OtpChallenge, requestID string, reservationID uint64, reservedAt time.Time) error {
	challenge.Attempts++
	// THE INCREMENT MUST BE PERSISTED, or the attempt limit does not exist.
	//
	// It previously relied on MemoryIdentityRepository.GetOtp returning the
	// stored POINTER, so `challenge.Attempts++` mutated the stored record by
	// aliasing. IdentityRepository is an interface: any implementation that
	// returns a fresh struct — which is what a SQL or Redis-backed one does —
	// would have discarded every increment, leaving Attempts permanently 0
	// and a six-digit code open to unlimited guessing.
	//
	// AND IT MUST BE CONDITIONAL. This was SaveOtp(challenge): a blind write
	// of the copy read above. A correct guess that consumed the challenge
	// after that read was undone by it — the challenge was written back and
	// the used code accepted again — and concurrent wrong guesses, each
	// writing its own stale count, lost one another's increments.
	// RecordFailedOtpAttempt increments the stored counter only while the
	// challenge exists; a challenge consumed meanwhile is simply refused.
	attempts, err := s.repo.RecordFailedOtpAttempt(ctx, requestID)
	if errors.Is(err, ErrInvalidOtp) {
		// Consumed by a concurrent request since it was read: not an
		// incorrect code against a live challenge, so not a counted
		// failure and not an audit event. A failure to withdraw the
		// reservation is the store's error — still a rejection.
		if rerr := s.repo.ReleaseOtpVerification(ctx, challenge.MSISDNPseudonym, reservationID); rerr != nil {
			return rerr
		}
		return ErrInvalidOtp
	}
	// The code WAS wrong for a live challenge, whether or not the
	// per-challenge counter could be written: the failure stands and is
	// audited, and a store error is still returned as one.
	if cerr := s.confirmFailure(ctx, challenge.MSISDNPseudonym, requestID, reservationID, reservedAt); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if attempts >= maxOtpAttempts {
		_ = s.repo.DeleteOtp(ctx, requestID) //nolint:errcheck // best effort: the attempt guard refuses an exhausted challenge whether or not it is deleted
	}
	return ErrInvalidOtp
}

func (s *IdentityService) validateOtp(ctx context.Context, requestID, code, deviceKeyThumbprint string) (*UserSession, error) {
	challenge, err := s.repo.GetOtp(ctx, requestID)
	if err != nil {
		return nil, err
	}

	// THE ATTEMPT LIMIT IS ENFORCED HERE, NOT BY THE DELETE BELOW.
	//
	// Exhaustion used to be enforced solely by deleting the challenge once the
	// counter reached the limit, and every DeleteOtp call discards its error with
	// `_ =`. A delete that failed therefore left the challenge in place with
	// Attempts at or above the limit, and nothing on this path read Attempts on
	// entry — so the next call evaluated the supplied code as though the budget
	// were untouched, and the caller had unlimited further guesses.
	//
	// Reading the persisted counter makes the control independent of whether any
	// cleanup succeeded. The comparison below is not reached for an exhausted
	// challenge, so an attacker gains nothing by guessing against one.
	if challenge.Attempts >= maxOtpAttempts {
		// Best effort: reclaim the record. If this fails the guard above still
		// refuses the challenge on every subsequent call, which is the point.
		_ = s.repo.DeleteOtp(ctx, requestID) //nolint:errcheck // best effort: the attempt guard refuses this challenge whether or not it is deleted
		// ErrInvalidOtp, deliberately, and NOT a distinct "exhausted" error.
		// Telling a caller that a challenge is exhausted rather than simply
		// wrong discloses the state of someone else's login attempt, and no
		// stronger error for this condition exists in this package.
		return nil, ErrInvalidOtp
	}

	// EXPIRY IS CHECKED BEFORE THE CODE IS COMPARED, AND THAT ORDER IS THE
	// BEHAVIOUR, NOT AN ACCIDENT.
	//
	// An expired challenge returns ErrExpiredOtp whether the submitted code was
	// right or wrong: the comparison below is never reached, so an expired
	// challenge cannot consume an attempt and cannot be brute-forced after the
	// window closes.
	//
	// The error is distinct from ErrInvalidOtp because
	// docs/domain/10-identity.md requires it: "Expiry and wrong-code are
	// distinct outcomes — collapsing them makes an expired code look like user
	// error." A user who waited too long needs a new code; a user who mistyped
	// needs to retype. Returning one error for both told them the wrong thing.
	//
	// The delete stays best-effort: if it fails, this same check rejects the
	// challenge on every later call, so nothing depends on it succeeding.
	if time.Now().After(challenge.ExpiresAt) {
		_ = s.repo.DeleteOtp(ctx, requestID) //nolint:errcheck // best effort: the expiry check refuses this challenge whether or not it is deleted
		return nil, ErrExpiredOtp
	}

	// hmac.Equal, not `!=`. Both operands are HMAC digests rather than the raw
	// code, so a timing oracle here is weak — but hmac.Equal is already the
	// package's own constant-time primitive (crypto/hmac is imported for
	// hashString), and string comparison short-circuits on the first differing
	// byte. Using the constant-time form costs nothing and removes the question.
	// PER-NUMBER FAILED-VERIFICATION LIMIT. Reached only for a challenge that
	// exists, is not exhausted and has not expired, so missing and expired
	// challenges are never counted. The reservation is taken BEFORE the code
	// is compared, atomically: it is what bounds how many codes are ever
	// evaluated for a number, however many requests race. A wrong code keeps
	// it as a counted failure; any other outcome releases it or, on success,
	// resets the number.
	reservedAt := s.now()
	reservationID, err := s.repo.ReserveOtpVerification(ctx, challenge.MSISDNPseudonym, reservedAt, s.ratePolicy)
	if err != nil {
		return nil, err
	}

	if !hmac.Equal([]byte(s.hashString(code)), []byte(challenge.CodeHash)) {
		return nil, s.wrongCode(ctx, challenge, requestID, reservationID, reservedAt)
	}

	// FAIL CLOSED. THE CODE BEING CORRECT IS NOT SUFFICIENT TO AUTHENTICATE.
	//
	// A one-time code is only one-time if its consumption is confirmed. This was
	// `_ = s.repo.DeleteOtp(...)`: when the delete failed, the challenge stayed
	// stored with Attempts unchanged — usually 0 — so the entry guard above did
	// not apply to it either, and the same correct code could be replayed until
	// it expired. ADR-0010 already records that "SMS OTP is the weakest link and
	// we ship it anyway" because SMS is interceptable; a replay window on top of
	// that turns one intercepted code into a reusable credential.
	//
	// Returning here rather than after minting the session is the whole point:
	// no SessionID, no refresh token and no access token exist on this path, so
	// a failure cannot leave a usable identity behind.
	//
	// The other three DeleteOtp calls in this function stay best-effort on
	// purpose. Each already has an independent control: the exhausted-challenge
	// guard re-refuses on every later call, and the expiry check re-rejects an
	// expired challenge. Only this one is the sole thing standing between a
	// correct code and its reuse.
	//
	// The raw repository error is propagated, matching the SaveOtp and
	// SaveSession failures either side of it. No new error type is introduced.
	//
	// CONSUMPTION IS ATOMIC, AND ONLY THE CONSUMER PROCEEDS. This was DeleteOtp,
	// which succeeds whether or not the challenge was still there: two requests
	// that both read it and both passed the code check above both deleted
	// "successfully" and both minted a session from one single-use code. It
	// still happens AFTER the code check, so a wrong code can never consume a
	// valid challenge. A challenge consumed by a concurrent request is answered
	// exactly as a missing one is: ErrInvalidOtp.
	consumed, err := s.repo.ConsumeOtp(ctx, requestID)
	if err != nil || !consumed {
		// Not a wrong code, so not a failure: the reservation is withdrawn. A
		// failure to withdraw it is reported alongside, never swallowed.
		if rerr := s.repo.ReleaseOtpVerification(ctx, challenge.MSISDNPseudonym, reservationID); rerr != nil {
			return nil, errors.Join(err, rerr)
		}
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidOtp
	}

	// A successful verification resets the number's failures and lockout. If
	// the reset cannot be recorded this fails closed: the code is spent and no
	// session exists, rather than authenticating with stale failure state.
	if err := s.repo.ResetOtpFailures(ctx, challenge.MSISDNPseudonym); err != nil {
		return nil, err
	}

	// Same defect as RequestID above, on the identifier of an AUTHENTICATED
	// session: two concurrent logins shared one SessionID.
	sessionID := "sess_" + s.generateSecureToken()
	refreshToken := s.generateSecureToken()

	session := &UserSession{
		SessionID: sessionID,
		// SubscriberID must be STABLE for a subscriber (the same person signing
		// in twice is the same subscriber) and must NOT disclose the phone
		// number. It was:
		//
		//	"usr_" + hex.EncodeToString([]byte(challenge.MSISDN))[:12]
		//
		// which is the first SIX BYTES of the MSISDN in hex. Two failures,
		// both measured:
		//
		//  1. It collides. Six bytes of "+919876543210" is "+91987", shared by
		//     every Indian number with that prefix - three distinct test MSISDNs
		//     produced the single id usr_2b3931393837. SubscriberID is the
		//     `sub` claim of an access token, so distinct people were sharing
		//     a subject identity.
		//  2. It is reversible. hex-decoding the id returns "+91987" directly,
		//     putting a personal identifier in plaintext in every token, log line
		//     and JSON body that carries a subscriber id. SECURITY.md requires
		//     personal identifiers to be "replaced with a keyed HMAC so records
		//     remain correlatable without exposing the value".
		//
		// hashString is that keyed HMAC (HMAC-SHA256 under s.secretKey) and is
		// already used in this file for the OTP code. It is stable per MSISDN,
		// not reversible without the key, and 128 bits of it will not collide.
		// It is computed in RequestOtp (subscriberID), where the number is in
		// hand, and carried on the challenge: the number is not stored.
		SubscriberID: challenge.SubscriberID,
		// Only the keyed hash is stored; the raw token goes back to the caller
		// on a copy, below, and nowhere else.
		RefreshTokenHash: s.refreshTokenHash(refreshToken),
		Roles:            []string{"subscriber"},
		CreatedAt:        time.Now(),
		ExpiresAt:        time.Now().Add(24 * time.Hour),
		IsRevoked:        false,
		// Only ever a thumbprint the proof verifier produced, or empty.
		DeviceKeyThumbprint: deviceKeyThumbprint,
	}

	if err := s.repo.SaveSession(ctx, session); err != nil {
		return nil, err
	}
	issued := cloneSession(session)
	issued.RefreshToken = refreshToken
	return issued, nil
}

// msisdnPseudonym is MSISDNPseudonym under this service's HMAC key.
func (s *IdentityService) msisdnPseudonym(msisdn string) (string, error) {
	return MSISDNPseudonym(s.secretKey, msisdn)
}

// subscriberID is the stable subject identifier for a number: "usr_" and the
// first 32 hex characters of HMAC-SHA-256 over the canonical number. Unchanged
// from before pseudonymisation, so existing subscriber ids stay the same.
func (s *IdentityService) subscriberID(msisdn string) string {
	return "usr_" + s.hashString(msisdn)[:32]
}

// refreshTokenHash is HashRefreshToken under this service's HMAC key.
func (s *IdentityService) refreshTokenHash(raw string) string {
	return HashRefreshToken(s.secretKey, raw)
}

// RefreshToken rotates the refresh token of a session that is NOT bound to a
// device key. A device-bound session is refused here and refreshes only
// through RefreshTokenWithDeviceProof.
func (s *IdentityService) RefreshToken(ctx context.Context, sessionID, oldRefreshToken string) (string, string, error) {
	session, err := s.repo.GetSession(ctx, sessionID)
	if err != nil || session.IsRevoked {
		return "", "", ErrInvalidToken
	}

	// AN EXPIRED SESSION CANNOT BE REFRESHED. This was never checked, so a
	// session past its lifetime rotated indefinitely. The comparison is the one
	// ValidateOtp uses for a challenge: valid up to and including ExpiresAt.
	//
	// Checked BEFORE the reuse comparison and answered like a missing session,
	// with no revocation: expiry is not evidence of a stolen token, and a dead
	// session has nothing left to protect by revoking it.
	if time.Now().After(session.ExpiresAt) {
		return "", "", ErrInvalidToken
	}

	// Compared as keyed hashes, in constant time: the store holds no raw
	// token to compare against.
	presentedHash := s.refreshTokenHash(oldRefreshToken)
	if !equalHash(session.RefreshTokenHash, presentedHash) {
		// ErrTokenReused says the family was revoked. When the store refused the
		// revocation that is untrue — the session is still live for whoever
		// holds its current token — so the store's error travels with it. Both
		// stay matchable with errors.Is: the reuse is still a reuse, and the
		// failure is not hidden behind it. A successful revocation still
		// returns the sentinel itself.
		revokeErr := s.repo.RevokeSession(ctx, sessionID)
		s.audit(SecurityEvent{Type: EventRefreshTokenReused, At: time.Now(), SessionID: sessionID})
		if revokeErr != nil {
			return "", "", fmt.Errorf("%w: revoking the session failed: %w", ErrTokenReused, revokeErr)
		}
		return "", "", ErrTokenReused
	}

	// A DEVICE-BOUND SESSION IS NOT REFRESHED ON THE REFRESH TOKEN ALONE. Its
	// access tokens carry cnf.jkt, and ADR-0010 binds refresh to the device
	// key, so a refresh must prove possession of the enrolled key — through
	// RefreshTokenWithDeviceProof, never this method. It runs AFTER the reuse
	// check, so a stale token still revokes, and BEFORE rotation, so the token
	// presented stays valid for the proof-bound refresh.
	if session.DeviceKeyThumbprint != "" {
		return "", "", ErrDeviceProofRequired
	}

	newRefreshToken := s.generateSecureToken()
	session.RefreshTokenHash = s.refreshTokenHash(newRefreshToken)

	// A ROTATION THAT WAS NOT PERSISTED DID NOT HAPPEN. This error used to be
	// discarded and the new pair returned anyway; the store still held the old
	// token, so the client's next refresh was classified as reuse above and a
	// valid session was revoked. Failing here leaves the stored session exactly
	// as it was — session is GetSession's copy — so the caller's current
	// refresh token stays valid for a retry.
	//
	// The raw repository error is propagated, as ValidateOtp does for the same
	// call. No new error type is introduced.
	//
	// THE WRITE IS CONDITIONAL ON WHAT WAS READ. It was SaveSession, a blind
	// overwrite: two refreshes presenting the same token both passed the check
	// above before either wrote, so both were issued a pair and the later write
	// silently replaced the earlier; and a revocation landing between the read
	// and this write was overwritten by the copy read before it. ReplaceSession
	// stores only if the session is still unrevoked and still holds the token
	// this request presented.
	//
	// Losing that race is answered with ErrInvalidToken and NO revocation: this
	// request's token was valid when read and was rotated by a concurrent one,
	// which is not evidence of theft — and revoking here would destroy the
	// winner's freshly issued session. A later presentation of the stale token
	// still meets the reuse check above, which does revoke.
	if err := s.repo.ReplaceSession(ctx, session, presentedHash); err != nil {
		if errors.Is(err, ErrSessionConflict) {
			return "", "", ErrInvalidToken
		}
		return "", "", err
	}

	// NO ACCESS TOKEN. Only an unenrolled session reaches here, and an
	// unenrolled session has no device key to bind a token to. It used to get
	// the unsigned legacy format; it now gets none. Rotation itself — the CAS,
	// the reuse check and the revocation above — is unchanged.
	return "", newRefreshToken, nil
}

// RefreshTokenWithDeviceProof rotates a device-bound session's refresh token
// on proof of possession of its enrolled key, and returns a signed access token
// bound to that key with the new refresh token.
//
// The proof is the enrolment profile (dpop): ES256, exactly jti, htm, htu and
// iat, the htu being the refresh verifier's configured canonical URL and method
// the request's HTTP method. It carries no ath — this request presents a
// refresh token, not an access token — and the verifier rejects one.
//
// ORDER, and why:
//
//  1. Verifier and signer configured, else refused before anything is read.
//  2. Session missing, revoked or expired: ErrInvalidToken, exactly as
//     RefreshToken answers.
//  3. A refresh token that is not the current one is reuse: the session is
//     revoked, exactly as RefreshToken does. This runs BEFORE the proof, so a
//     stale token revokes whatever proof accompanies it.
//  4. An unenrolled session is refused: there is no key to prove.
//  5. The proof is verified and its jti reserved; then its key must be the
//     enrolled one. Any failure here returns without rotating or revoking, so
//     the presented refresh token stays valid — but a proof that passed
//     verification is spent, even if a later step fails, and must not be
//     resent.
//  6. The access token is signed from the STORED session, BEFORE rotating. A
//     signing failure therefore rotates nothing.
//  7. The rotation is the same compare-and-swap RefreshToken uses. Only if it
//     commits is anything returned: the token signed in 6 is released only
//     when the session it was signed from was still unrevoked and still held
//     the presented refresh token at the moment of the swap. Losing the swap
//     is ErrInvalidToken with no revocation, as in RefreshToken, and the
//     signed token is discarded.
func (s *IdentityService) RefreshTokenWithDeviceProof(ctx context.Context, sessionID, oldRefreshToken, method, proof string) (string, string, error) {
	if s.refreshProof == nil {
		return "", "", ErrDeviceRefreshUnavailable
	}
	if s.accessTokens == nil {
		return "", "", ErrTokenIssuanceUnavailable
	}

	session, err := s.repo.GetSession(ctx, sessionID)
	if err != nil || session.IsRevoked {
		return "", "", ErrInvalidToken
	}
	if time.Now().After(session.ExpiresAt) {
		return "", "", ErrInvalidToken
	}
	// Compared as keyed hashes, in constant time: the store holds no raw
	// token to compare against.
	presentedHash := s.refreshTokenHash(oldRefreshToken)
	if !equalHash(session.RefreshTokenHash, presentedHash) {
		revokeSessionErr := s.repo.RevokeSession(ctx, sessionID)
		s.audit(SecurityEvent{Type: EventRefreshTokenReused, At: time.Now(), SessionID: sessionID})
		if revokeSessionErr != nil {
			return "", "", fmt.Errorf("%w: revoking the session failed: %w", ErrTokenReused, revokeSessionErr)
		}
		return "", "", ErrTokenReused
	}
	if session.DeviceKeyThumbprint == "" {
		return "", "", ErrSessionNotDeviceBound
	}

	verified, err := s.refreshProof.Verify(ctx, proof, method)
	switch {
	case errors.Is(err, dpop.ErrInvalidProof):
		return "", "", fmt.Errorf("%w: %w", ErrInvalidDeviceProof, err)
	case err != nil:
		// The replay store could not answer: fail closed.
		return "", "", err
	}
	if verified.JKT != session.DeviceKeyThumbprint {
		return "", "", fmt.Errorf("%w: proof key is not the enrolled key", ErrInvalidDeviceProof)
	}

	accessToken, err := s.issueBoundAccessToken(ctx, sessionID)
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrAccessTokenIssuance, err)
	}

	newRefreshToken := s.generateSecureToken()
	session.RefreshTokenHash = s.refreshTokenHash(newRefreshToken)
	if err := s.repo.ReplaceSession(ctx, session, presentedHash); err != nil {
		if errors.Is(err, ErrSessionConflict) {
			return "", "", ErrInvalidToken
		}
		return "", "", err
	}
	return accessToken, newRefreshToken, nil
}

func (s *IdentityService) hashString(input string) string {
	h := hmac.New(sha256.New, s.secretKey)
	h.Write([]byte(input))
	return hex.EncodeToString(h.Sum(nil))
}

// generateSecureToken returns 32 bytes of cryptographic randomness, hex encoded.
//
// THE ENTROPY ERROR IS FATAL, NOT IGNORABLE. It was `_, _ = rand.Read(b)`.
// The function crypto/rand.Read leaves the buffer untouched on failure, so a failed read
// returned the zero value: 64 hex zeros, identical every time. This function
// mints the OTP challenge requestID, the SessionID and both the initial and
// rotated refresh tokens — four separate credentials that every caller treats
// as unguessable, all of which would have become the same constant.
//
// Panicking matches what this package and packages/go/runtime already do for
// the same condition (see secureOtpCode above, and runtime/ids.go, which records
// the reasoning: an error here means the OS entropy source has failed, and every
// identifier minted afterwards would be predictable, so continuing is worse than
// stopping). Using the established mechanism also keeps the signature unchanged,
// so none of the four call sites is affected.
func (s *IdentityService) generateSecureToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("identity: entropy source unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}

// isCredentialRejection reports whether err means "the credential was refused"
// rather than "the request could not be processed".
//
// The distinction decides an HTTP status, so it is stated once here instead of
// being spelled out at each call site. Only this package's own sentinels count:
// anything else reaching a handler came from the repository and is
// infrastructure, whatever it happens to say.
//
// ErrInvalidOtp covers a wrong code AND a challenge that does not exist — the
// repository returns it for a missing key deliberately, so that a caller cannot
// discover which request ids are real.
func isCredentialRejection(err error) bool {
	return errors.Is(err, ErrInvalidOtp) ||
		errors.Is(err, ErrExpiredOtp) ||
		errors.Is(err, ErrInvalidToken)
}

// retryAfterSeconds is the Retry-After value for a restriction lifting at
// retryAt: whole seconds, ROUNDED UP so a client is never told to retry before
// the restriction ends, and never less than 1 — a deadline already reached by
// the time the response is written still answers "1", not "0".
func retryAfterSeconds(retryAt, now time.Time) int64 {
	d := retryAt.Sub(now)
	secs := int64((d + time.Second - 1) / time.Second)
	if secs < 1 {
		return 1
	}
	return secs
}

// IdentityHandler is the legacy HTTP adapter over IdentityService. The
// production listener is internal/httpapi.
type IdentityHandler struct {
	service *IdentityService
}

// writeRateLimited answers ErrRateLimited with the platform's existing
// rate-limit response (packages/go/middleware RateLimitingMiddleware): the same
// status, title, detail and code, whichever limit applied and whether or not
// the number belongs to anyone.
//
// P-ID-5's countdown is the Retry-After header, set before the problem body is
// written (WriteProblemDetails writes the status line itself). It carries only
// a number of seconds: nothing about the MSISDN or which limit applied. An
// ErrRateLimited without a deadline gets no header rather than an invented one.
func (h *IdentityHandler) writeRateLimited(w http.ResponseWriter, r *http.Request, err error) {
	var rl *RateLimitError
	if errors.As(err, &rl) && !rl.RetryAt.IsZero() {
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds(rl.RetryAt, h.service.now()), 10))
	}
	middleware.WriteProblemDetails(w, http.StatusTooManyRequests, "Too Many Requests", "Rate limit exceeded", "ERR_RATE_LIMIT_EXCEEDED", r.URL.Path, "")
}

// NewIdentityHandler wraps service.
func NewIdentityHandler(service *IdentityService) *IdentityHandler {
	return &IdentityHandler{service: service}
}

// HandleRequestOtp serves a one-time-code request.
func (h *IdentityHandler) HandleRequestOtp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MSISDN string `json:"msisdn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MSISDN == "" {
		middleware.WriteProblemDetails(w, http.StatusBadRequest, "Bad Request", "Invalid MSISDN input", "ERR_INVALID_INPUT", r.URL.Path, "")
		return
	}

	reqID, err := h.service.RequestOtp(r.Context(), req.MSISDN)
	if errors.Is(err, ErrRateLimited) {
		h.writeRateLimited(w, r, err)
		return
	}
	if errors.Is(err, ErrInvalidMSISDN) {
		// The caller's input, not the store: the same 400 this handler already
		// gives an empty MSISDN, with the same fixed text — the sentinel's own
		// message is internal and is not echoed.
		middleware.WriteProblemDetails(w, http.StatusBadRequest, "Bad Request", "Invalid MSISDN input", "ERR_INVALID_INPUT", r.URL.Path, "")
		return
	}
	if err != nil {
		// The detail was err.Error(). Apart from ErrInvalidMSISDN, handled above,
		// RequestOtp propagates exactly one error — SaveOtp's — so everything
		// arriving here is the repository's own text:
		// driver messages, host names, database users, whatever the store
		// happens to say, handed to an unauthenticated caller.
		//
		// No classification branch is needed here, unlike HandleValidateOtp:
		// this endpoint has no credential to reject, so every error is
		// infrastructure. The status and code were already right; only the body
		// disclosed.
		//
		// The same fixed string as the ValidateOtp 500, so the two endpoints
		// cannot drift into describing the same condition differently.
		middleware.WriteProblemDetails(w, http.StatusInternalServerError, "Internal Error", "The request could not be processed. Please try again.", "ERR_INTERNAL", r.URL.Path, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// status was "SENT". Nothing sends anything: ADR-0010 (a) specifies SMS OTP,
	// and no SMS path exists in this repository. Reporting SENT told a client the
	// code was on its way to the handset, which is the one thing this endpoint
	// cannot do. No consumer reads this field — it is defined here and nowhere
	// else in the repository — so correcting it breaks nothing.
	_ = json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck // the status is sent; a failed body write is the client going away
		"requestId": reqID,
		"expiresIn": 300,
		"status":    "PENDING_DELIVERY",
	})
}

// HandleValidateOtp serves a one-time-code validation.
func (h *IdentityHandler) HandleValidateOtp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestID string `json:"requestId"`
		Code      string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RequestID == "" || req.Code == "" {
		middleware.WriteProblemDetails(w, http.StatusBadRequest, "Bad Request", "Missing request ID or OTP code", "ERR_INVALID_INPUT", r.URL.Path, "")
		return
	}

	session, token, err := h.service.ValidateOtp(r.Context(), req.RequestID, req.Code)
	if err != nil {
		if errors.Is(err, ErrRateLimited) {
			h.writeRateLimited(w, r, err)
			return
		}
		// An expired challenge is reported with its own machine-readable code so
		// a client can say "request a new code" instead of "check your code".
		// That distinction is the point of the separate error.
		//
		// STATUS STAYS 401. This package uses exactly three statuses — 400 for
		// malformed input, 401 for a rejected credential, 500 for internal
		// failure — and an expired OTP is a rejected credential. `code` is the
		// discriminator in a problem-details body, and inventing a fourth status
		// (410, say) would be adding an API convention this repository does not
		// have.
		//
		// The detail is a fixed string rather than err.Error(): it is the one
		// case here where the caller learns something actionable, and a fixed
		// string cannot later leak an internal message if the error's text
		// changes.
		if errors.Is(err, ErrExpiredOtp) {
			middleware.WriteProblemDetails(w, http.StatusUnauthorized, "Unauthorized", "The OTP challenge has expired. Request a new code.", "ERR_OTP_EXPIRED", r.URL.Path, "")
			return
		}
		if isCredentialRejection(err) {
			// The detail is err.Error() only because these are THIS package's own
			// sentinels, whose text is written to be shown. Preserved exactly as
			// it was.
			middleware.WriteProblemDetails(w, http.StatusUnauthorized, "Unauthorized", err.Error(), "ERR_UNAUTHORIZED", r.URL.Path, "")
			return
		}

		// EVERYTHING ELSE IS INFRASTRUCTURE, AND IS NOT THE CALLER'S FAULT.
		//
		// ValidateOtp propagates raw repository errors from four places — GetOtp,
		// SaveOtp, DeleteOtp and SaveSession. Every one of them used to arrive
		// here and be rendered as 401 with err.Error() in the body. That is wrong
		// twice over:
		//
		//   - A client told "401 Unauthorized" concludes its credential was
		//     rejected and stops. The correct code was in fact accepted and the
		//     STORE failed, which is a retryable condition it will never retry.
		//   - The detail carried the repository's own error text to an
		//     unauthenticated caller: driver messages, table or key names,
		//     connection strings — whatever the store happens to say.
		//
		// 500 / "Internal Error" / ERR_INTERNAL is the convention this package
		// already uses (HandleRequestOtp), so no new status or code is invented
		// here. The detail is a fixed string: the caller needs to know it can
		// retry, and nothing more.
		//
		// Nothing is logged because this package logs nothing anywhere, and
		// introducing a logging architecture is not this change. The consequence
		// is recorded honestly: the underlying error is currently discarded, so
		// an operator sees the 500 without the cause.
		middleware.WriteProblemDetails(w, http.StatusInternalServerError, "Internal Error", "The request could not be processed. Please try again.", "ERR_INTERNAL", r.URL.Path, "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	body := map[string]interface{}{
		"refreshToken": session.RefreshToken,
		"sessionId":    session.SessionID,
		"subscriberId": session.SubscriberID,
	}
	// accessToken is present only when there is one. ValidateOtp no longer
	// issues the unsigned legacy token, so this endpoint omits the field
	// rather than sending an empty string a client might present as a
	// credential.
	if token != "" {
		body["accessToken"] = token
	}
	_ = json.NewEncoder(w).Encode(body) //nolint:errcheck // the status is sent; a failed body write is the client going away
}
