package fence

import (
	"context"
	"fmt"
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
	// Under load the fence can fail closed and acquire again; each further
	// acquisition needs a loss before it, and every loss must be a fail-closed
	// timing one. Losses are read after acquisitions, so none can be missed.
	lost := append(l.records(t, msgLost), l.records(t, msgLate)...)
	if len(acq) == 0 || len(acq)-1 > len(lost) {
		t.Fatalf("acquisition records = %d after %d losses", len(acq), len(lost))
	}
	for _, r := range lost {
		ev := fenceEvent{msg: fmt.Sprint(r["msg"]), err: fmt.Sprint(r["error"]), lockMissing: r["lock_missing"] == true}
		if p, ok := r["phase"].(map[string]any); ok {
			if ns, ok := p[PhaseHeartbeat].(float64); ok {
				ev.heartbeat = time.Duration(ns)
			}
		}
		if !ev.failClosedTiming(c.HeartbeatTimeout) {
			t.Errorf("an ownership loss that is not a fail-closed timing loss: %v", r)
		}
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
