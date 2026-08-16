package toolruntime

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Benchmarks for the tool runtime's hot path. Every number in
// docs/tools/PERFORMANCE.md comes from these, on the machine named there.
//
// WHAT THESE DO AND DO NOT MEASURE. The tool is a fake that returns
// immediately, so these measure the RUNTIME'S OWN COST — planning, permission,
// validation, idempotency, admission, dispatch, events, audit — and nothing
// else. A real tool call is network work measured in milliseconds and will
// dominate every number here by three orders of magnitude. That is the point:
// the runtime's overhead has to be invisible next to the work it governs, and
// these say whether it is.

func benchHarness(b *testing.B) *Harness {
	b.Helper()
	h, err := NewHarness()
	if err != nil {
		b.Fatal(err)
	}
	return h
}

func benchIntent(cap CapabilityID) ToolIntent {
	return ToolIntent{
		ID: NewIntentID(), Correlation: NewCorrelationID(), Session: "s", Actor: "a",
		Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{{Ref: "only", Capability: cap,
			Version: AnyVersion(), Args: Arguments{"query": String("benchmark query")}}},
	}
}

// BenchmarkExecuteSingle is the headline number: one whole plan, end to end.
func BenchmarkExecuteSingle(b *testing.B) {
	h := benchHarness(b)
	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{})
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Runtime.Execute(ctx, benchIntent("cap")); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkExecuteSingleParallel measures the same path under contention. This
// is the number that decides whether the runtime is a bottleneck at scale.
func BenchmarkExecuteSingleParallel(b *testing.B) {
	cfg := DefaultConfig()
	cfg.DefaultToolConcurrency = 1024
	cfg.SandboxSlots = 1024
	cfg.Scheduler.MaxConcurrent = 1024
	h, err := NewHarness(WithHarnessConfig(cfg))
	if err != nil {
		b.Fatal(err)
	}
	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{})
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := h.Runtime.Execute(ctx, benchIntent("cap")); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkExecuteMutating adds the idempotency ledger to the path. The
// difference from BenchmarkExecuteSingle is what deduplication costs.
func BenchmarkExecuteMutating(b *testing.B) {
	h := benchHarness(b)
	h.Register(WriteContract("w", "1.0.0", "write"), &CompensatingFake{})
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		intent := ToolIntent{ID: NewIntentID(), Correlation: NewCorrelationID(), Actor: "a",
			Grant: Grant{Actor: "a"},
			Requests: []CapabilityRequest{{Ref: "w", Capability: "write", Version: AnyVersion(),
				Args: Arguments{"subject": String(fmt.Sprintf("s%08d", i))}}}}
		if _, err := h.Runtime.Execute(ctx, intent); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReplay measures a deduplicated execution: the ledger answers and no
// tool is entered. It should be far cheaper than an execution, or deduplication
// is not buying anything.
func BenchmarkReplay(b *testing.B) {
	h := benchHarness(b)
	h.Register(WriteContract("w", "1.0.0", "write"), &CompensatingFake{})
	ctx := context.Background()

	corr := NewCorrelationID()
	intent := ToolIntent{ID: NewIntentID(), Correlation: corr, Actor: "a",
		Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{{Ref: "w", Capability: "write", Version: AnyVersion(),
			Args: Arguments{"subject": String("fixed")}}}}
	if _, err := h.Runtime.Execute(ctx, intent); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Runtime.Execute(ctx, intent); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPlanSingle(b *testing.B) {
	h := benchHarness(b)
	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{})
	intent := benchIntent("cap")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Runtime.Plan(intent); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPlanFiveStep measures planning a dependency graph: two parallel
// levels plus a join.
func BenchmarkPlanFiveStep(b *testing.B) {
	h := benchHarness(b)
	for i := 0; i < 5; i++ {
		h.Register(ReadContract(ToolID(fmt.Sprintf("t%d", i)), "1.0.0",
			CapabilityID(fmt.Sprintf("cap%d", i))), &FakeTool{})
	}

	intent := ToolIntent{ID: NewIntentID(), Actor: "a", Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "a", Capability: "cap0", Version: AnyVersion(), Args: Arguments{"query": String("a")}},
			{Ref: "b", Capability: "cap1", Version: AnyVersion(), Args: Arguments{"query": String("b")}},
			{Ref: "c", Capability: "cap2", Version: AnyVersion(), Args: Arguments{"query": String("c")},
				DependsOn: []string{"a"}},
			{Ref: "d", Capability: "cap3", Version: AnyVersion(), Args: Arguments{"query": String("d")},
				DependsOn: []string{"b"}},
			{Ref: "e", Capability: "cap4", Version: AnyVersion(), Args: Arguments{"query": String("e")},
				DependsOn: []string{"c", "d"}},
		}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Runtime.Plan(intent); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDiscoveryResolve measures capability resolution against a registry
// holding fifty registrations, which is a realistic mid-life platform.
func BenchmarkDiscoveryResolve(b *testing.B) {
	h := benchHarness(b)
	for i := 0; i < 50; i++ {
		h.Register(ReadContract(ToolID(fmt.Sprintf("t%02d", i)),
			Version(fmt.Sprintf("1.%d.0", i)), "cap"), &FakeTool{})
	}
	req := Request{Capability: "cap", Version: AnyVersion()}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.Runtime.Discovery().Resolve(req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRegistryGet(b *testing.B) {
	h := benchHarness(b)
	c := ReadContract("t", "1.0.0", "cap")
	h.Register(c, &FakeTool{})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := h.Runtime.Registry().Get(c.Descriptor); !ok {
			b.Fatal("missing")
		}
	}
}

// BenchmarkRegistryRegister measures the copy-on-write write path, which is the
// cost the read path's lock-free access is paid for with.
func BenchmarkRegistryRegister(b *testing.B) {
	h := benchHarness(b)
	for i := 0; i < 50; i++ {
		h.Register(ReadContract(ToolID(fmt.Sprintf("t%02d", i)), "1.0.0", "cap"), &FakeTool{})
	}
	reg := Registration{Contract: ReadContract("churn", "1.0.0", "cap"),
		Tool: &FakeTool{}, Lifecycle: LifecycleActive, Health: HealthHealthy}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reg.Contract.Descriptor.Version = Version(fmt.Sprintf("1.0.%d", i%100))
		if err := h.Runtime.Registry().Register(reg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPermissionEvaluate(b *testing.B) {
	h := benchHarness(b)
	if err := h.Runtime.Permissions().DefineRole("r", "p1", "p2", "p3"); err != nil {
		b.Fatal(err)
	}
	c := ReadContract("t", "1.0.0", "cap")
	c.RequiredPermissions = []Permission{"p1", "p2"}
	g := Grant{Actor: "a", Roles: []string{"r"}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if d := h.Runtime.Permissions().Evaluate(c, g); !d.Allowed {
			b.Fatal("denied")
		}
	}
}

func BenchmarkValidateInput(b *testing.B) {
	c := ReadContract("t", "1.0.0", "cap")
	args := Arguments{"query": String("a reasonably typical query string")}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.ValidateInput(args); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDeriveKey(b *testing.B) {
	d := Descriptor{Tool: "t", Version: "1.0.0"}
	args := Arguments{
		"query": String("a reasonably typical query string"),
		"limit": Int(20),
		"opts":  Map(map[string]Value{"deep": Bool(true), "locale": String("hi-IN")}),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = DeriveKey(d, args, "actor", "correlation")
	}
}

func BenchmarkArgumentsFingerprint(b *testing.B) {
	args := Arguments{
		"query": String("a reasonably typical query string"),
		"limit": Int(20),
		"opts":  Map(map[string]Value{"deep": Bool(true), "locale": String("hi-IN")}),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = args.Fingerprint()
	}
}

func BenchmarkLedgerClaimSettle(b *testing.B) {
	h := benchHarness(b)
	l := h.Runtime.Ledger()
	d := Descriptor{Tool: "t", Version: "1.0.0"}
	out := Result{"reference": String("r")}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := IdempotencyKey(fmt.Sprintf("k%09d", i))
		if _, _, err := l.Claim(key, d, "e"); err != nil {
			b.Fatal(err)
		}
		l.Settle(key, out, "completed", true)
	}
}

func BenchmarkSchedulerAcquireRelease(b *testing.B) {
	s, err := NewToolScheduler(DefaultSchedulerConfig(), nil, NewMetrics())
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		release, err := s.Acquire(ctx, ClassInteractive)
		if err != nil {
			b.Fatal(err)
		}
		release()
	}
}

func BenchmarkSchedulerAcquireContended(b *testing.B) {
	s, err := NewToolScheduler(DefaultSchedulerConfig(), nil, NewMetrics())
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			release, err := s.Acquire(ctx, ClassInteractive)
			if err != nil {
				b.Fatal(err)
			}
			release()
		}
	})
}

func BenchmarkSandboxEnterRelease(b *testing.B) {
	s := NewBudgetSandbox(1024, 1024)
	d := Descriptor{Tool: "t", Version: "1.0.0"}
	budget := DefaultBudget()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lease, err := s.Enter(d, budget)
		if err != nil {
			b.Fatal(err)
		}
		lease.Release()
	}
}

func BenchmarkEventDispatch(b *testing.B) {
	m := NewMetrics()
	d := NewEventDispatcher(m, NoopPublisher{}, time.Now)
	e := Event{Type: EventCompleted, Execution: "e", Descriptor: Descriptor{Tool: "t", Version: "1.0.0"}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Dispatch(e)
	}
}

func BenchmarkAuditRecord(b *testing.B) {
	a := NewRecordingAuditor(1 << 20)
	entry := AuditEntry{Kind: AuditExecutionCompleted, Execution: "e",
		Descriptor: Descriptor{Tool: "t", Version: "1.0.0"}, Phase: PhaseComplete}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := a.Record(entry); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStreamChunk measures the per-chunk cost on the streaming path,
// including the budget charge. A streaming tool emitting a chunk per token
// pays this per token.
func BenchmarkStreamChunk(b *testing.B) {
	s := NewBudgetSandbox(4, 4)
	lease, err := s.Enter(Descriptor{Tool: "t", Version: "1.0.0"}, Budget{OutputBytes: 0, Slots: 1})
	if err != nil {
		b.Fatal(err)
	}
	sink := newMeteredSink(NoopSink{}, lease, time.Now)
	chunk := Chunk{Kind: ChunkPartial, Fields: Result{"answer": String("a partial fragment")}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sink.Write(chunk); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPlanFingerprint(b *testing.B) {
	h := benchHarness(b)
	for i := 0; i < 3; i++ {
		h.Register(ReadContract(ToolID(fmt.Sprintf("t%d", i)), "1.0.0",
			CapabilityID(fmt.Sprintf("cap%d", i))), &FakeTool{})
	}
	intent := ToolIntent{ID: NewIntentID(), Actor: "a", Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "a", Capability: "cap0", Version: AnyVersion(), Args: Arguments{"query": String("a")}},
			{Ref: "b", Capability: "cap1", Version: AnyVersion(), Args: Arguments{"query": String("b")}},
			{Ref: "c", Capability: "cap2", Version: AnyVersion(), Args: Arguments{"query": String("c")},
				DependsOn: []string{"a", "b"}},
		}}
	plan, err := h.Runtime.Plan(intent)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = plan.Fingerprint()
	}
}

func BenchmarkValueCanonicalLarge(b *testing.B) {
	args := Arguments{"blob": String(strings.Repeat("x", 4096)),
		"list": List(String("a"), String("b"), String("c"), Int(1), Bool(true))}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = args.canonicalBytes()
	}
}
