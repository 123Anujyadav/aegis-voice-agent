package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/accesstoken"
	"github.com/callscreen/callscreen-platform/packages/go/pgshared"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/app"
	"github.com/callscreen/callscreen-platform/services/go/identity/internal/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// # Activation
//
// The identity service is constructed only when it is deliberately activated.
// Activation is the presence of ANY of the variables in ActivationVariables:
//
//   - none present: identity is INACTIVE. The process boots exactly as the
//     skeleton did — health listener only — and readiness carries a
//     non-critical "identity" check reporting that identity is inactive. No
//     service is built and no secret is invented.
//   - some present: identity is being activated, so ALL are required and
//     validated. Anything missing or invalid stops startup.
//   - all present and valid: the service is built on PostgreSQL.
//
// To activate a deployment: provision the three files (signing key, HMAC key,
// DSN), set all eight variables, run `service migrate` once with the same
// CS_IDENTITY_DATABASE_DSN_FILE, then roll out. Nothing migrates at startup.
//
// Two further variables join the six of SecurityConfig:
//
//	CS_IDENTITY_HMAC_SECRET_KEY_FILE  raw key bytes, used exactly as stored —
//	                                  never trimmed. At least 32 bytes, not all
//	                                  one value, and not the signing key.
//	                                  Hashes OTP codes and derives subscriber
//	                                  ids, so it must be stable across restarts
//	                                  and replicas.
//	CS_IDENTITY_DATABASE_DSN_FILE     a PostgreSQL connection string. Surrounding
//	                                  whitespace (a trailing newline) is removed;
//	                                  a line break inside it is refused.

// ActivationVariables are the identity variables whose presence activates it.
var ActivationVariables = []string{
	EnvPrefix + "_ACCESS_TOKEN_PRIVATE_KEY_FILE",
	EnvPrefix + "_ACCESS_TOKEN_ISSUER",
	EnvPrefix + "_ACCESS_TOKEN_AUDIENCE",
	EnvPrefix + "_ACCESS_TOKEN_KID",
	EnvPrefix + "_ENROLLMENT_CANONICAL_URL",
	EnvPrefix + "_REFRESH_CANONICAL_URL",
	EnvPrefix + "_HMAC_SECRET_KEY_FILE",
	EnvPrefix + "_DATABASE_DSN_FILE",
}

// Bounds on the secret files: far above any real value, finite.
const (
	MaxHMACKeyFileBytes = 4 << 10
	MaxDSNFileBytes     = 8 << 10
)

// ConnectTimeout bounds the initial database connection.
const ConnectTimeout = 10 * time.Second

// ActivationConfig is the full contract: the six security variables and the
// two secret files.
type ActivationConfig struct {
	Security          SecurityConfig
	HMACSecretKeyFile string `env:"HMAC_SECRET_KEY_FILE" required:"true"`
	DatabaseDSNFile   string `env:"DATABASE_DSN_FILE" required:"true"`

	// dbTLS is the database's TLS policy (ADR-0029): verified TLS unless
	// ENVIRONMENT is explicitly development. LoadActivation sets it; the zero
	// value is RequireVerifiedTLS. Unexported, so platform.LoadConfig cannot
	// populate it from a variable of the same name.
	dbTLS pgshared.TLSPolicy
}

// Validate applies SecurityConfig.Validate and the same blank and padding rule
// to the two paths, and refuses one file serving two purposes.
func (c ActivationConfig) Validate() error {
	if err := c.Security.Validate(); err != nil {
		return err
	}
	var problems []string
	for _, f := range []struct{ name, value string }{
		{"HMAC_SECRET_KEY_FILE", c.HMACSecretKeyFile},
		{"DATABASE_DSN_FILE", c.DatabaseDSNFile},
	} {
		switch {
		case strings.TrimSpace(f.value) == "":
			problems = append(problems, EnvPrefix+"_"+f.name+" is blank")
		case strings.TrimSpace(f.value) != f.value:
			problems = append(problems, EnvPrefix+"_"+f.name+" has leading or trailing whitespace")
		}
	}
	if len(problems) == 0 {
		paths := map[string]string{}
		for name, p := range map[string]string{
			"ACCESS_TOKEN_PRIVATE_KEY_FILE": c.Security.AccessTokenPrivateKeyFile,
			"HMAC_SECRET_KEY_FILE":          c.HMACSecretKeyFile,
			"DATABASE_DSN_FILE":             c.DatabaseDSNFile,
		} {
			clean := filepath.Clean(p)
			if other, dup := paths[clean]; dup {
				problems = append(problems, fmt.Sprintf("%s_%s and %s_%s name the same file", EnvPrefix, other, EnvPrefix, name))
			}
			paths[clean] = name
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return nil
}

// LoadActivation reports whether identity is activated and, if so, its
// validated configuration. Inactive is (zero, false, nil).
func LoadActivation() (ActivationConfig, bool, error) {
	present := false
	for _, name := range ActivationVariables {
		if _, ok := os.LookupEnv(name); ok {
			present = true
			break
		}
	}
	if !present {
		return ActivationConfig{}, false, nil
	}
	var c ActivationConfig
	if err := platform.LoadConfig(&c, EnvPrefix); err != nil {
		return ActivationConfig{}, true, fmt.Errorf("%w: identity is partially configured: %w", ErrInvalidConfig, err)
	}
	if err := c.Validate(); err != nil {
		return ActivationConfig{}, true, err
	}
	c.dbTLS = pgshared.PolicyForEnvironment(os.LookupEnv(EnvPrefix + "_ENVIRONMENT"))
	return c, true, nil
}

// readSecretFile reads a regular file of at most limit bytes. Errors name the
// variable, never the path or the content.
func readSecretFile(name, path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the path is operator configuration (a secret file); reading it is the purpose
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s: the file does not exist", ErrInvalidConfig, name)
	case errors.Is(err, fs.ErrPermission):
		return nil, fmt.Errorf("%w: %s: permission denied reading the file", ErrInvalidConfig, name)
	case err != nil:
		return nil, fmt.Errorf("%w: %s: the file cannot be opened", ErrInvalidConfig, name)
	}
	defer f.Close() //nolint:errcheck // read-only file, already fully read: a Close error cannot lose data
	if info, statErr := f.Stat(); statErr != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s: not a regular file", ErrInvalidConfig, name)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		clear(data)
		return nil, fmt.Errorf("%w: %s: the file cannot be read", ErrInvalidConfig, name)
	}
	if int64(len(data)) > limit {
		clear(data)
		return nil, fmt.Errorf("%w: %s: the file is larger than %d bytes", ErrInvalidConfig, name, limit)
	}
	return data, nil
}

// ReadHMACSecretKeyFile returns the file's bytes exactly as stored — no
// trimming, no decoding, a trailing newline included. It refuses fewer than
// MinSecretKeyBytes and a key whose bytes are all one value (an unfilled
// secret: zeros, or a single repeated character).
func ReadHMACSecretKeyFile(path string) ([]byte, error) {
	const name = EnvPrefix + "_HMAC_SECRET_KEY_FILE"
	key, err := readSecretFile(name, path, MaxHMACKeyFileBytes)
	if err != nil {
		return nil, err
	}
	if len(key) < MinSecretKeyBytes {
		clear(key)
		return nil, fmt.Errorf("%w: %s: the key must be at least %d bytes", ErrInvalidConfig, name, MinSecretKeyBytes)
	}
	if bytes.Count(key, key[:1]) == len(key) {
		clear(key)
		return nil, fmt.Errorf("%w: %s: the key is a single repeated byte", ErrInvalidConfig, name)
	}
	return key, nil
}

// ReadDSNFile returns the connection string with surrounding whitespace
// removed. It refuses an empty value, a line break inside the value, and a
// string the driver cannot parse — and never echoes it.
func ReadDSNFile(path string) (string, error) {
	const name = EnvPrefix + "_DATABASE_DSN_FILE"
	data, err := readSecretFile(name, path, MaxDSNFileBytes)
	if err != nil {
		return "", err
	}
	dsn := string(bytes.TrimSpace(data))
	clear(data)
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

// distinctFromSigningKey refuses an HMAC key that is the signing-key file, or
// that contains the signing key's seed.
func distinctFromSigningKey(hmacKey []byte, signingKeyFile string) error {
	const name = EnvPrefix + "_HMAC_SECRET_KEY_FILE"
	pemBytes, err := readSecretFile(EnvPrefix+"_ACCESS_TOKEN_PRIVATE_KEY_FILE", signingKeyFile, MaxKeyFileBytes)
	if err != nil {
		return err
	}
	defer clear(pemBytes)
	if bytes.Equal(hmacKey, pemBytes) {
		return fmt.Errorf("%w: %s: the HMAC key is the signing key file", ErrInvalidConfig, name)
	}
	key, err := accesstoken.ParsePrivateKeyPEM(pemBytes)
	if err != nil {
		return fmt.Errorf("%w: %s_ACCESS_TOKEN_PRIVATE_KEY_FILE: %w", ErrInvalidConfig, EnvPrefix, err)
	}
	defer clear(key)
	if bytes.Contains(hmacKey, key.Seed()) {
		return fmt.Errorf("%w: %s: the HMAC key contains the signing key", ErrInvalidConfig, name)
	}
	return nil
}

// Runtime is an activated identity service and the pool it owns.
type Runtime struct {
	Service     *app.IdentityService
	pool        *pgxpool.Pool
	housekeeper *pgstore.Housekeeper
	once        sync.Once
}

// Housekeeper is the bounded cleanup for this runtime's database, built on the
// same pool; it exists only when identity is activated.
func (r *Runtime) Housekeeper() *pgstore.Housekeeper { return r.housekeeper }

// Close releases the pool. Safe to call more than once.
func (r *Runtime) Close() { r.once.Do(r.pool.Close) }

// CheckDatabase pings the database for readiness. Its error is fixed text:
// readiness bodies are served to anyone who can reach the health port.
func (r *Runtime) CheckDatabase(ctx context.Context) error {
	if err := pgstore.Ping(ctx, r.pool); err != nil {
		return errors.New("identity database unreachable")
	}
	return nil
}

// newPool is the pool constructor, replaceable in tests.
var newPool = pgstore.NewPool

// Open builds the activated identity service on PostgreSQL. Everything local
// is validated before any connection is made: configuration, the HMAC key,
// its distinctness from the signing key, the DSN and the signing key itself.
// Then the pool is created and pinged, and Build constructs the service with a
// pgstore repository and separate enrolment and refresh replay stores on that
// pool. On any failure every resource opened so far is closed and nothing is
// returned. Errors carry no key bytes and no connection details.
//
// The database must already be migrated (`service migrate`); Open never
// applies DDL.
func Open(ctx context.Context, cfg ActivationConfig, now func() time.Time) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	secret, err := ReadHMACSecretKeyFile(cfg.HMACSecretKeyFile)
	if err != nil {
		return nil, err
	}
	defer clear(secret) // Build keeps its own copy
	if distinctFromSigningKeyErr := distinctFromSigningKey(secret, cfg.Security.AccessTokenPrivateKeyFile); distinctFromSigningKeyErr != nil {
		return nil, distinctFromSigningKeyErr
	}
	dsn, err := ReadDSNFile(cfg.DatabaseDSNFile)
	if err != nil {
		return nil, err
	}
	if _, loadSignerErr := loadSigner(cfg.Security, now); loadSignerErr != nil {
		return nil, loadSignerErr
	}

	pool, err := newPool(dsn, cfg.dbTLS)
	if err != nil {
		return nil, fmt.Errorf("%w: %s_DATABASE_DSN_FILE: %w", ErrInvalidConfig, EnvPrefix, pgstore.Redact(err))
	}
	pingCtx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	defer cancel()
	if pingErr := pgstore.Ping(pingCtx, pool); pingErr != nil {
		pool.Close()
		return nil, fmt.Errorf("identity bootstrap: %w", pingErr)
	}
	svc, err := Build(cfg.Security, Dependencies{
		Repository:       pgstore.NewRepository(pool),
		SecretKey:        secret,
		EnrollmentReplay: pgstore.NewReplayStore(pool),
		RefreshReplay:    pgstore.NewReplayStore(pool),
		Now:              now,
	})
	if err != nil {
		pool.Close()
		return nil, err
	}
	// Every security audit event is appended to the durable, append-only log
	// (migration 0005; ADR-0010 §10, ADR-0012 LEGAL_HOLD). Other auditors, such
	// as the metrics counter, are added alongside it.
	svc.AddSecurityAuditor(pgstore.NewSecurityAuditLog(pool))
	hk, err := pgstore.NewHousekeeper(pool, pgstore.HousekeepingConfig{
		BatchSize:        pgstore.DefaultHousekeepingBatchSize,
		Margin:           pgstore.DefaultHousekeepingMargin,
		RateWindow:       app.DefaultOtpRatePolicy().Window,
		IterationTimeout: pgstore.DefaultHousekeepingIterationTimeout,
	}, now)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Runtime{Service: svc, pool: pool, housekeeper: hk}, nil
}
