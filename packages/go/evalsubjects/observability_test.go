// Cross-subsystem observability verification.
//
// This file exists because `evalsubjects` is the only module that imports all
// five frozen engines, which makes it the only place a property spanning them
// can be checked by a compiler rather than asserted in a document.
//
// Everything here is about the SHAPE of what the subsystems expose, not about
// what any of them decides. Behaviour is the evaluation platform's job; this is
// the scrape surface an operator has to build a dashboard on.
package evalsubjects

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	cv "github.com/callscreen/callscreen-platform/packages/go/conversation"
	ev "github.com/callscreen/callscreen-platform/packages/go/evaluation"
	gov "github.com/callscreen/callscreen-platform/packages/go/governance"
	mem "github.com/callscreen/callscreen-platform/packages/go/memory"
	"github.com/callscreen/callscreen-platform/packages/go/metrics"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	tr "github.com/callscreen/callscreen-platform/packages/go/toolruntime"
)

// subsystemMetrics names each frozen phase's instrument set and its registry.
//
// The registries are the point: every subsystem now hands back a
// *metrics.Registry, so this slice type-checks. Before Phase 10.5 each phase had
// its own incompatible Counter, Gauge, Histogram and Sample, and a slice like
// this could not be written at all.
func subsystemMetrics() []struct {
	Name string
	Reg  *metrics.Registry
} {
	return []struct {
		Name string
		Reg  *metrics.Registry
	}{
		{"runtime", rt.NewMetrics().Registry()},
		{"conversation", cv.NewMetrics().Registry()},
		{"memory", mem.NewMetrics().Registry()},
		{"toolruntime", tr.NewMetrics().Registry()},
		{"governance", gov.NewMetrics().Registry()},
		{"evaluation", ev.NewMetrics().Registry()},
	}
}

// TestObservability_EverySubsystemExportsHistogramData is the regression test
// for the defect that motivated the shared metrics package.
//
// Three of the six subsystems used to emit a histogram from Snapshot() as a
// single synthetic `name_count` value: no bounds, no cumulative buckets, no sum.
// A scraper reading governance, tool runtime or evaluation could not reconstruct
// a percentile or an average, so the cross-subsystem latency dashboard the
// platform's SLOs depend on was not buildable — not "inconsistent", not
// buildable.
func TestObservability_EverySubsystemExportsHistogramData(t *testing.T) {
	t.Parallel()

	for _, sub := range subsystemMetrics() {
		t.Run(sub.Name, func(t *testing.T) {
			t.Parallel()

			histograms := len(sub.Reg.Histograms())
			if histograms == 0 {
				t.Fatalf("%s registers no histograms; it can report no latency at all",
					sub.Name)
			}
			for _, h := range sub.Reg.Histograms() {
				h.Observe(0.5, seriesLabels(8)...)
			}

			var checked int
			for _, s := range sub.Reg.Snapshot() {
				if s.Kind != metrics.KindHistogram {
					continue
				}
				checked++
				if len(s.Bounds) == 0 {
					t.Errorf("%s/%s exports no bucket bounds; a consumer cannot "+
						"compute a percentile", sub.Name, s.Name)
				}
				if len(s.Buckets) == 0 {
					t.Errorf("%s/%s exports no bucket counts", sub.Name, s.Name)
				}
				if s.Count == 0 {
					t.Errorf("%s/%s exports no observation count", sub.Name, s.Name)
				}
				if s.Sum == 0 {
					t.Errorf("%s/%s exports no sum; a consumer cannot compute a mean",
						sub.Name, s.Name)
				}
			}
			if checked == 0 {
				t.Errorf("%s exported %d histograms but none reached the snapshot",
					sub.Name, histograms)
			}
			t.Logf("%-12s %d instruments, %d histogram series exported in full",
				sub.Name, sub.Reg.Len(), checked)
		})
	}
}

// TestObservability_SnapshotShapeIsUniform checks that one consumer can read
// every subsystem.
//
// The concrete requirement behind it: a single Prometheus adapter, written once,
// must handle all six. Previously it would have needed a branch per subsystem —
// three different Sample shapes and two different conventions for naming a
// histogram series.
func TestObservability_SnapshotShapeIsUniform(t *testing.T) {
	t.Parallel()

	kindsSeen := map[string]map[metrics.Kind]int{}

	for _, sub := range subsystemMetrics() {
		// Exercise every instrument first. Counters and histograms hold no
		// series until something is recorded, so a snapshot of a fresh registry
		// contains only gauges — and the first version of this test passed
		// while checking nothing but those.
		for _, c := range sub.Reg.Counters() {
			c.Inc(seriesLabels(8)...)
		}
		for _, h := range sub.Reg.Histograms() {
			h.Observe(0.25, seriesLabels(8)...)
		}

		if conflicts := sub.Reg.KindConflicts(); len(conflicts) > 0 {
			t.Errorf("%s registers %v under more than one instrument kind; a "+
				"name-keyed exporter would merge them", sub.Name, conflicts)
		}

		counts := map[metrics.Kind]int{}
		for _, s := range sub.Reg.Snapshot() {
			counts[s.Kind]++

			if s.Name == "" {
				t.Errorf("%s exported a sample with no name", sub.Name)
			}
			// A synthetic suffix is how three subsystems used to smuggle a
			// histogram through a counter-shaped Sample. Nothing should need to
			// any more, and a consumer keying on names must not have to strip
			// suffixes to find the real instrument.
			if strings.HasSuffix(s.Name, "_count") && s.Kind == metrics.KindCounter {
				t.Errorf("%s exports %q as a counter: a histogram is being "+
					"flattened into a synthetic count series", sub.Name, s.Name)
			}
			if s.Kind == metrics.KindHistogram && len(s.Bounds) == 0 {
				t.Errorf("%s/%s is a histogram with no bounds", sub.Name, s.Name)
			}
		}
		kindsSeen[sub.Name] = counts
	}

	names := make([]string, 0, len(kindsSeen))
	for n := range kindsSeen {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Logf("%-12s counters=%d gauges=%d histograms=%d", n,
			kindsSeen[n][metrics.KindCounter], kindsSeen[n][metrics.KindGauge],
			kindsSeen[n][metrics.KindHistogram])
	}
}

// TestObservability_InstrumentNamesArePrefixedBySubsystem checks that six
// registries can be merged into one scrape without colliding.
//
// A shared implementation makes collision possible for the first time: two
// subsystems can now genuinely register the same name into a combined exporter,
// where before their incompatible types kept them apart by accident.
func TestObservability_InstrumentNamesArePrefixedBySubsystem(t *testing.T) {
	t.Parallel()

	owner := map[string]string{}
	var collisions []string

	for _, sub := range subsystemMetrics() {
		for _, name := range sub.Reg.Names() {
			if prev, ok := owner[name]; ok {
				collisions = append(collisions,
					fmt.Sprintf("%q registered by both %s and %s", name, prev, sub.Name))
				continue
			}
			owner[name] = sub.Name
		}
	}

	if len(collisions) > 0 {
		for _, c := range collisions {
			t.Errorf("instrument name collision: %s", c)
		}
		t.Error("a merged scrape across subsystems would silently combine these series")
	}
	t.Logf("%d instrument names across six subsystems, no collisions", len(owner))
}

// TestObservability_SnapshotIsStableAcrossSubsystems checks that a diff between
// two scrapes is readable for every subsystem, not just the ones that happened
// to sort their output.
func TestObservability_SnapshotIsStableAcrossSubsystems(t *testing.T) {
	t.Parallel()

	for _, sub := range subsystemMetrics() {
		first := fmt.Sprint(sub.Reg.Snapshot())
		for i := 0; i < 5; i++ {
			if got := fmt.Sprint(sub.Reg.Snapshot()); got != first {
				t.Errorf("%s snapshot order is unstable; a diff between two "+
					"scrapes would be unreadable", sub.Name)
				break
			}
		}
	}
}

func seriesLabels(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "x"
	}
	return out
}
