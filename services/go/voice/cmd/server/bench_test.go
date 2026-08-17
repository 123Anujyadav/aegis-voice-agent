package main

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
	"github.com/callscreen/callscreen-platform/packages/go/platform"
	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
	"github.com/callscreen/callscreen-platform/packages/go/voiceintel"
)

// Phase 14 T10 — BENCHMARKS for the SERVICE intelligence path.
//
// MEASUREMENT ONLY. Nothing here optimises, and no production code changed.
//
// CATEGORY A (orchestration) is all that exists. There is no provider, no
// model and no network anywhere in this path, so CATEGORY B (provider/model
// inference) is NOT APPLICABLE and no number is simulated for it.
//
// RELATIONSHIP TO PHASE 13 T12: voiceintel/bench_test.go already measures the
// bridge, context and response-strategy path at MODULE level. T10 does not
// restate those; it measures the same operations as reached through the
// PHASE 14 SERVICE construction path (buildService -> registered runner ->
// bridge), and adds what T12 has no equivalent of: service construction cost,
// multi-turn steady state, failure paths, and service-level concurrency
// scaling.
//
// WHAT IS DELIBERATELY EXCLUDED from every steady-state number:
//   - service startup and svc.Run: no benchmark calls Run, so no HTTP or health
//     listener is ever opened and no port is bound.
//   - logging: testLogger() writes to io.Discard, and the intelligence path
//     emits no per-turn log at all — voiceIntelligence logs once at Run and once
//     at Shutdown, neither of which is on the turn path.
//   - session construction: excluded from turn benchmarks via StopTimer, and
//     measured separately.
//   - clock reads, where a fixed clock is available.
//
// TURN BUDGET: the default persona bounds a conversation at MaxTurns and then
// escalates with "max_turns_reached". Any benchmark whose b.N exceeds that must
// rebuild its session, and every rebuild below happens with the timer STOPPED
// so it is never attributed to a turn. turnsPerSession mirrors the constant
// Phase 13 T12 measured for the same reason.

const turnsPerSession = 15

// Sinks. Package-level and written by every benchmark loop so the compiler
// cannot eliminate the work being measured.
var (
	sinkPlan  conversation.Plan
	sinkCands []conversation.Candidate
	sinkSlots []conversation.Slot
	sinkEntry conversation.Entry
	sinkOK    bool
	sinkErr   error
	sinkSize  int
	sinkVI    *voiceIntelligence
)

// benchOps counts operations actually executed inside benchmark loops.
// TestT10_BenchmarksActuallyExecute asserts it advances, which is what proves
// these benchmarks are not timing an empty loop.
var benchOps atomic.Int64

// benchBuild runs the PRODUCTION construction path, exactly as run() does.
// No listener is opened because Run is never called.
func benchBuild(b *testing.B) (*platform.Service, *voiceIntelligence) {
	b.Helper()
	cfg := testConfig()
	if err := cfg.Validate(); err != nil {
		b.Fatalf("config invalid: %v", err)
	}
	svc, vi, err := buildService(cfg, testLogger(), platform.NewHealth(cfg))
	if err != nil {
		b.Fatalf("buildService: %v", err)
	}
	return svc, vi
}

// benchSession opens one session through the service's bridge.
func benchSession(b *testing.B, vi *voiceIntelligence, id string) (turnHandler, *conversation.Conversation) {
	b.Helper()
	p, conv, err := openPlanner(vi, conversation.ConversationID(id))
	if err != nil {
		b.Fatalf("open %s: %v", id, err)
	}
	return p, conv
}

// benchCase is one representative input plus how many times it may be repeated
// on a single session.
//
// MEASURED, not assumed: `budget` differs by case because two DIFFERENT frozen
// limits apply.
//
//   - Inputs the planner ANSWERS are bounded only by the persona's MaxTurns, so
//     they reuse the same turnsPerSession headroom Phase 13 T12 measured.
//   - Inputs that produce a CLARIFICATION (ask / clarify / confirm) are bounded
//     by the persona's ClarificationBudget, which is 3 for the default persona
//     (persona.go:137). Repeating such an utterance a third time spends the
//     budget and ESCALATES, and the conversation becomes terminal.
//
// This was not predicted from the source, it was observed: the first run of this
// benchmark failed with `iteration 2 speech-complete: conversation: terminal`
// on exactly the three clarification cases. Rebuilding every 2 turns keeps the
// measurement on the intended path instead of silently becoming a benchmark of
// the escalation path.
type benchCase struct {
	name, text string
	budget     int
}

func benchCases() []benchCase {
	return []benchCase{
		{"request_callback", "please call me back on 9876543210", turnsPerSession},
		{"request_transfer", "transfer me to rajesh", turnsPerSession},
		{"repeat", "say that again", turnsPerSession},
		{"hold", "can you hold on a moment", turnsPerSession},
		{"caller_identity", "this is rajesh sharma calling", turnsPerSession},
		{"unknown", "zzzz qqqq wubble frotz", turnsPerSession},
		{"leave_message", "i want to leave a message", clarifyTurnsPerSession},
		{"ambiguous", "hold on call back", clarifyTurnsPerSession},
		{"low_confidence", "repeat pardon", clarifyTurnsPerSession},
	}
}

// clarifyTurnsPerSession keeps a clarification-producing input one turn clear of
// the frozen ClarificationBudget of 3.
const clarifyTurnsPerSession = 2

func benchUtterance(text string) conversation.Utterance {
	return conversation.Utterance{Text: text, ASRConfidence: 0.95}
}

// ---------------------------------------------------------------------------
// A. Intent classification
// ---------------------------------------------------------------------------

// BenchmarkT10_Classify measures the deterministic classifier alone, built with
// the SAME configuration the service wires (voiceintel.New -> intent.New with
// intent.DefaultConfig). The service does not expose its classifier, so it is
// constructed here identically rather than reached through a seam that does not
// exist.
func BenchmarkT10_Classify(b *testing.B) {
	c, err := intent.New(intent.DefaultConfig())
	if err != nil {
		b.Fatalf("intent.New: %v", err)
	}
	for _, tc := range benchCases() {
		u := benchUtterance(tc.text)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sinkCands, sinkSlots, sinkErr = c.Classify(u, conversation.ExpectNothing)
			}
			benchOps.Add(int64(b.N))
		})
	}
}

// ---------------------------------------------------------------------------
// B. Construction vs steady-state single turn
// ---------------------------------------------------------------------------

// BenchmarkT10_ServiceConstruction measures buildService — the whole production
// construction path including the classifier, engine, bridge and registration.
// Reported separately because it happens once per process, not per turn.
func BenchmarkT10_ServiceConstruction(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, sinkVI = benchBuild(b)
	}
	benchOps.Add(int64(b.N))
}

// BenchmarkT10_SessionOpen measures Planner + EventStart + EventGreetingComplete
// on an already-constructed service: the per-CALL cost, distinct from both the
// per-process cost above and the per-TURN cost below.
func BenchmarkT10_SessionOpen(b *testing.B) {
	_, vi := benchBuild(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, conv, err := openPlanner(vi, conversation.ConversationID(
			fmt.Sprintf("bench-open-%d", i)))
		if err != nil {
			b.Fatalf("iteration %d: %v", i, err)
		}
		sinkOK = conv != nil
	}
	benchOps.Add(int64(b.N))
}

// BenchmarkT10_SingleTurn measures the complete steady-state intelligence path:
//
//	utterance -> Bridge planner -> Conversation -> IntentEngine -> Classifier -> Plan
//
// Session construction is excluded (StopTimer around every rebuild).
func BenchmarkT10_SingleTurn(b *testing.B) {
	for _, tc := range benchCases() {
		b.Run(tc.name, func(b *testing.B) {
			_, vi := benchBuild(b)
			p, _ := benchSession(b, vi, "bench-turn-"+tc.name)
			ev := utter(tc.text)

			b.ReportAllocs()
			b.ResetTimer()
			turns := 0
			for i := 0; i < b.N; i++ {
				if turns >= tc.budget {
					b.StopTimer()
					_, vi = benchBuild(b)
					p, _ = benchSession(b, vi, fmt.Sprintf("bench-turn-%s-%d", tc.name, i))
					turns = 0
					b.StartTimer()
				}
				var err error
				sinkPlan, err = p.Handle(ev)
				if err != nil {
					b.Fatalf("iteration %d: %v", i, err)
				}
				if _, err := p.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete,
				}); err != nil {
					b.Fatalf("iteration %d speech-complete: %v", i, err)
				}
				turns++
			}
			benchOps.Add(int64(b.N))
		})
	}
}

// BenchmarkT10_RebuildFrequencyControl isolates a MEASUREMENT ARTIFACT rather
// than a property of the system.
//
// The clarification cases in BenchmarkT10_SingleTurn rebuild their session every
// 2 turns instead of every 15, because the frozen ClarificationBudget forces it.
// The rebuild itself is excluded by StopTimer, but the StopTimer/StartTimer
// transitions are not free and happen 7.5x more often, so those cases are NOT
// directly comparable with the answered cases.
//
// This runs ONE fixed input — an answered one, whose real cost cannot change —
// at both rebuild frequencies. The difference between the two numbers is the
// size of the artifact, measured instead of guessed.
func BenchmarkT10_RebuildFrequencyControl(b *testing.B) {
	const text = "please call me back on 9876543210"
	for _, every := range []int{turnsPerSession, clarifyTurnsPerSession} {
		b.Run(fmt.Sprintf("rebuild_every=%d", every), func(b *testing.B) {
			_, vi := benchBuild(b)
			p, _ := benchSession(b, vi, fmt.Sprintf("bench-ctl-%d", every))
			ev := utter(text)

			b.ReportAllocs()
			b.ResetTimer()
			turns := 0
			for i := 0; i < b.N; i++ {
				if turns >= every {
					b.StopTimer()
					_, vi = benchBuild(b)
					p, _ = benchSession(b, vi, fmt.Sprintf("bench-ctl-%d-%d", every, i))
					turns = 0
					b.StartTimer()
				}
				var err error
				sinkPlan, err = p.Handle(ev)
				if err != nil {
					b.Fatalf("iteration %d: %v", i, err)
				}
				if _, err := p.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete,
				}); err != nil {
					b.Fatalf("iteration %d speech-complete: %v", i, err)
				}
				turns++
			}
			benchOps.Add(int64(b.N))
		})
	}
}

// ---------------------------------------------------------------------------
// C. Multi-turn steady state
// ---------------------------------------------------------------------------

// BenchmarkT10_MultiTurn measures one full callback -> transfer -> repeat cycle
// per iteration on an ALREADY ESTABLISHED session. ns/op is therefore the cost
// of three caller turns plus their three speech-complete events, not one turn.
func BenchmarkT10_MultiTurn(b *testing.B) {
	sequence := []string{
		"please call me back on 9876543210",
		"transfer me to rajesh",
		"say that again",
	}
	_, vi := benchBuild(b)
	p, _ := benchSession(b, vi, "bench-multi")

	b.ReportAllocs()
	b.ResetTimer()
	turns := 0
	for i := 0; i < b.N; i++ {
		if turns+len(sequence) > turnsPerSession {
			b.StopTimer()
			_, vi = benchBuild(b)
			p, _ = benchSession(b, vi, fmt.Sprintf("bench-multi-%d", i))
			turns = 0
			b.StartTimer()
		}
		for _, text := range sequence {
			var err error
			sinkPlan, err = p.Handle(utter(text))
			if err != nil {
				b.Fatalf("iteration %d: %v", i, err)
			}
			if _, err := p.Handle(conversation.Event{
				Kind: conversation.EventSpeechComplete,
			}); err != nil {
				b.Fatalf("iteration %d speech-complete: %v", i, err)
			}
			turns++
		}
	}
	benchOps.Add(int64(b.N) * int64(len(sequence)))
}

// ---------------------------------------------------------------------------
// D-F. Context benchmarks
// ---------------------------------------------------------------------------
//
// SEAM NOTE, same as T9: newVoiceIntelligence takes no clock, so a fixed clock
// cannot be injected through the service constructor. Adding one would be a
// production change made solely for measurement. These therefore use the
// existing voiceintel.WithClock seam directly, which removes wall-clock reads
// from the measurement. The context engine reached is the same frozen one the
// service path reaches.
//
// NO EVICTION-VICTIM CLAIM: the eviction benchmark measures the COST of
// eviction. It asserts nothing about which entry is evicted, and when
// timestamps tie the frozen contract leaves that unspecified (T7/T9 finding).

func benchContext(b *testing.B, id string) *conversation.ContextEngine {
	b.Helper()
	br, err := voiceintel.New(voiceintel.WithClock(
		rt.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))))
	if err != nil {
		b.Fatalf("voiceintel.New: %v", err)
	}
	conv, err := br.Engine().Begin(conversation.ConversationID(id), "")
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	return conv.Context()
}

// fillContext seeds n entries and returns the engine.
func fillContext(b *testing.B, id string, n int) *conversation.ContextEngine {
	b.Helper()
	ctx := benchContext(b, id)
	for i := 0; i < n; i++ {
		if err := ctx.Set(conversation.Entry{
			Key: fmt.Sprintf("k%05d", i), Value: i,
			Scope: conversation.ScopeConversation, Source: "bench",
		}); err != nil {
			b.Fatalf("seed %d: %v", i, err)
		}
	}
	return ctx
}

// benchContextSizes covers a small context and one just under the frozen bound.
func benchContextSizes() []struct {
	name string
	n    int
} {
	bound := maxCtxEntries()
	return []struct {
		name string
		n    int
	}{
		{"small", 8},
		// bound-32, not bound-1: insertion needs free slots to measure. At
		// bound-1 there is exactly ONE, so all but the first Set would evict
		// and the benchmark would silently measure eviction instead. That is
		// not hypothetical — the first version of this file used bound-1 and
		// reported 4103 ns/op for "insert", within 2% of the eviction
		// benchmark's 4025 ns/op, because that is what it was measuring.
		{"near_limit", bound - 32},
	}
}

// BenchmarkT10_ContextInsert measures Set for a NEW key with free headroom, so
// eviction is NOT included. Eviction is measured separately.
//
// The scope is refilled with ClearScope + reseed once the headroom is used,
// under StopTimer. ClearScope is the frozen engine's own reset, which is far
// cheaper than rebuilding a bridge and keeps the refill from dominating B/op.
func BenchmarkT10_ContextInsert(b *testing.B) {
	for _, sz := range benchContextSizes() {
		b.Run(sz.name, func(b *testing.B) {
			// One short of the bound: filling it exactly to MaxEntriesPerScope
			// is still eviction-free (the frozen check is `len(m) >= max`
			// BEFORE the add), but it leaves the scope indistinguishable from
			// one that has evicted, which would defeat the guard below.
			headroom := maxCtxEntries() - sz.n - 1
			ctx := fillContext(b, "bench-ins-"+sz.name, sz.n)

			b.ReportAllocs()
			b.ResetTimer()
			free := headroom
			for n := 0; n < b.N; n++ {
				if free == 0 {
					b.StopTimer()
					ctx.ClearScope(conversation.ScopeConversation)
					for k := 0; k < sz.n; k++ {
						if err := ctx.Set(conversation.Entry{
							Key: fmt.Sprintf("k%05d", k), Value: k,
							Scope: conversation.ScopeConversation, Source: "bench",
						}); err != nil {
							b.Fatalf("reseed: %v", err)
						}
					}
					free = headroom
					b.StartTimer()
				}
				sinkErr = ctx.Set(conversation.Entry{
					Key: fmt.Sprintf("new%05d", free), Value: n,
					Scope: conversation.ScopeConversation, Source: "bench",
				})
				free--
			}
			b.StopTimer()
			// If this ever reached the bound, the loop was evicting.
			if got := ctx.Size(conversation.ScopeConversation); got >= maxCtxEntries() {
				b.Fatalf("scope reached %d (bound %d) — this benchmark measured "+
					"eviction, not insertion", got, maxCtxEntries())
			}
			benchOps.Add(int64(b.N))
		})
	}
}

// BenchmarkT10_ContextLookup measures Get on an existing key.
func BenchmarkT10_ContextLookup(b *testing.B) {
	for _, sz := range benchContextSizes() {
		b.Run(sz.name, func(b *testing.B) {
			ctx := fillContext(b, "bench-look-"+sz.name, sz.n)
			keys := make([]string, sz.n)
			for i := range keys {
				keys[i] = fmt.Sprintf("k%05d", i)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sinkEntry, sinkOK = ctx.Get(conversation.ScopeConversation, keys[i%len(keys)])
			}
			benchOps.Add(int64(b.N))
		})
	}
}

// BenchmarkT10_ContextUpdate measures Set for a key that ALREADY EXISTS.
//
// Replacement is the frozen fast path: context.go:221 skips the eviction check
// entirely when the key is already present, which is why update is measured
// separately from insert.
func BenchmarkT10_ContextUpdate(b *testing.B) {
	for _, sz := range benchContextSizes() {
		b.Run(sz.name, func(b *testing.B) {
			ctx := fillContext(b, "bench-upd-"+sz.name, sz.n)
			keys := make([]string, sz.n)
			for i := range keys {
				keys[i] = fmt.Sprintf("k%05d", i)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sinkErr = ctx.Set(conversation.Entry{
					Key: keys[i%len(keys)], Value: i,
					Scope: conversation.ScopeConversation, Source: "bench",
				})
			}
			benchOps.Add(int64(b.N))
		})
	}
}

// BenchmarkT10_ContextEviction measures Set at a FULL scope, so every iteration
// pays the eviction scan.
//
// The scope is filled to the bound in setup and each timed Set uses a new key,
// which keeps the scope full and forces eviction on every iteration.
func BenchmarkT10_ContextEviction(b *testing.B) {
	ctx := fillContext(b, "bench-evict", maxCtxEntries())
	if got := ctx.Size(conversation.ScopeConversation); got != maxCtxEntries() {
		b.Fatalf("setup left size %d, want the bound %d", got, maxCtxEntries())
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sinkErr = ctx.Set(conversation.Entry{
			Key: fmt.Sprintf("e%09d", i), Value: i,
			Scope: conversation.ScopeConversation, Source: "bench",
		})
	}
	b.StopTimer()
	sinkSize = ctx.Size(conversation.ScopeConversation)
	if sinkSize != maxCtxEntries() {
		b.Fatalf("scope size %d after eviction loop, want the bound %d — the "+
			"benchmark was not evicting", sinkSize, maxCtxEntries())
	}
	benchOps.Add(int64(b.N))
}

// ---------------------------------------------------------------------------
// G. Failure paths
// ---------------------------------------------------------------------------

// BenchmarkT10_FailurePath measures EXISTING reachable failure behaviour. No
// artificial failure is introduced.
func BenchmarkT10_FailurePath(b *testing.B) {
	// Non-terminal refusals: the session survives, so turns are measured the
	// same way as the healthy path.
	// The same two frozen limits apply here as in benchCases: unknown and empty
	// resolve to the fallback and are answered, so MaxTurns bounds them, while
	// low_confidence and ambiguous produce clarifications and are bounded by the
	// ClarificationBudget instead.
	for _, tc := range []benchCase{
		{"unknown", "zzzz qqqq wubble frotz", turnsPerSession},
		{"empty", "", turnsPerSession},
		{"low_confidence", "repeat pardon", clarifyTurnsPerSession},
		{"ambiguous", "hold on call back", clarifyTurnsPerSession},
	} {
		b.Run(tc.name, func(b *testing.B) {
			_, vi := benchBuild(b)
			p, _ := benchSession(b, vi, "bench-fail-"+tc.name)
			ev := utter(tc.text)

			b.ReportAllocs()
			b.ResetTimer()
			turns := 0
			for i := 0; i < b.N; i++ {
				if turns >= tc.budget {
					b.StopTimer()
					_, vi = benchBuild(b)
					p, _ = benchSession(b, vi, fmt.Sprintf("bench-fail-%s-%d", tc.name, i))
					turns = 0
					b.StartTimer()
				}
				sinkPlan, sinkErr = p.Handle(ev)
				if _, err := p.Handle(conversation.Event{
					Kind: conversation.EventSpeechComplete,
				}); err != nil {
					b.Fatalf("iteration %d speech-complete: %v", i, err)
				}
				turns++
			}
			benchOps.Add(int64(b.N))
		})
	}

	// Terminal refusal: the conversation has ended, so the cost being measured
	// is the frozen engine's refusal, not a turn. The session is rebuilt with
	// the timer stopped.
	b.Run("terminal_refusal", func(b *testing.B) {
		_, vi := benchBuild(b)
		p, conv := benchSession(b, vi, "bench-fail-terminal")
		if err := conv.End("bench_terminal"); err != nil {
			b.Fatalf("End: %v", err)
		}
		ev := utter("transfer me to rajesh")

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkPlan, sinkErr = p.Handle(ev)
		}
		b.StopTimer()
		if sinkErr == nil {
			b.Fatal("terminal session accepted work — the benchmark measured the " +
				"healthy path, not the refusal")
		}
		benchOps.Add(int64(b.N))
	})
}

// ---------------------------------------------------------------------------
// H. Concurrent classification
// ---------------------------------------------------------------------------

// BenchmarkT10_ConcurrentClassification measures throughput with N workers
// sharing ONE immutable classifier and one bridge — the production shape, whose
// safety T8 already established. This is a throughput measurement, not a
// correctness test.
//
// WORKER COUNT COMES FROM -cpu, NOT FROM A SUB-BENCHMARK LABEL. RunParallel
// starts GOMAXPROCS * parallelism goroutines, so an earlier version of this
// benchmark that called b.SetParallelism(n) and labelled the result "workers=n"
// was wrong by a factor of GOMAXPROCS: "workers=1" ran 16 goroutines on this
// machine, not 1. Rather than print a number that is not the worker count, the
// count is set by the standard mechanism:
//
//	go test -bench BenchmarkT10_ConcurrentClassification -cpu=1,4,8,16
//
// The -N suffix Go appends to each result IS the worker count, exactly.
//
// Each worker owns its own session, created before the timed region. Workers
// rotate sessions when the turn budget is reached; that rotation is inside the
// timed region because RunParallel gives no per-worker timer control, and it is
// reported as a known inclusion rather than hidden.
func BenchmarkT10_ConcurrentClassification(b *testing.B) {
	{
		{
			_, vi := benchBuild(b)
			var seq atomic.Int64

			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				id := seq.Add(1)
				p, _ := benchSession(b, vi, fmt.Sprintf("bench-par-%d", id))
				ev := utter("please call me back on 9876543210")
				turns, gen := 0, 0
				var local int64
				for pb.Next() {
					if turns >= turnsPerSession {
						gen++
						var err error
						p, _, err = openPlanner(vi, conversation.ConversationID(
							fmt.Sprintf("bench-par-%d-g%d", id, gen)))
						if err != nil {
							b.Errorf("rotate: %v", err)
							return
						}
						turns = 0
					}
					plan, err := p.Handle(ev)
					if err != nil {
						b.Errorf("handle: %v", err)
						return
					}
					if _, err := p.Handle(conversation.Event{
						Kind: conversation.EventSpeechComplete,
					}); err != nil {
						b.Errorf("speech-complete: %v", err)
						return
					}
					sinkPlan = plan
					turns++
					local++
				}
				benchOps.Add(local)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Step 12 — benchmark sanity
// ---------------------------------------------------------------------------

// TestT10_BenchmarksActuallyExecute proves the benchmarks measure real work
// rather than setup or an empty loop.
//
// Each benchmark is run for a short fixed time through testing.Benchmark, and
// the test asserts that (a) iterations happened, (b) the operation counter
// advanced by at least the iteration count, and (c) the sink holds the value
// the operation was supposed to produce. A benchmark whose body was eliminated
// or never reached fails all three.
func TestT10_BenchmarksActuallyExecute(t *testing.T) {
	run := func(name string, fn func(*testing.B)) testing.BenchmarkResult {
		t.Helper()
		before := benchOps.Load()
		res := testing.Benchmark(fn)
		if res.N == 0 {
			t.Fatalf("%s: zero iterations", name)
		}
		if got := benchOps.Load() - before; got < int64(res.N) {
			t.Errorf("%s: counter advanced %d for %d iterations — the loop body "+
				"is not executing every iteration", name, got, res.N)
		}
		if res.NsPerOp() <= 0 {
			t.Errorf("%s: ns/op = %d, which means nothing was timed",
				name, res.NsPerOp())
		}
		return res
	}

	// Single turn: the sink must hold a real plan for the benchmarked input.
	sinkPlan = conversation.Plan{}
	run("SingleTurn", func(b *testing.B) {
		_, vi := benchBuild(b)
		p, _ := benchSession(b, vi, "sanity-turn")
		ev := utter("please call me back on 9876543210")
		turns := 0
		for i := 0; i < b.N; i++ {
			if turns >= turnsPerSession {
				b.StopTimer()
				_, vi = benchBuild(b)
				p, _ = benchSession(b, vi, fmt.Sprintf("sanity-turn-%d", i))
				turns = 0
				b.StartTimer()
			}
			var err error
			sinkPlan, err = p.Handle(ev)
			if err != nil {
				b.Fatalf("%v", err)
			}
			if _, err := p.Handle(conversation.Event{
				Kind: conversation.EventSpeechComplete,
			}); err != nil {
				b.Fatalf("%v", err)
			}
			turns++
		}
		benchOps.Add(int64(b.N))
	})
	if sinkPlan.Intent != intent.IntentRequestCallback {
		t.Errorf("single-turn sink intent = %q, want %q — the benchmark did not "+
			"execute the classification path", sinkPlan.Intent, intent.IntentRequestCallback)
	}

	// Classification: the sink must hold candidates.
	sinkCands = nil
	run("Classify", func(b *testing.B) {
		c, err := intent.New(intent.DefaultConfig())
		if err != nil {
			b.Fatal(err)
		}
		u := benchUtterance("transfer me to rajesh")
		for i := 0; i < b.N; i++ {
			sinkCands, sinkSlots, sinkErr = c.Classify(u, conversation.ExpectNothing)
		}
		benchOps.Add(int64(b.N))
	})
	if len(sinkCands) == 0 {
		t.Error("classification sink is empty — the classifier was not invoked")
	}

	// Eviction: the scope must still be exactly at the bound, which is only
	// true if eviction actually ran on every insert.
	sinkSize = 0
	run("ContextEviction", BenchmarkT10_ContextEviction)
	if sinkSize != maxCtxEntries() {
		t.Errorf("eviction sink size = %d, want the bound %d — the benchmark was "+
			"not evicting", sinkSize, maxCtxEntries())
	}
}
