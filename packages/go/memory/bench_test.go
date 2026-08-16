package memory

import (
	"fmt"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

// Benchmarks for the memory hot path. Every number in
// docs/memory/PERFORMANCE.md comes from these, on the machine named there.
//
// The engine is in-process and in-memory, so these measure the engine's own
// cost with no network, no broker and no database. A production deployment adds
// a durable store behind this; that latency is Phase 10D and is not modelled.

func benchHarness(b *testing.B) *Harness {
	b.Helper()
	h, err := NewHarness()
	if err != nil {
		b.Fatal(err)
	}
	return h
}

// storeBudgetHeadroom is how many records an insert benchmark may add to one
// subject before it must start over with a fresh store.
//
// DefaultPolicy caps a subject at 50,000 records (INV-MEM: MaxRecordsPerSubject),
// and an insert benchmark writes b.N records under a single subject. Go chooses
// b.N by timing, so at roughly 1.4 µs per store, `-benchtime=100ms` lands near
// 71,000 iterations and the benchmark fails with "memory: budget exceeded" —
// the engine correctly enforcing its own invariant against a benchmark that
// ignored it.
//
// This went unnoticed because these benchmarks had only ever been run at the
// default `-benchtime=1s`-with-small-N or explicit low counts.
// BenchmarkStoreWithAttributes was passing at 47,667 iterations, which is under
// the cap by luck rather than by design. Found in Phase 10.5 when CI began
// running the suite at a fixed `-benchtime`.
const storeBudgetHeadroom = 40_000

// resetStore returns a fresh store when the subject cap is close.
//
// The timer is stopped around the reset so the cost of building a harness never
// enters the measurement. The alternative — reusing keys so records update
// instead of accumulating — would silently change what these benchmarks measure
// from insert to update, which is a different and much cheaper path.
func resetStore(b *testing.B, i int, st *Store, h *Harness) (*Store, *Harness) {
	if i == 0 || i%storeBudgetHeadroom != 0 {
		return st, h
	}
	b.StopTimer()
	fresh := benchHarness(b)
	b.StartTimer()
	return fresh.Assistant().Store, fresh
}

func BenchmarkStore(b *testing.B) {
	h := benchHarness(b)
	st := h.Assistant().Store

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st, h = resetStore(b, i, st, h)
		r := InternalRecord("s", KindConversation, fmt.Sprintf("k%08d", i), "payload data here")
		if _, err := st.Store(r); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStoreWithAttributes measures the secondary-index maintenance cost,
// which is what a record with conversation and session bindings actually pays.
func BenchmarkStoreWithAttributes(b *testing.B) {
	h := benchHarness(b)
	st := h.Assistant().Store

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st, h = resetStore(b, i, st, h)
		r := WithAttributes(
			InternalRecord("s", KindConversation, fmt.Sprintf("k%08d", i), "payload"),
			map[string]string{AttrConversation: "conv-1", AttrSession: "sess-1"})
		if _, err := st.Store(r); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRetrieve is the headline read number. It takes the shard WRITE lock
// because a read updates access statistics — see store.go Touch.
func BenchmarkRetrieve(b *testing.B) {
	h := benchHarness(b)
	st := h.Assistant().Store
	r, _ := st.Store(InternalRecord("s", KindUser, "hot", "payload"))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.Retrieve(r.Key, ""); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRetrieveParallel measures the read path under contention. This is
// the number that decides whether the write-lock-on-read design holds.
func BenchmarkRetrieveParallel(b *testing.B) {
	h := benchHarness(b)
	st := h.Assistant().Store

	keys := make([]Key, 256)
	for i := range keys {
		r, _ := st.Store(InternalRecord("s", KindUser, fmt.Sprintf("k%03d", i), "payload"))
		keys[i] = r.Key
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = st.Retrieve(keys[i&255], "")
			i++
		}
	})
}

// BenchmarkRetrieveContended is the pathological case: every goroutine reading
// ONE key, so every read contends for the same shard lock.
func BenchmarkRetrieveContended(b *testing.B) {
	h := benchHarness(b)
	st := h.Assistant().Store
	r, _ := st.Store(InternalRecord("s", KindUser, "single", "payload"))

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = st.Retrieve(r.Key, "")
		}
	})
}

func BenchmarkUpdateCAS(b *testing.B) {
	h := benchHarness(b)
	st := h.Assistant().Store
	r, _ := st.Store(InternalRecord("s", KindUser, "k", "v"))
	version := r.Version

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		updated, err := st.Update(r.Key, version, func(rec *Record) error { return nil })
		if err != nil {
			b.Fatal(err)
		}
		version = updated.Version
	}
}

func BenchmarkIndexByAttribute(b *testing.B) {
	h := benchHarness(b)
	st := h.Assistant().Store
	for i := 0; i < 100; i++ {
		_, _ = st.Store(WithAttributes(
			InternalRecord("s", KindConversation, fmt.Sprintf("k%03d", i), "v"),
			map[string]string{AttrConversation: "conv-1"}))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = st.Index().ByConversation("conv-1")
	}
}

func BenchmarkIndexByTimeRange(b *testing.B) {
	h := benchHarness(b)
	st := h.Assistant().Store
	start := h.Clock.Now()
	for i := 0; i < 500; i++ {
		_, _ = st.Store(InternalRecord("s", KindConversation, fmt.Sprintf("k%04d", i), "v"))
		h.Clock.Advance(time.Second)
	}
	end := h.Clock.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = st.Index().ByTimeRange(start, end)
	}
}

// BenchmarkIndexBySubject measures the deliberately-unindexed full scan. It is
// the cost erasure and diagnostics pay so that every write does not maintain a
// subject index.
func BenchmarkIndexBySubject(b *testing.B) {
	h := benchHarness(b)
	st := h.Assistant().Store
	for i := 0; i < 1000; i++ {
		_, _ = st.Store(InternalRecord(
			SubjectID(fmt.Sprintf("s%02d", i%20)), KindConversation,
			fmt.Sprintf("k%04d", i), "v"))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = st.Index().BySubject("s05")
	}
}

func BenchmarkSearchByKind(b *testing.B) {
	h := benchHarness(b)
	bundle := h.Assistant()
	for i := 0; i < 200; i++ {
		_, _ = bundle.Store.Store(InternalRecord("s", KindConversation,
			fmt.Sprintf("k%03d", i), "payload"))
	}
	q := Query{Subject: "s", Kinds: []Kind{KindConversation},
		MaxSensitivity: Internal, Limit: 20}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := bundle.Retriever.Search(q, ""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPromotionEvaluate(b *testing.B) {
	p := DefaultPromotionPolicy()
	now := time.Now()
	r := &Record{Tier: TierWorking, State: StateActive, AccessCount: 3,
		CreatedAt: now.Add(-time.Minute), AccessedAt: now}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.Evaluate(r, now)
	}
}

func BenchmarkPolicyAdmit(b *testing.B) {
	p := DefaultPolicy()
	p.Auditor = NoopAuditor{}
	r := InternalRecord("s", KindUser, "k", "payload")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := p.admit(r); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSweep measures one maintenance pass over 10,000 records. It runs on
// a timer and must not starve the request path.
func BenchmarkSweep(b *testing.B) {
	h := benchHarness(b)
	st := h.Assistant().Store
	for i := 0; i < 10_000; i++ {
		_, _ = st.Store(InternalRecord(
			SubjectID(fmt.Sprintf("s%03d", i%100)), KindConversation,
			fmt.Sprintf("k%05d", i), "v"))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Runtime.Sweep()
	}
}

func BenchmarkContextBuild(b *testing.B) {
	h := benchHarness(b)
	bundle := h.Assistant()

	pol := NewPolicy("s", "limits", Value{ContentType: "text/plain", Data: []byte("max=3")})
	pol.Provenance = Provenance{Source: "bench"}
	_, _ = bundle.Store.Store(pol)
	pref := NewPreference("s", "lang", Value{ContentType: "text/plain", Data: []byte("hi-IN")}, Internal)
	pref.Provenance = Provenance{Source: "bench"}
	_, _ = bundle.Store.Store(pref)
	for i := 0; i < 50; i++ {
		_, _ = bundle.Store.Store(InternalRecord("s", KindConversation,
			fmt.Sprintf("c%03d", i), "a conversational message of some length"))
	}

	req := ContextRequest{Subject: "s", Budget: TokenBudget{MaxTokens: 2048},
		MaxSensitivity: Internal}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := bundle.Context.Build(req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompressionPlan(b *testing.B) {
	h := benchHarness(b)
	bundle := h.Assistant()
	for i := 0; i < 100; i++ {
		_, _ = bundle.Store.Store(InternalRecord("s", KindConversation,
			fmt.Sprintf("m%03d", i), "message body"))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bundle.Compressor.Plan("s", KindConversation, TriggerCount)
	}
}

func BenchmarkTokenEstimateLatin(b *testing.B) {
	est := DefaultTokenEstimator()
	data := []byte("the quick brown fox jumps over the lazy dog, repeatedly and at length")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = est.Estimate(data)
	}
}

func BenchmarkTokenEstimateDevanagari(b *testing.B) {
	est := DefaultTokenEstimator()
	data := []byte("नमस्ते दुनिया यह एक परीक्षण संदेश है जो काफी लंबा है")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = est.Estimate(data)
	}
}

func BenchmarkRecordClone(b *testing.B) {
	r := WithAttributes(InternalRecord("s", KindUser, "k", "a reasonably sized payload"),
		map[string]string{AttrConversation: "c1", AttrSession: "s1"})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Clone()
	}
}

func BenchmarkEventDispatch(b *testing.B) {
	m := NewMetrics()
	d := NewDispatcher(m, NoopPublisher{})
	e := Event{Type: EventCreated, Key: Key{Subject: "s", Kind: KindUser, Name: "k"},
		Tier: TierShortTerm, Version: 1}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Dispatch(e)
	}
}

func BenchmarkForgetSubject(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		h, _ := NewHarness()
		st := h.Assistant().Store
		for j := 0; j < 100; j++ {
			_, _ = st.Store(InternalRecord("victim", KindConversation,
				fmt.Sprintf("k%03d", j), "v"))
		}
		b.StartTimer()

		if _, err := h.Runtime.Coordinator().Forget("victim", "dpo"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFakeClockOverhead(b *testing.B) {
	c := rt.NewFakeClock(time.Time{})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Now()
	}
}
