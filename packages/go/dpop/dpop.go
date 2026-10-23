// Package dpop verifies the device-key proof presented at enrolment: an
// RFC 9449-aligned DPoP proof JWT, signed ES256 with the device's EC P-256
// Keystore key (ADR-0010 §5(c)).
//
// # What a verified proof establishes, and what it does not
//
// It establishes that whoever sent THIS request held the private key matching
// the embedded public key, for this method and this URL, recently, and once.
// It says nothing about the device's integrity (that is Play Integrity) or
// about any later request (each of those needs its own proof).
//
// # The accepted profile, exactly — narrower than RFC 9449
//
// Encoding: three segments of canonical unpadded base64url, at most MaxProofBytes.
//
// Header: exactly {"typ":"dpop+jwt","alg":"ES256","jwk":{...}}. No other member.
//
// jwk: exactly {"kty":"EC","crv":"P-256","x":...,"y":...}, with x and y the
// 32-byte coordinates of a point on the curve. A private member ("d") or any
// other member is rejected. (RFC 9449 permits extra public members; this
// profile does not, because nothing here uses them.)
//
// Claims, in two profiles that share every other rule:
//
//   - Verifier (enrolment and refresh at identity): exactly
//     {"jti","htm","htu","iat"}. ath is rejected: these proofs accompany no
//     access token. htu must equal the verifier's configured canonical URL.
//   - RequestVerifier (a request presenting an access token, at a resource
//     server): exactly {"jti","htm","htu","iat","ath"}. ath is REQUIRED and
//     must equal base64url(SHA-256(access token)) (RFC 9449 §4.2); htu must
//     equal the canonical URL the caller computed for this request; and the
//     proof's key thumbprint must equal the token's cnf.jkt — checked BEFORE
//     the jti is reserved, so a proof by another key reserves nothing.
//
// nonce is rejected in both: this server issues no nonces. htm must equal the
// request's method and htu its canonical URL, byte-for-byte (no URI
// normalisation; the client must send the canonical form). iat must lie within
// [now-MaxAge, now+FutureSkew].
//
// Signature: ES256, the 64-byte R||S form RFC 7518 §3.4 requires. A DER
// signature is rejected.
//
// Replay: after every check above passes, (thumbprint, jti) is reserved in a
// ReplayStore until the proof could no longer be accepted. A second
// presentation is rejected; a store that cannot answer fails closed. ECDSA
// signatures are malleable, but the reservation is keyed by jti, which is
// signed, so a re-encoded signature is still a replay.
package dpop

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
)

const (
	// Type is the only proof type this profile accepts (RFC 9449 dpop+jwt).
	Type = "dpop+jwt"
	// Algorithm is the only signature algorithm this profile accepts.
	Algorithm = "ES256"

	// MaxProofBytes bounds what is read. A proof of this profile is a few
	// hundred bytes.
	MaxProofBytes = 4096
	// MaxJTIBytes bounds the proof identifier.
	MaxJTIBytes = 128
)

var (
	// ErrInvalidConfig is a verifier that cannot be built safely.
	ErrInvalidConfig = errors.New("dpop: invalid configuration")
	// ErrInvalidProof wraps every rejection of the proof itself. The wrapped
	// detail is a fixed reason; it never contains proof content.
	ErrInvalidProof = errors.New("dpop: invalid proof")
	// ErrReplayed is a proof already presented. It also matches
	// ErrInvalidProof.
	ErrReplayed = fmt.Errorf("%w: proof already used", ErrInvalidProof)
	// ErrReplayUnavailable is a replay store that could not answer: the proof
	// may be fresh, but that cannot be established, so it is refused.
	ErrReplayUnavailable = errors.New("dpop: replay protection unavailable")
)

var b64 = base64.RawURLEncoding.Strict()

// ReplayStore reserves proof identifiers. Reserve must be atomic: of any number
// of concurrent calls for one (jkt, jti), at most one returns nil. A held
// reservation returns ErrReplayed; any other error means the store could not
// tell, and the proof is refused.
type ReplayStore interface {
	Reserve(ctx context.Context, jkt, jti string, expiresAt, now time.Time) error
}

// MemoryReplayStore is a process-local ReplayStore.
//
// PROCESS-LOCAL ONLY. Each replica would hold its own set, so a proof replayed
// against a different replica within the window would be accepted there. It is
// correct for a single process and for tests, and insufficient by itself for a
// multi-replica deployment, which needs a shared store with an atomic
// set-if-absent and expiry.
type MemoryReplayStore struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

// NewMemoryReplayStore returns an empty store.
func NewMemoryReplayStore() *MemoryReplayStore {
	return &MemoryReplayStore{entries: map[string]time.Time{}}
}

// Reserve implements ReplayStore. Expired entries are dropped as it runs, so
// the set is bounded by the proofs accepted within one window.
func (m *MemoryReplayStore) Reserve(_ context.Context, jkt, jti string, expiresAt, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, exp := range m.entries {
		if !now.Before(exp) {
			delete(m.entries, k)
		}
	}
	key := jkt + "\x00" + jti
	if exp, held := m.entries[key]; held && now.Before(exp) {
		return ErrReplayed
	}
	m.entries[key] = expiresAt
	return nil
}

// Len reports how many reservations are held; for tests and monitoring.
func (m *MemoryReplayStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// Config is everything a verifier needs. HTU has no default: the canonical URL
// of the enrolment endpoint is a deployment fact, and deriving it from request
// headers would let a client choose it.
type Config struct {
	// HTU is the canonical absolute URL proofs must name: http or https, a
	// host, and no query, fragment or user info.
	HTU string
	// MaxAge is how old a proof's iat may be. Required, and at most 5 minutes.
	MaxAge time.Duration
	// FutureSkew is how far ahead of the server clock iat may be. 0 to 1 minute.
	FutureSkew time.Duration
	Replay     ReplayStore
	Now        func() time.Time
}

// Verifier checks enrolment proofs.
type Verifier struct {
	cfg Config
}

// NewVerifier validates cfg and builds a verifier. It fails closed on any
// missing or unsafe input.
func NewVerifier(cfg Config) (*Verifier, error) {
	if !canonicalHTU(cfg.HTU) {
		return nil, fmt.Errorf("%w: HTU must be a canonical absolute http(s) URL with no query or fragment", ErrInvalidConfig)
	}
	if err := checkWindow(cfg.MaxAge, cfg.FutureSkew, cfg.Replay); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Verifier{cfg: cfg}, nil
}

// canonicalHTU reports whether s is an absolute http(s) URL with a host and no
// query, fragment or user info, written exactly as url.URL.String renders it.
func canonicalHTU(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.User == nil && u.String() == s
}

// checkWindow validates the freshness window and the replay store shared by
// both verifiers.
func checkWindow(maxAge, futureSkew time.Duration, replay ReplayStore) error {
	if maxAge <= 0 || maxAge > 5*time.Minute {
		return fmt.Errorf("%w: MaxAge must be in (0, 5m]", ErrInvalidConfig)
	}
	if futureSkew < 0 || futureSkew > time.Minute {
		return fmt.Errorf("%w: FutureSkew must be in [0, 1m]", ErrInvalidConfig)
	}
	if replay == nil {
		return fmt.Errorf("%w: a replay store is required", ErrInvalidConfig)
	}
	return nil
}

// Verified is what a valid proof establishes.
type Verified struct {
	// JKT is the RFC 7638 thumbprint of the proven key, as
	// accesstoken.ThumbprintP256 computes it.
	JKT      string
	JTI      string
	IssuedAt time.Time
}

type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type header struct {
	Typ string `json:"typ"`
	Alg string `json:"alg"`
	JWK *jwk   `json:"jwk"`
}

type claims struct {
	JTI string  `json:"jti"`
	HTM string  `json:"htm"`
	HTU string  `json:"htu"`
	IAT *int64  `json:"iat"`
	ATH *string `json:"ath"`
}

// rules is what one verification checks a proof against.
type rules struct {
	htu        string
	ath        string // required value of ath; "" means ath must be absent
	jkt        string // required key thumbprint; "" means any key
	maxAge     time.Duration
	futureSkew time.Duration
	replay     ReplayStore
	now        func() time.Time
}

func invalid(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidProof, reason) }

// decodeStrict decodes exactly one JSON object under the shared strict rules
// (accesstoken.DecodeStrictJSON): no unknown, duplicate or non-lower-case
// member names, and nothing after the object.
func decodeStrict(data []byte, v any) error { return accesstoken.DecodeStrictJSON(data, v) }

// printableJTI reports whether jti is 1..MaxJTIBytes printable, non-space
// ASCII bytes (0x21-0x7E). RFC 9449 allows any string; this profile does not
// (ADR-0029 D11.4): a NUL byte, for example, cannot be stored in a PostgreSQL
// TEXT column, and would turn a valid client's proof into a store error.
func printableJTI(jti string) bool {
	if jti == "" || len(jti) > MaxJTIBytes {
		return false
	}
	for i := 0; i < len(jti); i++ {
		if jti[i] < 0x21 || jti[i] > 0x7e {
			return false
		}
	}
	return true
}

// publicKey turns the jwk into a P-256 key, refusing anything else.
func (k *jwk) publicKey() (*ecdsa.PublicKey, error) {
	if k == nil || k.Kty != "EC" || k.Crv != "P-256" {
		return nil, errors.New("not an EC P-256 key")
	}
	x, errX := b64.DecodeString(k.X)
	y, errY := b64.DecodeString(k.Y)
	if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
		return nil, errors.New("coordinates are not canonical 32-byte base64url")
	}
	// ParseUncompressedPublicKey rejects a point that is not on the curve.
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{0x04}, x...), y...))
}

// Verify checks proof for an enrolment request made with method, and reserves
// it. Every check runs before the reservation, so a proof that fails a check
// does not consume a replay slot.
func (v *Verifier) Verify(ctx context.Context, proof, method string) (*Verified, error) {
	return verify(ctx, proof, method, rules{
		htu: v.cfg.HTU, maxAge: v.cfg.MaxAge, futureSkew: v.cfg.FutureSkew, replay: v.cfg.Replay, now: v.cfg.Now,
	})
}

// verify is every check, for both profiles, in one place.
func verify(ctx context.Context, proof, method string, rl rules) (*Verified, error) {
	if len(proof) > MaxProofBytes {
		return nil, invalid("proof exceeds the size limit")
	}
	parts := strings.Split(proof, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, invalid("not a three-segment compact JWS")
	}
	pub, err := proofKey(parts[0])
	if err != nil {
		return nil, err
	}
	if sigErr := verifyES256(pub, parts); sigErr != nil {
		return nil, sigErr
	}

	// Only now is the payload read.
	rawClaims, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, invalid("payload is not canonical unpadded base64url")
	}
	var c claims
	if err = decodeStrict(rawClaims, &c); err != nil {
		return nil, invalid("payload is not exactly this profile's claims")
	}
	iat, now, err := rl.checkClaims(&c, method)
	if err != nil {
		return nil, err
	}

	jkt, err := accesstoken.ThumbprintP256(pub)
	if err != nil {
		return nil, invalid("jwk thumbprint could not be computed")
	}
	// The binding is checked before the reservation: a proof made with any key
	// but the token's reserves nothing.
	if rl.jkt != "" && subtle.ConstantTimeCompare([]byte(jkt), []byte(rl.jkt)) != 1 {
		return nil, invalid("the proof key is not the key the access token is bound to")
	}
	if err := rl.reserve(ctx, jkt, c.JTI, iat, now); err != nil {
		return nil, err
	}
	return &Verified{JKT: jkt, JTI: c.JTI, IssuedAt: iat}, nil
}

// proofKey decodes the header, which must be exactly this profile's, and
// returns the public key its jwk carries.
func proofKey(segment string) (*ecdsa.PublicKey, error) {
	rawHeader, err := b64.DecodeString(segment)
	if err != nil {
		return nil, invalid("header is not canonical unpadded base64url")
	}
	// A private-key member is named in its own error: it is the one header
	// fault that means a client has mishandled its key.
	if bytes.Contains(rawHeader, []byte(`"d"`)) {
		return nil, invalid("the jwk carries private key material")
	}
	var h header
	if err = decodeStrict(rawHeader, &h); err != nil {
		return nil, invalid("header is not exactly {typ, alg, jwk} with jwk exactly {kty, crv, x, y}")
	}
	if h.Typ != Type {
		return nil, invalid("unsupported type")
	}
	if h.Alg != Algorithm {
		return nil, invalid("unsupported algorithm")
	}
	pub, err := h.JWK.publicKey()
	if err != nil {
		return nil, invalid("jwk is not a valid EC P-256 public key")
	}
	return pub, nil
}

// verifyES256 checks the 64-byte R||S ES256 signature over the signing input.
func verifyES256(pub *ecdsa.PublicKey, parts []string) error {
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return invalid("signature is not a canonical 64-byte ES256 signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return invalid("signature does not verify")
	}
	return nil
}

// checkClaims applies the claim rules for this verifier's profile and returns
// the proof's issue time and the instant the checks used.
func (rl rules) checkClaims(c *claims, method string) (iat, now time.Time, err error) {
	if !printableJTI(c.JTI) {
		return time.Time{}, time.Time{}, invalid("jti is missing or too long")
	}
	if c.HTM != method {
		return time.Time{}, time.Time{}, invalid("htm does not match the request method")
	}
	if c.HTU != rl.htu {
		return time.Time{}, time.Time{}, invalid("htu does not match the request URL")
	}
	switch {
	case rl.ath == "" && c.ATH != nil:
		return time.Time{}, time.Time{}, invalid("ath is not accepted on this endpoint")
	case rl.ath != "" && (c.ATH == nil || subtle.ConstantTimeCompare([]byte(*c.ATH), []byte(rl.ath)) != 1):
		return time.Time{}, time.Time{}, invalid("ath does not match the presented access token")
	}
	if c.IAT == nil {
		return time.Time{}, time.Time{}, invalid("iat is missing")
	}
	now = rl.now()
	iat = time.Unix(*c.IAT, 0)
	if iat.After(now.Add(rl.futureSkew)) {
		return time.Time{}, time.Time{}, invalid("iat is in the future")
	}
	if now.Sub(iat) > rl.maxAge {
		return time.Time{}, time.Time{}, invalid("proof is stale")
	}
	return iat, now, nil
}

// reserve holds (jkt, jti) until the proof could no longer pass the age check.
// A replay is ErrReplayed; a store that cannot answer is ErrReplayUnavailable,
// wrapping the store's error so a caller can still recognise, for example, a
// cancelled request.
func (rl rules) reserve(ctx context.Context, jkt, jti string, iat, now time.Time) error {
	expiresAt := iat.Add(rl.maxAge + time.Second)
	switch err := rl.replay.Reserve(ctx, jkt, jti, expiresAt, now); {
	case err == nil:
		return nil
	case errors.Is(err, ErrReplayed):
		return ErrReplayed
	default:
		return fmt.Errorf("%w: %w", ErrReplayUnavailable, err)
	}
}

// RequestConfig is everything a resource-request verifier needs. There is no
// HTU: each request's canonical URL is supplied per call by the caller, which
// alone knows its public origin.
type RequestConfig struct {
	// MaxAge and FutureSkew are as in Config: (0, 5m] and [0, 1m].
	MaxAge     time.Duration
	FutureSkew time.Duration
	Replay     ReplayStore
	Now        func() time.Time
}

// RequestVerifier checks the proof that accompanies an access token on a
// resource request (RFC 9449 §7).
type RequestVerifier struct {
	cfg RequestConfig
}

// NewRequestVerifier validates cfg and fails closed on any unsafe input.
func NewRequestVerifier(cfg RequestConfig) (*RequestVerifier, error) {
	if err := checkWindow(cfg.MaxAge, cfg.FutureSkew, cfg.Replay); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &RequestVerifier{cfg: cfg}, nil
}

// AccessTokenHash is the ath value for an access token: base64url without
// padding of SHA-256 over its ASCII bytes (RFC 9449 §4.2).
func AccessTokenHash(accessToken string) string {
	sum := sha256.Sum256([]byte(accessToken))
	return b64.EncodeToString(sum[:])
}

// Verify checks proof for a request with the given method and canonical URL
// presenting accessToken, whose cnf.jkt is tokenJKT. Every argument is
// required; an empty one is a refusal, never a relaxation.
func (v *RequestVerifier) Verify(ctx context.Context, proof, method, htu, accessToken, tokenJKT string) (*Verified, error) {
	if htu == "" || accessToken == "" || tokenJKT == "" || method == "" {
		return nil, invalid("the request context for verification is incomplete")
	}
	return verify(ctx, proof, method, rules{
		htu: htu, ath: AccessTokenHash(accessToken), jkt: tokenJKT,
		maxAge: v.cfg.MaxAge, futureSkew: v.cfg.FutureSkew, replay: v.cfg.Replay, now: v.cfg.Now,
	})
}
