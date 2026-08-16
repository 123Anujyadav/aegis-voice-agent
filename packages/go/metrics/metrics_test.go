package metrics

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Counter
// ---------------------------------------------------------------------------

func TestCounter_IncAndAdd(t *testing.T) {
	t.Parallel()
	c := NewCounter("requests", "outcome")

	c.Inc("ok")
	c.Inc("ok")
	c.Add(5, "denied")

	if got := c.Value("ok"); got != 2 {
		t.Errorf("ok = %d, want 2", got)
	}
	if got := c.Value("denied"); got != 5 {
		t.Errorf("denied = %d, want 5", got)
	}
	if got := c.Total(); got != 7 {
		t.Errorf("total = %d, want 7", got)
	}
}

func TestCounter_NegativeIsIgnored(t *testing.T) {
	t.Parallel()
	c := NewCounter("requests")
	c.Add(10)
	c.Add(-5)

	// A counter that could decrease would corrupt every rate computed from it,
	// and the corruption is invisible: the series simply reads lower than the
	// events that produced it.
	if got := c.Value(); got != 10 {
		t.Errorf("value = %d after a negative add, want 10", got)
	}
}

func TestCounter_UnknownSeriesIsZeroNotPanic(t *testing.T) {
	t.Parallel()
	c := NewCounter("requests", "outcome")
	if got := c.Value("never-incremented"); got != 0 {
		t.Errorf("value = %d, want 0", got)
	}
}

func TestCounter_PrefixSum(t *testing.T) {
	t.Parallel()
	c := NewCounter("decisions", "outcome", "scope")

	c.Inc("deny", "consent")
	c.Inc("deny", "risk")
	c.Inc("deny", "risk")
	c.Inc("allow", "consent")

	if got := c.PrefixSum("deny"); got != 3 {
		t.Errorf("PrefixSum(deny) = %d, want 3", got)
	}
	if got := c.PrefixSum("allow"); got != 1 {
		t.Errorf("PrefixSum(allow) = %d, want 1", got)
	}
	if got := c.PrefixSum("escalate"); got != 0 {
		t.Errorf("PrefixSum(escalate) = %d, want 0", got)
	}
}

// TestCounter_PrefixSumDoesNotMatchAPartialLabel is the reason the separator is
// a control character.
//
// "deny" must not match "denied": a prefix match on raw string content would
// fold two distinct outcomes into one rate, and the resulting number would look
// entirely plausible.
func TestCounter_PrefixSumDoesNotMatchAPartialLabel(t *testing.T) {
	t.Parallel()
	c := NewCounter("decisions", "outcome", "scope")

	c.Inc("deny", "consent")
	c.Inc("denied_by_policy", "consent")

	if got := c.PrefixSum("deny"); got != 1 {
		t.Errorf("PrefixSum(deny) = %d, want 1 — it matched 'denied_by_policy'", got)
	}
}

// ---------------------------------------------------------------------------
// Gauge
// ---------------------------------------------------------------------------

func TestGauge_SetAddValue(t *testing.T) {
	t.Parallel()
	g := NewGauge("in_flight")

	g.Set(10)
	g.Add(5)
	g.Add(-3)

	if got := g.Value(); got != 12 {
		t.Errorf("value = %v, want 12", got)
	}
}

func TestGauge_ZeroValueReadsZero(t *testing.T) {
	t.Parallel()
	if got := NewGauge("fresh").Value(); got != 0 {
		t.Errorf("value = %v, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Histogram
// ---------------------------------------------------------------------------

func TestHistogram_CountSumMean(t *testing.T) {
	t.Parallel()
	h := NewHistogram("latency", []float64{0.1, 0.5, 1})

	h.Observe(0.05)
	h.Observe(0.2)
	h.Observe(0.8)

	if got := h.Count(); got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
	if got := h.Sum(); math.Abs(got-1.05) > 1e-9 {
		t.Errorf("sum = %v, want 1.05", got)
	}
	if got := h.Mean(); math.Abs(got-0.35) > 1e-9 {
		t.Errorf("mean = %v, want 0.35", got)
	}
}

func TestHistogram_MeanIsExactNotBucketed(t *testing.T) {
	t.Parallel()
	// Both observations land in the same wide bucket. A mean derived from
	// bucket midpoints would report the midpoint; a mean from the running sum
	// reports the truth.
	h := NewHistogram("latency", []float64{100})
	h.Observe(1)
	h.Observe(3)

	if got := h.Mean(); math.Abs(got-2) > 1e-9 {
		t.Errorf("mean = %v, want 2 — the mean is being derived from buckets", got)
	}
}

func TestHistogram_EmptyIsZeroNotNaN(t *testing.T) {
	t.Parallel()
	h := NewHistogram("latency", []float64{1})

	if got := h.Mean(); got != 0 {
		t.Errorf("mean = %v on an empty histogram, want 0", got)
	}
	if got := h.Quantile(0.99); got != 0 {
		t.Errorf("quantile = %v on an empty histogram, want 0", got)
	}
}

func TestHistogram_QuantileInterpolates(t *testing.T) {
	t.Parallel()
	h := NewHistogram("latency", []float64{1, 2, 3})
	for i := 0; i < 100; i++ {
		h.Observe(1.5)
	}

	// Everything is in the (1, 2] bucket, so any quantile must fall inside it.
	got := h.Quantile(0.5)
	if got <= 1 || got > 2 {
		t.Errorf("p50 = %v, want a value inside the (1,2] bucket", got)
	}
}

func TestHistogram_QuantileClampsOutOfRange(t *testing.T) {
	t.Parallel()
	h := NewHistogram("latency", []float64{1, 2})
	h.Observe(0.5)

	if got := h.Quantile(-1); got < 0 {
		t.Errorf("Quantile(-1) = %v, want a clamped non-negative result", got)
	}
	if got := h.Quantile(5); got <= 0 {
		t.Errorf("Quantile(5) = %v, want a clamped result", got)
	}
}

// TestHistogram_OverflowReportsTheLastBound documents the deliberate floor.
func TestHistogram_OverflowReportsTheLastBound(t *testing.T) {
	t.Parallel()
	h := NewHistogram("latency", []float64{1, 2})
	h.Observe(1000)

	if got := h.Quantile(0.99); got != 2 {
		t.Errorf("p99 = %v, want the last bound 2 — the true value is unbounded "+
			"and inventing a number would be worse than reporting the floor", got)
	}
}

func TestHistogram_ObserveDurationUsesSeconds(t *testing.T) {
	t.Parallel()
	h := NewHistogram("latency", []float64{0.001, 0.01, 0.1})
	h.ObserveDuration(5 * time.Millisecond)

	// If the helper used nanoseconds, 5e6 would land in the overflow bucket and
	// the sum would be 5,000,000 rather than 0.005.
	if got := h.Sum(); math.Abs(got-0.005) > 1e-9 {
		t.Errorf("sum = %v, want 0.005 — ObserveDuration is not using seconds", got)
	}
}

func TestHistogram_UnsortedBoundsAreSorted(t *testing.T) {
	t.Parallel()
	h := NewHistogram("latency", []float64{1, 0.1, 0.5})

	bounds := h.Bounds()
	for i := 1; i < len(bounds); i++ {
		if bounds[i] < bounds[i-1] {
			t.Fatalf("bounds are not ascending: %v", bounds)
		}
	}
}

func TestHistogram_BoundsAreCopiedNotAliased(t *testing.T) {
	t.Parallel()
	caller := []float64{1, 2, 3}
	h := NewHistogram("latency", caller)

	caller[0] = 999 // a caller reusing its slice must not re-bucket a live instrument

	if h.Bounds()[0] != 1 {
		t.Errorf("bounds tracked the caller's slice: %v", h.Bounds())
	}
}

// ---------------------------------------------------------------------------
// Registry and Snapshot
// ---------------------------------------------------------------------------

func TestRegistry_ReRegistrationReturnsTheSameInstrument(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	first := r.Counter("requests", "outcome")
	first.Inc("ok")
	second := r.Counter("requests", "outcome")

	// Replacing would silently zero a counter another holder still points at,
	// and the symptom — a rate dropping to zero for no reason — is nearly
	// untraceable.
	if second.Value("ok") != 1 {
		t.Error("re-registration replaced the instrument and lost its value")
	}
	if r.Len() != 1 {
		t.Errorf("registry holds %d instruments, want 1", r.Len())
	}
}

// TestSnapshot_HistogramCarriesBucketsCountAndSum is the regression test for the
// defect that motivated this package.
//
// Three of the six forked implementations emitted a histogram as a single
// synthetic `name_count` value: no bounds, no cumulative buckets, no sum. A
// scraper reading those subsystems could not reconstruct a percentile or an
// average, so a cross-subsystem latency dashboard was not buildable at all.
func TestSnapshot_HistogramCarriesBucketsCountAndSum(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	h := r.Histogram("latency", []float64{0.1, 0.5, 1}, "subsystem")

	h.Observe(0.05, "governance")
	h.Observe(0.2, "governance")
	h.Observe(2, "governance")

	var found *Sample
	for i := range r.Snapshot() {
		s := r.Snapshot()[i]
		if s.Name == "latency" {
			found = &s
			break
		}
	}
	if found == nil {
		t.Fatal("no latency sample in the snapshot")
	}

	if found.Kind != KindHistogram {
		t.Errorf("kind = %s, want histogram", found.Kind)
	}
	if found.Count != 3 {
		t.Errorf("count = %d, want 3", found.Count)
	}
	if math.Abs(found.Sum-2.25) > 1e-9 {
		t.Errorf("sum = %v, want 2.25", found.Sum)
	}
	if len(found.Bounds) != 3 {
		t.Errorf("bounds = %v, want 3 entries", found.Bounds)
	}
	if found.Labels["subsystem"] != "governance" {
		t.Errorf("labels = %v, want subsystem=governance", found.Labels)
	}

	// Cumulative, which is what makes a quantile computable by the consumer.
	if found.Buckets[0.1] != 1 {
		t.Errorf("bucket 0.1 = %d, want 1", found.Buckets[0.1])
	}
	if found.Buckets[0.5] != 2 {
		t.Errorf("bucket 0.5 = %d cumulative, want 2", found.Buckets[0.5])
	}
	if found.Buckets[1] != 2 {
		t.Errorf("bucket 1 = %d cumulative, want 2", found.Buckets[1])
	}
}

func TestSnapshot_KindIsSetForEveryInstrument(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Counter("c").Inc()
	r.Gauge("g").Set(1)
	r.Histogram("h", []float64{1}).Observe(0.5)

	kinds := map[string]Kind{}
	for _, s := range r.Snapshot() {
		kinds[s.Name] = s.Kind
	}

	// A reader must not have to infer the kind from which fields happen to be
	// populated — a counter reading zero looks exactly like an empty histogram.
	if kinds["c"] != KindCounter {
		t.Errorf("c kind = %s", kinds["c"])
	}
	if kinds["g"] != KindGauge {
		t.Errorf("g kind = %s", kinds["g"])
	}
	if kinds["h"] != KindHistogram {
		t.Errorf("h kind = %s", kinds["h"])
	}
}

func TestSnapshot_IsStable(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	c := r.Counter("requests", "outcome")
	for _, o := range []string{"ok", "denied", "escalated", "failed"} {
		c.Inc(o)
	}
	r.Gauge("in_flight").Set(3)
	r.Histogram("latency", []float64{0.1, 1}, "kind").Observe(0.5, "a")

	first := fmt.Sprint(r.Snapshot())
	for i := 0; i < 20; i++ {
		if got := fmt.Sprint(r.Snapshot()); got != first {
			t.Fatal("snapshot order is not stable across calls; a diff between " +
				"two snapshots would be unreadable")
		}
	}
}

// TestRegistry_LookupDoesNotRegister covers the sharp edge that the registering
// methods create on miss.
//
// An exporter asking "is `name` a histogram?" via Histogram(name, nil) would
// register an empty histogram under that name and then export it. The accessors
// exist so nothing has to ask that way.
func TestRegistry_LookupDoesNotRegister(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	r.Counter("requests").Inc()

	if got := len(r.Histograms()); got != 0 {
		t.Errorf("Histograms() = %d on a registry holding only a counter", got)
	}
	if got := r.Len(); got != 1 {
		t.Errorf("registry grew to %d instruments from a lookup", got)
	}
	if got := len(r.Counters()); got != 1 {
		t.Errorf("Counters() = %d, want 1", got)
	}
}

func TestRegistry_KindConflicts(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	r.Counter("latency")
	r.Histogram("latency", []float64{1})

	// Separate maps per kind means nothing clashes at registration, but the
	// snapshot then carries two series with the same Name and different Kinds
	// and an exporter keyed on name merges them.
	conflicts := r.KindConflicts()
	if len(conflicts) != 1 || conflicts[0] != "latency" {
		t.Errorf("KindConflicts() = %v, want [latency]", conflicts)
	}

	if got := NewRegistry().KindConflicts(); len(got) != 0 {
		t.Errorf("KindConflicts() = %v on a clean registry", got)
	}
}

func TestSnapshot_EmptyRegistryIsEmptyNotNil(t *testing.T) {
	t.Parallel()
	if got := len(NewRegistry().Snapshot()); got != 0 {
		t.Errorf("snapshot of an empty registry has %d entries", got)
	}
}

func TestRegistry_TwoRegistriesShareNothing(t *testing.T) {
	t.Parallel()
	a, b := NewRegistry(), NewRegistry()
	a.Counter("requests").Inc()

	if got := b.Counter("requests").Value(); got != 0 {
		t.Errorf("registry b saw registry a's counter: %d", got)
	}
}

func TestSplitLabels_ToleratesArityMismatch(t *testing.T) {
	t.Parallel()
	// A snapshot is diagnostic output. Crashing while reporting on a problem is
	// a poor way to report on it.
	if got := splitLabels([]string{"a", "b", "c"}, "1"+labelSeparator+"2"); got["a"] != "1" {
		t.Errorf("labels = %v", got)
	}
	if got := splitLabels(nil, "x"); got != nil {
		t.Errorf("labels = %v, want nil for an unlabelled instrument", got)
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestConcurrent_AllInstruments(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	c := r.Counter("requests", "worker")
	g := r.Gauge("in_flight")
	h := r.Histogram("latency", []float64{0.001, 0.01, 0.1, 1}, "worker")

	const workers, iterations = 32, 200

	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			label := fmt.Sprintf("w%d", w%4)
			<-start
			for i := 0; i < iterations; i++ {
				c.Inc(label)
				g.Add(1)
				g.Add(-1)
				h.Observe(0.005, label)
				if i%16 == 0 {
					_ = r.Snapshot() // readers concurrent with writers
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()

	if got := c.Total(); got != workers*iterations {
		t.Errorf("counter total = %d, want %d", got, workers*iterations)
	}
	var observed uint64
	for w := 0; w < 4; w++ {
		observed += h.Count(fmt.Sprintf("w%d", w))
	}
	if observed != workers*iterations {
		t.Errorf("histogram observations = %d, want %d", observed, workers*iterations)
	}
	if got := g.Value(); got != 0 {
		t.Errorf("gauge = %v after balanced add/subtract, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkCounterInc(b *testing.B) {
	c := NewCounter("requests", "outcome")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Inc("ok")
	}
}

func BenchmarkCounterIncParallel(b *testing.B) {
	c := NewCounter("requests", "outcome")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Inc("ok")
		}
	})
}

func BenchmarkGaugeSet(b *testing.B) {
	g := NewGauge("in_flight")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Set(float64(i))
	}
}

func BenchmarkHistogramObserve(b *testing.B) {
	h := NewHistogram("latency", []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1}, "kind")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Observe(0.02, "a")
	}
}

func BenchmarkHistogramObserveParallel(b *testing.B) {
	h := NewHistogram("latency", []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1}, "kind")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			h.Observe(0.02, "a")
		}
	})
}

func BenchmarkHistogramQuantile(b *testing.B) {
	h := NewHistogram("latency", []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1}, "kind")
	for i := 0; i < 10000; i++ {
		h.Observe(float64(i%100)/100, "a")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Quantile(0.99, "a")
	}
}

func BenchmarkSnapshot(b *testing.B) {
	r := NewRegistry()
	c := r.Counter("requests", "outcome")
	h := r.Histogram("latency", []float64{0.001, 0.01, 0.1, 1}, "kind")
	for i := 0; i < 32; i++ {
		c.Inc(fmt.Sprintf("o%d", i))
		h.Observe(0.05, fmt.Sprintf("k%d", i))
	}
	r.Gauge("in_flight").Set(4)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Snapshot()
	}
}
