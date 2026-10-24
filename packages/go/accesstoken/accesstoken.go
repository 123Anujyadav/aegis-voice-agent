// Package accesstoken issues and verifies the platform's access tokens: compact
// JWTs signed with Ed25519, in ONE deliberately narrow profile.
//
// # What this is, and what it is not
//
// ADR-0010 specifies EdDSA-signed access tokens that live 15 minutes and carry a
// `cnf` claim binding them to the device's Keystore key. This package is the
// signing and verification core of that: standard library only, no JWT
// dependency.
//
// It is the token core of the ADR-0010 flow, not the whole flow. A `cnf.jkt`
// claim names a device key; it does not prove the presenter holds it. That
// proof is a DPoP proof made with the device key on every request (ADR-0029
// D11.4), verified by packages/go/dpop: edge-api's auth.Middleware checks it
// after this package verifies the token, and identity's enrolment binds the
// key in the first place. A token verified here WITHOUT that proof check is a
// bearer token, and must not be treated as device-bound.
//
// An access token can never pass as a DPoP proof, nor a proof as an access
// token (RFC 8725 §3.12): typ, alg and key source differ, and
// dpop/crosstype_test.go presents genuine tokens of each kind to the other's
// verifier.
//
// # The accepted profile, exactly
//
// Header: exactly {"alg":"EdDSA","typ":"at+jwt","kid":"<non-empty>"}. Any other
// header member — jku, x5u, jwk, crit, anything — is rejected, so no
// verification input can come from the token. The algorithm is never chosen by
// the token: there is one, and the header must name it.
//
// Claims: exactly iss, aud (a single string), sub, iat, nbf, exp, jti, cnf
// ({"jkt": <RFC 7638 SHA-256 thumbprint, base64url>}) and roles. nbf equals iat,
// and exp is exactly iat + 15 minutes.
//
// Encoding: three segments, unpadded base64url in its canonical form, at most
// MaxTokenBytes in all.
package accesstoken

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// Algorithm is the only signature algorithm this profile accepts.
	Algorithm = "EdDSA"
	// Type is the only token type this profile accepts (RFC 9068 at+jwt).
	Type = "at+jwt"

	// Lifetime is ADR-0010's access-token lifetime.
	Lifetime = 15 * time.Minute

	// DefaultClockSkew is the verifier's allowance when none is configured.
	DefaultClockSkew = 60 * time.Second

	// MaxTokenBytes bounds what the verifier will read. A token of this profile
	// is a few hundred bytes; the bound only has to be generous, and finite.
	MaxTokenBytes = 4096

	// The legacyPrefix is the unsigned "Bearer_JWT.<hex json>" format
	// identity issued before this package. It must never be accepted.
	legacyPrefix = "Bearer_JWT."
)

var (
	// ErrInvalidConfig is a signer or verifier that cannot be built safely.
	ErrInvalidConfig = errors.New("accesstoken: invalid configuration")
	// ErrInvalidClaims is a token that cannot be issued as requested.
	ErrInvalidClaims = errors.New("accesstoken: invalid claims for issuance")
	// ErrInvalidToken wraps every verification failure. The wrapped detail is
	// for logs; callers should answer every case the same way.
	ErrInvalidToken = errors.New("accesstoken: invalid token")
)

var b64 = base64.RawURLEncoding.Strict()

// Confirmation is the cnf claim: the thumbprint of the device key the token is
// issued to. It binds; it does not prove.
type Confirmation struct {
	JKT string `json:"jkt"`
}

// Claims is the full claim set of this profile.
type Claims struct {
	Issuer       string       `json:"iss"`
	Audience     string       `json:"aud"`
	Subject      string       `json:"sub"`
	IssuedAt     int64        `json:"iat"`
	NotBefore    int64        `json:"nbf"`
	Expiry       int64        `json:"exp"`
	ID           string       `json:"jti"`
	Confirmation Confirmation `json:"cnf"`
	Roles        []string     `json:"roles"`
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

// ParsePrivateKeyPEM reads a PKCS#8 PEM "PRIVATE KEY" holding an Ed25519 key.
// Any other key type, PEM type or trailing data is refused. The key is never
// echoed in an error.
func ParsePrivateKeyPEM(data []byte) (ed25519.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block", ErrInvalidConfig)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%w: trailing data after the PEM block", ErrInvalidConfig)
	}
	if block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%w: PEM type %q, want PRIVATE KEY (PKCS#8)", ErrInvalidConfig, block.Type)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: not a PKCS#8 private key", ErrInvalidConfig)
	}
	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: PKCS#8 key is %T, want Ed25519", ErrInvalidConfig, key)
	}
	return edKey, nil
}

// ParsePublicKeyPEM reads a PKIX PEM "PUBLIC KEY" holding an Ed25519 key — what
// a verifier is configured with. Any other key type, PEM type or trailing data
// is refused, and the key is never echoed in an error.
func ParsePublicKeyPEM(data []byte) (ed25519.PublicKey, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block", ErrInvalidConfig)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%w: trailing data after the PEM block", ErrInvalidConfig)
	}
	if block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("%w: PEM type %q, want PUBLIC KEY (PKIX)", ErrInvalidConfig, block.Type)
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: not a PKIX public key", ErrInvalidConfig)
	}
	edKey, ok := key.(ed25519.PublicKey)
	if !ok || len(edKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: PKIX key is %T, want Ed25519", ErrInvalidConfig, key)
	}
	return edKey, nil
}

// validPrivateKey reports whether k is a well-formed Ed25519 private key whose
// stored public half matches its seed.
func validPrivateKey(k ed25519.PrivateKey) bool {
	if len(k) != ed25519.PrivateKeySize {
		return false
	}
	derived := ed25519.NewKeyFromSeed(k.Seed())
	return bytes.Equal(derived[ed25519.SeedSize:], k[ed25519.SeedSize:])
}

// ThumbprintP256 is the RFC 7638 JWK SHA-256 thumbprint of an EC P-256 public
// key, base64url without padding: the value cnf.jkt carries. The required
// members are serialised in lexicographic order with no whitespace, and x and y
// are the fixed 32-byte big-endian coordinates (RFC 7518 §6.2.1.2).
func ThumbprintP256(pub *ecdsa.PublicKey) (string, error) {
	if pub == nil || pub.Curve != elliptic.P256() {
		return "", fmt.Errorf("%w: not a P-256 public key", ErrInvalidClaims)
	}
	point, err := pub.Bytes() // 0x04 || X(32) || Y(32)
	if err != nil || len(point) != 65 {
		return "", fmt.Errorf("%w: invalid P-256 public key", ErrInvalidClaims)
	}
	canonical := `{"crv":"P-256","kty":"EC","x":"` + b64.EncodeToString(point[1:33]) +
		`","y":"` + b64.EncodeToString(point[33:65]) + `"}`
	sum := sha256.Sum256([]byte(canonical))
	return b64.EncodeToString(sum[:]), nil
}

// validJKT reports whether s is a canonical base64url SHA-256 digest.
func validJKT(s string) bool {
	raw, err := b64.DecodeString(s)
	return err == nil && len(raw) == sha256.Size
}

// validKeyID accepts a short printable identifier with no separators that
// could confuse a log line or a header.
func validKeyID(kid string) bool {
	if kid == "" || len(kid) > 128 {
		return false
	}
	for _, r := range kid {
		if r <= 0x20 || r >= 0x7f || r == '"' || r == '\\' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Signer
// ---------------------------------------------------------------------------

// SignerConfig is everything a signer needs. Issuer and Audience have no
// default: a token minted for an unstated audience is the confusion they exist
// to prevent.
type SignerConfig struct {
	Issuer   string
	Audience string
	KeyID    string
	Key      ed25519.PrivateKey
	// Now and Random are injectable for tests; nil means time.Now and
	// crypto/rand.Reader.
	Now    func() time.Time
	Random io.Reader
}

// Signer issues access tokens with one active key.
type Signer struct {
	cfg    SignerConfig
	header string // the encoded header, fixed for this signer
}

// NewSigner validates cfg and builds a signer. It fails closed on any missing
// or inconsistent input, and never logs the key.
func NewSigner(cfg SignerConfig) (*Signer, error) {
	if strings.TrimSpace(cfg.Issuer) == "" || cfg.Issuer != strings.TrimSpace(cfg.Issuer) {
		return nil, fmt.Errorf("%w: issuer is required and must not be padded", ErrInvalidConfig)
	}
	if strings.TrimSpace(cfg.Audience) == "" || cfg.Audience != strings.TrimSpace(cfg.Audience) {
		return nil, fmt.Errorf("%w: audience is required and must not be padded", ErrInvalidConfig)
	}
	if !validKeyID(cfg.KeyID) {
		return nil, fmt.Errorf("%w: key id is required and must be printable ASCII", ErrInvalidConfig)
	}
	if !validPrivateKey(cfg.Key) {
		return nil, fmt.Errorf("%w: signing key is not a valid Ed25519 private key", ErrInvalidConfig)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	cfg.Key = append(ed25519.PrivateKey(nil), cfg.Key...)
	h, err := json.Marshal(header{Alg: Algorithm, Typ: Type, Kid: cfg.KeyID})
	if err != nil {
		return nil, fmt.Errorf("%w: encoding header: %w", ErrInvalidConfig, err)
	}
	return &Signer{cfg: cfg, header: b64.EncodeToString(h)}, nil
}

// Issue mints a token for subject, bound to the device key whose RFC 7638
// thumbprint is jkt. Roles are carried as given; this package defines no role
// vocabulary. The token never carries an MSISDN, a code, a refresh token or
// key material: there is no parameter through which one could arrive.
func (s *Signer) Issue(subject string, roles []string, jkt string) (string, error) {
	if strings.TrimSpace(subject) == "" {
		return "", fmt.Errorf("%w: subject is required", ErrInvalidClaims)
	}
	if !validJKT(jkt) {
		return "", fmt.Errorf("%w: cnf.jkt must be a base64url SHA-256 thumbprint", ErrInvalidClaims)
	}
	for _, r := range roles {
		if strings.TrimSpace(r) == "" {
			return "", fmt.Errorf("%w: empty role", ErrInvalidClaims)
		}
	}
	jti := make([]byte, 16)
	if _, err := io.ReadFull(s.cfg.Random, jti); err != nil {
		return "", fmt.Errorf("%w: generating jti: %w", ErrInvalidClaims, err)
	}

	iat := s.cfg.Now().Unix()
	claims := Claims{
		Issuer:       s.cfg.Issuer,
		Audience:     s.cfg.Audience,
		Subject:      subject,
		IssuedAt:     iat,
		NotBefore:    iat,
		Expiry:       iat + int64(Lifetime/time.Second),
		ID:           b64.EncodeToString(jti),
		Confirmation: Confirmation{JKT: jkt},
		Roles:        append([]string{}, roles...),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("%w: encoding claims: %w", ErrInvalidClaims, err)
	}
	signingInput := s.header + "." + b64.EncodeToString(payload)
	sig := ed25519.Sign(s.cfg.Key, []byte(signingInput))
	return signingInput + "." + b64.EncodeToString(sig), nil
}

// ---------------------------------------------------------------------------
// Verifier
// ---------------------------------------------------------------------------

// VerifierConfig is everything a verifier needs. Keys maps key id to public
// key: the ONLY source of verification keys. Holding a previous key alongside
// the current one is how a rotation overlaps; removing it ends the overlap.
type VerifierConfig struct {
	Issuer   string
	Audience string
	Keys     map[string]ed25519.PublicKey
	// ClockSkew is the allowance applied to every time check. Zero means
	// DefaultClockSkew; negative is refused.
	ClockSkew time.Duration
	Now       func() time.Time
}

// Verifier checks tokens against a fixed local key set.
type Verifier struct {
	issuer, audience string
	keys             map[string]ed25519.PublicKey
	skew             time.Duration
	now              func() time.Time
}

// NewVerifier validates cfg and builds a verifier, copying the key set so a
// later change to the caller's map cannot add a key.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if strings.TrimSpace(cfg.Issuer) == "" || cfg.Issuer != strings.TrimSpace(cfg.Issuer) {
		return nil, fmt.Errorf("%w: issuer is required and must not be padded", ErrInvalidConfig)
	}
	if strings.TrimSpace(cfg.Audience) == "" || cfg.Audience != strings.TrimSpace(cfg.Audience) {
		return nil, fmt.Errorf("%w: audience is required and must not be padded", ErrInvalidConfig)
	}
	if len(cfg.Keys) == 0 {
		return nil, fmt.Errorf("%w: at least one verification key is required", ErrInvalidConfig)
	}
	keys := make(map[string]ed25519.PublicKey, len(cfg.Keys))
	for kid, k := range cfg.Keys {
		if !validKeyID(kid) {
			return nil, fmt.Errorf("%w: invalid key id", ErrInvalidConfig)
		}
		if len(k) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: key %q is not an Ed25519 public key", ErrInvalidConfig, kid)
		}
		keys[kid] = append(ed25519.PublicKey(nil), k...)
	}
	if cfg.ClockSkew < 0 {
		return nil, fmt.Errorf("%w: clock skew must not be negative", ErrInvalidConfig)
	}
	skew := cfg.ClockSkew
	if skew == 0 {
		skew = DefaultClockSkew
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Verifier{issuer: cfg.Issuer, audience: cfg.Audience, keys: keys, skew: skew, now: now}, nil
}

func invalid(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidToken, reason) }

// DecodeStrictJSON decodes exactly one JSON object into v under this
// repository's JOSE profiles (ADR-0029 D11.3 condition (a)). Beyond refusing
// unknown members and trailing data, it refuses, in any object at any depth:
//   - a duplicate member name, which encoding/json would silently resolve to
//     the last value;
//   - a member name that is not lower-case ASCII letters, which encoding/json
//     would match case-insensitively ("ALG" for "alg").
//
// Every member name in these profiles is lower-case, so together with
// DisallowUnknownFields this is exact-name matching, and the parser cannot
// disagree with another JOSE implementation about what the object says.
func DecodeStrictJSON(data []byte, v any) error { return decodeStrict(data, v) }

// decodeStrict decodes one JSON object, refusing unknown members, duplicate or
// non-lower-case member names, and anything after the object.
func decodeStrict(data []byte, v any) error {
	if err := checkMemberNames(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// jsonFrame is one open object or array while checkMemberNames walks a
// document.
type jsonFrame struct {
	object    bool
	expectKey bool
	seen      map[string]struct{}
}

// memberWalker tracks the open objects and arrays while checkMemberNames
// walks a document's tokens.
type memberWalker struct{ stack []*jsonFrame }

// expectingKey reports whether the next token is a member name.
func (w *memberWalker) expectingKey() bool {
	n := len(w.stack)
	return n > 0 && w.stack[n-1].object && w.stack[n-1].expectKey
}

// valueDone records that a complete value ended: inside an object, the next
// token is a member name again.
func (w *memberWalker) valueDone() {
	if n := len(w.stack); n > 0 && w.stack[n-1].object {
		w.stack[n-1].expectKey = true
	}
}

// key handles a token in member-name position: the end of the object, or a
// name that must be lower-case ASCII and not yet seen in this object.
func (w *memberWalker) key(tok json.Token) error {
	top := w.stack[len(w.stack)-1]
	if d, ok := tok.(json.Delim); ok && d == '}' {
		w.stack = w.stack[:len(w.stack)-1]
		w.valueDone()
		return nil
	}
	name, ok := tok.(string)
	if !ok || !lowerASCIIName(name) {
		return errors.New("a member name is not lower-case ASCII letters")
	}
	if _, dup := top.seen[name]; dup {
		return errors.New("duplicate member name")
	}
	top.seen[name] = struct{}{}
	top.expectKey = false
	return nil
}

// value handles a token in value position: the start or end of a container,
// or a complete scalar.
func (w *memberWalker) value(tok json.Token) {
	if d, ok := tok.(json.Delim); ok {
		switch d {
		case '{':
			w.stack = append(w.stack, &jsonFrame{object: true, expectKey: true, seen: map[string]struct{}{}})
			return
		case '[':
			w.stack = append(w.stack, &jsonFrame{})
			return
		case ']':
			w.stack = w.stack[:len(w.stack)-1]
		}
	}
	w.valueDone()
}

// checkMemberNames walks data's tokens and refuses duplicate member names and
// names that are not lower-case ASCII letters, in every object at any depth.
func checkMemberNames(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	w := &memberWalker{}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			if len(w.stack) != 0 {
				return errors.New("unterminated JSON")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("json: %w", err)
		}
		if w.expectingKey() {
			if err := w.key(tok); err != nil {
				return err
			}
			continue
		}
		w.value(tok)
	}
}

func lowerASCIIName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}

// Verify checks token and returns its claims. Nothing in the token is trusted
// before its signature has verified with a locally configured key.
//
// Time checks, all with the configured skew s, on whole seconds:
//   - expired:      now >= exp + s
//   - not yet valid: now < nbf - s
//   - issued in the future: iat > now + s
//
// and, independent of the clock, the profile's own shape: nbf == iat and
// exp == iat + Lifetime.
func (v *Verifier) Verify(token string) (*Claims, error) {
	parts, err := splitToken(token)
	if err != nil {
		return nil, err
	}
	key, err := v.headerKey(parts[0])
	if err != nil {
		return nil, err
	}

	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, invalid("signature is not a canonical Ed25519 signature")
	}
	if !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, invalid("signature does not verify")
	}

	// Only now is the payload read.
	rawClaims, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, invalid("payload is not canonical unpadded base64url")
	}
	var c Claims
	if err := decodeStrict(rawClaims, &c); err != nil {
		return nil, invalid("payload is not exactly this profile's claims")
	}
	if err := v.checkClaims(&c); err != nil {
		return nil, err
	}
	if err := v.checkTime(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// splitToken applies the size and legacy-format refusals and returns the three
// compact-JWS segments.
func splitToken(token string) ([]string, error) {
	if len(token) > MaxTokenBytes {
		return nil, invalid("token exceeds the size limit")
	}
	if strings.HasPrefix(token, legacyPrefix) {
		return nil, invalid("the legacy unsigned format is not accepted")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, invalid("not a three-segment compact JWS")
	}
	return parts, nil
}

// headerKey decodes the header, which must be exactly this profile's, and
// returns the configured key its kid names.
func (v *Verifier) headerKey(segment string) (ed25519.PublicKey, error) {
	rawHeader, err := b64.DecodeString(segment)
	if err != nil {
		return nil, invalid("header is not canonical unpadded base64url")
	}
	var h header
	if err := decodeStrict(rawHeader, &h); err != nil {
		return nil, invalid("header is not exactly {alg, typ, kid}")
	}
	if h.Alg != Algorithm {
		return nil, invalid("unsupported algorithm")
	}
	if h.Typ != Type {
		return nil, invalid("unsupported token type")
	}
	key, ok := v.keys[h.Kid]
	if !ok {
		return nil, invalid("unknown key id")
	}
	return key, nil
}

// checkClaims applies the clock-independent claim rules.
func (v *Verifier) checkClaims(c *Claims) error {
	switch {
	case c.Issuer != v.issuer:
		return invalid("wrong issuer")
	case c.Audience != v.audience:
		return invalid("wrong audience")
	case strings.TrimSpace(c.Subject) == "":
		return invalid("missing subject")
	case c.ID == "":
		return invalid("missing jti")
	case !validJKT(c.Confirmation.JKT):
		return invalid("missing or malformed cnf.jkt")
	case c.IssuedAt <= 0 || c.NotBefore != c.IssuedAt || c.Expiry != c.IssuedAt+int64(Lifetime/time.Second):
		return invalid("iat, nbf and exp do not have this profile's shape")
	}
	return nil
}

// checkTime applies the expiry, not-before and future-iat rules with the
// configured skew.
func (v *Verifier) checkTime(c *Claims) error {
	now := v.now().Unix()
	skew := int64(v.skew / time.Second)
	switch {
	case now >= c.Expiry+skew:
		return invalid("expired")
	case now < c.NotBefore-skew:
		return invalid("not yet valid")
	case c.IssuedAt > now+skew:
		return invalid("issued in the future")
	}
	return nil
}
