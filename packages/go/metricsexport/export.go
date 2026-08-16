// Package metricsexport renders [metrics.Registry] contents as Prometheus text
// exposition format v0.0.4.
//
// It is the adapter that packages/go/metrics deliberately does not contain. That
// module's header states the rule: instrument code must not acquire a network
// dependency, so Snapshot() returns plain values and something else converts
// them. This is that something else.
//
// The conversion is a pure function of []metrics.Sample. It reads registries and
// never writes to them, so a scrape cannot change what a call observes, and a
// scrape that fails cannot fail a call.
//
// See docs/adr/0013-metrics-exposition-format.md.
package metricsexport

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/callscreen/callscreen-platform/packages/go/metrics"
)

// ContentType is the media type Prometheus expects for the v0.0.4 text format.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// DefaultMaxSeries bounds a single exposition.
//
// An unbounded scrape is how a metrics backend becomes an outage: the cost of a
// cardinality mistake is paid by the monitoring system, which is the one thing
// that must keep working while everything else is failing. 10,000 is far above
// this platform's actual series count — the six Phase 10 subsystems register 183
// instruments between them — so the cap should never engage in normal operation.
// If it does, that is a cardinality bug worth failing loudly about, and
// [Stats.TruncatedSeries] says so.
const DefaultMaxSeries = 10000

// A Source is one subsystem's registry.
//
// Subsystem is used for diagnostics and collision reporting only. It is
// deliberately NOT emitted as a label: instrument names are already
// subsystem-prefixed by convention (governance_decisions_total,
// voice_sessions_opened_total), and adding a redundant label would both inflate
// every series and change what the frozen subsystems chose to publish.
type Source struct {
	Subsystem string
	Registry  *metrics.Registry
}

// Stats reports what the most recent exposition did, including what it refused
// to do. Anything dropped is counted rather than silently discarded — a guard
// whose effects are invisible is indistinguishable from a guard that is not
// running.
type Stats struct {
	// Series is the number of series lines emitted.
	Series int
	// DroppedSeries were refused: an invalid metric name, an invalid label
	// name, or a label name on the sensitive deny list.
	DroppedSeries int
	// TruncatedSeries were discarded because MaxSeries was reached.
	TruncatedSeries int
	// SkippedSources had a nil Registry.
	SkippedSources int
	// NameCollisions counts instrument names registered by more than one
	// subsystem. The frozen observability test asserts this cannot happen for
	// the real registries; this counts it in case that ever stops holding.
	NameCollisions int
}

// Exporter renders a fixed set of sources.
//
// Safe for concurrent use: Prometheus scrapes on a timer and nothing prevents
// two scrapes overlapping, or a scrape overlapping the call path that is
// recording into the same registries.
type Exporter struct {
	sources []Source

	// MaxSeries bounds one exposition. Zero means DefaultMaxSeries.
	MaxSeries int

	mu    sync.Mutex
	stats Stats
}

// New builds an exporter over the given sources.
func New(sources ...Source) *Exporter {
	return &Exporter{
		sources:   append([]Source(nil), sources...),
		MaxSeries: DefaultMaxSeries,
	}
}

// Stats returns the result of the most recent WriteTo.
func (e *Exporter) Stats() Stats {
	if e == nil {
		return Stats{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stats
}

// Handler serves the exposition over HTTP.
//
// It writes the status and content type before rendering, because a scrape is
// streamed and there is no way to retract a header once the body has started. A
// mid-write failure is almost always the client disconnecting, which is normal
// and not worth reporting as a server error.
func (e *Exporter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ContentType)
		w.WriteHeader(http.StatusOK)
		if _, err := e.WriteTo(w); err != nil {
			// The header is already sent, so the status cannot be revised. A
			// failure here is almost always the client disconnecting
			// mid-scrape, which is routine and not actionable.
			return
		}
	})
}

// WriteTo renders the exposition.
//
// A nil Exporter writes nothing and reports no error, so an unwired service
// degrades to an empty scrape rather than a panic.
func (e *Exporter) WriteTo(w io.Writer) (int64, error) {
	if e == nil {
		return 0, nil
	}

	entries, stats := e.collect()

	limit := e.MaxSeries
	if limit <= 0 {
		limit = DefaultMaxSeries
	}

	var (
		out      strings.Builder
		lastName string
		emitted  int
	)
	for _, en := range entries {
		if emitted >= limit {
			stats.TruncatedSeries += len(en.lines)
			continue
		}
		if en.name != lastName {
			out.WriteString("# TYPE ")
			out.WriteString(en.name)
			out.WriteByte(' ')
			out.WriteString(en.kind.String())
			out.WriteByte('\n')
			lastName = en.name
		}
		for _, l := range en.lines {
			if emitted >= limit {
				stats.TruncatedSeries++
				continue
			}
			out.WriteString(l)
			out.WriteByte('\n')
			emitted++
		}
	}
	stats.Series = emitted

	e.mu.Lock()
	e.stats = stats
	e.mu.Unlock()

	n, err := io.WriteString(w, out.String())
	if err != nil {
		return int64(n), fmt.Errorf("metricsexport: writing exposition: %w", err)
	}
	return int64(n), nil
}

// entry is one instrument series, pre-rendered.
type entry struct {
	name    string // base instrument name, used for the # TYPE line
	kind    metrics.Kind
	sortKey string
	lines   []string
}

// collect snapshots every source and renders the acceptable series.
func (e *Exporter) collect() ([]entry, Stats) {
	var (
		stats   Stats
		entries []entry
		owner   = make(map[string]string)
	)

	for _, src := range e.sources {
		if src.Registry == nil {
			// A subsystem that failed to build its metrics must not take the
			// whole scrape down with it: the other subsystems are still the
			// operator's best information about what is happening.
			stats.SkippedSources++
			continue
		}

		for _, s := range src.Registry.Snapshot() {
			if prev, seen := owner[s.Name]; seen && prev != src.Subsystem {
				stats.NameCollisions++
			} else if !seen {
				owner[s.Name] = src.Subsystem
			}

			if !validMetricName(s.Name) || !labelsAcceptable(s.Labels) {
				stats.DroppedSeries++
				continue
			}

			lines := renderSample(s)
			if len(lines) == 0 {
				stats.DroppedSeries++
				continue
			}
			entries = append(entries, entry{
				name:    s.Name,
				kind:    s.Kind,
				sortKey: s.Name + "\x00" + labelSortKey(s.Labels),
				lines:   lines,
			})
		}
	}

	// Deterministic output. A scrape that reorders itself makes diffs useless
	// and turns a real change into noise.
	sort.Slice(entries, func(i, j int) bool { return entries[i].sortKey < entries[j].sortKey })
	return entries, stats
}

// renderSample turns one sample into its exposition lines.
func renderSample(s metrics.Sample) []string {
	switch s.Kind {
	case metrics.KindHistogram:
		return renderHistogram(s)
	default:
		return []string{s.Name + renderLabels(s.Labels, "", "") + " " + formatFloat(s.Value)}
	}
}

// renderHistogram emits the cumulative buckets, the +Inf bucket, _sum and
// _count.
//
// Buckets are already CUMULATIVE in metrics.Sample — its own documentation calls
// that "the convention Prometheus expects" — so this must not re-accumulate
// them. The +Inf bucket is the total observation count by definition.
func renderHistogram(s metrics.Sample) []string {
	bounds := append([]float64(nil), s.Bounds...)
	sort.Float64s(bounds)

	lines := make([]string, 0, len(bounds)+3)
	for _, b := range bounds {
		lines = append(lines, s.Name+"_bucket"+
			renderLabels(s.Labels, "le", formatFloat(b))+" "+
			strconv.FormatUint(s.Buckets[b], 10))
	}
	lines = append(lines,
		s.Name+"_bucket"+renderLabels(s.Labels, "le", "+Inf")+" "+strconv.FormatUint(s.Count, 10),
		s.Name+"_sum"+renderLabels(s.Labels, "", "")+" "+formatFloat(s.Sum),
		s.Name+"_count"+renderLabels(s.Labels, "", "")+" "+strconv.FormatUint(s.Count, 10),
	)
	return lines
}

// renderLabels renders a label set, optionally appending one extra pair last.
// "le" goes last by Prometheus convention.
func renderLabels(labels map[string]string, extraKey, extraVal string) string {
	if len(labels) == 0 && extraKey == "" {
		return ""
	}

	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(labels[k]))
		b.WriteByte('"')
	}
	if extraKey != "" {
		if len(keys) > 0 {
			b.WriteByte(',')
		}
		b.WriteString(extraKey)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(extraVal))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func labelSortKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('\x00')
		b.WriteString(labels[k])
		b.WriteByte('\x00')
	}
	return b.String()
}

// escapeLabelValue escapes the three characters the format requires.
//
// An unescaped quote or newline terminates the line early and corrupts every
// series after it, so this is a correctness requirement rather than tidiness.
func escapeLabelValue(v string) string {
	if !strings.ContainsAny(v, "\\\"\n") {
		return v
	}
	var b strings.Builder
	b.Grow(len(v) + 8)
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// formatFloat renders a value.
//
// Go's strconv already spells the special values the way Prometheus reads
// them:
// "+Inf", "-Inf", "NaN". 'g' with precision -1 gives the shortest form that
// round-trips, so no precision is invented and none is lost.
func formatFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// validMetricName reports whether a name matches [a-zA-Z_:][a-zA-Z0-9_:]*.
//
// One invalid name makes the entire scrape unparseable, so a bad instrument is
// dropped rather than allowed to poison every other subsystem's data.
func validMetricName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == ':':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// validLabelName reports whether a name matches [a-zA-Z_][a-zA-Z0-9_]*.
func validLabelName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// sensitiveLabels are label names that must never leave the process.
//
// ADR-0012 (DPDP) governs this: a metric label is exported data, and a per-call
// or per-subject identifier in a label is both a privacy violation and an
// unbounded-cardinality bug. The frozen subsystems use a closed, bounded
// vocabulary — reason, outcome, from, to, kind, provider, stage, action, basis —
// and none of them appears here. This guard exists for the instrument nobody has
// written yet.
//
// Names are matched after normalisation, so call_id, CallID, Call-Id and
// callId are all the same key. A guard defeated by a spelling is not a guard.
var sensitiveLabels = map[string]struct{}{
	"callid": {}, "sessionid": {}, "requestid": {}, "correlationid": {},
	"traceid": {}, "spanid": {}, "turnid": {}, "legid": {}, "streamid": {},
	"transcript": {}, "text": {}, "utterance": {}, "content": {}, "prompt": {},
	"audio": {}, "pcm": {}, "frame": {}, "payload": {}, "body": {},
	"token": {}, "apikey": {}, "key": {}, "secret": {}, "password": {},
	"credential": {}, "authorization": {}, "auth": {}, "cookie": {},
	"phone": {}, "msisdn": {}, "number": {}, "caller": {}, "callee": {},
	"email": {}, "userid": {}, "user": {},
	"ip": {}, "address": {}, "location": {},
}

// NOTE ON WHAT IS DELIBERATELY *NOT* ON THAT LIST.
//
// "name" and "subject" were on it during development and had to be removed:
// the platform uses both as bounded, authored labels — "name" is an intent name
// (conv_intents_proposed_total) and an emergency-policy name
// (governance_emergency_activations_total), and "subject" is the evaluated
// subsystem, which has five values. Denying them silently dropped 15 real
// series across conversation, governance and evaluation.
//
// That is the failure mode this list must be judged against. A deny list tuned
// by imagination rather than by the actual label vocabulary does not fail
// loudly; it quietly deletes data an operator is relying on. The regression
// test replays the platform's real 38-label vocabulary and asserts nothing is
// dropped, so widening this list is only possible if the vocabulary genuinely
// does not use the word.

// normaliseLabel lowercases and strips separators so that spelling variants
// collapse onto one key.
func normaliseLabel(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			// '_', '-', '.', ' ' and anything else are separators.
		}
	}
	return b.String()
}

// labelsAcceptable reports whether every label name is valid and non-sensitive.
//
// The whole series is refused rather than the offending label being stripped:
// dropping a label silently changes what a series means and can merge two
// distinct series into one, which is worse than losing the series.
func labelsAcceptable(labels map[string]string) bool {
	for k := range labels {
		if !validLabelName(k) {
			return false
		}
		if _, bad := sensitiveLabels[normaliseLabel(k)]; bad {
			return false
		}
	}
	return true
}
