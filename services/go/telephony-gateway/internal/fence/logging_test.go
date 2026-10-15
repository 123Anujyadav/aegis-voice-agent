package fence

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/platform"
	"github.com/jackc/pgx/v5"
)

// P1-43 (F-8): the timing fields must survive the gateway's REAL logger —
// platform.LoadConfig → Validate → platform.NewLogger, exactly as
// cmd/server/main.go builds it, including the RedactingHandler — not just a
// plain slog handler. The redactor replaces any key containing a deny-listed
// substring (for example "credential") with "[REDACTED]".

// gatewayEnvPrefix is cmd/server/main.go's envPrefix (package main cannot be
// imported).
const gatewayEnvPrefix = "CS_TELEPHONY_GATEWAY"

// gatewayLog returns a capture and the gateway's production logger writing
// into it. It sets environment variables, so callers must not be parallel.
func gatewayLog(t *testing.T, environment string) (*jsonLog, *slog.Logger) {
	t.Helper()
	t.Setenv(gatewayEnvPrefix+"_ENVIRONMENT", environment)
	t.Setenv(gatewayEnvPrefix+"_LOG_LEVEL", "debug") // the P1-42 measurement window
	t.Setenv(gatewayEnvPrefix+"_LOG_FORMAT", "json")
	var c platform.ServiceConfig
	if err := platform.LoadConfig(&c, gatewayEnvPrefix); err != nil {
		t.Fatal(err)
	}
	c.Name, c.Version = "telephony-gateway", "test"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	l := &jsonLog{}
	w := writerFunc(func(p []byte) (int, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.buf.Write(p)
	})
	return l, platform.NewLogger(c, w, make([]byte, 32))
}

var gatewayEnvironments = []string{"staging", "production"}

// Every phase field and the STS outcome keep their numeric (nanosecond)
// values through the real logger, also inside an enclosing group, while
// sensitive keys logged next to them are still redacted.
func TestGatewayLogger_TimingFieldsSurviveRedaction(t *testing.T) {
	for _, env := range gatewayEnvironments {
		t.Run(env, func(t *testing.T) {
			l, log := gatewayLog(t, env)
			tm := NewTimings()
			want := map[string]time.Duration{}
			for i, p := range phaseOrder {
				d := time.Duration(i+1)*time.Millisecond + time.Duration(i+1) // distinct, sub-ms precision
				tm.set(p, d)
				want[p] = d
			}
			RecordSTSCredentials(WithTimings(context.Background(), tm), STSCredentialsRefreshed)

			log.Info("fence: lock acquired; owner after the grace period", slog.Duration("grace", time.Second), tm.attr(),
				slog.String("password", "SECRETMARKER-password"),
				slog.Group("probe", slog.String("token", "TOKEN-MARKER-1"),
					slog.Group("inner", slog.String("session_token", "TOKEN-MARKER-2"), slog.Duration("tcp", 5))))
			log.WithGroup("outer").Info("nested probe", tm.attr())

			check := func(what string, holder map[string]any) {
				g, _ := holder["phase"].(map[string]any)
				if strings.Contains(strings.Join(values(g), " "), "[REDACTED]") {
					t.Errorf("%s: a timing field was redacted: %v", what, g)
				}
				got := phases(t, holder)
				for p, d := range want {
					if got[p] != d {
						t.Errorf("%s: %s = %v, want %v (integer nanoseconds)", what, p, got[p], d)
					}
				}
				if g[FieldSTSOutcome] != string(STSCredentialsRefreshed) {
					t.Errorf("%s: %s = %v", what, FieldSTSOutcome, g[FieldSTSOutcome])
				}
			}
			recs := l.records(t, "lock acquired")
			if len(recs) != 1 {
				t.Fatalf("records = %d\n%s", len(recs), l.raw())
			}
			rec := recs[0]
			check("top level", rec)
			nested := l.records(t, "nested probe")
			if len(nested) != 1 {
				t.Fatalf("nested records = %d", len(nested))
			}
			outer, _ := nested[0]["outer"].(map[string]any)
			check("inside a group", outer)

			// The redactor still works on everything else.
			if rec["password"] != "[REDACTED]" {
				t.Errorf("password = %v", rec["password"])
			}
			probe, _ := rec["probe"].(map[string]any)
			inner, _ := probe["inner"].(map[string]any)
			if probe["token"] != "[REDACTED]" || inner["session_token"] != "[REDACTED]" {
				t.Errorf("nested sensitive keys not redacted: %v", probe)
			}
			if inner["tcp"] != float64(5) {
				t.Errorf("nested probe tcp = %v", inner["tcp"])
			}
			noLeaks(t, l.raw(), "TOKEN-MARKER-1", "TOKEN-MARKER-2")
		})
	}
}

func values(m map[string]any) []string {
	var out []string
	for _, v := range m {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// partialSource fails a credential preparation after recording only what it
// reached (as rdsauth does on a failed refresh, or a source that records
// nothing).
type partialSource struct {
	record func(ctx context.Context)
	err    error
}

func (s partialSource) Password(ctx context.Context, _ string, _ uint16, _ string) (string, error) {
	s.record(ctx)
	return "", s.err
}

// Through a running fence and the real logger: a failed preparation logs the
// phases it reached (numeric), omits the ones it did not, keeps the vetted
// reason and leaks neither the source's text, the host nor the user.
func TestGatewayLogger_FailedPreparationKeepsReachedPhasesOnly(t *testing.T) {
	for name, c := range map[string]struct {
		src         partialSource
		present     []string
		outcome     any // nil: the field must be absent
		errContains string
	}{
		"failed STS refresh": {
			src: partialSource{
				record: func(ctx context.Context) {
					RecordCredentialPhase(ctx, PhaseCredentialsSTS, 3*time.Millisecond)
					RecordSTSCredentials(ctx, STSCredentialsRefreshFailed)
				},
				err: reasonErr{reason: "sts_access_denied", text: secretText},
			},
			present:     []string{PhaseCredentials, PhaseCredentialsSTS},
			outcome:     string(STSCredentialsRefreshFailed),
			errContains: "sts_access_denied",
		},
		"source records nothing": {
			src:         partialSource{record: func(context.Context) {}, err: errors.New(secretText)},
			present:     []string{PhaseCredentials},
			errContains: "details withheld",
		},
	} {
		t.Run(name, func(t *testing.T) {
			l, log := gatewayLog(t, "production")
			cc, err := ParseDSN("postgres://userMARKER@hostMARKER.invalid:5432/dbMARKER")
			if err != nil {
				t.Fatal(err)
			}
			dial := credentialDialer(cc, c.src, func(context.Context, *pgx.ConnConfig) (Session, error) {
				t.Error("connected after a failed credential preparation")
				return nil, errors.New("unreachable")
			})
			f, err := New(cfg, dial, log)
			if err != nil {
				t.Fatal(err)
			}
			go func() { _ = f.Run(context.Background()) }()
			t.Cleanup(func() { _ = f.Shutdown(context.Background()) })
			eventually(t, "a dial-failure record", func() bool {
				return strings.Contains(l.raw(), "cannot open a database session")
			})
			rec := l.records(t, "cannot open a database session")[0]
			got := phases(t, rec)
			absent := []string{
				PhaseCredentialsSign, PhaseDNS, PhaseTCP, PhaseTLS, PhaseAuth, PhaseSessionCheck,
				PhaseConnect, PhasePrimary, PhaseLock, PhaseHeartbeat,
			}
			if len(c.present) == 1 {
				absent = append(absent, PhaseCredentialsSTS)
			}
			wantPhases(t, name, got, c.present, absent)
			g, _ := rec["phase"].(map[string]any)
			if o, ok := g[FieldSTSOutcome]; (c.outcome == nil && ok) || (c.outcome != nil && o != c.outcome) {
				t.Errorf("%s = %v (present %v), want %v", FieldSTSOutcome, o, ok, c.outcome)
			}
			if e, _ := rec["error"].(string); !strings.Contains(e, c.errContains) {
				t.Errorf("error = %q, want it to contain %q", e, c.errContains)
			}
			noLeaks(t, l.raw(), "userMARKER", "hostMARKER", "dbMARKER")
		})
	}
}
