package toolruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func newHarness(t *testing.T) *Harness {
	t.Helper()
	h, err := NewHarness()
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return h
}

// ---------------------------------------------------------------------------
// Values
// ---------------------------------------------------------------------------

// TestValue_FingerprintIgnoresMapOrder is the property every other guarantee in
// this module rests on. If argument fingerprints depended on map iteration
// order, idempotency keys would stop deduplicating and two audit records of the
// same call would look like two different calls.
func TestValue_FingerprintIgnoresMapOrder(t *testing.T) {
	t.Parallel()

	a := Arguments{
		"zulu":  String("z"),
		"alpha": Int(1),
		"mid":   Map(map[string]Value{"b": Bool(true), "a": Float(1.5)}),
	}
	b := Arguments{
		"mid":   Map(map[string]Value{"a": Float(1.5), "b": Bool(true)}),
		"alpha": Int(1),
		"zulu":  String("z"),
	}

	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("same arguments fingerprinted differently: %s vs %s",
			a.Fingerprint(), b.Fingerprint())
	}

	// Repeated over many runs, because a single agreement could be luck with
	// one map layout.
	first := a.Fingerprint()
	for i := 0; i < 200; i++ {
		if got := a.Fingerprint(); got != first {
			t.Fatalf("fingerprint drifted on run %d: %s != %s", i, got, first)
		}
	}
}

// TestValue_DistinctValuesDoNotCollide checks the encoding actually
// distinguishes things a naive encoder would confuse.
func TestValue_DistinctValuesDoNotCollide(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b Arguments
	}{
		{"string vs int", Arguments{"k": String("1")}, Arguments{"k": Int(1)}},
		{"int vs float", Arguments{"k": Int(1)}, Arguments{"k": Float(1)}},
		{"null vs absent", Arguments{"k": Null()}, Arguments{}},
		{"list join ambiguity",
			Arguments{"k": List(String("a"), String("b"))},
			Arguments{"k": List(String("a,b"))}},
		{"key/value swap", Arguments{"a": String("b")}, Arguments{"b": String("a")}},
	}

	for _, tc := range cases {
		if tc.a.Fingerprint() == tc.b.Fingerprint() {
			t.Errorf("%s: distinct arguments share a fingerprint %s", tc.name, tc.a.Fingerprint())
		}
	}
}

// TestValue_IsImmutable checks that a caller cannot change a value after it has
// been fingerprinted, which would make the audit record a story rather than a
// record.
func TestValue_IsImmutable(t *testing.T) {
	t.Parallel()

	buf := []byte("secret")
	v := Bytes(buf)
	before := Arguments{"k": v}.Fingerprint()

	buf[0] = 'S' // mutate the caller's backing array
	if after := (Arguments{"k": v}).Fingerprint(); after != before {
		t.Fatal("mutating the caller's slice changed a stored value")
	}

	out, _ := v.Blob()
	out[0] = 'X' // mutate what the accessor returned
	if after := (Arguments{"k": v}).Fingerprint(); after != before {
		t.Fatal("mutating an accessor's return changed the stored value")
	}
}

func TestVersion_CompareAndConstraints(t *testing.T) {
	t.Parallel()

	if Version("1.2.3").Compare("1.10.0") >= 0 {
		t.Error("1.2.3 should sort below 1.10.0; string comparison would say otherwise")
	}
	if !ExactVersion("1.2.3").Satisfies("1.2.3") || ExactVersion("1.2.3").Satisfies("1.2.4") {
		t.Error("exact constraint is wrong")
	}
	if !MajorVersion(2).Satisfies("2.9.9") || MajorVersion(2).Satisfies("3.0.0") {
		t.Error("major constraint is wrong")
	}
	if !AnyVersion().Satisfies("0.0.1") {
		t.Error("any constraint should accept anything")
	}
	// A malformed version sorts below every well-formed one rather than
	// erroring, so one bad registry entry cannot stop the other nine sorting.
	if Version("not-a-version").Compare("0.0.1") >= 0 {
		t.Error("a malformed version should sort below a well-formed one")
	}
}

// ---------------------------------------------------------------------------
// Contracts
// ---------------------------------------------------------------------------

func TestContract_ValidationCatchesContradictions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		mutate   func(*Contract)
		contains string
	}{
		{"no capability", func(c *Contract) { c.Capabilities = nil }, "capability is required"},
		{"no owner", func(c *Contract) { c.Owner = "" }, "owner is required"},
		{"no timeout", func(c *Contract) { c.Timeout = 0 }, "timeout is required"},
		{"timeout over cap", func(c *Contract) { c.Timeout = time.Hour }, "exceeds the runtime maximum"},
		{"required with default", func(c *Contract) {
			c.Input = []FieldSpec{{Name: "q", Kind: ValueString, Required: true,
				HasDefault: true, Default: String("x")}}
		}, "required fields cannot have a default"},
		{"irreversible and compensable", func(c *Contract) {
			c.Effect = EffectIrreversible
			c.Compensable = true
		}, "irreversible effects cannot be compensable"},
		{"irreversible with retries", func(c *Contract) {
			c.Effect = EffectIrreversible
			c.Retry = RetrySpec{MaxAttempts: 3}
		}, "must not be retried automatically"},
		{"enum on non-string", func(c *Contract) {
			c.Input = []FieldSpec{{Name: "n", Kind: ValueInt, Enum: []string{"a"}}}
		}, "Enum applies to string fields only"},
	}

	for _, tc := range cases {
		c := ReadContract("t", "1.0.0", "cap")
		tc.mutate(&c)
		problems := c.validate(30 * time.Second)
		if len(problems) == 0 {
			t.Errorf("%s: expected a validation problem", tc.name)
			continue
		}
		if !strings.Contains(strings.Join(problems, "; "), tc.contains) {
			t.Errorf("%s: expected a problem mentioning %q, got %v", tc.name, tc.contains, problems)
		}
	}
}

// TestContract_UndeclaredInputIsAlwaysRefused covers the asymmetry between
// input and output: undeclared output can be opted into, undeclared input never
// can. An ignored argument presents to a user as "the tool ignored my request",
// which is among the hardest bugs to diagnose from a support ticket.
func TestContract_UndeclaredInputIsAlwaysRefused(t *testing.T) {
	t.Parallel()

	c := ReadContract("t", "1.0.0", "cap")
	c.AllowExtraOutput = true

	if _, err := c.ValidateInput(Arguments{"query": String("q"), "typo": String("x")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for an undeclared argument, got %v", err)
	}
	if err := c.ValidateOutput(Result{"answer": String("a"), "extra": Int(1)}); err != nil {
		t.Fatalf("AllowExtraOutput should permit undeclared output: %v", err)
	}
}

func TestContract_DefaultsAndBounds(t *testing.T) {
	t.Parallel()

	c := ReadContract("t", "1.0.0", "cap")
	c.Input = append(c.Input,
		FieldSpec{Name: "limit", Kind: ValueInt, Bounded: true, MinNum: 1, MaxNum: 10,
			HasDefault: true, Default: Int(5)},
		FieldSpec{Name: "mode", Kind: ValueString, Enum: []string{"fast", "slow"}})

	out, err := c.ValidateInput(Arguments{"query": String("q")})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if n, _ := out["limit"].Num(); n != 5 {
		t.Errorf("default not applied, got %v", out["limit"])
	}

	if _, err := c.ValidateInput(Arguments{"query": String("q"), "limit": Int(99)}); err == nil {
		t.Error("out-of-range value was accepted")
	}
	if _, err := c.ValidateInput(Arguments{"query": String("q"), "mode": String("medium")}); err == nil {
		t.Error("value outside the enum was accepted")
	}
	// Int where Float is wanted widens; the reverse must not narrow silently.
	c2 := Contract{Descriptor: Descriptor{Tool: "t", Version: "1.0.0"},
		Capabilities: []CapabilityID{"c"}, Owner: "o", Timeout: time.Second,
		Input: []FieldSpec{{Name: "f", Kind: ValueFloat}}}
	if _, err := c2.ValidateInput(Arguments{"f": Int(3)}); err != nil {
		t.Errorf("int should widen to float: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Registry and discovery
// ---------------------------------------------------------------------------

func TestRegistry_LifecycleTableIsWellFormed(t *testing.T) {
	t.Parallel()

	table := lifecycleTransitions()
	all := []Lifecycle{LifecyclePending, LifecycleActive, LifecycleDeprecated,
		LifecycleDraining, LifecycleRetired}

	// Every stage reachable from Pending.
	seen := map[Lifecycle]bool{LifecyclePending: true}
	frontier := []Lifecycle{LifecyclePending}
	for len(frontier) > 0 {
		cur := frontier[0]
		frontier = frontier[1:]
		for _, next := range table[cur] {
			if !seen[next] {
				seen[next] = true
				frontier = append(frontier, next)
			}
		}
	}
	for _, l := range all {
		if !seen[l] {
			t.Errorf("%s is unreachable from pending: a stage nothing can enter is dead code", l)
		}
	}

	if len(table[LifecycleRetired]) != 0 {
		t.Error("retired must be terminal")
	}
	if canTransition(LifecycleRetired, LifecycleActive) {
		t.Error("retired must not return to active: it would make an audit record's retirement a lie")
	}
	if canTransition(LifecycleDraining, LifecycleActive) {
		t.Error("draining must not return to active: un-deciding a drain silently makes retirement unanswerable")
	}
}

func TestRegistry_CopyOnWriteIsolatesReaders(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	reg := h.Runtime.Registry()

	c := ReadContract("t", "1.0.0", "cap")
	h.Register(c, &FakeTool{})

	got, ok := reg.Get(c.Descriptor)
	if !ok {
		t.Fatal("registration missing")
	}
	got.Contract.Capabilities[0] = "hijacked"
	got.Contract.Owner = "someone-else"

	again, _ := reg.Get(c.Descriptor)
	if again.Contract.Capabilities[0] != "cap" || again.Contract.Owner != "test-team" {
		t.Fatal("a caller mutated the registry through a returned registration")
	}
}

func TestRegistry_RedeployPreservesCounters(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	reg := h.Runtime.Registry()

	c := ReadContract("t", "1.0.0", "cap")
	h.Register(c, &FakeTool{})
	reg.RecordOutcome(c.Descriptor, false)
	reg.RecordOutcome(c.Descriptor, true)

	h.Register(c, &FakeTool{}) // redeploy

	got, _ := reg.Get(c.Descriptor)
	if got.Executions != 2 || got.Failures != 1 {
		t.Fatalf("redeploy reset lifetime counters: %d executions, %d failures; "+
			"a tool that fails on every deploy would be invisible",
			got.Executions, got.Failures)
	}
}

// TestDiscovery_DistinguishesDeploymentGapFromOutage is the distinction an
// on-call engineer needs: page the deploy owner, or page the integration owner.
func TestDiscovery_DistinguishesDeploymentGapFromOutage(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	d := h.Runtime.Discovery()

	if _, err := d.Resolve(Request{Capability: "nothing", Version: AnyVersion()}); !errors.Is(err, ErrNoCapability) {
		t.Fatalf("expected ErrNoCapability, got %v", err)
	}

	c := ReadContract("t", "1.0.0", "cap")
	h.RegisterAt(c, &FakeTool{}, LifecycleActive, HealthUnhealthy, 0)

	_, err := d.Resolve(Request{Capability: "cap", Version: AnyVersion()})
	if !errors.Is(err, ErrNoHealthyProvider) {
		t.Fatalf("expected ErrNoHealthyProvider when a tool exists but is unhealthy, got %v", err)
	}

	_, err = d.Resolve(Request{Capability: "cap", Version: MajorVersion(9)})
	if !errors.Is(err, ErrVersionUnsatisfiable) {
		t.Fatalf("expected ErrVersionUnsatisfiable, got %v", err)
	}
}

func TestDiscovery_OrderIsTotalAndDeterministic(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// Two tools, identical priority and health, differing only in name — the
	// case that proves the ordering is total rather than merely mostly-ordered.
	h.RegisterAt(ReadContract("bravo", "1.0.0", "cap"), &FakeTool{}, LifecycleActive, HealthHealthy, 5)
	h.RegisterAt(ReadContract("alpha", "1.0.0", "cap"), &FakeTool{}, LifecycleActive, HealthHealthy, 5)
	h.RegisterAt(ReadContract("charlie", "2.0.0", "cap"), &FakeTool{}, LifecycleDeprecated, HealthHealthy, 9)
	h.RegisterAt(ReadContract("delta", "1.0.0", "cap"), &FakeTool{}, LifecycleActive, HealthDegraded, 99)

	var first []Descriptor
	for run := 0; run < 50; run++ {
		got, err := h.Runtime.Discovery().Resolve(Request{
			Capability: "cap", Version: AnyVersion(), MaxCandidates: 10})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		order := make([]Descriptor, 0, len(got))
		for _, c := range got {
			order = append(order, c.Descriptor())
		}
		if run == 0 {
			first = order
			continue
		}
		if fmt.Sprint(order) != fmt.Sprint(first) {
			t.Fatalf("resolution order changed between runs: %v vs %v", order, first)
		}
	}

	if first[0].Tool != "alpha" {
		t.Errorf("healthy+active+priority should win, and ties break on tool name; got %v", first)
	}
	if first[len(first)-1].Tool != "delta" {
		t.Errorf("a degraded tool should rank last despite the highest priority; got %v", first)
	}
}

// ---------------------------------------------------------------------------
// Intent validation
// ---------------------------------------------------------------------------

func TestIntent_ValidationCatchesGraphMistakes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		intent   ToolIntent
		contains string
	}{
		{"no actor", ToolIntent{Requests: []CapabilityRequest{{Capability: "c"}}},
			"actor is required"},
		{"no requests", ToolIntent{Actor: "a"}, "at least one request"},
		{"duplicate ref", ToolIntent{Actor: "a", Requests: []CapabilityRequest{
			{Ref: "x", Capability: "c"}, {Ref: "x", Capability: "c"}}}, "duplicate ref"},
		{"unknown dependency", ToolIntent{Actor: "a", Requests: []CapabilityRequest{
			{Ref: "x", Capability: "c"}, {Ref: "y", Capability: "c", DependsOn: []string{"ghost"}}}},
			"unknown ref"},
		{"anonymous in a multi-request intent", ToolIntent{Actor: "a", Requests: []CapabilityRequest{
			{Capability: "c"}, {Capability: "c"}}}, "ref is required"},
		{"cycle", ToolIntent{Actor: "a", Requests: []CapabilityRequest{
			{Ref: "x", Capability: "c", DependsOn: []string{"y"}},
			{Ref: "y", Capability: "c", DependsOn: []string{"x"}}}}, "dependency cycle"},
	}

	for _, tc := range cases {
		err := tc.intent.Validate()
		if err == nil {
			t.Errorf("%s: expected a validation error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.contains) {
			t.Errorf("%s: expected %q in %v", tc.name, tc.contains, err)
		}
	}
}

func TestCondition_MissingSourceIsFalseNotAnError(t *testing.T) {
	t.Parallel()

	results := map[string]Result{"have": {"n": Int(5), "s": String("x")}}

	cases := []struct {
		cond Condition
		want bool
	}{
		{Condition{FromRef: "ghost", Field: "n", Op: CondExists}, false},
		{Condition{FromRef: "ghost", Field: "n", Op: CondAbsent}, true},
		{Condition{FromRef: "have", Field: "n", Op: CondExists}, true},
		{Condition{FromRef: "have", Field: "missing", Op: CondAbsent}, true},
		{Condition{FromRef: "have", Field: "n", Op: CondGreaterThan, Value: Int(3)}, true},
		{Condition{FromRef: "have", Field: "n", Op: CondLessThan, Value: Int(3)}, false},
		{Condition{FromRef: "have", Field: "s", Op: CondEquals, Value: String("x")}, true},
		// A non-numeric comparison is false rather than an error: failing the
		// plan here would report a validation problem at the wrong place.
		{Condition{FromRef: "have", Field: "s", Op: CondGreaterThan, Value: Int(1)}, false},
	}

	for i, tc := range cases {
		if got := tc.cond.Evaluate(results); got != tc.want {
			t.Errorf("case %d (%s.%s %s): got %v, want %v",
				i, tc.cond.FromRef, tc.cond.Field, tc.cond.Op, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Planning
// ---------------------------------------------------------------------------

func TestPlan_BuildingAPlanExecutesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	tool := &FakeTool{}
	h.Register(ReadContract("t", "1.0.0", "cap"), tool)

	plan, err := h.Runtime.Plan(h.Intent("cap", Arguments{"query": String("q")}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if tool.Calls() != 0 {
		t.Fatalf("INV-TOOL-8: planning invoked the tool %d times", tool.Calls())
	}
	if h.Events.Len() != 0 {
		t.Fatalf("planning published %d events; a plan must be inert", h.Events.Len())
	}
	if plan.StepCount() != 1 || plan.Shape != "single" {
		t.Fatalf("unexpected plan: %s", plan.Explain())
	}
}

func TestPlan_ShapesFollowDependencies(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{})

	req := func(ref string, deps ...string) CapabilityRequest {
		return CapabilityRequest{Ref: ref, Capability: "cap", Version: AnyVersion(),
			Args: Arguments{"query": String(ref)}, DependsOn: deps}
	}

	cases := []struct {
		name  string
		reqs  []CapabilityRequest
		shape string
		steps int
	}{
		{"single", []CapabilityRequest{req("a")}, "single", 1},
		{"parallel", []CapabilityRequest{req("a"), req("b")}, "parallel", 2},
		{"sequential", []CapabilityRequest{req("a"), req("b", "a")}, "sequential", 2},
		{"mixed", []CapabilityRequest{req("a"), req("b"), req("c", "a", "b")}, "mixed", 3},
	}

	for _, tc := range cases {
		intent := ToolIntent{ID: NewIntentID(), Actor: "a", Requests: tc.reqs,
			Grant: Grant{Actor: "a"}}
		plan, err := h.Runtime.Plan(intent)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if plan.Shape != tc.shape || plan.StepCount() != tc.steps {
			t.Errorf("%s: got shape %s with %d steps, want %s with %d\n%s",
				tc.name, plan.Shape, plan.StepCount(), tc.shape, tc.steps, plan.Explain())
		}
	}
}

// TestPlan_IsDeterministic asserts the property that makes a plan fingerprint
// worth having: the same registry and the same intent produce the same plan.
func TestPlan_IsDeterministic(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(ReadContract("alpha", "1.0.0", "cap"), &FakeTool{})
	h.Register(ReadContract("bravo", "1.0.0", "cap"), &FakeTool{})

	intent := ToolIntent{ID: "int-fixed", Actor: "a", Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{
			{Ref: "z", Capability: "cap", Version: AnyVersion(), Args: Arguments{"query": String("z")}},
			{Ref: "a", Capability: "cap", Version: AnyVersion(), Args: Arguments{"query": String("a")}},
			{Ref: "m", Capability: "cap", Version: AnyVersion(), Args: Arguments{"query": String("m")},
				DependsOn: []string{"a", "z"}},
		}}

	var want Fingerprint
	for i := 0; i < 100; i++ {
		plan, err := h.Runtime.Plan(intent)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		got := plan.Fingerprint()
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("plan fingerprint changed on run %d: %s != %s\n%s", i, got, want, plan.Explain())
		}
	}
}

func TestPlan_PinsVersionAtPlanTime(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{})

	plan, err := h.Runtime.Plan(h.Intent("cap", Arguments{"query": String("q")}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// A newer version appears after planning. The plan must not follow it: an
	// audit record naming a version that never ran is worse than no record.
	h.Register(ReadContract("t", "2.0.0", "cap"), &FakeTool{})

	if plan.Steps()[0].Descriptor.Version != "1.0.0" {
		t.Fatalf("INV-TOOL-9: a registry change moved a built plan to %s",
			plan.Steps()[0].Descriptor.Version)
	}
}

func TestPlan_ReportsCompensabilityBeforeAnythingRuns(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.Register(WriteContract("safe", "1.0.0", "book"), &CompensatingFake{})

	unsafe := WriteContract("unsafe", "1.0.0", "charge")
	unsafe.Compensable = false
	h.Register(unsafe, &WriteFake{})

	good, err := h.Runtime.Plan(h.Intent("book", Arguments{"subject": String("s")}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !good.FullyCompensable() || !good.Mutates() {
		t.Error("a compensable write plan should report mutating and fully compensable")
	}

	bad, err := h.Runtime.Plan(h.Intent("charge", Arguments{"subject": String("s")}))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if bad.FullyCompensable() {
		t.Error("a non-compensable write plan must say so before it runs")
	}
}

func TestPlan_FallbackOverIrreversibleIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(IrreversibleContract("sms-a", "1.0.0", "send"), &WriteFake{})
	h.Register(IrreversibleContract("sms-b", "1.0.0", "send"), &WriteFake{})

	intent := ToolIntent{ID: NewIntentID(), Actor: "a", Grant: Grant{Actor: "a"},
		Requests: []CapabilityRequest{{Ref: "s", Capability: "send", Version: AnyVersion(),
			Args: Arguments{"subject": String("x")}, Fallback: true}}}

	_, err := h.Runtime.Plan(intent)
	if !errors.Is(err, ErrInvariant) {
		t.Fatalf("a fallback chain over an irreversible tool must be refused, got %v", err)
	}
}

func TestPlan_MissingRequiredArgumentIsCaughtAtPlanTime(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{})

	_, err := h.Runtime.Plan(h.Intent("cap", Arguments{}))
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected a plan-time input failure, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Permissions
// ---------------------------------------------------------------------------

func TestPermission_MissingPermissionIsDeniedWithTheList(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	c := ReadContract("t", "1.0.0", "cap")
	c.RequiredPermissions = []Permission{"tool.read", "tool.extra"}

	d := h.Runtime.Permissions().Evaluate(c, Grant{Actor: "a", Permissions: []Permission{"tool.read"}})
	if d.Allowed {
		t.Fatal("missing permission was allowed")
	}
	if len(d.Missing) != 1 || d.Missing[0] != "tool.extra" {
		t.Fatalf("denial should name exactly what is missing, got %v", d.Missing)
	}
	if !errors.Is(d.Error(), ErrPermissionDenied) {
		t.Fatalf("denial should surface as ErrPermissionDenied, got %v", d.Error())
	}
}

func TestPermission_RolesExpand(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if err := h.Runtime.Permissions().DefineRole("receptionist", "tool.read", "tool.book"); err != nil {
		t.Fatal(err)
	}

	c := ReadContract("t", "1.0.0", "cap")
	c.RequiredPermissions = []Permission{"tool.book"}

	if d := h.Runtime.Permissions().Evaluate(c, Grant{Actor: "a", Roles: []string{"receptionist"}}); !d.Allowed {
		t.Fatalf("role did not expand: %v", d.Missing)
	}
}

func TestPermission_ConsentIsSeparateFromPermission(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	c := ReadContract("t", "1.0.0", "cap")
	c.RequiresConsent = "call_recording"

	d := h.Runtime.Permissions().Evaluate(c, Grant{Actor: "a"})
	if d.Allowed {
		t.Fatal("missing consent was allowed")
	}
	if !errors.Is(d.Error(), ErrConsentRequired) {
		t.Fatalf("a consent gap must be actionable as ErrConsentRequired, not a generic denial: %v", d.Error())
	}

	d = h.Runtime.Permissions().Evaluate(c, Grant{Actor: "a",
		ConsentRefs: StaticConsent(map[string]string{"call_recording": "consent-123"})})
	if !d.Allowed || d.ConsentRef != "consent-123" {
		t.Fatalf("consent not honoured or not recorded: %+v", d)
	}
}

// TestPermission_OverrideMustCoverEverythingMissing checks the rule that stops
// an emergency override half-authorising something. Waiving two of three
// requirements is not a decision anybody made about the third.
func TestPermission_OverrideMustCoverEverythingMissing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pe := h.Runtime.Permissions()

	expiry := h.Clock.Now().Add(time.Hour)
	if err := pe.AddOverride(Override{Name: "partial", Permissions: []Permission{"a"},
		ExpiresAt: expiry, AuthorisedBy: "oncall", Reason: "incident-1"}); err != nil {
		t.Fatal(err)
	}

	c := ReadContract("t", "1.0.0", "cap")
	c.RequiredPermissions = []Permission{"a", "b"}

	if d := pe.Evaluate(c, Grant{Actor: "x"}); d.Allowed {
		t.Fatal("a partial override allowed the call")
	}

	if err := pe.AddOverride(Override{Name: "full", Permissions: []Permission{"a", "b"},
		ExpiresAt: expiry, AuthorisedBy: "oncall", Reason: "incident-1"}); err != nil {
		t.Fatal(err)
	}
	d := pe.Evaluate(c, Grant{Actor: "x"})
	if !d.Allowed || d.Override != "full" {
		t.Fatalf("a covering override should allow and be named: %+v", d)
	}

	// Used overrides are audited. An override that leaves no trace is
	// indistinguishable from a permission bug.
	if len(h.Audit.OfKind(AuditOverrideUsed)) != 1 {
		t.Error("an override that fired was not audited")
	}
}

func TestPermission_OverrideRequiresAnExpiryAndAnAuthor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	err := h.Runtime.Permissions().AddOverride(Override{Name: "forever",
		Permissions: []Permission{"a"}, AuthorisedBy: "x", Reason: "y"})
	if err == nil || !strings.Contains(err.Error(), "ExpiresAt is required") {
		t.Fatalf("an override with no expiry must be refused, got %v", err)
	}

	err = h.Runtime.Permissions().AddOverride(Override{Name: "anon",
		Permissions: []Permission{"a"}, ExpiresAt: h.Clock.Now().Add(time.Hour), Reason: "y"})
	if err == nil || !strings.Contains(err.Error(), "AuthorisedBy is required") {
		t.Fatalf("an anonymous override must be refused, got %v", err)
	}
}

func TestPermission_ExpiredGrantAndOverrideStopWorking(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pe := h.Runtime.Permissions()

	c := ReadContract("t", "1.0.0", "cap")
	c.RequiredPermissions = []Permission{"a"}

	grant := Grant{Actor: "x", Permissions: []Permission{"a"},
		ExpiresAt: h.Clock.Now().Add(time.Minute)}
	if d := pe.Evaluate(c, grant); !d.Allowed {
		t.Fatal("a live grant was refused")
	}

	h.Clock.Advance(2 * time.Minute)
	if d := pe.Evaluate(c, grant); d.Allowed || d.Reason != "grant_expired" {
		t.Fatalf("an expired grant was honoured: %+v", d)
	}
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

func TestIdempotency_KeyDependsOnWhatMattersAndNothingElse(t *testing.T) {
	t.Parallel()

	d1 := Descriptor{Tool: "t", Version: "1.0.0"}
	d2 := Descriptor{Tool: "t", Version: "2.0.0"}
	args := Arguments{"a": String("x")}

	base := DeriveKey(d1, args, "actor", "corr")

	if DeriveKey(d1, args.Clone(), "actor", "corr") != base {
		t.Error("the same request produced a different key")
	}
	if DeriveKey(d2, args, "actor", "corr") == base {
		t.Error("a different version must not share a key: it may do a different thing")
	}
	if DeriveKey(d1, Arguments{"a": String("y")}, "actor", "corr") == base {
		t.Error("different arguments must not share a key")
	}
	if DeriveKey(d1, args, "other", "corr") == base {
		t.Error("two subscribers asking the same question are two questions")
	}
	if DeriveKey(d1, args, "actor", "other-corr") == base {
		t.Error("a later turn must not deduplicate against an earlier one")
	}
}

func TestLedger_ReplaysASettledResult(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := h.Runtime.Ledger()
	d := Descriptor{Tool: "t", Version: "1.0.0"}

	fresh, existing, err := l.Claim("k1", d, "e1")
	if err != nil || fresh == nil || existing != nil {
		t.Fatalf("first claim should be fresh: %v %v %v", fresh, existing, err)
	}
	l.Settle("k1", Result{"answer": String("done")}, "completed", true)

	fresh, existing, err = l.Claim("k1", d, "e2")
	if err != nil || fresh != nil || existing == nil {
		t.Fatalf("second claim should replay: %v %v %v", fresh, existing, err)
	}
	if got, _ := existing.Result["answer"].Str(); got != "done" {
		t.Fatalf("replayed result is wrong: %v", existing.Result)
	}
	if existing.Replays != 1 {
		t.Errorf("replay count not tracked, got %d", existing.Replays)
	}
}

// TestLedger_InFlightEntriesAreNeverEvicted covers the rule that stops the
// ledger from causing the exact double-execution it exists to prevent.
func TestLedger_InFlightEntriesAreNeverEvicted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := NewLedger(h.Clock, h.Metrics, time.Minute, 2)
	d := Descriptor{Tool: "t", Version: "1.0.0"}

	if _, _, err := l.Claim("held", d, "e0"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		key := IdempotencyKey(fmt.Sprintf("k%d", i))
		if _, _, err := l.Claim(key, d, ExecutionID(key)); err != nil {
			t.Fatal(err)
		}
		l.Settle(key, Result{}, "completed", true)
	}
	h.Clock.Advance(2 * time.Minute)
	l.Sweep()

	if _, ok := l.Get("held"); !ok {
		t.Fatal("an in-flight entry was evicted; a duplicate could now run the same mutating call twice")
	}
}

func TestLedger_ConcurrentDuplicatesShareOneResult(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := h.Runtime.Ledger()
	d := Descriptor{Tool: "t", Version: "1.0.0"}

	holder, _, err := l.Claim("k", d, "first")
	if err != nil {
		t.Fatal(err)
	}

	_, existing, err := l.Claim("k", d, "second")
	if !errors.Is(err, ErrDuplicate) || existing == nil {
		t.Fatalf("a concurrent duplicate should report ErrDuplicate, got %v", err)
	}

	done := make(chan *LedgerEntry, 1)
	go func() {
		settled, _ := l.Await(context.Background(), existing)
		done <- settled
	}()

	l.Settle(holder.Key, Result{"answer": String("shared")}, "completed", true)

	select {
	case settled := <-done:
		if settled == nil || settled.State != LedgerCompleted {
			t.Fatalf("awaiting duplicate did not receive the outcome: %+v", settled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("awaiting duplicate never woke")
	}
}

// ---------------------------------------------------------------------------
// Retry, breakers, budgets
// ---------------------------------------------------------------------------

func TestClassify_NeverRetriesWhatCannotSucceed(t *testing.T) {
	t.Parallel()

	never := []error{ErrPermissionDenied, ErrConsentRequired, ErrInvalidInput,
		ErrInvalidOutput, ErrNoCapability, ErrVersionUnsatisfiable, ErrCancelled,
		ErrDuplicate, ErrBudgetExceeded, ErrClosed, ErrCircuitOpen,
		&InvariantError{Invariant: "X", Detail: "y"}}
	for _, err := range never {
		if Classify(err) {
			t.Errorf("%v was classified retryable; asking again cannot change the answer", err)
		}
	}

	always := []error{ErrTimeout, ErrNoHealthyProvider, errors.New("connection reset")}
	for _, err := range always {
		if !Classify(err) {
			t.Errorf("%v should be retryable", err)
		}
	}
}

func TestBudget_TightensNeverLoosens(t *testing.T) {
	t.Parallel()

	ceiling := Budget{WallClock: 30 * time.Second, OutputBytes: 1000, MaxAttempts: 3, Slots: 1}
	tighter := ceiling.tighten(Budget{WallClock: 5 * time.Second, OutputBytes: 100, MaxAttempts: 1})
	if tighter.WallClock != 5*time.Second || tighter.OutputBytes != 100 || tighter.MaxAttempts != 1 {
		t.Fatalf("tighten failed to tighten: %+v", tighter)
	}

	looser := ceiling.tighten(Budget{WallClock: time.Hour, OutputBytes: 1 << 20, MaxAttempts: 99})
	if looser.WallClock != 30*time.Second || looser.OutputBytes != 1000 || looser.MaxAttempts != 3 {
		t.Fatalf("a contract raised a runtime ceiling: %+v", looser)
	}
}

func TestSandbox_CapsConcurrencyPerToolAndOverall(t *testing.T) {
	t.Parallel()
	s := NewBudgetSandbox(3, 2)
	d := Descriptor{Tool: "t", Version: "1.0.0"}
	b := Budget{Slots: 1}

	l1, err := s.Enter(d, b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enter(d, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enter(d, b); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("per-tool cap not enforced, got %v", err)
	}

	other := Descriptor{Tool: "other", Version: "1.0.0"}
	if _, err := s.Enter(other, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enter(other, b); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("overall slot cap not enforced, got %v", err)
	}

	l1.Release()
	l1.Release() // idempotent; a double release must not create a slot
	if s.InUse() != 2 {
		t.Fatalf("double release created capacity: %d slots in use", s.InUse())
	}
}

func TestLease_ChargesOutputIncrementally(t *testing.T) {
	t.Parallel()
	s := NewBudgetSandbox(4, 4)
	lease, err := s.Enter(Descriptor{Tool: "t", Version: "1.0.0"}, Budget{OutputBytes: 100, Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.ChargeOutput(60); err != nil {
		t.Fatal(err)
	}
	if err := lease.ChargeOutput(60); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("cumulative output budget not enforced, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Scheduler
// ---------------------------------------------------------------------------

func TestScheduler_ShedsAtAdmissionRatherThanQueueingForever(t *testing.T) {
	t.Parallel()
	s, err := NewToolScheduler(SchedulerConfig{MaxConcurrent: 1,
		MaxQueuedInteractive: 1, MaxQueuedBackground: 1, MaxQueuedBulk: 1,
		StarvationRatio: 4}, nil, NewMetrics())
	if err != nil {
		t.Fatal(err)
	}

	release, err := s.Acquire(context.Background(), ClassInteractive)
	if err != nil {
		t.Fatal(err)
	}

	// One waiter fills the queue.
	waiting := make(chan struct{})
	go func() {
		r, _ := s.Acquire(context.Background(), ClassInteractive)
		close(waiting)
		if r != nil {
			r()
		}
	}()
	for s.Depth() < 1 {
		time.Sleep(time.Millisecond)
	}

	if _, err := s.Acquire(context.Background(), ClassInteractive); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull once the queue is full, got %v", err)
	}

	release()
	<-waiting
}

func TestScheduler_CancelledWaiterDoesNotLeakASlot(t *testing.T) {
	t.Parallel()
	s, err := NewToolScheduler(SchedulerConfig{MaxConcurrent: 1,
		MaxQueuedInteractive: 4, MaxQueuedBackground: 4, MaxQueuedBulk: 4}, nil, NewMetrics())
	if err != nil {
		t.Fatal(err)
	}

	release, _ := s.Acquire(context.Background(), ClassInteractive)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for s.Depth() < 1 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	if _, err := s.Acquire(ctx, ClassInteractive); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}

	release()
	if got := s.InFlight(); got != 0 {
		t.Fatalf("a cancelled waiter left %d slots occupied by nobody", got)
	}
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

func TestEvents_TopicNamingMatchesTheFrozenConvention(t *testing.T) {
	t.Parallel()
	for _, et := range AllEventTypes() {
		topic := et.Topic()
		if !strings.HasPrefix(topic, "tool.execution.") || !strings.HasSuffix(topic, ".v1") {
			t.Errorf("topic %q does not match <domain>.<entity>.<event>.v<major>", topic)
		}
		if strings.Contains(topic, "-") {
			t.Errorf("topic %q contains a hyphen, which collides with Prometheus "+
				"metric-name normalisation", topic)
		}
		if topic != strings.ToLower(topic) {
			t.Errorf("topic %q is not lowercase", topic)
		}
	}
}

// TestEvents_CarryNoPayload is the mechanical check behind frozen invariant I7.
// Kafka cannot delete an individual record, so an event that carries content is
// a permanent copy of it.
func TestEvents_CarryNoPayload(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	const secret = "SUPER-SECRET-PAYLOAD-VALUE"
	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{
		Produce: func(Invocation) (Result, error) {
			return Result{"answer": String(secret)}, nil
		}})

	if _, err := h.Runtime.Execute(context.Background(),
		h.Intent("cap", Arguments{"query": String(secret)})); err != nil {
		t.Fatalf("execute: %v", err)
	}

	for _, e := range h.Events.Events() {
		rendered := fmt.Sprintf("%+v", e)
		if strings.Contains(rendered, secret) {
			t.Fatalf("event %s carries caller content: %s", e.Type, rendered)
		}
	}
	if h.Events.Count(EventCompleted) == 0 {
		t.Fatal("no completion event was published at all")
	}
}

func TestEvents_SequenceIsMonotonic(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{})

	for i := 0; i < 5; i++ {
		if _, err := h.Runtime.Execute(context.Background(),
			h.Intent("cap", Arguments{"query": String("q")})); err != nil {
			t.Fatal(err)
		}
	}

	var last uint64
	for _, e := range h.Events.Events() {
		if e.Sequence <= last {
			t.Fatalf("sequence went backwards: %d after %d", e.Sequence, last)
		}
		last = e.Sequence
	}
}

func TestEvents_FailingPublisherDoesNotFailTheExecution(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Register(ReadContract("t", "1.0.0", "cap"), &FakeTool{})
	h.Events.FailWith(errors.New("broker down"))

	if _, err := h.Runtime.Execute(context.Background(),
		h.Intent("cap", Arguments{"query": String("q")})); err != nil {
		t.Fatalf("a broken subscriber must not fail a tool execution: %v", err)
	}
	if h.Metrics.EventsDropped.Total() == 0 {
		t.Fatal("a dropped event should be counted so the loss is visible")
	}
}

// ---------------------------------------------------------------------------
// Runtime lifecycle
// ---------------------------------------------------------------------------

func TestRuntime_StartsOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	if err := h.Runtime.Start(); err != nil {
		t.Fatal(err)
	}
	if err := h.Runtime.Start(); !errors.Is(err, ErrInvariant) {
		t.Fatalf("INV-TOOL-10: a second Start should be refused, got %v", err)
	}
	if err := h.Runtime.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := h.Runtime.Stop(); err != nil {
		t.Fatalf("Stop should be idempotent: %v", err)
	}
	if _, err := h.Runtime.Execute(context.Background(), h.Intent("cap", nil)); !errors.Is(err, ErrClosed) {
		t.Fatalf("a stopped runtime should refuse work, got %v", err)
	}
}

func TestRuntime_RequiresAnAuditorByDefault(t *testing.T) {
	t.Parallel()

	_, err := New(DefaultConfig(), WithClock(nil))
	if err == nil || !strings.Contains(err.Error(), "no audit trail") {
		t.Fatalf("a runtime that acts on the world must refuse to start without an auditor, got %v", err)
	}

	cfg := DefaultConfig()
	cfg.RequireAuditor = false
	if _, err := New(cfg); err != nil {
		t.Fatalf("an explicitly audit-free runtime should start: %v", err)
	}
}

func TestRuntime_TwoRuntimesShareNothing(t *testing.T) {
	t.Parallel()
	a, b := newHarness(t), newHarness(t)

	a.Register(ReadContract("only-in-a", "1.0.0", "cap"), &FakeTool{})

	if b.Runtime.Registry().Len() != 0 {
		t.Fatal("registering in one runtime appeared in another; there is global state somewhere")
	}
	if _, err := b.Runtime.Discovery().Resolve(Request{Capability: "cap", Version: AnyVersion()}); !errors.Is(err, ErrNoCapability) {
		t.Fatalf("second runtime resolved another runtime's tool: %v", err)
	}
}

func TestMetrics_SnapshotIsStablyOrdered(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.Started.Inc("b", "cap")
	m.Started.Inc("a", "cap")
	m.Completed.Inc("a", "cap")
	m.QueueDepth.Set(3)

	first := fmt.Sprint(m.Snapshot())
	for i := 0; i < 20; i++ {
		if got := fmt.Sprint(m.Snapshot()); got != first {
			t.Fatal("metric snapshot ordering is unstable")
		}
	}
}

func TestMetrics_RatesAreUndefinedRatherThanWrongWithNoData(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	if m.SuccessRate("") != 0 || m.RetryRate("") != 0 || m.TimeoutRate("") != 0 {
		t.Fatal("rates over no observations should be zero rather than NaN")
	}
	m.Started.Inc("t", "c")
	m.Completed.Inc("t", "c")
	m.Failed.Inc("t", "invoke", "boom")
	if got := m.SuccessRate("t"); got != 0.5 {
		t.Fatalf("success rate is %v, want 0.5", got)
	}
}
