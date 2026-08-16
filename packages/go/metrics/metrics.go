// Package metrics provides the platform's metric instruments.
//
// Three instruments — [Counter], [Gauge], [Histogram] — and a [Registry] that
// owns them and produces a uniform [Snapshot].
//
// # The registry is open for extension
//
// Every constructor is exported. The predecessor implementations kept theirs
// unexported, which meant a subsystem could use only the instruments its own
// metrics struct happened to declare, and adding one required editing that
// struct. That is what produced six copies of this file: it was easier to fork
// the machinery than to extend it.
//
//	reg := metrics.NewRegistry()
//	hits := reg.Counter("cache_hits", "tier")
//	hits.Inc("l1")
//
// # Concurrency
//
// Every instrument is safe for concurrent use. Counters and histograms hold a
// map of label series behind an RWMutex, with the per-series values in atomics
// so the hot path takes a read lock and an atomic add. Gauges are a single
// atomic and take no lock at all.
package metrics

import (
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// labelSeparator joins label values into one map key.
//
// U+001F (unit separator) rather than a printable character, because a label
// value containing the separator would silently merge two distinct series into
// one. A control character cannot appear in the identifiers, reason codes and
// enum names this platform uses as label values.
const labelSeparator = "\x1f"

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// Registry owns a set of instruments and snapshots them together.
//
// A registry is scoped, never global. Two registries in one process share
// nothing, which is what lets tests run in parallel without one subsystem's
// counters appearing in another's assertions.
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
	}
}

// Counter registers and returns a counter.
//
// Re-registering the same name returns the EXISTING instrument rather than
// replacing it. Replacing would silently reset a counter that another part of
// the process still holds a pointer to, and the symptom — a rate that drops to
// zero for no reason — is nearly impossible to trace back to its cause.
func (r *Registry) Counter(name string, labels ...string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.counters[name]; ok {
		return existing
	}
	c := NewCounter(name, labels...)
	r.counters[name] = c
	return c
}

// Gauge registers and returns a gauge.
func (r *Registry) Gauge(name string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.gauges[name]; ok {
		return existing
	}
	g := NewGauge(name)
	r.gauges[name] = g
	return g
}

// Histogram registers and returns a histogram.
//
// Bounds are the inclusive upper edges of each bucket and must be sorted
// ascending; an unsorted set is sorted here rather than refused, because the
// alternative is a panic at start-up in a code path nobody exercises until
// production.
func (r *Registry) Histogram(name string, bounds []float64, labels ...string) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.histograms[name]; ok {
		return existing
	}
	h := NewHistogram(name, bounds, labels...)
	r.histograms[name] = h
	return h
}

// Snapshot returns every series from every instrument, in a stable order.
//
// UNIFORM ACROSS SUBSYSTEMS. This is the property the six forked
// implementations did not have: three of them emitted histograms as a single
// synthetic `name_count` value with no bounds and no sum, so a scraper reading
// them could not reconstruct a percentile or an average. Anything reading a
// Snapshot from any subsystem now gets the same shape, and [Sample.Kind] says
// which of the three it is holding.
//
// The order is stable — name, then label key — so a diff between two snapshots
// is readable by a human and comparable by a test.
func (r *Registry) Snapshot() []Sample {
	r.mu.RLock()
	counters := make([]*Counter, 0, len(r.counters))
	for _, c := range r.counters {
		counters = append(counters, c)
	}
	gauges := make([]*Gauge, 0, len(r.gauges))
	for _, g := range r.gauges {
		gauges = append(gauges, g)
	}
	hists := make([]*Histogram, 0, len(r.histograms))
	for _, h := range r.histograms {
		hists = append(hists, h)
	}
	r.mu.RUnlock()

	var out []Sample
	for _, c := range counters {
		out = append(out, c.samples()...)
	}
	for _, g := range gauges {
		out = append(out, g.sample())
	}
	for _, h := range hists {
		out = append(out, h.samples()...)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return labelKey(out[i].Labels) < labelKey(out[j].Labels)
	})
	return out
}

// Names returns every registered instrument name, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.counters)+len(r.gauges)+len(r.histograms))
	for n := range r.counters {
		out = append(out, n)
	}
	for n := range r.gauges {
		out = append(out, n)
	}
	for n := range r.histograms {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Len returns the number of registered instruments.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.counters) + len(r.gauges) + len(r.histograms)
}

// Counters returns every registered counter, ordered by name.
//
// Lookup accessors exist because the registering methods CREATE on miss. An
// exporter that called Histogram(name, nil) to find out whether `name` is a
// histogram would register an empty one under that name instead — a silent
// junk instrument in a scrape.
func (r *Registry) Counters() []*Counter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Counter, 0, len(r.counters))
	for _, c := range r.counters {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// Gauges returns every registered gauge, ordered by name.
func (r *Registry) Gauges() []*Gauge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Gauge, 0, len(r.gauges))
	for _, g := range r.gauges {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// Histograms returns every registered histogram, ordered by name.
func (r *Registry) Histograms() []*Histogram {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Histogram, 0, len(r.histograms))
	for _, h := range r.histograms {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// KindConflicts returns names registered under more than one instrument kind.
//
// The three kinds live in separate maps, so a registry will happily hold a
// counter and a histogram both called "latency". Nothing detects it at
// registration — the types differ, so there is no signature to clash — but
// [Registry.Snapshot] then emits two series with the same Name and different
// Kinds, and a downstream exporter keyed on name silently merges or overwrites
// them.
//
// A conflict is a naming mistake rather than a runtime fault, so this reports
// rather than refuses: it belongs in a start-up check or a test, where a human
// can rename the instrument, not in a hot path that would panic in production.
func (r *Registry) KindConflicts() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]int, len(r.counters)+len(r.gauges)+len(r.histograms))
	for n := range r.counters {
		seen[n]++
	}
	for n := range r.gauges {
		seen[n]++
	}
	for n := range r.histograms {
		seen[n]++
	}

	var out []string
	for n, count := range seen {
		if count > 1 {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Sample
// ---------------------------------------------------------------------------

// Kind distinguishes what an instrument is, so a reader of a [Sample] does not
// have to infer it from which fields happen to be populated.
type Kind uint8

// The instrument kinds.
const (
	KindCounter Kind = iota
	KindGauge
	KindHistogram
)

// String renders the kind.
func (k Kind) String() string {
	switch k {
	case KindGauge:
		return "gauge"
	case KindHistogram:
		return "histogram"
	default:
		return "counter"
	}
}

// Sample is one instrument series at one instant.
//
// A union across the three kinds. Which fields are meaningful depends on
// [Sample.Kind]: counters and gauges use Value; histograms use Bounds, Buckets,
// Count and Sum.
type Sample struct {
	// Kind is which instrument produced this.
	Kind Kind
	// Name is the instrument name.
	Name string
	// Labels is the label set, nil for an unlabelled series.
	Labels map[string]string

	// Value is the counter or gauge reading.
	Value float64

	// Bounds are the histogram's upper bucket edges, ascending.
	Bounds []float64
	// Buckets maps an upper bound to the CUMULATIVE count at or below it,
	// which is the convention Prometheus expects and the one that makes a
	// quantile computable by a consumer without the raw observations.
	Buckets map[float64]uint64
	// Count is the histogram's total observation count.
	Count uint64
	// Sum is the histogram's summed observations, so a consumer can compute a
	// mean without the raw values.
	Sum float64
}

// ---------------------------------------------------------------------------
// Counter
// ---------------------------------------------------------------------------

// Counter is a monotonically increasing value, optionally partitioned by
// labels.
//
// Label values are joined into a single map key rather than nested maps. At the
// cardinality this platform produces — a handful of classes, reasons, outcomes
// and subsystem names — a string join is faster than a map-of-maps and vastly
// simpler to snapshot correctly.
type Counter struct {
	name       string
	labelNames []string

	mu     sync.RWMutex
	values map[string]*atomic.Uint64
}

// NewCounter builds an unregistered counter.
func NewCounter(name string, labels ...string) *Counter {
	return &Counter{
		name:       name,
		labelNames: labels,
		values:     make(map[string]*atomic.Uint64),
	}
}

// Name returns the instrument name.
func (c *Counter) Name() string { return c.name }

// Inc adds one to the series identified by the label values.
func (c *Counter) Inc(labelValues ...string) { c.Add(1, labelValues...) }

// Add adds v to the series identified by the label values.
//
// v is a float64 for interface symmetry with other metric systems but is stored
// as an integer: every counter in this platform counts events, and floating
// point accumulation over millions of increments loses precision in a way that
// silently corrupts a rate.
//
// A negative v is ignored. A counter cannot decrease, and silently ignoring
// beats corrupting a monotonic series that a rate calculation depends on.
func (c *Counter) Add(v float64, labelValues ...string) {
	if v < 0 {
		return
	}
	c.slot(joinLabels(labelValues)).Add(uint64(v))
}

func (c *Counter) slot(key string) *atomic.Uint64 {
	c.mu.RLock()
	slot, ok := c.values[key]
	c.mu.RUnlock()
	if ok {
		return slot
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if slot, ok = c.values[key]; !ok {
		slot = new(atomic.Uint64)
		c.values[key] = slot
	}
	return slot
}

// Value returns the current count for a label set.
func (c *Counter) Value(labelValues ...string) uint64 {
	key := joinLabels(labelValues)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if slot, ok := c.values[key]; ok {
		return slot.Load()
	}
	return 0
}

// Total returns the sum across every label set.
func (c *Counter) Total() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var total uint64
	for _, slot := range c.values {
		total += slot.Load()
	}
	return total
}

// PrefixSum sums every series whose FIRST label value matches.
//
// The rate helpers in several subsystems need "every decision with outcome
// deny, whatever the other labels were". Exported here because it was an
// unexported duplicate in three of the six forked implementations, and a helper
// three subsystems independently needed is a helper that belongs in the shared
// package.
func (c *Counter) PrefixSum(first string) uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var total uint64
	for key, slot := range c.values {
		if key == first || strings.HasPrefix(key, first+labelSeparator) {
			total += slot.Load()
		}
	}
	return total
}

// Series returns the label keys currently present, sorted. Intended for tests
// and diagnostics.
func (c *Counter) Series() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.values))
	for k := range c.values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *Counter) samples() []Sample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Sample, 0, len(c.values))
	for key, slot := range c.values {
		out = append(out, Sample{
			Kind:   KindCounter,
			Name:   c.name,
			Labels: splitLabels(c.labelNames, key),
			Value:  float64(slot.Load()),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Gauge
// ---------------------------------------------------------------------------

// Gauge is a value that may go up or down.
//
// Stored as the bit pattern of a float64 in an atomic uint64, which is the
// standard lock-free gauge. A mutex would be correct but a gauge is written on
// every admission and release, and a lock there is contention on the hot path.
type Gauge struct {
	name string
	bits atomic.Uint64
}

// NewGauge builds an unregistered gauge.
func NewGauge(name string) *Gauge { return &Gauge{name: name} }

// Name returns the instrument name.
func (g *Gauge) Name() string { return g.name }

// Set replaces the value.
func (g *Gauge) Set(v float64) { g.bits.Store(math.Float64bits(v)) }

// Add applies a delta, which may be negative.
func (g *Gauge) Add(delta float64) {
	for {
		old := g.bits.Load()
		next := math.Float64bits(math.Float64frombits(old) + delta)
		if g.bits.CompareAndSwap(old, next) {
			return
		}
	}
}

// Value returns the current reading.
func (g *Gauge) Value() float64 { return math.Float64frombits(g.bits.Load()) }

func (g *Gauge) sample() Sample {
	return Sample{Kind: KindGauge, Name: g.name, Value: g.Value()}
}

// ---------------------------------------------------------------------------
// Histogram
// ---------------------------------------------------------------------------

type histSeries struct {
	counts []atomic.Uint64
	count  atomic.Uint64
	sum    atomic.Uint64 // float64 bits
}

// Histogram counts observations into fixed buckets.
//
// Bucket bounds are supplied by the caller and are NOT standardised across the
// platform, deliberately. Governance decides in hundreds of nanoseconds and a
// conversation turn takes seconds; one shared bucket set would put every
// governance observation in the first bucket and every conversation observation
// in the last, which is the same as measuring nothing.
type Histogram struct {
	name       string
	labelNames []string
	bounds     []float64

	mu     sync.RWMutex
	series map[string]*histSeries
}

// NewHistogram builds an unregistered histogram.
//
// The bounds slice is copied and sorted, so a caller's slice cannot be mutated
// underneath a live instrument and an accidentally unsorted set still produces
// correct buckets.
func NewHistogram(name string, bounds []float64, labels ...string) *Histogram {
	b := append([]float64(nil), bounds...)
	sort.Float64s(b)
	return &Histogram{
		name:       name,
		labelNames: labels,
		bounds:     b,
		series:     make(map[string]*histSeries),
	}
}

// Name returns the instrument name.
func (h *Histogram) Name() string { return h.name }

// Bounds returns a copy of the bucket upper edges.
func (h *Histogram) Bounds() []float64 { return append([]float64(nil), h.bounds...) }

// Observe records a value.
func (h *Histogram) Observe(v float64, labelValues ...string) {
	s := h.seriesFor(joinLabels(labelValues))

	// Linear scan. With sixteen or fewer bounds this beats a binary search on
	// modern hardware — the whole bound slice is one or two cache lines and the
	// branch predictor handles the loop well.
	idx := len(h.bounds)
	for i, b := range h.bounds {
		if v <= b {
			idx = i
			break
		}
	}
	s.counts[idx].Add(1)
	s.count.Add(1)

	for {
		old := s.sum.Load()
		next := math.Float64bits(math.Float64frombits(old) + v)
		if s.sum.CompareAndSwap(old, next) {
			return
		}
	}
}

// ObserveDuration records a duration in SECONDS.
//
// Seconds, not nanoseconds, because every bucket set in this platform is
// expressed in seconds and a helper that silently used a different unit than
// the bounds it feeds would put every observation in the overflow bucket.
func (h *Histogram) ObserveDuration(d time.Duration, labelValues ...string) {
	h.Observe(d.Seconds(), labelValues...)
}

func (h *Histogram) seriesFor(key string) *histSeries {
	h.mu.RLock()
	s, ok := h.series[key]
	h.mu.RUnlock()
	if ok {
		return s
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok = h.series[key]; !ok {
		s = &histSeries{counts: make([]atomic.Uint64, len(h.bounds)+1)}
		h.series[key] = s
	}
	return s
}

// Count returns the observation count for a label set.
func (h *Histogram) Count(labelValues ...string) uint64 {
	h.mu.RLock()
	s, ok := h.series[joinLabels(labelValues)]
	h.mu.RUnlock()
	if !ok {
		return 0
	}
	return s.count.Load()
}

// Sum returns the summed observations for a label set.
func (h *Histogram) Sum(labelValues ...string) float64 {
	h.mu.RLock()
	s, ok := h.series[joinLabels(labelValues)]
	h.mu.RUnlock()
	if !ok {
		return 0
	}
	return math.Float64frombits(s.sum.Load())
}

// Mean returns the arithmetic mean for a label set, or zero when there are no
// observations.
//
// EXACT, unlike [Histogram.Quantile]. The mean is computed from the running sum
// and count rather than from bucket midpoints, so it does not inherit the
// bucket resolution.
func (h *Histogram) Mean(labelValues ...string) float64 {
	n := h.Count(labelValues...)
	if n == 0 {
		return 0
	}
	return h.Sum(labelValues...) / float64(n)
}

// Quantile estimates a quantile by linear interpolation within the bucket the
// target falls into.
//
// AN ESTIMATE, bounded by the bucket resolution. A p99 reported from a bucket
// spanning 0.5 s to 1 s cannot be more precise than that span, and a caller
// treating the result as exact will over-read it. Where exactness matters, use
// [Histogram.Mean], which is computed from the running sum.
//
// Values above the last bound return that bound: the true value is unbounded,
// and reporting the last bound is the honest floor rather than an invented
// number.
func (h *Histogram) Quantile(q float64, labelValues ...string) float64 {
	if q < 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}

	h.mu.RLock()
	s, ok := h.series[joinLabels(labelValues)]
	h.mu.RUnlock()
	if !ok {
		return 0
	}
	total := s.count.Load()
	if total == 0 {
		return 0
	}
	target := q * float64(total)

	var cumulative float64
	for i := 0; i < len(h.bounds); i++ {
		prev := cumulative
		cumulative += float64(s.counts[i].Load())
		if cumulative >= target {
			lower := 0.0
			if i > 0 {
				lower = h.bounds[i-1]
			}
			inBucket := cumulative - prev
			if inBucket == 0 {
				return h.bounds[i]
			}
			return lower + (h.bounds[i]-lower)*((target-prev)/inBucket)
		}
	}
	if len(h.bounds) == 0 {
		return 0
	}
	return h.bounds[len(h.bounds)-1]
}

func (h *Histogram) samples() []Sample {
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]Sample, 0, len(h.series))
	for key, s := range h.series {
		buckets := make(map[float64]uint64, len(h.bounds))
		var cumulative uint64
		for i, b := range h.bounds {
			cumulative += s.counts[i].Load()
			buckets[b] = cumulative
		}
		out = append(out, Sample{
			Kind:    KindHistogram,
			Name:    h.name,
			Labels:  splitLabels(h.labelNames, key),
			Bounds:  append([]float64(nil), h.bounds...),
			Buckets: buckets,
			Count:   s.count.Load(),
			Sum:     math.Float64frombits(s.sum.Load()),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Label helpers
// ---------------------------------------------------------------------------

func joinLabels(values []string) string {
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0]
	default:
		return strings.Join(values, labelSeparator)
	}
}

// splitLabels pairs label names with the values encoded in a series key.
//
// Tolerant by design: a key with more or fewer parts than there are names still
// produces a map rather than panicking. A metric snapshot is diagnostic output,
// and crashing while reporting on a problem is a poor way to report on it.
func splitLabels(names []string, key string) map[string]string {
	if len(names) == 0 || key == "" {
		return nil
	}
	parts := strings.Split(key, labelSeparator)
	out := make(map[string]string, len(names))
	for i, n := range names {
		if i < len(parts) {
			out[n] = parts[i]
		}
	}
	return out
}

// labelKey renders a label map deterministically, for snapshot ordering.
func labelKey(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
	}
	return b.String()
}
