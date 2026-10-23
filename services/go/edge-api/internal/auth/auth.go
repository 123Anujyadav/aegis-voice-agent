// Package auth is edge-api's authentication boundary: every request to a
// protected handler must present an access token issued by identity AND a
// fresh DPoP proof made with the device key that token is bound to (ADR-0010,
// RFC 9449). Verification is local — a configured public key, no call to
// identity per request — using the same accesstoken and dpop packages identity
// issues and checks with.
//
// # Request contract
//
//	Authorization: DPoP <access token>     exactly one header; the DPoP scheme
//	                                        only (Bearer and every other scheme
//	                                        are refused)
//	DPoP: <proof>                           exactly one header
//
// The token must verify against the configured Ed25519 key and key id, with
// the configured issuer and audience, inside its lifetime (accesstoken's
// profile: typ at+jwt, alg EdDSA, exact claims, required cnf.jkt; the legacy
// unsigned format is refused). The proof must be ES256 with exactly
// {jti, htm, htu, iat, ath}: htm the request method; htu the CONFIGURED public
// origin followed by the request's escaped path (no query, no fragment); ath
// the base64url SHA-256 of the presented token; its key's thumbprint equal to
// the token's cnf.jkt; fresh; and its jti never seen before.
//
// The request's Host and X-Forwarded-* headers are never consulted: the origin
// is configuration. A proxy that rewrites paths is therefore unsupported — the
// client signs the URL it sees, and the path this service sees must be the
// same.
//
// Replay protection is SHARED: every replica reserves (jkt, jti) in one
// PostgreSQL table (pgshared.ReplayStore on identity_dpop_replay), atomically,
// so a proof accepted by one replica is a replay at every other. The table is
// created by identity's migrations; Open refuses to start without it.
//
// Failures are 401 problem+json with WWW-Authenticate: DPoP (error
// "invalid_token" or "invalid_dpop_proof"), or 503 if replay protection cannot
// answer. Nothing from the token, the proof or the key is ever echoed or
// logged, and the protected handler is never reached.
package auth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/dpop"
	"github.com/callscreen/callscreen-platform/packages/go/middleware"
	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvPrefix is edge-api's environment namespace.
const EnvPrefix = "CS_EDGE_API"

// Proof freshness, identical to identity's (bootstrap.ProofMaxAge and
// ProofFutureSkew): a proof is accepted for 2 minutes after iat and up to 60s
// before it, the access-token verifier's clock skew.
const (
	ProofMaxAge     = 2 * time.Minute
	ProofFutureSkew = accesstoken.DefaultClockSkew
)

// MaxKeyFileBytes bounds the public-key file read.
const MaxKeyFileBytes = 16 << 10

// ErrInvalidConfig wraps every configuration and key-file failure.
var ErrInvalidConfig = errors.New("edge-api auth: invalid configuration")

// Config is the trusted-key contract. Every value is required once any is set.
type Config struct {
	PublicKeyFile string `env:"ACCESS_TOKEN_PUBLIC_KEY_FILE" required:"true"`
	KeyID         string `env:"ACCESS_TOKEN_KID" required:"true"`
	Issuer        string `env:"ACCESS_TOKEN_ISSUER" required:"true"`
	Audience      string `env:"ACCESS_TOKEN_AUDIENCE" required:"true"`
	// PublicOrigin is the scheme and host clients use to reach edge-api, e.g.
	// https://api.example.com — no path, query, fragment or user info. Every
	// proof's htu is this followed by the request path.
	PublicOrigin string `env:"PUBLIC_ORIGIN" required:"true"`
	// DatabaseDSNFile names a file holding the PostgreSQL connection string for
	// the shared replay store. The DSN itself is never accepted from the
	// environment. The database must be the one identity migrates.
	DatabaseDSNFile string `env:"DATABASE_DSN_FILE" required:"true"`

	// PreviousPublicKeyFile and PreviousKeyID are the optional overlap key of
	// a signing-key rotation (ADR-0029, "Keys and rotation"): set both, or
	// neither. While set, tokens signed with either key verify, so identity can
	// switch to the new key without rejecting tokens it issued with the old
	// one. Remove them once the old key's last token has expired (15 minutes
	// plus the 60-second skew after identity switched).
	PreviousPublicKeyFile string `env:"ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE" default:""`
	PreviousKeyID         string `env:"ACCESS_TOKEN_PREVIOUS_KID" default:""`

	// dbTLS is the replay database's TLS policy (ADR-0029): verified TLS
	// unless ENVIRONMENT is explicitly development. LoadActivation sets it;
	// the zero value is RequireVerifiedTLS. Unexported, so platform.LoadConfig
	// cannot populate it from a variable of the same name.
	dbTLS pgshared.TLSPolicy
}

// trustedKeys returns the verification key set: the active key and, during a
// rotation, the previous one.
func (c Config) trustedKeys() (map[string]ed25519.PublicKey, error) {
	pub, err := readPublicKey(c.PublicKeyFile)
	if err != nil {
		return nil, err
	}
	keys := map[string]ed25519.PublicKey{c.KeyID: pub}
	if c.PreviousKeyID == "" {
		return keys, nil
	}
	prev, err := readPublicKeyFile(EnvPrefix+"_ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE", c.PreviousPublicKeyFile)
	if err != nil {
		return nil, err
	}
	if prev.Equal(pub) {
		return nil, fmt.Errorf("%w: %s_ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE holds the active key", ErrInvalidConfig, EnvPrefix)
	}
	keys[c.PreviousKeyID] = prev
	return keys, nil
}

// ActivationVariables are the variables whose presence enables authentication.
var ActivationVariables = []string{
	EnvPrefix + "_ACCESS_TOKEN_PUBLIC_KEY_FILE",
	EnvPrefix + "_ACCESS_TOKEN_KID",
	EnvPrefix + "_ACCESS_TOKEN_ISSUER",
	EnvPrefix + "_ACCESS_TOKEN_AUDIENCE",
	EnvPrefix + "_PUBLIC_ORIGIN",
	EnvPrefix + "_DATABASE_DSN_FILE",
}

// LoadActivation reports whether authentication is configured and, if so, its
// validated configuration. None set: inactive (the skeleton's behaviour). Any
// set: all are required, and anything missing or invalid is an error.
func LoadActivation() (Config, bool, error) {
	present := false
	for _, name := range ActivationVariables {
		if _, ok := os.LookupEnv(name); ok {
			present = true
			break
		}
	}
	if !present {
		return Config{}, false, nil
	}
	var c Config
	if err := platform.LoadConfig(&c, EnvPrefix); err != nil {
		return Config{}, true, fmt.Errorf("%w: authentication is partially configured: %w", ErrInvalidConfig, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, true, err
	}
	c.dbTLS = pgshared.PolicyForEnvironment(os.LookupEnv(EnvPrefix + "_ENVIRONMENT"))
	return c, true, nil
}

// Validate refuses blank or padded values and a non-canonical origin. Issuer,
// audience and key id are further validated by accesstoken.NewVerifier.
func (c Config) Validate() error {
	problems := c.fieldProblems()
	if len(problems) == 0 && filepath.Clean(c.PublicKeyFile) == filepath.Clean(c.DatabaseDSNFile) {
		problems = append(problems, EnvPrefix+"_ACCESS_TOKEN_PUBLIC_KEY_FILE and "+EnvPrefix+"_DATABASE_DSN_FILE name the same file")
	}
	problems = append(problems, c.previousKeyProblems()...)
	if len(problems) == 0 && !canonicalHTTPSOrigin(c.PublicOrigin) {
		problems = append(problems, EnvPrefix+"_PUBLIC_ORIGIN must be a canonical https origin: scheme and host only")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return nil
}

// fieldProblems reports each required variable that is blank or padded.
func (c Config) fieldProblems() []string {
	var problems []string
	for _, f := range []struct{ name, value string }{
		{"ACCESS_TOKEN_PUBLIC_KEY_FILE", c.PublicKeyFile},
		{"ACCESS_TOKEN_KID", c.KeyID},
		{"ACCESS_TOKEN_ISSUER", c.Issuer},
		{"ACCESS_TOKEN_AUDIENCE", c.Audience},
		{"PUBLIC_ORIGIN", c.PublicOrigin},
		{"DATABASE_DSN_FILE", c.DatabaseDSNFile},
	} {
		switch {
		case strings.TrimSpace(f.value) == "":
			problems = append(problems, EnvPrefix+"_"+f.name+" is blank")
		case strings.TrimSpace(f.value) != f.value:
			problems = append(problems, EnvPrefix+"_"+f.name+" has leading or trailing whitespace")
		}
	}
	return problems
}

// canonicalHTTPSOrigin reports whether origin is exactly an https scheme and a
// host, in the form url.URL prints it.
func canonicalHTTPSOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Path == "" && u.RawPath == "" &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.User == nil && u.Opaque == "" && u.String() == origin
}

// previousKeyProblems validates the optional rotation overlap key: both of its
// variables or neither, a kid distinct from the active one, and a file distinct
// from the active key's and the DSN's.
func (c Config) previousKeyProblems() []string {
	const file, kid = EnvPrefix + "_ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE", EnvPrefix + "_ACCESS_TOKEN_PREVIOUS_KID"
	if c.PreviousPublicKeyFile == "" && c.PreviousKeyID == "" {
		return nil
	}
	var problems []string
	switch {
	case strings.TrimSpace(c.PreviousPublicKeyFile) == "" || strings.TrimSpace(c.PreviousKeyID) == "":
		return []string{file + " and " + kid + " must be set together"}
	case strings.TrimSpace(c.PreviousPublicKeyFile) != c.PreviousPublicKeyFile || strings.TrimSpace(c.PreviousKeyID) != c.PreviousKeyID:
		return []string{file + " or " + kid + " has leading or trailing whitespace"}
	}
	if c.PreviousKeyID == c.KeyID {
		problems = append(problems, kid+" must differ from "+EnvPrefix+"_ACCESS_TOKEN_KID")
	}
	if p := filepath.Clean(c.PreviousPublicKeyFile); p == filepath.Clean(c.PublicKeyFile) || p == filepath.Clean(c.DatabaseDSNFile) {
		problems = append(problems, file+" must name its own file")
	}
	return problems
}

// Principal is what a verified request establishes, for protected handlers.
type Principal struct {
	Subject string
	Roles   []string
	// DeviceKeyThumbprint is the token's cnf.jkt, which the request's proof was
	// verified against.
	DeviceKeyThumbprint string
	TokenID             string
}

type principalKey struct{}

// PrincipalFrom returns the verified principal. Only Middleware sets it.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Authenticator verifies requests.
type Authenticator struct {
	tokens *accesstoken.Verifier
	proofs *dpop.RequestVerifier
	origin string
	log    *slog.Logger
}

// New builds an authenticator from cfg, the trusted key it names, and a replay
// store. It fails closed on any missing or invalid input, and never echoes the
// key or the file's path.
func New(cfg Config, replay dpop.ReplayStore, now func() time.Time, log *slog.Logger) (*Authenticator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if replay == nil {
		return nil, fmt.Errorf("%w: a replay store is required", ErrInvalidConfig)
	}
	keys, err := cfg.trustedKeys()
	if err != nil {
		return nil, err
	}
	tokens, err := accesstoken.NewVerifier(accesstoken.VerifierConfig{
		Issuer: cfg.Issuer, Audience: cfg.Audience, Keys: keys, Now: now,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: access-token verifier: %w", ErrInvalidConfig, err)
	}
	proofs, err := dpop.NewRequestVerifier(dpop.RequestConfig{
		MaxAge: ProofMaxAge, FutureSkew: ProofFutureSkew, Replay: replay, Now: now,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: proof verifier: %w", ErrInvalidConfig, err)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Authenticator{tokens: tokens, proofs: proofs, origin: cfg.PublicOrigin, log: log}, nil
}

func readPublicKey(path string) (ed25519.PublicKey, error) {
	return readPublicKeyFile(EnvPrefix+"_ACCESS_TOKEN_PUBLIC_KEY_FILE", path)
}

// readPublicKeyFile reads one Ed25519 public-key PEM file; name is the
// variable that configured path, for error messages (the path is never shown).
func readPublicKeyFile(name, path string) (ed25519.PublicKey, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the path is operator configuration (a key or DSN file); reading it is the purpose
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s: the key file does not exist", ErrInvalidConfig, name)
	case errors.Is(err, fs.ErrPermission):
		return nil, fmt.Errorf("%w: %s: permission denied reading the key file", ErrInvalidConfig, name)
	case err != nil:
		return nil, fmt.Errorf("%w: %s: the key file cannot be opened", ErrInvalidConfig, name)
	}
	defer f.Close() //nolint:errcheck // read-only file, already fully read: a Close error cannot lose data
	if info, statErr := f.Stat(); statErr != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s: not a regular file", ErrInvalidConfig, name)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxKeyFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: the key file cannot be read", ErrInvalidConfig, name)
	}
	if len(data) > MaxKeyFileBytes {
		return nil, fmt.Errorf("%w: %s: the key file is larger than %d bytes", ErrInvalidConfig, name, MaxKeyFileBytes)
	}
	pub, err := accesstoken.ParsePublicKeyPEM(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalidConfig, name, err)
	}
	return pub, nil
}

// Middleware admits a request to next only if its token and proof verify. On
// any failure next is not called.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := dpopToken(r.Header.Values("Authorization"))
		if !ok {
			a.refuse(w, r, http.StatusUnauthorized, "", "ERR_UNAUTHENTICATED")
			return
		}
		proofs := r.Header.Values("DPoP")
		if len(proofs) != 1 || proofs[0] == "" || len(proofs[0]) > dpop.MaxProofBytes {
			a.refuse(w, r, http.StatusUnauthorized, "invalid_dpop_proof", "ERR_INVALID_DPOP_PROOF")
			return
		}
		claims, err := a.tokens.Verify(token)
		if err != nil {
			a.refuse(w, r, http.StatusUnauthorized, "invalid_token", "ERR_INVALID_TOKEN")
			return
		}
		path := r.URL.EscapedPath()
		if !strings.HasPrefix(path, "/") {
			a.refuse(w, r, http.StatusUnauthorized, "invalid_dpop_proof", "ERR_INVALID_DPOP_PROOF")
			return
		}
		_, err = a.proofs.Verify(r.Context(), proofs[0], r.Method, a.origin+path, token, claims.Confirmation.JKT)
		switch {
		case errors.Is(err, dpop.ErrInvalidProof):
			a.refuse(w, r, http.StatusUnauthorized, "invalid_dpop_proof", "ERR_INVALID_DPOP_PROOF")
			return
		case err != nil:
			a.refuse(w, r, http.StatusServiceUnavailable, "", "ERR_TEMPORARILY_UNAVAILABLE")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, Principal{
			Subject: claims.Subject, Roles: append([]string(nil), claims.Roles...),
			DeviceKeyThumbprint: claims.Confirmation.JKT, TokenID: claims.ID,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// dpopToken returns the token of a single "DPoP <token68>" Authorization
// header. The scheme is case-insensitive (RFC 9110 §11.1); the token must be
// token68 and nothing may follow it.
func dpopToken(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "DPoP") || token == "" || len(token) > accesstoken.MaxTokenBytes {
		return "", false
	}
	if !isToken68(token) {
		return "", false
	}
	return token, true
}

// isToken68 reports whether every byte of s is a token68 character (RFC 9110
// §11.2).
func isToken68(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' || c == '+' || c == '/' || c == '=') {
			return false
		}
	}
	return true
}

// Fixed text per code; nothing from the request is ever added.
var problems = map[string]struct{ title, detail string }{
	"ERR_UNAUTHENTICATED":         {"Unauthorized", "A DPoP-bound access token is required."},
	"ERR_INVALID_TOKEN":           {"Unauthorized", "The access token is not valid."},
	"ERR_INVALID_DPOP_PROOF":      {"Unauthorized", "A valid DPoP proof for this request is required."},
	"ERR_TEMPORARILY_UNAVAILABLE": {"Service Unavailable", "The request could not be completed. Retry later."},
}

func (a *Authenticator) refuse(w http.ResponseWriter, r *http.Request, status int, dpopError, code string) {
	if status == http.StatusUnauthorized {
		challenge := `DPoP algs="ES256"`
		if dpopError != "" {
			challenge += `, error="` + dpopError + `"`
		}
		w.Header().Set("WWW-Authenticate", challenge)
	} else {
		a.log.Warn("edge authentication unavailable", slog.String("code", code), slog.String("path", r.URL.Path))
	}
	w.Header().Set("Cache-Control", "no-store")
	p := problems[code]
	middleware.WriteProblemDetails(w, status, p.title, p.detail, code, r.URL.Path, correlationID(r))
}

// correlationID returns the request's correlation ID, or "" when none is set.
func correlationID(r *http.Request) string {
	if id, ok := r.Context().Value(middleware.CorrelationContextKey).(string); ok {
		return id
	}
	return ""
}

// ---------------------------------------------------------------------------
// Shared replay store
// ---------------------------------------------------------------------------

// MaxDSNFileBytes bounds the DSN file read.
const MaxDSNFileBytes = 8 << 10

// ConnectTimeout bounds the startup connection and schema check.
const ConnectTimeout = 10 * time.Second

// Runtime is an authenticator and the database pool its replay store uses. The
// runtime owns the pool: Close releases it, once.
type Runtime struct {
	a    *Authenticator
	pool *pgxpool.Pool
	once sync.Once
}

// Authenticator returns the authenticator, whose replay store is the pool's.
func (r *Runtime) Authenticator() *Authenticator { return r.a }

// Close releases the pool. Safe to call more than once.
func (r *Runtime) Close() { r.once.Do(r.pool.Close) }

// CheckDatabase pings the replay store's database for readiness. Its error is
// fixed text: readiness bodies are served to anyone who can reach the health
// port.
func (r *Runtime) CheckDatabase(ctx context.Context) error {
	if err := pgshared.Ping(ctx, r.pool); err != nil {
		return errors.New("edge replay store unreachable")
	}
	return nil
}

// newPool is the pool constructor, replaceable in tests.
var newPool = pgshared.NewPool

// Open builds the authenticator on the shared PostgreSQL replay store. Local
// inputs are validated first — configuration, DSN file, public key — so a
// local mistake never opens a connection; then the pool is created, pinged and
// the replay table checked; then New is called with pgshared.ReplayStore. Any
// failure closes the pool and returns nothing. Errors carry no DSN, credential,
// path or key material.
func Open(ctx context.Context, cfg Config, now func() time.Time, log *slog.Logger) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	dsn, err := readDSNFile(cfg.DatabaseDSNFile)
	if err != nil {
		return nil, err
	}
	if _, readPublicKeyErr := readPublicKey(cfg.PublicKeyFile); readPublicKeyErr != nil {
		return nil, readPublicKeyErr
	}
	pool, err := newPool(dsn, cfg.dbTLS)
	if err != nil {
		return nil, fmt.Errorf("%w: %s_DATABASE_DSN_FILE: %w", ErrInvalidConfig, EnvPrefix, pgshared.Redact(err))
	}
	cctx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	defer cancel()
	if pingErr := pgshared.Ping(cctx, pool); pingErr != nil {
		pool.Close()
		return nil, fmt.Errorf("edge-api auth: replay store: %w", pingErr)
	}
	if checkReplaySchemaErr := pgshared.CheckReplaySchema(cctx, pool); checkReplaySchemaErr != nil {
		pool.Close()
		return nil, fmt.Errorf("edge-api auth: replay store: %w", checkReplaySchemaErr)
	}
	a, err := New(cfg, pgshared.NewReplayStore(pool), now, log)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Runtime{a: a, pool: pool}, nil
}

// readDSNFile returns the connection string with surrounding whitespace
// removed. It refuses an empty value, a line break inside it, and a string the
// driver cannot parse — and never echoes it or the path.
func readDSNFile(path string) (string, error) {
	const name = EnvPrefix + "_DATABASE_DSN_FILE"
	f, err := os.Open(path) //nolint:gosec // G304: the path is operator configuration (a key or DSN file); reading it is the purpose
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w: %s: the file does not exist", ErrInvalidConfig, name)
	case errors.Is(err, fs.ErrPermission):
		return "", fmt.Errorf("%w: %s: permission denied reading the file", ErrInvalidConfig, name)
	case err != nil:
		return "", fmt.Errorf("%w: %s: the file cannot be opened", ErrInvalidConfig, name)
	}
	defer f.Close() //nolint:errcheck // read-only file, already fully read: a Close error cannot lose data
	if info, statErr := f.Stat(); statErr != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s: not a regular file", ErrInvalidConfig, name)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxDSNFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: %s: the file cannot be read", ErrInvalidConfig, name)
	}
	if len(data) > MaxDSNFileBytes {
		return "", fmt.Errorf("%w: %s: the file is larger than %d bytes", ErrInvalidConfig, name, MaxDSNFileBytes)
	}
	dsn := strings.TrimSpace(string(data))
	switch {
	case dsn == "":
		return "", fmt.Errorf("%w: %s: the file holds no connection string", ErrInvalidConfig, name)
	case strings.ContainsAny(dsn, "\r\n\x00"):
		return "", fmt.Errorf("%w: %s: the connection string spans more than one line", ErrInvalidConfig, name)
	}
	if _, err := pgxpool.ParseConfig(dsn); err != nil {
		return "", fmt.Errorf("%w: %s: the connection string cannot be parsed (value not shown)", ErrInvalidConfig, name)
	}
	return dsn, nil
}
