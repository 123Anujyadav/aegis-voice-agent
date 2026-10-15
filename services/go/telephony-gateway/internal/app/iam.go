package app

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/fence"
	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/rdsauth"
	"github.com/jackc/pgx/v5"
)

// IAM authentication-mode rules for the fence connection (Phase 19 P1-A1;
// decisions C1, O-5, R1–R6, B1, B2, H1–H3, M2, M3 and environment naming —
// see docs/telephony/P1-40_A1_DECISIONS_AND_IMPLEMENTATION.md).
//
// Every rule here is STATIC: it inspects configuration, files and the process
// environment, makes no network call (R6), and any violation stops startup.
// Errors name variables and rules, never values (R3).

// ErrIAMAdapterUnavailable is returned when a fully valid IAM configuration
// is supplied but no IAM credential adapter is set (iamCredentials is nil).
// IAM mode then refuses to start rather than fall back to anything (R6).
var ErrIAMAdapterUnavailable = errors.New("telephony-gateway: IAM authentication is configured, but this build has no IAM credential adapter; refusing to start")

// iamCredentials builds the IAM credential source for a validated IRSA
// identity: the AWS SDK binding (rdsauth.NewAWS), which makes no network call
// until the first dial (R6). Tests replace it; nil means "no adapter" and
// stops startup (ErrIAMAdapterUnavailable).
var iamCredentials = func(id rdsauth.Identity) (fence.Credentials, error) {
	return rdsauth.NewAWS(id)
}

// staticAWSCredentials are the environment variables that carry static AWS
// keys. IAM mode uses IRSA only; any of them being set is a misconfiguration
// that stops startup (no static keys, ever).
var staticAWSCredentials = []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"}

// pgxEnvironment lists every environment variable pgx v5.10.0 reads while
// parsing a connection string (pgconn/config.go, parseEnvSettings). In IAM mode
// the connection string must be the SINGLE source of connection settings, so
// any of these being set stops startup (H1/H2). RE-REVIEW ON ANY pgx UPGRADE.
var pgxEnvironment = []string{
	"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE",
	"PGAPPNAME", "PGCONNECT_TIMEOUT", "PGSSLMODE", "PGSSLKEY", "PGSSLCERT",
	"PGSSLSNI", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGSSLNEGOTIATION",
	"PGTARGETSESSIONATTRS", "PGSERVICE", "PGSERVICEFILE", "PGTZ", "PGOPTIONS",
	"PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION", "PGCHANNELBINDING",
	"PGREQUIREAUTH",
}

// regionAllowList is R4b, keyed on the PLATFORM environment value. There is no
// development entry: IAM mode is refused in development until a separate
// region decision (B2). The deployment environment prod-global also runs as
// "production" and cannot be told apart here; its region policy is unresolved
// and must be decided before any prod-global deployment.
var regionAllowList = map[string][]string{
	"staging":    {"ap-south-1"},
	"production": {"ap-south-1", "ap-south-2"},
}

// Query parameters an IAM-mode connection string may carry (B1).
var iamQueryAllowed = map[string]bool{
	"sslmode": true, "sslrootcert": true, "target_session_attrs": true, "application_name": true,
}

// Known pgx parameters that are named when refused (their NAMES are not
// secret). Any other unknown parameter is refused without echoing its name,
// because a malformed query can turn a value into a key.
var iamQueryNamed = map[string]bool{
	"password": true, "passfile": true, "service": true, "servicefile": true,
	"sslcert": true, "sslkey": true, "sslpassword": true, "sslnegotiation": true,
	"sslsni": true, "connect_timeout": true, "channel_binding": true,
	"require_auth": true, "host": true, "hostaddr": true, "port": true,
	"dbname": true, "user": true, "options": true, "min_protocol_version": true,
	"max_protocol_version": true,
}

// checkIAMEnvironment applies the environment, region, PostgreSQL-environment
// and IRSA rules. The region argument is the (non-empty) FENCE_AWS_REGION
// value.
func checkIAMEnvironment(prefix string, lookup Lookup, region string) error {
	envName := prefix + "_" + EnvEnvironment
	regionName := prefix + "_" + EnvFenceAWSRegion
	problems := environmentRegionProblems(envName, regionName, lookup, region)
	problems = append(problems, awsRegionProblems(regionName, lookup, region)...)
	if pgSet := setVariables(lookup, pgxEnvironment); len(pgSet) > 0 {
		problems = append(problems, "IAM mode forbids PostgreSQL client environment variables; unset "+strings.Join(pgSet, ", "))
	}
	if static := setVariables(lookup, staticAWSCredentials); len(static) > 0 {
		problems = append(problems, "IAM mode uses IRSA only and forbids static AWS credentials; unset "+strings.Join(static, ", "))
	}
	problems = append(problems, irsaProblems(lookup)...)
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return nil
}

// environmentRegionProblems applies R4b: IAM mode needs ENVIRONMENT
// explicitly staging or production, and region on that environment's
// allow-list.
func environmentRegionProblems(envName, regionName string, lookup Lookup, region string) []string {
	env, envSet := lookup(envName)
	switch {
	case !envSet:
		return []string{"IAM mode requires " + envName + " to be explicitly set to staging or production"}
	case env == "development":
		return []string{"IAM mode is not permitted when " + envName + " is development (no development region is approved)"}
	}
	allowed, ok := regionAllowList[env]
	switch {
	case !ok:
		return []string{"IAM mode requires " + envName + " to be staging or production"}
	case !contains(allowed, region):
		return []string{fmt.Sprintf("%s is not an approved region for %s=%s (approved: %s)",
			regionName, envName, env, strings.Join(allowed, ", "))}
	}
	return nil
}

// awsRegionProblems applies H3: webhook-injected AWS regions must match.
func awsRegionProblems(regionName string, lookup Lookup, region string) []string {
	var problems []string
	for _, v := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if val, ok := lookup(v); ok && val != region {
			problems = append(problems, v+" is set and does not match "+regionName)
		}
	}
	return problems
}

// setVariables returns, in list order, the variables of names that are set.
func setVariables(lookup Lookup, names []string) []string {
	var set []string
	for _, v := range names {
		if _, ok := lookup(v); ok {
			set = append(set, v)
		}
	}
	return set
}

// irsaProblems requires the IRSA role and a readable projected token file.
func irsaProblems(lookup Lookup) []string {
	var problems []string
	if role, ok := lookup("AWS_ROLE_ARN"); !ok || strings.TrimSpace(role) == "" {
		problems = append(problems, "IAM mode requires AWS_ROLE_ARN (IRSA)")
	}
	tokenFile, ok := lookup("AWS_WEB_IDENTITY_TOKEN_FILE")
	switch {
	case !ok || strings.TrimSpace(tokenFile) == "":
		problems = append(problems, "IAM mode requires AWS_WEB_IDENTITY_TOKEN_FILE (IRSA)")
	case !readableRegularFile(tokenFile):
		problems = append(problems, "AWS_WEB_IDENTITY_TOKEN_FILE does not name a readable regular file")
	}
	return problems
}

// iamTarget is what the raw connection string says; the parsed configuration
// must agree with it exactly.
type iamTarget struct {
	host     string
	port     uint16
	user     string
	database string
}

// validateIAMDSN checks the raw connection string (B1, M2, R1, R3). It never
// includes any part of dsn in an error: url.Parse errors embed the whole
// string, password included, so they are dropped.
func validateIAMDSN(name, dsn string) (iamTarget, error) {
	fail := func(msg string) (iamTarget, error) {
		return iamTarget{}, fmt.Errorf("%w: %s: %s", ErrInvalidConfig, name, msg)
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Opaque != "" {
		return fail("IAM mode requires a URL-form connection string (postgres://…) (value not shown)")
	}
	problems := dsnUserProblems(u)
	hostname, port, hostProblems := dsnHostPort(u)
	problems = append(problems, hostProblems...)
	database := strings.TrimPrefix(u.Path, "/")
	if database == "" || strings.Contains(database, "/") {
		problems = append(problems, "must name exactly one database")
	}
	if u.Fragment != "" {
		problems = append(problems, "must not contain a fragment")
	}
	problems = append(problems, dsnQueryProblems(u.RawQuery)...)
	if len(problems) > 0 {
		return fail(strings.Join(problems, "; "))
	}
	return iamTarget{host: hostname, port: port, user: u.User.Username(), database: database}, nil
}

// dsnUserProblems requires a user and forbids a password in the string.
func dsnUserProblems(u *url.URL) []string {
	if u.User == nil || u.User.Username() == "" {
		return []string{"must name a database user"}
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		return []string{"must not contain a password (IAM tokens are generated per connection)"}
	}
	return nil
}

// dsnHostPort requires exactly one network host with an explicit port.
func dsnHostPort(u *url.URL) (hostname string, port uint16, problems []string) {
	hostname = u.Hostname()
	switch {
	case strings.Contains(u.Host, ","):
		problems = append(problems, "must name exactly one host (the cluster writer endpoint)")
	case hostname == "" || strings.HasPrefix(hostname, "/"):
		problems = append(problems, "must name a network host")
	default:
		p, perr := strconv.ParseUint(u.Port(), 10, 16)
		if u.Port() == "" || perr != nil || p == 0 {
			problems = append(problems, "must name an explicit port")
		}
		port = uint16(p)
	}
	return hostname, port, problems
}

// dsnQueryProblems applies the query rules (B1, M2): only allowed
// parameters, each once; verify-full TLS with an explicit CA file; the
// read-write session check. Values are never echoed.
func dsnQueryProblems(rawQuery string) []string {
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return []string{"query cannot be parsed (value not shown)"}
	}
	problems := dsnQueryKeyProblems(q)
	problems = append(problems, dsnTLSProblems(q)...)
	if v := q["target_session_attrs"]; len(v) != 1 || v[0] != "read-write" {
		problems = append(problems, "IAM mode requires target_session_attrs=read-write")
	}
	return problems
}

// dsnQueryKeyProblems reports repeated allowed parameters, then refused
// parameters whose names are safe to show (sorted), then whether any other
// unsupported parameter is present (name not shown).
func dsnQueryKeyProblems(q url.Values) []string {
	var problems, named []string
	unsupported := false
	for k, vs := range q {
		switch {
		case !iamQueryAllowed[k] && iamQueryNamed[k]:
			named = append(named, k)
		case !iamQueryAllowed[k]:
			unsupported = true
		case len(vs) != 1:
			problems = append(problems, "query parameter "+k+" must appear exactly once")
		}
	}
	sort.Strings(named)
	for _, k := range named {
		problems = append(problems, "query parameter "+k+" is not permitted in IAM mode")
	}
	if unsupported {
		problems = append(problems, "an unsupported query parameter is present (name not shown)")
	}
	return problems
}

// dsnTLSProblems requires sslmode=verify-full and an explicit, absolute,
// readable CA bundle (never sslrootcert=system).
func dsnTLSProblems(q url.Values) []string {
	var problems []string
	if v := q["sslmode"]; len(v) != 1 || v[0] != "verify-full" {
		problems = append(problems, "IAM mode requires sslmode=verify-full")
	}
	switch v := q["sslrootcert"]; {
	case len(v) != 1 || v[0] == "":
		problems = append(problems, "IAM mode requires sslrootcert naming a CA bundle file")
	case v[0] == "system":
		problems = append(problems, "IAM mode forbids sslrootcert=system; name an explicit CA bundle file")
	case !filepath.IsAbs(v[0]):
		problems = append(problems, "sslrootcert must be an absolute path")
	case !readableRegularFile(v[0]):
		problems = append(problems, "sslrootcert does not name a readable regular file")
	}
	return problems
}

// validateIAMParsed checks that pgx's parsed configuration is exactly what the
// raw string says, with nothing contributed by environment variables,
// passfiles, service files or default certificate files (B1, R1, R3).
func validateIAMParsed(name string, want iamTarget, c *pgx.ConnConfig) error {
	var problems []string
	if c.Password != "" {
		problems = append(problems, "a password reached the parsed configuration (connection string, passfile or service file); IAM mode forbids it")
	}
	if c.Host != want.host || c.Port != want.port || c.User != want.user || c.Database != want.database {
		problems = append(problems, "the parsed connection target differs from the connection string (an environment variable, passfile or service file interfered)")
	}
	if len(c.Fallbacks) != 0 {
		problems = append(problems, "the parsed configuration has fallback connections (multi-host or plaintext fallback)")
	}
	t := c.TLSConfig
	if t == nil || t.InsecureSkipVerify || t.VerifyPeerCertificate != nil || t.ServerName != c.Host ||
		t.RootCAs == nil || len(t.Certificates) != 0 {
		problems = append(problems, "TLS is not verify-full with an explicit CA, hostname verification and no client certificate")
	}
	if c.ValidateConnect == nil {
		problems = append(problems, "target_session_attrs=read-write did not take effect")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s: %s", ErrInvalidConfig, name, strings.Join(problems, "; "))
	}
	return nil
}

func readableRegularFile(path string) bool {
	f, err := os.Open(path) //nolint:gosec // G304: the path is operator configuration (CA bundle or IRSA token file); probing it is the purpose
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // read-only probe: a Close error carries no information about the file
	info, err := f.Stat()
	return err == nil && info.Mode().IsRegular()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// isWriterEndpoint enforces C1 ("no CNAMEs in the DSN") and C3 ("direct
// writer endpoint; no PgBouncer or RDS Proxy") statically: host must be an
// Aurora cluster writer endpoint in region,
//
//	<cluster>.cluster-<id>.<region>.rds.amazonaws.com
//
// where <id> is letters and digits only. That excludes the reader
// (cluster-ro-<id>) and custom (cluster-custom-<id>) endpoints, RDS Proxy
// (<name>.proxy-<id>), instance endpoints, any other domain (a CNAME) and IP
// addresses. The region label must equal FENCE_AWS_REGION, so the token's
// signing region and the database's region cannot disagree.
func isWriterEndpoint(host, region string) bool {
	h := strings.ToLower(host)
	suffix := "." + region + ".rds.amazonaws.com"
	if region == "" || !strings.HasSuffix(h, suffix) {
		return false
	}
	labels := strings.Split(strings.TrimSuffix(h, suffix), ".")
	if len(labels) != 2 || !dnsLabel(labels[0]) {
		return false
	}
	id, ok := strings.CutPrefix(labels[1], "cluster-")
	if !ok || id == "" {
		return false
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func dnsLabel(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
