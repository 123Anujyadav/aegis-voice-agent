package fence

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// P1-41 §8 — phase timing against a REAL disposable PostgreSQL (SKIP without
// FenceTestDSNEnv): a real acquisition logs the connect phases and the query
// phases, heartbeats log their round trip, and no connection detail appears.
func TestPostgresFence_PhaseTimingOnARealAcquisition(t *testing.T) {
	dsn := testDSN(t)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	l := &jsonLog{}
	c := testCfg
	c.LockKey = uniqueKey()
	f, err := New(c, PgxDialer(connCfg(t, dsn)), l.logger())
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = f.Run(context.Background()) }()
	t.Cleanup(func() { _ = f.Shutdown(context.Background()) })

	waitFor(t, 10*time.Second, "ownership", f.Owned)
	waitFor(t, 5*time.Second, "heartbeat records", func() bool { return len(l.records(t, "ownership confirmed")) >= 2 })
	acq := l.records(t, "lock acquired")
	if len(acq) != 1 {
		t.Fatalf("acquisition records = %d", len(acq))
	}
	// Password mode over sslmode=disable: no credentials phase, no TLS.
	wantPhases(t, "real acquisition", phases(t, acq[0]),
		[]string{PhaseDNS, PhaseTCP, PhaseAuth, PhaseConnect, PhasePrimary, PhaseLock},
		[]string{PhaseCredentials, PhaseTLS, PhaseHeartbeat})
	for _, r := range l.records(t, "ownership confirmed") {
		wantPhases(t, "real heartbeat", phases(t, r), []string{PhaseHeartbeat}, nil)
	}
	noLeaks(t, l.raw(), pw, u.Hostname()+":"+u.Port(), "user="+u.User.Username(), strconv.Quote(u.Path))
}
