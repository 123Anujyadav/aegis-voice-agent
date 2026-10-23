package pgshared

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TLSPolicy is the transport security a connection string must establish
// (ADR-0029, "Database TLS").
type TLSPolicy int

const (
	// RequireVerifiedTLS demands sslmode=verify-full with an explicit,
	// absolute, readable CA bundle (never sslrootcert=system), host name
	// verification, and no plaintext or multi-host fallback. It is the policy
	// everywhere except explicit development.
	RequireVerifiedTLS TLSPolicy = iota
	// AllowPlaintextForDevelopment applies no transport requirement. It exists
	// only for a local database on a developer's machine or a CI runner.
	AllowPlaintextForDevelopment
)

// PolicyForEnvironment returns the TLS policy for the platform ENVIRONMENT
// variable. Plaintext is allowed ONLY when the variable is explicitly present
// and exactly "development": the platform's implicit default of development
// must not be what relaxes a security control (the telephony gateway's R2 rule,
// applied here).
func PolicyForEnvironment(value string, explicitlySet bool) TLSPolicy {
	if explicitlySet && value == "development" {
		return AllowPlaintextForDevelopment
	}
	return RequireVerifiedTLS
}

// ErrInsecureDSN is a connection string that does not meet the TLS policy. The
// value is never included: it may carry a password.
var ErrInsecureDSN = errors.New("postgres: the connection configuration is not verified TLS (sslmode=verify-full with an explicit CA bundle is required; value not shown)")

// checkTLS enforces policy on dsn and on pgx's parsed form of it. The raw
// check proves the operator asked for verify-full with an explicit CA; the
// parsed check proves nothing else (environment variables, service files,
// multiple hosts) weakened what pgx will actually do.
func checkTLS(dsn string, cfg *pgxpool.Config, policy TLSPolicy) error {
	if policy == AllowPlaintextForDevelopment {
		return nil
	}
	params, err := dsnParams(dsn)
	if err != nil {
		return ErrInsecureDSN
	}
	var problems []string
	if params["sslmode"] != "verify-full" {
		problems = append(problems, "sslmode must be verify-full")
	}
	if p := rootCertProblem(params["sslrootcert"]); p != "" {
		problems = append(problems, p)
	}
	problems = append(problems, parsedTLSProblems(cfg)...)
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInsecureDSN, strings.Join(problems, "; "))
	}
	return nil
}

// rootCertProblem describes what is wrong with an sslrootcert value, or
// returns "" when it names an absolute, readable CA bundle file.
func rootCertProblem(root string) string {
	switch {
	case root == "":
		return "sslrootcert must name a CA bundle file"
	case root == "system":
		return "sslrootcert=system is not accepted; name an explicit CA bundle file"
	case !filepath.IsAbs(root):
		return "sslrootcert must be an absolute path"
	case !readableRegularFile(root):
		return "sslrootcert does not name a readable regular file"
	}
	return ""
}

// parsedTLSProblems checks what pgx will actually do: no fallback connection,
// and a TLS configuration that verifies the server against an explicit CA by
// host name, without a client certificate or a custom verifier.
func parsedTLSProblems(cfg *pgxpool.Config) []string {
	var problems []string
	c := cfg.ConnConfig
	if len(c.Fallbacks) != 0 {
		problems = append(problems, "fallback connections (multiple hosts or a plaintext fallback) are not accepted")
	}
	t := c.TLSConfig
	if t == nil || t.InsecureSkipVerify || t.VerifyPeerCertificate != nil || t.RootCAs == nil ||
		t.ServerName != c.Host || len(t.Certificates) != 0 {
		problems = append(problems, "the parsed TLS configuration is not verify-full with an explicit CA and host name verification")
	}
	return problems
}

// dsnParams returns the connection parameters of a URL-form
// (postgres://, postgresql://) or keyword/value-form connection string. Only
// the parameter names are used by callers; values are never logged.
func dsnParams(dsn string) (map[string]string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return nil, ErrInvalidDSN
		}
		out := map[string]string{}
		for k, v := range u.Query() {
			if len(v) != 1 {
				return nil, ErrInvalidDSN
			}
			out[k] = v[0]
		}
		return out, nil
	}
	return keywordParams(dsn)
}

// keywordParams parses libpq keyword/value syntax: key=value pairs separated by
// whitespace, values optionally single-quoted with backslash escapes. A
// repeated key is refused rather than resolved.
func keywordParams(s string) (map[string]string, error) {
	out := map[string]string{}
	sc := &dsnScanner{s: s}
	for {
		sc.skipSpace()
		if sc.done() {
			return out, nil
		}
		key, err := sc.key()
		if err != nil {
			return nil, err
		}
		val, err := sc.value()
		if err != nil {
			return nil, err
		}
		if _, dup := out[key]; dup {
			return nil, ErrInvalidDSN
		}
		out[key] = val
	}
}

// dsnScanner reads a keyword/value connection string left to right.
type dsnScanner struct {
	s string
	i int
}

func (sc *dsnScanner) done() bool { return sc.i >= len(sc.s) }

func isDSNSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

func (sc *dsnScanner) skipSpace() {
	for !sc.done() && isDSNSpace(sc.s[sc.i]) {
		sc.i++
	}
}

// key reads "name=" and returns name.
func (sc *dsnScanner) key() (string, error) {
	eq := strings.IndexByte(sc.s[sc.i:], '=')
	if eq <= 0 {
		return "", ErrInvalidDSN
	}
	key := strings.TrimSpace(sc.s[sc.i : sc.i+eq])
	sc.i += eq + 1
	for !sc.done() && sc.s[sc.i] == ' ' {
		sc.i++
	}
	return key, nil
}

// value reads a quoted or bare value.
func (sc *dsnScanner) value() (string, error) {
	if !sc.done() && sc.s[sc.i] == '\'' {
		sc.i++
		return sc.quoted()
	}
	start := sc.i
	for !sc.done() && !isDSNSpace(sc.s[sc.i]) {
		sc.i++
	}
	return sc.s[start:sc.i], nil
}

// quoted reads the rest of a single-quoted value, after its opening quote.
func (sc *dsnScanner) quoted() (string, error) {
	var val strings.Builder
	for !sc.done() {
		switch c := sc.s[sc.i]; {
		case c == '\\' && sc.i+1 < len(sc.s):
			val.WriteByte(sc.s[sc.i+1])
			sc.i += 2
		case c == '\'':
			sc.i++
			return val.String(), nil
		default:
			val.WriteByte(c)
			sc.i++
		}
	}
	return "", ErrInvalidDSN
}

func readableRegularFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path) //nolint:gosec // an operator-configured CA bundle path, checked to be absolute
	if err != nil {
		return false
	}
	return f.Close() == nil
}
