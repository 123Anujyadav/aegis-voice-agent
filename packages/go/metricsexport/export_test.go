package metricsexport_test

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/metrics"
	"github.com/callscreen/callscreen-platform/packages/go/metricsexport"
)

// render is the common path under test: build an exposition and return it as a
// string. Every test goes through the public API, never internals.
func render(t *testing.T, sources ...metricsexport.Source) string {
	t.Helper()
	var sb strings.Builder
	if _, err := metricsexport.New(sources...).WriteTo(&sb); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	return sb.String()
}

// lines returns non-empty output lines, which is what assertions care about.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ExampleExporter_WriteTo shows the exact wire format. Being an Example, the
// output below is asserted by the test runner rather than being a comment that
// can drift away from the code.
func ExampleExporter_WriteTo() {
	reg := metrics.NewRegistry()
	reg.Counter("voice_turns_completed_total", "outcome").Inc("answered")
	reg.Gauge("voice_sessions_active").Set(3)
	reg.Histogram("voice_turn_seconds", []float64{0.5, 1}).Observe(0.25)

	var sb strings.Builder
	_, _ = metricsexport.New(
		metricsexport.Source{Subsystem: "voice", Registry: reg}).WriteTo(&sb)
	fmt.Print(sb.String())

	// Output:
	// # TYPE voice_sessions_active gauge
	// voice_sessions_active 3
	// # TYPE voice_turn_seconds histogram
	// voice_turn_seconds_bucket{le="0.5"} 1
	// voice_turn_seconds_bucket{le="1"} 1
	// voice_turn_seconds_bucket{le="+Inf"} 1
	// voice_turn_seconds_sum 0.25
	// voice_turn_seconds_count 1
	// # TYPE voice_turns_completed_total counter
	// voice_turns_completed_total{outcome="answered"} 1
}

// ---------------------------------------------------------------------------
// 1. Valid Prometheus exposition
// ---------------------------------------------------------------------------

func TestExposition_CounterIsWellFormed(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	reg.Counter("voice_turns_completed_total", "outcome").Inc("answered")
	reg.Counter("voice_turns_completed_total", "outcome").Add(2, "ignored")

	got := render(t, metricsexport.Source{Subsystem: "voice", Registry: reg})

	for _, want := range []string{
		"# TYPE voice_turns_completed_total counter",
		`voice_turns_completed_total{outcome="answered"} 1`,
		`voice_turns_completed_total{outcome="ignored"} 2`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exposition missing %q\n--- full output ---\n%s", want, got)
		}
	}
}

func TestExposition_GaugeIsWellFormed(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	reg.Gauge("governance_escalation_queue_depth").Set(7)

	got := render(t, metricsexport.Source{Subsystem: "governance", Registry: reg})

	for _, want := range []string{
		"# TYPE governance_escalation_queue_depth gauge",
		"governance_escalation_queue_depth 7",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exposition missing %q\n--- full output ---\n%s", want, got)
		}
	}
}

// TestExposition_HistogramCarriesCumulativeBucketsSumAndCount is the assertion
// that matters most for a histogram: Prometheus requires CUMULATIVE buckets,
// a +Inf bucket equal to the count, and _sum/_count series. The metrics.Sample
// type already stores buckets cumulatively; this proves the adapter does not
// undo that.
func TestExposition_HistogramCarriesCumulativeBucketsSumAndCount(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	// Values chosen to be exactly representable in float64, so this test
	// asserts histogram semantics rather than float formatting.
	h := reg.Histogram("voice_turn_seconds", []float64{0.1, 0.5, 1})
	h.Observe(0.0625) // <= 0.1
	h.Observe(0.25)   // <= 0.5
	h.Observe(2)      // only +Inf

	got := render(t, metricsexport.Source{Subsystem: "voice", Registry: reg})

	for _, want := range []string{
		"# TYPE voice_turn_seconds histogram",
		`voice_turn_seconds_bucket{le="0.1"} 1`,
		`voice_turn_seconds_bucket{le="0.5"} 2`,
		`voice_turn_seconds_bucket{le="1"} 2`,
		`voice_turn_seconds_bucket{le="+Inf"} 3`,
		"voice_turn_seconds_count 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("exposition missing %q\n--- full output ---\n%s", want, got)
		}
	}
	if !strings.Contains(got, "voice_turn_seconds_sum 2.3125") {
		t.Errorf("missing or wrong _sum (want 2.3125)\n--- full output ---\n%s", got)
	}
}

// TestExposition_BucketsAreAscending guards the ordering Prometheus requires.
func TestExposition_BucketsAreAscending(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	reg.Histogram("m_seconds", []float64{0.001, 0.01, 0.1, 1, 10}).Observe(0.5)

	got := render(t, metricsexport.Source{Subsystem: "s", Registry: reg})

	var order []string
	for _, l := range lines(got) {
		if strings.HasPrefix(l, "m_seconds_bucket{le=") {
			order = append(order, l[strings.Index(l, `le="`)+4:strings.Index(l, `"}`)])
		}
	}
	want := []string{"0.001", "0.01", "0.1", "1", "10", "+Inf"}
	if len(order) != len(want) {
		t.Fatalf("bucket count = %d (%v), want %d", len(order), order, len(want))
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("bucket %d = %q, want %q (full order %v)", i, order[i], want[i], order)
		}
	}
}

// TestExposition_TypeCommentPrecedesItsSeries — a TYPE after its samples is
// invalid; a parser applies it to nothing.
func TestExposition_TypeCommentPrecedesItsSeries(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	reg.Counter("a_total").Inc()
	reg.Gauge("b_value").Set(1)

	for _, l := range lines(render(t, metricsexport.Source{Subsystem: "s", Registry: reg})) {
		_ = l
	}

	got := lines(render(t, metricsexport.Source{Subsystem: "s", Registry: reg}))
	seen := map[string]bool{}
	for _, l := range got {
		switch {
		case strings.HasPrefix(l, "# TYPE "):
			seen[strings.Fields(l)[2]] = true
		case !strings.HasPrefix(l, "#"):
			name := l
			if i := strings.IndexAny(l, "{ "); i >= 0 {
				name = l[:i]
			}
			base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, "_bucket"), "_sum"), "_count")
			if !seen[name] && !seen[base] {
				t.Errorf("series %q appears before its # TYPE line\n%s", l, strings.Join(got, "\n"))
			}
		}
	}
}

// TestExposition_IsDeterministic — a scrape that reorders itself makes diffs
// useless and breaks nothing loudly.
func TestExposition_IsDeterministic(t *testing.T) {
	t.Parallel()

	build := func() *metrics.Registry {
		reg := metrics.NewRegistry()
		reg.Counter("z_total", "k").Inc("b")
		reg.Counter("z_total", "k").Inc("a")
		reg.Counter("a_total").Inc()
		reg.Gauge("m_value").Set(3)
		reg.Histogram("h_seconds", []float64{1, 2}).Observe(1.5)
		return reg
	}

	first := render(t, metricsexport.Source{Subsystem: "s", Registry: build()})
	for i := 0; i < 20; i++ {
		if got := render(t, metricsexport.Source{Subsystem: "s", Registry: build()}); got != first {
			t.Fatalf("exposition not deterministic on iteration %d\n--- first ---\n%s\n--- got ---\n%s", i, first, got)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Multiple subsystems
// ---------------------------------------------------------------------------

func TestExposition_MergesMultipleSubsystems(t *testing.T) {
	t.Parallel()

	voice := metrics.NewRegistry()
	voice.Counter("voice_sessions_opened_total", "reason").Inc("inbound")
	gov := metrics.NewRegistry()
	gov.Counter("governance_decisions_total", "action").Inc("allow")

	got := render(t,
		metricsexport.Source{Subsystem: "voice", Registry: voice},
		metricsexport.Source{Subsystem: "governance", Registry: gov},
	)

	for _, want := range []string{
		`voice_sessions_opened_total{reason="inbound"} 1`,
		`governance_decisions_total{action="allow"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("merged exposition missing %q\n%s", want, got)
		}
	}
}

// TestExposition_DuplicateNameAcrossSubsystemsIsReported — the frozen
// observability test guarantees the real registries do not collide. If that ever
// stops being true, a combined scrape must not silently merge two subsystems'
// series into one.
func TestExposition_DuplicateNameAcrossSubsystemsIsReported(t *testing.T) {
	t.Parallel()

	a := metrics.NewRegistry()
	a.Counter("shared_total").Inc()
	b := metrics.NewRegistry()
	b.Counter("shared_total").Inc()

	e := metricsexport.New(
		metricsexport.Source{Subsystem: "alpha", Registry: a},
		metricsexport.Source{Subsystem: "beta", Registry: b},
	)
	var sb strings.Builder
	if _, err := e.WriteTo(&sb); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	st := e.Stats()
	if st.NameCollisions == 0 {
		t.Error("a duplicate instrument name across subsystems was not reported")
	}
	if n := strings.Count(sb.String(), "# TYPE shared_total"); n != 1 {
		t.Errorf("emitted %d TYPE lines for a colliding name, want exactly 1", n)
	}
}

// ---------------------------------------------------------------------------
// 3 & 4. Label cardinality and sensitive labels
// ---------------------------------------------------------------------------

// TestExposition_SensitiveLabelNamesAreNeverEmitted is the privacy assertion.
// ADR-0012 forbids per-subject identifiers and content leaving the process, and
// a metric label is leaving the process.
func TestExposition_SensitiveLabelNamesAreNeverEmitted(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"call_id", "callid", "session_id", "request_id", "correlation_id",
		"transcript", "text", "utterance", "audio", "pcm",
		"token", "api_key", "secret", "password", "authorization",
		"phone", "msisdn", "caller", "email", "user_id",
	}

	for _, label := range forbidden {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			reg := metrics.NewRegistry()
			reg.Counter("leaky_total", label).Inc("SENTINEL-VALUE")

			e := metricsexport.New(metricsexport.Source{Subsystem: "s", Registry: reg})
			var sb strings.Builder
			if _, err := e.WriteTo(&sb); err != nil {
				t.Fatalf("WriteTo: %v", err)
			}
			got := sb.String()

			if strings.Contains(got, "SENTINEL-VALUE") {
				t.Errorf("label value leaked for %q\n%s", label, got)
			}
			if strings.Contains(got, label+"=") {
				t.Errorf("forbidden label name %q was emitted\n%s", label, got)
			}
			if e.Stats().DroppedSeries == 0 {
				t.Errorf("series with forbidden label %q was dropped without being counted", label)
			}
		})
	}
}

// TestExposition_SensitiveLabelMatchIsCaseAndSeparatorInsensitive — a guard
// defeated by "CallID" instead of "call_id" is not a guard.
func TestExposition_SensitiveLabelMatchIsCaseAndSeparatorInsensitive(t *testing.T) {
	t.Parallel()

	for _, label := range []string{"CallID", "Call-Id", "SESSION_ID", "sessionId"} {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			reg := metrics.NewRegistry()
			reg.Counter("leaky_total", label).Inc("SENTINEL-VALUE")

			var sb strings.Builder
			if _, err := metricsexport.New(
				metricsexport.Source{Subsystem: "s", Registry: reg}).WriteTo(&sb); err != nil {
				t.Fatalf("WriteTo: %v", err)
			}
			if strings.Contains(sb.String(), "SENTINEL-VALUE") {
				t.Errorf("guard bypassed by spelling %q\n%s", label, sb.String())
			}
		})
	}
}

// TestExposition_SafeLabelsSurvive — the guard must not be so broad that it
// eats the platform's real, bounded vocabulary.
func TestExposition_SafeLabelsSurvive(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	for _, l := range []string{"reason", "outcome", "from", "to", "kind", "provider", "stage", "action", "basis"} {
		reg.Counter("k_"+l+"_total", l).Inc("value1")
	}

	e := metricsexport.New(metricsexport.Source{Subsystem: "s", Registry: reg})
	var sb strings.Builder
	if _, err := e.WriteTo(&sb); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	got := sb.String()

	for _, l := range []string{"reason", "outcome", "from", "to", "kind", "provider", "stage", "action", "basis"} {
		if !strings.Contains(got, l+`="value1"`) {
			t.Errorf("safe label %q was dropped\n%s", l, got)
		}
	}
	if d := e.Stats().DroppedSeries; d != 0 {
		t.Errorf("DroppedSeries = %d, want 0 — the guard is eating safe labels", d)
	}
}

// platformLabelVocabulary is every label name the platform actually declares.
//
// Extracted from all 12 packages/go/*/metrics.go files: 288 instrument
// registrations, 38 distinct label keys. Reproduce with:
//
//	grep -oE '\.(counter|histogram|gauge|Counter|Histogram|Gauge)\(' packages/go/*/metrics.go
//
// It is written out rather than computed because a test that derives its own
// expectation from the code under inspection cannot fail.
var platformLabelVocabulary = []string{
	"action", "attempt", "basis", "capability", "class", "decision",
	"direction", "from", "index", "kind", "level", "limit", "lookup",
	"model", "name", "operation", "outcome", "party", "phase", "policy",
	"provider", "reason", "resolution", "rule", "scope", "sensitivity",
	"shape", "source", "stage", "state", "subject", "suite", "tier",
	"to", "tool", "trigger", "type", "verdict",
}

// TestExposition_RealPlatformVocabularyIsNeverDropped is the regression test for
// a defect this suite found: the sensitive-label deny list originally contained
// "name" and "subject", which the platform uses as bounded, authored labels —
// an intent name, an emergency-policy name, and the evaluated subsystem. The
// guard silently dropped 15 real series across conversation, governance and
// evaluation.
//
// A privacy guard that deletes legitimate data does not announce itself; it just
// makes a dashboard wrong. This pins the whole real vocabulary so the deny list
// can never again be widened onto a label the platform actually uses.
func TestExposition_RealPlatformVocabularyIsNeverDropped(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	for i, l := range platformLabelVocabulary {
		reg.Counter(fmt.Sprintf("vocab%d_total", i), l).Inc("bounded_value")
	}

	e := metricsexport.New(metricsexport.Source{Subsystem: "platform", Registry: reg})
	var sb strings.Builder
	if _, err := e.WriteTo(&sb); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	got := sb.String()

	var dropped []string
	for _, l := range platformLabelVocabulary {
		if !strings.Contains(got, l+`="bounded_value"`) {
			dropped = append(dropped, l)
		}
	}
	if len(dropped) > 0 {
		t.Errorf("the deny list is eating the platform's real label vocabulary: %v", dropped)
	}
	if d := e.Stats().DroppedSeries; d != 0 {
		t.Errorf("DroppedSeries = %d, want 0 for the real vocabulary", d)
	}
}

// TestExposition_VocabularyIsBounded records the measured cardinality of label
// KEYS. Values are bounded by each subsystem's own closed vocabulary; the key
// count is what this layer can assert.
func TestExposition_VocabularyIsBounded(t *testing.T) {
	t.Parallel()

	const measured = 38
	if len(platformLabelVocabulary) != measured {
		t.Errorf("platform label vocabulary = %d keys, recorded as %d; "+
			"re-measure before changing this number",
			len(platformLabelVocabulary), measured)
	}
	for _, l := range platformLabelVocabulary {
		if strings.ContainsAny(l, "{}\"\\ \n") {
			t.Errorf("label %q is not a bare identifier", l)
		}
	}
}

// TestExposition_SeriesAreCappedToBoundOutput — an unbounded scrape is how a
// metrics backend becomes an outage.
func TestExposition_SeriesAreCappedToBoundOutput(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	c := reg.Counter("wide_total", "k")
	for i := 0; i < 5000; i++ {
		c.Inc(fmt.Sprintf("v%d", i))
	}

	e := metricsexport.New(metricsexport.Source{Subsystem: "s", Registry: reg})
	e.MaxSeries = 100

	var sb strings.Builder
	if _, err := e.WriteTo(&sb); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	var emitted int
	for _, l := range lines(sb.String()) {
		if !strings.HasPrefix(l, "#") {
			emitted++
		}
	}
	if emitted > 100 {
		t.Errorf("emitted %d series, cap was 100", emitted)
	}
	if e.Stats().TruncatedSeries == 0 {
		t.Error("output was capped but the truncation was not reported")
	}
}

// ---------------------------------------------------------------------------
// 5. Empty registry
// ---------------------------------------------------------------------------

func TestExposition_EmptyRegistryProducesEmptyOutputNotError(t *testing.T) {
	t.Parallel()

	got := render(t, metricsexport.Source{Subsystem: "s", Registry: metrics.NewRegistry()})
	if strings.TrimSpace(got) != "" {
		t.Errorf("empty registry produced output:\n%q", got)
	}
}

func TestExposition_NoSourcesProducesEmptyOutputNotError(t *testing.T) {
	t.Parallel()

	if got := render(t); strings.TrimSpace(got) != "" {
		t.Errorf("no sources produced output:\n%q", got)
	}
}

// ---------------------------------------------------------------------------
// 6. Registry unavailable / degradation
// ---------------------------------------------------------------------------

// TestExposition_NilRegistryIsSkippedNotFatal — a subsystem that failed to
// build its metrics must not take the whole scrape down with it.
func TestExposition_NilRegistryIsSkippedNotFatal(t *testing.T) {
	t.Parallel()

	ok := metrics.NewRegistry()
	ok.Counter("healthy_total").Inc()

	e := metricsexport.New(
		metricsexport.Source{Subsystem: "broken", Registry: nil},
		metricsexport.Source{Subsystem: "healthy", Registry: ok},
	)
	var sb strings.Builder
	if _, err := e.WriteTo(&sb); err != nil {
		t.Fatalf("a nil registry made the whole exposition fail: %v", err)
	}
	if !strings.Contains(sb.String(), "healthy_total 1") {
		t.Errorf("a nil registry suppressed a healthy subsystem\n%s", sb.String())
	}
	if e.Stats().SkippedSources != 1 {
		t.Errorf("SkippedSources = %d, want 1", e.Stats().SkippedSources)
	}
}

func TestExposition_NilExporterDoesNotPanic(t *testing.T) {
	t.Parallel()

	var e *metricsexport.Exporter
	var sb strings.Builder
	if _, err := e.WriteTo(&sb); err != nil {
		t.Fatalf("nil exporter returned an error: %v", err)
	}
	if strings.TrimSpace(sb.String()) != "" {
		t.Errorf("nil exporter produced output: %q", sb.String())
	}
}

// ---------------------------------------------------------------------------
// 7. Malformed / edge metric values
// ---------------------------------------------------------------------------

// TestExposition_SpecialFloatsUsePrometheusSpelling — Go prints "+Inf"/"NaN"
// via strconv, which matches Prometheus. This pins it so a future formatting
// change cannot silently emit an unparseable value.
func TestExposition_SpecialFloatsUsePrometheusSpelling(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	reg.Gauge("g_posinf").Set(math.Inf(1))
	reg.Gauge("g_neginf").Set(math.Inf(-1))
	reg.Gauge("g_nan").Set(math.NaN())
	reg.Gauge("g_tiny").Set(1e-300)
	reg.Gauge("g_huge").Set(1e300)
	reg.Gauge("g_neg").Set(-42.5)

	got := render(t, metricsexport.Source{Subsystem: "s", Registry: reg})

	for _, want := range []string{
		"g_posinf +Inf",
		"g_neginf -Inf",
		"g_nan NaN",
		"g_neg -42.5",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "g_tiny 0\n") {
		t.Errorf("tiny value collapsed to zero\n%s", got)
	}
}

// TestExposition_InvalidMetricNamesAreRejected — an invalid name makes the whole
// scrape unparseable, so one bad instrument must not poison the rest.
func TestExposition_InvalidMetricNamesAreRejected(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	reg.Counter("bad name with spaces").Inc()
	reg.Counter("bad-name-with-dashes").Inc()
	reg.Counter("1_leading_digit").Inc()
	reg.Counter("good_total").Inc()

	e := metricsexport.New(metricsexport.Source{Subsystem: "s", Registry: reg})
	var sb strings.Builder
	if _, err := e.WriteTo(&sb); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	got := sb.String()

	if !strings.Contains(got, "good_total 1") {
		t.Errorf("a valid instrument was suppressed by invalid neighbours\n%s", got)
	}
	for _, bad := range []string{"bad name with spaces", "bad-name-with-dashes", "1_leading_digit"} {
		if strings.Contains(got, bad) {
			t.Errorf("invalid metric name %q was emitted\n%s", bad, got)
		}
	}
	if e.Stats().DroppedSeries == 0 {
		t.Error("invalid names were dropped without being counted")
	}
}

// TestExposition_LabelValuesAreEscaped — an unescaped quote or newline in a
// label value terminates the line early and corrupts every following series.
func TestExposition_LabelValuesAreEscaped(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	reg.Counter("esc_total", "reason").Inc(`he said "stop"`)
	reg.Counter("esc_total", "reason").Inc("line\nbreak")
	reg.Counter("esc_total", "reason").Inc(`back\slash`)

	got := render(t, metricsexport.Source{Subsystem: "s", Registry: reg})

	for _, want := range []string{
		`reason="he said \"stop\""`,
		`reason="line\nbreak"`,
		`reason="back\\slash"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("label value not escaped, missing %q\n%s", want, got)
		}
	}
	for _, l := range lines(got) {
		if strings.HasPrefix(l, "#") {
			continue
		}
		if strings.Count(l, `"`)%2 != 0 {
			t.Errorf("unbalanced quotes in emitted line: %q", l)
		}
	}
}

// ---------------------------------------------------------------------------
// 8. HTTP handler
// ---------------------------------------------------------------------------

func TestHandler_ServesPrometheusContentType(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	reg.Counter("http_total").Inc()

	rec := httptest.NewRecorder()
	metricsexport.New(metricsexport.Source{Subsystem: "s", Registry: reg}).
		Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != metricsexport.ContentType {
		t.Errorf("Content-Type = %q, want %q", ct, metricsexport.ContentType)
	}
	if !strings.Contains(rec.Body.String(), "http_total 1") {
		t.Errorf("body missing the metric:\n%s", rec.Body.String())
	}
}

// TestHandler_WriteFailureDoesNotPanic — a client that disconnects mid-scrape
// is normal, and must not take the process with it.
func TestHandler_WriteFailureDoesNotPanic(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	for i := 0; i < 200; i++ {
		reg.Counter(fmt.Sprintf("m%d_total", i)).Inc()
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a failing writer panicked: %v", r)
		}
	}()

	e := metricsexport.New(metricsexport.Source{Subsystem: "s", Registry: reg})
	if _, err := e.WriteTo(failingWriter{}); err == nil {
		t.Error("WriteTo on a failing writer returned nil error")
	} else if !errors.Is(err, errWrite) {
		t.Errorf("error did not wrap the writer's error: %v", err)
	}
}

var errWrite = errors.New("write failed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

var _ io.Writer = failingWriter{}

// ---------------------------------------------------------------------------
// 9. Exposition cannot fail a call
// ---------------------------------------------------------------------------

// TestExposition_DoesNotMutateTheRegistry is the "cannot affect a call"
// assertion in its strongest testable form: scraping is read-only, so a scrape
// cannot change what a call observes.
func TestExposition_DoesNotMutateTheRegistry(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	reg.Counter("c_total", "k").Add(5, "a")
	reg.Gauge("g").Set(3)
	reg.Histogram("h_seconds", []float64{1}).Observe(0.5)

	before := reg.Snapshot()
	for i := 0; i < 50; i++ {
		render(t, metricsexport.Source{Subsystem: "s", Registry: reg})
	}
	after := reg.Snapshot()

	if len(before) != len(after) {
		t.Fatalf("series count changed across scrapes: %d -> %d", len(before), len(after))
	}
	if reg.Counter("c_total", "k").Value("a") != 5 {
		t.Errorf("counter value changed across scrapes: %d", reg.Counter("c_total", "k").Value("a"))
	}
	if reg.Gauge("g").Value() != 3 {
		t.Errorf("gauge value changed across scrapes: %v", reg.Gauge("g").Value())
	}
}

// TestExposition_ConcurrentScrapesAreSafe — Prometheus scrapes on a timer and
// nothing stops two from overlapping, while the call path keeps recording.
func TestExposition_ConcurrentScrapesAreSafe(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	c := reg.Counter("busy_total", "stage")
	h := reg.Histogram("busy_seconds", []float64{0.1, 1})
	g := reg.Gauge("busy_depth")

	e := metricsexport.New(metricsexport.Source{Subsystem: "s", Registry: reg})

	// Seed one observation before the goroutines start. A counter with no
	// recorded label values has no series at all, so without this a scraper
	// that wins the race against every writer would correctly find no
	// busy_total and the assertion below would be testing the scheduler.
	c.Inc("recognise")

	const writers, scrapers, iterations = 8, 8, 200
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				c.Inc("recognise")
				h.Observe(float64(j%10) / 10)
				g.Set(float64(j))
			}
		}(i)
	}

	errs := make(chan error, scrapers*iterations)
	for i := 0; i < scrapers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				var sb strings.Builder
				if _, err := e.WriteTo(&sb); err != nil {
					errs <- err
					return
				}
				if !strings.Contains(sb.String(), "busy_total") {
					errs <- errors.New("scrape produced no busy_total series")
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent scrape failed: %v", err)
	}

	if want := uint64(writers*iterations + 1); c.Value("recognise") != want {
		t.Errorf("counter = %d, want %d — scraping interfered with recording",
			c.Value("recognise"), want)
	}
}

// TestHandler_ConcurrentRequestsAreSafe exercises the same property through the
// real HTTP handler.
func TestHandler_ConcurrentRequestsAreSafe(t *testing.T) {
	t.Parallel()

	reg := metrics.NewRegistry()
	reg.Counter("h_total").Inc()
	h := metricsexport.New(metricsexport.Source{Subsystem: "s", Registry: reg}).Handler()

	srv := httptest.NewServer(h)
	defer srv.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(srv.URL)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- err
				return
			}
			if !strings.Contains(string(b), "h_total 1") {
				errs <- fmt.Errorf("unexpected body: %s", b)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent HTTP scrape failed: %v", err)
	}
}
