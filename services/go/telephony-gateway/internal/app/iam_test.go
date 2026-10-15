package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/fence"
	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/rdsauth"
	"github.com/jackc/pgx/v5/pgconn"
)

// P1-A1 — IAM-mode configuration rules (R1–R6, B1, B2, H1–H3, M2, M3,
// environment naming). Offline: no AWS, no network, no database. Tests that
// let pgx parse a connection string control the REAL process environment
// (pgx reads PG* variables via os.Getenv) and therefore cannot run in
// parallel.

const secret = "hunter2"

// clearPGEnv unsets every pgx-consumed PG* variable for the test, restoring
// them afterwards. Marks the test non-parallel (t.Setenv).
func clearPGEnv(t *testing.T) {
	t.Helper()
	for _, k := range pgxEnvironment {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// writeCA writes a self-signed CA certificate (PEM) and returns its path.
func writeCA(t *testing.T, dir string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// writerHost is a well-formed Aurora writer endpoint. No test connects to it
// (credential sources in these tests fail before any dial), so no DNS query
// for it is ever made.
const writerHost = "telephony.cluster-abc123def456.ap-south-1.rds.amazonaws.com"

func iamDSNWith(ca string, extra string) string {
	return iamDSNFor(writerHost, ca, extra)
}

func iamDSNFor(host, ca, extra string) string {
	return "postgres://fence_iam@" + host + ":5432/telephony?sslmode=verify-full&sslrootcert=" +
		url.QueryEscape(ca) + "&target_session_attrs=read-write" + extra
}

// iamEnv returns a complete, valid IAM-mode configuration (staging).
func iamEnv(t *testing.T) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	ca := writeCA(t, dir)
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("not-a-real-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		prefix + "_DATABASE_DSN_FILE":        dsnFile(t, iamDSNWith(ca, "")),
		prefix + "_FENCE_LOCK_KEY":           "8813",
		prefix + "_FENCE_HEARTBEAT_INTERVAL": "200ms",
		prefix + "_FENCE_HEARTBEAT_TIMEOUT":  "100ms",
		prefix + "_FENCE_AUTH_MODE":          "iam",
		prefix + "_FENCE_AWS_REGION":         "ap-south-1",
		prefix + "_ENVIRONMENT":              "staging",
		"AWS_ROLE_ARN":                       "arn:aws:iam::000000000000:role/test-only",
		"AWS_WEB_IDENTITY_TOKEN_FILE":        token,
	}, ca
}

func noLeak(t *testing.T, name string, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("%s: error leaks a secret: %v", name, err)
	}
}

// M3: a configuration attempt never silently becomes "unconfigured".
func TestLoadFence_PresenceRules(t *testing.T) {
	t.Parallel()
	for _, v := range FenceVariables(prefix) {
		for _, val := range []string{"", "x"} {
			_, err := LoadFence(prefix, env(map[string]string{v: val}))
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("only %s=%q present: %v, want a configuration error (never unconfigured)", v, val, err)
			}
		}
	}
	for _, v := range FenceVariables(prefix)[:5] {
		m := full(t, "postgres://gw@127.0.0.1:5432/telephony")
		m[v] = ""
		if _, err := LoadFence(prefix, env(m)); err == nil || !strings.Contains(err.Error(), "set but empty") {
			t.Errorf("%s empty: %v", v, err)
		}
	}
}

// R2: password mode only with ENVIRONMENT explicitly set to development.
func TestLoadFence_PasswordModeRequiresExplicitDevelopment(t *testing.T) {
	t.Parallel()
	base := func() map[string]string { return full(t, "postgres://gw@127.0.0.1:5432/telephony") }
	if _, err := LoadFence(prefix, env(base())); err != nil {
		t.Fatalf("explicit development: %v", err)
	}
	for name, mutate := range map[string]func(m map[string]string){
		"ENVIRONMENT absent (platform default)": func(m map[string]string) { delete(m, prefix+"_ENVIRONMENT") },
		"staging":                               func(m map[string]string) { m[prefix+"_ENVIRONMENT"] = "staging" },
		"production":                            func(m map[string]string) { m[prefix+"_ENVIRONMENT"] = "production" },
		"region set in password mode":           func(m map[string]string) { m[prefix+"_FENCE_AWS_REGION"] = "ap-south-1" },
		"empty region in password mode":         func(m map[string]string) { m[prefix+"_FENCE_AWS_REGION"] = "" },
		"unknown mode":                          func(m map[string]string) { m[prefix+"_FENCE_AUTH_MODE"] = "IAM" },
		"mode missing":                          func(m map[string]string) { delete(m, prefix+"_FENCE_AUTH_MODE") },
	} {
		m := base()
		mutate(m)
		if _, err := LoadFence(prefix, env(m)); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: %v, want refusal", name, err)
		}
	}
}

func TestLoadFence_AValidIAMConfiguration(t *testing.T) {
	clearPGEnv(t)
	m, ca := iamEnv(t)
	s, err := LoadFence(prefix, env(m))
	if err != nil {
		t.Fatalf("valid IAM configuration refused: %v", err)
	}
	if !s.Configured || s.Mode != AuthIAM || s.Region != "ap-south-1" || s.Conn.Password != "" {
		t.Errorf("settings = %+v", s)
	}
	tc := s.Conn.TLSConfig
	if tc == nil || tc.InsecureSkipVerify || tc.ServerName != writerHost || tc.RootCAs == nil {
		t.Errorf("not verify-full: %+v", tc)
	}
	// production may use either approved region.
	m[prefix+"_ENVIRONMENT"] = "production"
	m[prefix+"_FENCE_AWS_REGION"] = "ap-south-2"
	m["AWS_REGION"] = "ap-south-2"
	m[prefix+"_DATABASE_DSN_FILE"] = dsnFile(t, iamDSNFor("telephony.cluster-abc123def456.ap-south-2.rds.amazonaws.com", ca, ""))
	if _, err := LoadFence(prefix, env(m)); err != nil {
		t.Errorf("production ap-south-2: %v", err)
	}
}

// B2, R4a/R4b, H1/H2, H3, R6 (static IRSA).
func TestLoadFence_IAMEnvironmentRules(t *testing.T) {
	clearPGEnv(t)
	type envCase struct {
		mutate func(m map[string]string)
		want   string
	}
	cases := map[string]envCase{
		"ENVIRONMENT absent":      {func(m map[string]string) { delete(m, prefix+"_ENVIRONMENT") }, "explicitly set to staging or production"},
		"development (B2)":        {func(m map[string]string) { m[prefix+"_ENVIRONMENT"] = "development" }, "not permitted when"},
		"unknown environment":     {func(m map[string]string) { m[prefix+"_ENVIRONMENT"] = "prod-global" }, "to be staging or production"},
		"staging with ap-south-2": {func(m map[string]string) { m[prefix+"_FENCE_AWS_REGION"] = "ap-south-2" }, "not an approved region"},
		"production with us-east-1": {func(m map[string]string) {
			m[prefix+"_ENVIRONMENT"] = "production"
			m[prefix+"_FENCE_AWS_REGION"] = "us-east-1"
		}, "not an approved region"},
		"region missing":              {func(m map[string]string) { delete(m, prefix+"_FENCE_AWS_REGION") }, "required in IAM mode"},
		"region empty":                {func(m map[string]string) { m[prefix+"_FENCE_AWS_REGION"] = "" }, "set but empty"},
		"AWS_REGION mismatch":         {func(m map[string]string) { m["AWS_REGION"] = "ap-south-2" }, "AWS_REGION is set and does not match"},
		"AWS_DEFAULT_REGION mismatch": {func(m map[string]string) { m["AWS_DEFAULT_REGION"] = "us-east-1" }, "AWS_DEFAULT_REGION is set and does not match"},
		"AWS_REGION empty":            {func(m map[string]string) { m["AWS_REGION"] = "" }, "AWS_REGION is set and does not match"},
		"AWS_ROLE_ARN missing":        {func(m map[string]string) { delete(m, "AWS_ROLE_ARN") }, "requires AWS_ROLE_ARN"},
		"token file missing":          {func(m map[string]string) { delete(m, "AWS_WEB_IDENTITY_TOKEN_FILE") }, "requires AWS_WEB_IDENTITY_TOKEN_FILE"},
		"token file does not exist":   {func(m map[string]string) { m["AWS_WEB_IDENTITY_TOKEN_FILE"] = filepath.Join(t.TempDir(), "absent") }, "readable regular file"},
		"token file is a directory":   {func(m map[string]string) { m["AWS_WEB_IDENTITY_TOKEN_FILE"] = t.TempDir() }, "readable regular file"},
		"role ARN padded":             {func(m map[string]string) { m["AWS_ROLE_ARN"] += " " }, "IRSA identity is incomplete or malformed"},
		"static access key":           {func(m map[string]string) { m["AWS_ACCESS_KEY_ID"] = secret }, "unset AWS_ACCESS_KEY_ID"},
		"static secret key":           {func(m map[string]string) { m["AWS_SECRET_ACCESS_KEY"] = secret }, "unset AWS_SECRET_ACCESS_KEY"},
		"static session token":        {func(m map[string]string) { m["AWS_SESSION_TOKEN"] = "" }, "unset AWS_SESSION_TOKEN"},
	}
	for name, c := range cases {
		m, _ := iamEnv(t)
		c.mutate(m)
		_, err := LoadFence(prefix, env(m))
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want refusal containing %q", name, err, c.want)
		}
		noLeak(t, name, err)
	}
	m, _ := iamEnv(t)
	m["AWS_REGION"], m["AWS_DEFAULT_REGION"] = "ap-south-1", "ap-south-1"
	if _, err := LoadFence(prefix, env(m)); err != nil {
		t.Errorf("matching AWS_REGION/AWS_DEFAULT_REGION refused: %v", err)
	}
	// H1/H2: every pgx-consumed variable, named in the error, value never shown.
	for _, v := range pgxEnvironment {
		m, _ := iamEnv(t)
		m[v] = secret
		_, err := LoadFence(prefix, env(m))
		if err == nil || !strings.Contains(err.Error(), v) {
			t.Errorf("%s set: %v, want refusal naming it", v, err)
		}
		noLeak(t, v, err)
	}
}

// B1, M2, R1, R3: the raw connection string.
func TestValidateIAMDSN_Rules(t *testing.T) {
	t.Parallel()
	ca := writeCA(t, t.TempDir())
	if _, err := validateIAMDSN("DSN", iamDSNWith(ca, "")); err != nil {
		t.Fatalf("valid DSN refused: %v", err)
	}
	if _, err := validateIAMDSN("DSN", iamDSNWith(ca, "&application_name=gw")); err != nil {
		t.Errorf("application_name refused: %v", err)
	}
	q := "sslmode=verify-full&sslrootcert=" + url.QueryEscape(ca) + "&target_session_attrs=read-write"
	for name, c := range map[string]struct{ dsn, want string }{
		"keyword/value form":          {"host=writer.example.internal port=5432 user=u password=" + secret + " dbname=t sslmode=verify-full", "URL-form"},
		"wrong scheme":                {"mysql://u@writer.example.internal:5432/t?" + q, "URL-form"},
		"userinfo password":           {"postgres://u:" + secret + "@writer.example.internal:5432/t?" + q, "must not contain a password"},
		"no user":                     {"postgres://writer.example.internal:5432/t?" + q, "must name a database user"},
		"multi-host":                  {"postgres://u@a.example.internal:5432,b.example.internal:5432/t?" + q, "exactly one host"},
		"no port":                     {"postgres://u@writer.example.internal/t?" + q, "explicit port"},
		"no database":                 {"postgres://u@writer.example.internal:5432/?" + q, "exactly one database"},
		"two path segments":           {"postgres://u@writer.example.internal:5432/t/x?" + q, "exactly one database"},
		"fragment":                    {"postgres://u@writer.example.internal:5432/t?" + q + "#" + secret, "fragment"},
		"sslmode disable":             {strings.Replace(iamDSNWith(ca, ""), "verify-full", "disable", 1), "sslmode=verify-full"},
		"sslmode prefer":              {strings.Replace(iamDSNWith(ca, ""), "verify-full", "prefer", 1), "sslmode=verify-full"},
		"sslmode require":             {strings.Replace(iamDSNWith(ca, ""), "verify-full", "require", 1), "sslmode=verify-full"},
		"sslmode verify-ca":           {strings.Replace(iamDSNWith(ca, ""), "verify-full", "verify-ca", 1), "sslmode=verify-full"},
		"sslmode missing":             {"postgres://u@writer.example.internal:5432/t?sslrootcert=" + url.QueryEscape(ca) + "&target_session_attrs=read-write", "sslmode=verify-full"},
		"sslrootcert system":          {"postgres://u@writer.example.internal:5432/t?sslmode=verify-full&sslrootcert=system&target_session_attrs=read-write", "forbids sslrootcert=system"},
		"sslrootcert relative":        {"postgres://u@writer.example.internal:5432/t?sslmode=verify-full&sslrootcert=ca.pem&target_session_attrs=read-write", "absolute path"},
		"sslrootcert missing file":    {"postgres://u@writer.example.internal:5432/t?sslmode=verify-full&sslrootcert=" + url.QueryEscape(filepath.Join(t.TempDir(), "nope.pem")) + "&target_session_attrs=read-write", "readable regular file"},
		"sslrootcert absent":          {"postgres://u@writer.example.internal:5432/t?sslmode=verify-full&target_session_attrs=read-write", "naming a CA bundle"},
		"target_session_attrs any":    {strings.Replace(iamDSNWith(ca, ""), "read-write", "any", 1), "target_session_attrs=read-write"},
		"target_session_attrs absent": {"postgres://u@writer.example.internal:5432/t?sslmode=verify-full&sslrootcert=" + url.QueryEscape(ca), "target_session_attrs=read-write"},
		"duplicate sslmode":           {iamDSNWith(ca, "&sslmode=disable"), "exactly once"},
		"password parameter":          {iamDSNWith(ca, "&password="+secret), "query parameter password"},
		"passfile parameter":          {iamDSNWith(ca, "&passfile=/tmp/"+secret), "query parameter passfile"},
		"service parameter":           {iamDSNWith(ca, "&service="+secret), "query parameter service"},
		"servicefile parameter":       {iamDSNWith(ca, "&servicefile=/tmp/x"), "query parameter servicefile"},
		"sslcert parameter":           {iamDSNWith(ca, "&sslcert=/tmp/c.pem"), "query parameter sslcert"},
		"connect_timeout":             {iamDSNWith(ca, "&connect_timeout=5"), "query parameter connect_timeout"},
		"unknown key is a secret":     {iamDSNWith(ca, "&"+secret+"=1"), "name not shown"},
		"unparseable URL":             {"postgres://u:" + secret + "@[::1", "URL-form"},
	} {
		_, err := validateIAMDSN("DSN", c.dsn)
		if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want refusal containing %q", name, err, c.want)
		}
		noLeak(t, name, err)
	}
	_, err := validateIAMDSN("DSN", iamDSNWith(ca, "&"+secret+"=1"))
	if err == nil || !strings.Contains(err.Error(), "name not shown") {
		t.Errorf("unknown key must be refused without its name: %v", err)
	}
	_, err = validateIAMDSN("DSN", iamDSNWith(ca, "&service=x"))
	if err == nil || !strings.Contains(err.Error(), "query parameter service") {
		t.Errorf("a known forbidden key should be named: %v", err)
	}
}

// B1, R1, R3: the parsed pgx configuration must equal the raw string.
func TestValidateIAMParsed_Invariants(t *testing.T) {
	clearPGEnv(t)
	ca := writeCA(t, t.TempDir())
	dsn := iamDSNWith(ca, "")
	want, err := validateIAMDSN("DSN", dsn)
	if err != nil {
		t.Fatal(err)
	}
	good, err := fence.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateIAMParsed("DSN", want, good); err != nil {
		t.Fatalf("valid parsed configuration refused: %v", err)
	}
	for name, mutate := range map[string]func(c *pgconn.Config){
		"password present": func(c *pgconn.Config) { c.Password = secret },
		"host differs":     func(c *pgconn.Config) { c.Host = "other.example.internal" },
		"port differs":     func(c *pgconn.Config) { c.Port = 5433 },
		"user differs":     func(c *pgconn.Config) { c.User = "other" },
		"database differs": func(c *pgconn.Config) { c.Database = "other" },
		"fallback present": func(c *pgconn.Config) {
			c.Fallbacks = append(c.Fallbacks, &pgconn.FallbackConfig{Host: c.Host, Port: c.Port})
		},
		"no TLS":               func(c *pgconn.Config) { c.TLSConfig = nil },
		"insecure skip verify": func(c *pgconn.Config) { c.TLSConfig.InsecureSkipVerify = true },
		"custom verify (verify-ca)": func(c *pgconn.Config) {
			c.TLSConfig.VerifyPeerCertificate = func([][]byte, [][]*x509.Certificate) error { return nil }
		},
		"server name differs": func(c *pgconn.Config) { c.TLSConfig.ServerName = "x" },
		"no explicit CA":      func(c *pgconn.Config) { c.TLSConfig.RootCAs = nil },
		"client certificate":  func(c *pgconn.Config) { c.TLSConfig.Certificates = []tls.Certificate{{}} },
		"no read-write check": func(c *pgconn.Config) { c.ValidateConnect = nil },
	} {
		c := good.Copy()
		mutate(&c.Config)
		err := validateIAMParsed("DSN", want, c)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: %v, want refusal", name, err)
		}
		noLeak(t, name, err)
	}
}

// Defence in depth: a PG* variable present in the REAL process environment but
// missing from the lookup is still caught, by the parsed-configuration check.
func TestLoadFence_RealEnvironmentInterferenceIsCaughtAfterParsing(t *testing.T) {
	clearPGEnv(t)
	m, _ := iamEnv(t)
	t.Setenv("PGPASSWORD", secret)
	_, err := LoadFence(prefix, env(m)) // the lookup map does not contain PGPASSWORD
	if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "password reached the parsed configuration") {
		t.Fatalf("err = %v", err)
	}
	noLeak(t, "PGPASSWORD", err)
}

// defaultIAMCredentials is the production adapter, restored after tests that
// replace it.
var defaultIAMCredentials = iamCredentials

// R6: without an adapter, a valid IAM configuration stops startup; it never
// falls back to password authentication.
func TestWireFence_IAMWithoutAnAdapterRefusesToStart(t *testing.T) {
	clearPGEnv(t)
	iamCredentials = nil
	t.Cleanup(func() { iamCredentials = defaultIAMCredentials })
	m, _ := iamEnv(t)
	svc, h, _ := newService(t)
	f, err := WireFence(svc, h, prefix, env(m), quiet)
	if !errors.Is(err, ErrIAMAdapterUnavailable) || f != nil {
		t.Fatalf("= %v, %v", f, err)
	}
}

type countingCreds struct {
	n       atomic.Int64
	mu      sync.Mutex
	regions []string
}

// Password counts the request and then fails, so the fence never dials (and
// never resolves the test writer host).
func (c *countingCreds) Password(context.Context, string, uint16, string) (string, error) {
	c.n.Add(1)
	return "", errors.New("test source: no token " + secret)
}

// With an adapter (a fake here), the fence dials with the credential source —
// never with a stored password — and stays unready without a database.
func TestWireFence_IAMUsesTheCredentialSource(t *testing.T) {
	clearPGEnv(t)
	creds := &countingCreds{}
	iamCredentials = func(id rdsauth.Identity) (fence.Credentials, error) {
		creds.mu.Lock()
		creds.regions = append(creds.regions, id.Region)
		creds.mu.Unlock()
		return creds, nil
	}
	t.Cleanup(func() { iamCredentials = defaultIAMCredentials })

	m, _ := iamEnv(t)
	svc, h, _ := newService(t)
	f, err := WireFence(svc, h, prefix, env(m), quiet)
	if err != nil || f == nil {
		t.Fatalf("= %v, %v", f, err)
	}
	creds.mu.Lock()
	if len(creds.regions) != 1 || creds.regions[0] != "ap-south-1" {
		t.Errorf("adapter built for regions %v; want the explicit FENCE_AWS_REGION", creds.regions)
	}
	creds.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for creds.n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if creds.n.Load() == 0 {
		t.Error("the fence never asked the credential source for a token")
	}
	if code, body := readiness(h); code != http.StatusServiceUnavailable || strings.Contains(body, secret) {
		t.Errorf("readiness = %d (body leaks: %v)", code, strings.Contains(body, secret))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("service did not shut down")
	}
}

func TestWireFence_IAMAdapterConstructionFailureIsWithheld(t *testing.T) {
	clearPGEnv(t)
	iamCredentials = func(rdsauth.Identity) (fence.Credentials, error) {
		return nil, errors.New("sts: arn:aws:iam::123456789012:role/x " + secret)
	}
	t.Cleanup(func() { iamCredentials = defaultIAMCredentials })
	m, _ := iamEnv(t)
	svc, h, _ := newService(t)
	_, err := WireFence(svc, h, prefix, env(m), quiet)
	if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), "arn:aws") {
		t.Fatalf("err = %v", err)
	}
	noLeak(t, "construction", err)
}

// The real adapter core, wired: the identity reaches it, every dial asks for a
// new token, a failing token source keeps the service unready (never a
// password fallback), and no secret reaches readiness output or logs.
func TestWireFence_IAMWithTheRDSAuthSourceFailsClosedWithoutLeaking(t *testing.T) {
	clearPGEnv(t)
	var builds atomic.Int64
	var got rdsauth.Identity
	iamCredentials = func(id rdsauth.Identity) (fence.Credentials, error) {
		got = id
		return rdsauth.New(id, rdsauth.TokenBuilderFunc(func(_ context.Context, endpoint, region, user string) (string, error) {
			builds.Add(1)
			if endpoint != writerHost+":5432" || region != "ap-south-1" || user != "fence_iam" {
				t.Errorf("token inputs = %q %q %q", endpoint, region, user)
			}
			return "", errors.New("operation error STS: AssumeRoleWithWebIdentity, arn:aws:iam::123456789012:role/x " + secret)
		}))
	}
	t.Cleanup(func() { iamCredentials = defaultIAMCredentials })

	m, _ := iamEnv(t)
	var logs strings.Builder
	var logMu sync.Mutex
	log := slog.New(slog.NewTextHandler(lockedWriter{&logMu, &logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, h, _ := newService(t)
	f, err := WireFence(svc, h, prefix, env(m), log)
	if err != nil || f == nil {
		t.Fatalf("= %v, %v", f, err)
	}
	if got.Region != "ap-south-1" || got.RoleARN != m["AWS_ROLE_ARN"] || got.WebIdentityTokenFile != m["AWS_WEB_IDENTITY_TOKEN_FILE"] {
		t.Errorf("identity = %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for builds.Load() < 2 && time.Now().Before(deadline) {
		if code, _ := readiness(h); code == http.StatusOK {
			t.Fatal("ready without a token")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if builds.Load() < 2 {
		t.Errorf("token builds = %d; each re-dial must build a new token", builds.Load())
	}
	if f.Owned() {
		t.Error("owner without a token")
	}
	code, body := readiness(h)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("service did not shut down")
	}
	if code != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d", code)
	}
	logMu.Lock()
	out := logs.String()
	logMu.Unlock()
	for _, s := range []string{secret, "arn:aws", "AssumeRoleWithWebIdentity", m["AWS_WEB_IDENTITY_TOKEN_FILE"]} {
		if strings.Contains(body, s) || strings.Contains(out, s) {
			t.Errorf("%q leaked into readiness or logs", s)
		}
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// This build has the AWS adapter, and constructing it during startup makes
// no AWS call (R6): the fence is wired, but nothing has dialled yet.
func TestWireFence_IAMBuildsTheAWSAdapterWithoutContactingAWS(t *testing.T) {
	clearPGEnv(t)
	if iamCredentials == nil {
		t.Fatal("this build must have the AWS IAM adapter")
	}
	m, _ := iamEnv(t)
	var built atomic.Bool
	iamCredentials = func(id rdsauth.Identity) (fence.Credentials, error) {
		built.Store(true)
		return defaultIAMCredentials(id)
	}
	t.Cleanup(func() { iamCredentials = defaultIAMCredentials })
	svc, h, _ := newService(t)
	f, err := WireFence(svc, h, prefix, env(m), quiet)
	if err != nil || f == nil || !built.Load() {
		t.Fatalf("= %v, %v (adapter built: %v)", f, err, built.Load())
	}
	if f.Owned() {
		t.Error("owner before any dial")
	}
	if code, _ := readiness(h); code != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d before any dial", code)
	}
}

// C1/C3: only the Aurora cluster writer endpoint in the configured region.
func TestLoadFence_IAMRequiresTheWriterClusterEndpoint(t *testing.T) {
	clearPGEnv(t)
	for host, ok := range map[string]bool{
		writerHost: true,
		"TELEPHONY.CLUSTER-ABC123DEF456.AP-SOUTH-1.RDS.AMAZONAWS.COM":      true,
		"telephony.cluster-ro-abc123def456.ap-south-1.rds.amazonaws.com":   false, // reader
		"custom.cluster-custom-abc123def456.ap-south-1.rds.amazonaws.com":  false, // custom endpoint
		"fence.proxy-abc123def456.ap-south-1.rds.amazonaws.com":            false, // RDS Proxy
		"telephony-instance-1.abc123def456.ap-south-1.rds.amazonaws.com":   false, // instance
		"telephony.cluster-abc123def456.ap-south-2.rds.amazonaws.com":      false, // other region
		"telephony.cluster-abc123def456.ap-south-1.rds.amazonaws.com.evil": false,
		"db.example.internal": false, // CNAME / private name
		"10.0.0.5":            false,
		"a.b.cluster-abc123def456.ap-south-1.rds.amazonaws.com": false,
		".cluster-abc123def456.ap-south-1.rds.amazonaws.com":    false,
		"telephony.cluster-.ap-south-1.rds.amazonaws.com":       false,
	} {
		m, ca := iamEnv(t)
		m[prefix+"_DATABASE_DSN_FILE"] = dsnFile(t, iamDSNFor(host, ca, ""))
		_, err := LoadFence(prefix, env(m))
		switch {
		case ok && err != nil:
			t.Errorf("%s refused: %v", host, err)
		case !ok && (!errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "WRITER endpoint")):
			t.Errorf("%s: %v, want the writer-endpoint refusal", host, err)
		case !ok && strings.Contains(err.Error(), host):
			t.Errorf("%s: error echoes the host", host)
		}
	}
}
