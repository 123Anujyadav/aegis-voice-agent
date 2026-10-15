package rdsauth

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/callscreen/callscreen-platform/services/go/telephony-gateway/internal/fence"
)

// P1-43 (F-8), end to end: a running fence with the AWS source and the
// gateway's REAL logger (platform.LoadConfig → Validate → platform.NewLogger,
// as cmd/server/main.go builds it, RedactingHandler included). The first dial
// refreshes the STS credentials, the next reuses them; both log numeric
// sub-phases and the right outcome, and nothing sensitive.
func TestAWS_SubPhasesSurviveTheGatewayLogger(t *testing.T) {
	const prefix = "CS_TELEPHONY_GATEWAY" // cmd/server/main.go envPrefix
	t.Setenv(prefix+"_ENVIRONMENT", "production")
	t.Setenv(prefix+"_LOG_LEVEL", "debug")
	t.Setenv(prefix+"_LOG_FORMAT", "json")
	var c platform.ServiceConfig
	if err := platform.LoadConfig(&c, prefix); err != nil {
		t.Fatal(err)
	}
	c.Name, c.Version = "telephony-gateway", "test"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var buf strings.Builder
	log := platform.NewLogger(c, lockedWriter{&mu, &buf}, make([]byte, 32))
	read := func() string { mu.Lock(); defer mu.Unlock(); return buf.String() }

	f := newFakeSTS(t, reply(200, stsOK(time.Now().Add(time.Hour))))
	id := identityWithTokenFile(t)
	s := f.source(t, id)
	cfg, err := fence.ParseDSN("postgres://fence_iam@127.0.0.1:1/telephony?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	dial, _ := fence.PgxCredentialDialer(cfg, s)
	fc, err := fence.New(fence.Config{LockKey: 7, HeartbeatInterval: 300 * time.Millisecond, HeartbeatTimeout: 200 * time.Millisecond}, dial, log) // TEST values, not D10
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = fc.Run(context.Background()) }()
	t.Cleanup(func() { _ = fc.Shutdown(context.Background()) })

	// Wait for dials that obtained credentials: on a loaded host earlier
	// dials may end at the 200 ms test bound before preparation completes
	// (checkDialRecords enforces what such records may and may not contain;
	// P1-46, F-9). Of the credentialed dials, at most one refreshed; the
	// rest reused the cache.
	got := awaitCredentialedDials(t, read, 2)
	if got[1][fence.FieldSTSOutcome] != string(fence.STSCredentialsReused) {
		t.Errorf("second credentialed dial: %s = %v, want %s", fence.FieldSTSOutcome, got[1][fence.FieldSTSOutcome], fence.STSCredentialsReused)
	}
	// Once the cache holds credentials, dials make no STS request.
	filled := f.requests.Load()
	if filled < 1 {
		t.Error("no STS request was made")
	}
	awaitCredentialedDials(t, read, 3)
	if n := f.requests.Load(); n != filled {
		t.Errorf("STS requests rose from %d to %d after the cache was filled", filled, n)
	}
	out := read()
	if strings.Contains(out, "[REDACTED]") {
		t.Errorf("a field was redacted:\n%s", out)
	}
	for _, leak := range []string{
		stsKeyID, stsSecret, stsSession, secret, "web-identity-jwt", id.RoleARN, id.WebIdentityTokenFile,
		SessionName, "127.0.0.1", "fence_iam", "X-Amz", "Action=connect",
	} {
		if strings.Contains(out, leak) {
			t.Errorf("log contains %q", leak)
		}
	}
}
