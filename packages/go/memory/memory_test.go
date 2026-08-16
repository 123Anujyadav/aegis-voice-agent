package memory

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	rt "github.com/callscreen/callscreen-platform/packages/go/runtime"
)

func newHarness(t *testing.T, opts ...HarnessOption) *Harness {
	t.Helper()
	h, err := NewHarness(opts...)
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h
}

// ---------------------------------------------------------------------------
// Model: Kind × Tier
// ---------------------------------------------------------------------------

// TestModel_KindAndTierAreOrthogonal asserts the central modelling decision:
// the brief's eleven "memory types" are a grid, not a list.
func TestModel_KindAndTierAreOrthogonal(t *testing.T) {
	t.Parallel()

	// Every kind can exist at every tier. If they were one enum this loop
	// could not be written.
	for _, k := range AllKinds() {
		for _, tier := range AllTiers() {
			if !k.Valid() || !tier.Valid() {
				t.Fatalf("kind %v / tier %v should both be valid", k, tier)
			}
		}
	}
	if len(AllKinds()) != 8 || len(AllTiers()) != 3 {
		t.Fatalf("expected an 8x3 grid, got %dx%d", len(AllKinds()), len(AllTiers()))
	}
}

func TestModel_NamedTypesMapToKinds(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]Kind{
		"ConversationMemory": KindConversation,
		"SessionMemory":      KindSession,
		"UserMemory":         KindUser,
		"BusinessMemory":     KindBusiness,
		"PreferenceMemory":   KindPreference,
		"ContactMemory":      KindContact,
		"ScratchpadMemory":   KindScratchpad,
		"PolicyMemory":       KindPolicy,
	} {
		got, ok := KindOf(name)
		if !ok || got != want {
			t.Errorf("%s -> %v (%v), want %v", name, got, ok, want)
		}
	}
	// The three tier names are tiers, not kinds.
	for _, tierName := range []string{"WorkingMemory", "ShortTermMemory", "LongTermMemory"} {
		if _, ok := KindOf(tierName); ok {
			t.Errorf("%s is a tier and must not resolve to a kind", tierName)
		}
	}
}

func TestModel_TierLadderIsSingleStepAndBounded(t *testing.T) {
	t.Parallel()
	if TierWorking.Promote() != TierShortTerm {
		t.Fatal("working promotes to short term")
	}
	if TierShortTerm.Promote() != TierLongTerm {
		t.Fatal("promotion must be single step")
	}
	if TierLongTerm.Promote() != TierLongTerm {
		t.Fatal("long term is the ceiling")
	}
	if TierWorking.Demote() != TierWorking {
		t.Fatal("working is the floor")
	}
}

func TestModel_ScratchpadIsAlwaysWorking(t *testing.T) {
	t.Parallel()
	r := NewScratchpad("s1", "note", Value{Data: []byte("x")})
	if r.Tier != TierWorking {
		t.Fatalf("scratchpad tier = %v, want working", r.Tier)
	}

	// And a hand-built scratchpad at another tier is refused.
	bad := &Record{
		Key:  Key{Subject: "s1", Kind: KindScratchpad, Name: "n"},
		Tier: TierLongTerm, Sensitivity: Internal,
		Provenance: Provenance{Source: "test"},
	}
	if problems := bad.validate(); len(problems) == 0 {
		t.Fatal("a long-term scratchpad must be refused")
	}
}

func TestModel_PreferencesArePinnedByDefault(t *testing.T) {
	t.Parallel()
	r := NewPreference("s1", "language", Value{Data: []byte("hi-IN")}, Internal)
	if !r.Pinned {
		t.Fatal("a preference is something the subject asked for; evicting it " +
			"and silently reverting to a default is the failure users notice")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func TestLifecycle_TableIsWellFormed(t *testing.T) {
	t.Parallel()
	table := lifecycleTable()

	for _, s := range AllStates() {
		if _, ok := table[s]; !ok {
			t.Errorf("state %s has no entry in the lifecycle table", s)
		}
	}
	if len(table[StateDeleted]) != 0 {
		t.Error("Deleted is terminal and must declare no outgoing edges")
	}

	// Reachability from Active.
	reached := map[State]bool{StateActive: true}
	queue := []State{StateActive}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range table[cur] {
			if !reached[next] {
				reached[next] = true
				queue = append(queue, next)
			}
		}
	}
	for _, s := range AllStates() {
		if !reached[s] {
			t.Errorf("state %s is unreachable from Active", s)
		}
	}

	// Every non-terminal state can reach Deleted, or a record could never be
	// reclaimed — which for a memory engine means a leak with a legal name.
	for _, s := range AllStates() {
		if s.Terminal() {
			continue
		}
		seen := map[State]bool{s: true}
		q := []State{s}
		ok := false
		for len(q) > 0 && !ok {
			cur := q[0]
			q = q[1:]
			for _, next := range table[cur] {
				if next == StateDeleted {
					ok = true
					break
				}
				if !seen[next] {
					seen[next] = true
					q = append(q, next)
				}
			}
		}
		if !ok {
			t.Errorf("state %s can never be reclaimed", s)
		}
	}
}

func TestLifecycle_RedactedNeverReturnsToActive(t *testing.T) {
	t.Parallel()
	if canTransition(StateRedacted, StateActive) {
		t.Fatal("reviving a redacted record would recover content destroyed on purpose")
	}
}

func TestLifecycle_CorruptIsNeverRepaired(t *testing.T) {
	t.Parallel()
	if canTransition(StateCorrupt, StateActive) {
		t.Fatal("silently repairing a record that failed an integrity check makes " +
			"corruption indistinguishable from truth")
	}
}

func TestLifecycle_ExpiredCanRevive(t *testing.T) {
	t.Parallel()
	if !canTransition(StateExpired, StateActive) {
		t.Fatal("a subscriber raising their retention preference should not have " +
			"destroyed data that has not yet been reclaimed")
	}
}

func TestLifecycle_FSMValidatesTheTable(t *testing.T) {
	t.Parallel()
	if _, err := newLifecycleFSM(rt.NewFakeClock(time.Time{})); err != nil {
		t.Fatalf("the lifecycle table must satisfy runtime.FSM's validation: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Promotion policy
// ---------------------------------------------------------------------------

func TestPromotion_DecisionTable(t *testing.T) {
	t.Parallel()
	p := DefaultPromotionPolicy()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		rec  *Record
		now  time.Time
		want TierDecision
	}{
		{"pinned never moves",
			&Record{Tier: TierWorking, State: StateActive, Pinned: true,
				CreatedAt: base, AccessedAt: base.Add(-time.Hour)},
			base, TierHold},
		{"idle working expires",
			&Record{Tier: TierWorking, State: StateActive,
				CreatedAt: base, AccessedAt: base},
			base.Add(31 * time.Second), TierExpireNow},
		{"hot enough promotes",
			&Record{Tier: TierWorking, State: StateActive, AccessCount: 3,
				CreatedAt: base, AccessedAt: base.Add(6 * time.Second)},
			base.Add(6 * time.Second), TierPromoteUp},
		{"hot but too young holds",
			&Record{Tier: TierWorking, State: StateActive, AccessCount: 10,
				CreatedAt: base, AccessedAt: base.Add(time.Second)},
			base.Add(time.Second), TierHold},
		{"idle short-term demotes",
			&Record{Tier: TierShortTerm, State: StateActive,
				CreatedAt: base, AccessedAt: base},
			base.Add(11 * time.Minute), TierDemoteDown},
		{"derived does not promote by default",
			&Record{Tier: TierShortTerm, State: StateActive, AccessCount: 99,
				CreatedAt: base, AccessedAt: base.Add(10 * time.Second),
				Provenance: Provenance{Derived: true}},
			base.Add(10 * time.Second), TierHold},
		{"inactive record holds",
			&Record{Tier: TierWorking, State: StateArchived,
				CreatedAt: base, AccessedAt: base},
			base.Add(time.Hour), TierHold},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Evaluate(tc.rec, tc.now); got != tc.want {
				t.Fatalf("decision = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPromotion_IsDeterministic(t *testing.T) {
	t.Parallel()
	p := DefaultPromotionPolicy()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &Record{Tier: TierWorking, State: StateActive, AccessCount: 3,
		CreatedAt: base, AccessedAt: base.Add(6 * time.Second)}

	first := p.Evaluate(r, base.Add(6*time.Second))
	for i := 0; i < 100; i++ {
		if got := p.Evaluate(r, base.Add(6*time.Second)); got != first {
			t.Fatalf("promotion is not deterministic: %s vs %s", got, first)
		}
	}
}

func TestPromotion_ConfigRejectsImpossibleWindows(t *testing.T) {
	t.Parallel()
	p := PromotionPolicy{AccessesToPromote: 1, IdleToDemote: time.Second,
		IdleToExpire: 10 * time.Second}
	if len(p.validate()) == 0 {
		t.Fatal("a record that expires before it can be demoted is a misconfiguration")
	}
}

// ---------------------------------------------------------------------------
// Policy: consent, DPDP, classification
// ---------------------------------------------------------------------------

// TestPolicy_PersonalDataRequiresConsent is INV-MEM-2, the engine's central
// compliance property.
func TestPolicy_PersonalDataRequiresConsent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	r := PersonalRecord("subj-1", KindUser, "name", "value", "")
	if _, err := b.Store.Store(r); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("Personal data with no basis must be refused at write, got %v", err)
	}
	if b.Store.Count() != 0 {
		t.Fatal("an unlawful memory must never be created and then detected")
	}
	if h.Metrics.ConsentRefusals.Total() != 1 {
		t.Fatal("a consent refusal should be counted")
	}
}

func TestPolicy_ConsentMustBeCurrentNotMerelyPresent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	r := PersonalRecord("subj-1", KindUser, "name", "value", "consent-abc")
	if _, err := b.Store.Store(r); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("an unrecognised consent reference must be refused, got %v", err)
	}

	h.Grant("subj-1", "consent-abc")
	if _, err := b.Store.Store(r); err != nil {
		t.Fatalf("a granted consent should admit the write: %v", err)
	}
}

func TestPolicy_SecretIsNeverStored(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	r := &Record{
		Key:  Key{Subject: "s", Kind: KindUser, Name: "token"},
		Tier: TierLongTerm, Sensitivity: Secret, ConsentRef: "c",
		Provenance: Provenance{Source: "test"},
	}
	if _, err := h.Assistant().Store.Store(r); err == nil {
		t.Fatal("a memory engine that accepted credentials would be one more " +
			"place a token can leak from")
	}
}

func TestPolicy_DerivedLongTermIsRefusedByDefault(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	r := InternalRecord("s", KindUser, "inferred", "guess")
	r.Tier = TierLongTerm
	r.Provenance.Derived = true

	if _, err := h.Assistant().Store.Store(r); !errors.Is(err, ErrInvariant) {
		t.Fatalf("the platform must not permanently remember an inference about a "+
			"person by default, got %v", err)
	}
}

func TestPolicy_LegalHoldHasNoExpiry(t *testing.T) {
	t.Parallel()
	p := DefaultRetentionPolicy()
	r := &Record{Retention: RetentionLegalHold, Key: Key{Kind: KindPolicy}}
	if _, expires := p.Lifetime(r); expires {
		t.Fatal("giving a legal hold a duration is a way to accidentally delete " +
			"the records that must be kept")
	}
}

func TestPolicy_RequireAuditorRefusesToStartBlind(t *testing.T) {
	t.Parallel()
	p := DefaultPolicy() // RequireAuditor true, Auditor nil
	problems := p.validate()
	found := false
	for _, s := range problems {
		if strings.Contains(s, "Auditor") {
			found = true
		}
	}
	if !found {
		t.Fatal("an engine holding Sensitive data with no audit trail cannot " +
			"answer 'who read this'")
	}
}

func TestPolicy_SensitiveReadsAreAudited(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Grant("s", "c1")
	b := h.Assistant()

	r := PersonalRecord("s", KindUser, "note", "data", "c1")
	r.Sensitivity = Sensitive
	if _, err := b.Store.Store(r); err != nil {
		t.Fatalf("Store: %v", err)
	}

	before := h.Audit.Count()
	if _, err := b.Store.Retrieve(r.Key, "operator-7"); err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if h.Audit.Count() != before+1 {
		t.Fatal("every read of Sensitive data must produce an audit entry")
	}
	events := h.Audit.Events()
	last := events[len(events)-1]
	if last.Actor != "operator-7" || !last.Granted {
		t.Fatalf("audit entry should name the actor and the outcome: %+v", last)
	}
}

func TestPolicy_InternalReadsAreNotAudited(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	r := InternalRecord("s", KindSession, "n", "d")
	_, _ = b.Store.Store(r)
	_, _ = b.Store.Retrieve(r.Key, "actor")

	if h.Audit.Count() != 0 {
		t.Fatal("auditing every Internal read buries the entries that matter")
	}
}

// ---------------------------------------------------------------------------
// Consistency: versioning, optimistic locking
// ---------------------------------------------------------------------------

func TestConsistency_VersionIncrementsMonotonically(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	r, err := b.Store.Store(InternalRecord("s", KindUser, "k", "v1"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if r.Version != 1 {
		t.Fatalf("first version = %d, want 1", r.Version)
	}

	r2, err := b.Store.Update(r.Key, r.Version, func(rec *Record) error {
		rec.Value.Data = []byte("v2")
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if r2.Version != 2 {
		t.Fatalf("second version = %d, want 2", r2.Version)
	}
}

// TestConsistency_StaleWriteIsRefused is the compare-and-swap property. Without
// it, two subsystems updating one memory silently lose a write.
func TestConsistency_StaleWriteIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	r, _ := b.Store.Store(InternalRecord("s", KindUser, "k", "v1"))
	stale := r.Version

	// Writer A commits.
	if _, err := b.Store.Update(r.Key, stale, func(rec *Record) error {
		rec.Value.Data = []byte("A")
		return nil
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}

	// Writer B, holding the old version, is refused.
	_, err := b.Store.Update(r.Key, stale, func(rec *Record) error {
		rec.Value.Data = []byte("B")
		return nil
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected a version conflict, got %v", err)
	}

	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatal("the conflict must carry both versions so the caller can decide " +
			"between retry, merge and abandon")
	}
	if ce.Stale() != 1 {
		t.Fatalf("staleness = %d, want 1", ce.Stale())
	}
	if h.Metrics.Conflicts.Total() != 1 {
		t.Fatal("conflicts should be counted")
	}

	// A's write survived.
	got, _ := b.Store.Retrieve(r.Key, "")
	if string(got.Value.Data) != "A" {
		t.Fatalf("the losing write overwrote the winner: %q", got.Value.Data)
	}
}

func TestConsistency_CloneIsolatesCallers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	orig := InternalRecord("s", KindUser, "k", "original")
	orig.Value.Attributes = map[string]string{"a": "1"}
	stored, _ := b.Store.Store(orig)

	// Mutating a returned record must not reach the store.
	stored.Value.Data = []byte("tampered")
	stored.Value.Attributes["a"] = "2"

	got, _ := b.Store.Retrieve(stored.Key, "")
	if string(got.Value.Data) != "original" {
		t.Fatal("a caller mutating a returned record reached the store, " +
			"defeating optimistic locking")
	}
	if got.Value.Attributes["a"] != "1" {
		t.Fatal("attribute map was shared rather than copied")
	}
}

// ---------------------------------------------------------------------------
// Index
// ---------------------------------------------------------------------------

func TestIndex_ReplacementRetractsStaleAttributes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	r := WithAttributes(InternalRecord("s", KindConversation, "k", "v"),
		map[string]string{AttrConversation: "conv-1"})
	if _, err := b.Store.Store(r); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if got := b.Store.Index().ByConversation("conv-1"); len(got) != 1 {
		t.Fatalf("expected one posting, got %d", len(got))
	}

	// Replace with a different conversation binding.
	r2 := WithAttributes(InternalRecord("s", KindConversation, "k", "v2"),
		map[string]string{AttrConversation: "conv-2"})
	if _, err := b.Store.Store(r2); err != nil {
		t.Fatalf("Store replacement: %v", err)
	}

	if got := b.Store.Index().ByConversation("conv-1"); len(got) != 0 {
		t.Fatalf("the old posting leaked: %v — a stale secondary index resolves "+
			"to a record that no longer carries the attribute", got)
	}
	if got := b.Store.Index().ByConversation("conv-2"); len(got) != 1 {
		t.Fatalf("the new posting is missing, got %d", len(got))
	}
}

func TestIndex_DeleteRemovesFromEveryIndex(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	r := WithAttributes(InternalRecord("s", KindConversation, "k", "v"),
		map[string]string{AttrConversation: "c1", AttrSession: "sess1"})
	_, _ = b.Store.Store(r)

	if err := b.Store.Delete(r.Key, "test"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(b.Store.Index().ByConversation("c1")) != 0 {
		t.Error("conversation posting survived deletion")
	}
	if len(b.Store.Index().BySession("sess1")) != 0 {
		t.Error("session posting survived deletion")
	}
	if b.Store.Count() != 0 {
		t.Error("record count did not drop")
	}
}

func TestIndex_TimeRangeSelectsOnlyOverlappingBuckets(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	t0 := h.Clock.Now()
	_, _ = b.Store.Store(InternalRecord("s", KindConversation, "early", "a"))
	h.Clock.Advance(10 * time.Minute)
	_, _ = b.Store.Store(InternalRecord("s", KindConversation, "late", "b"))

	early := b.Store.Index().ByTimeRange(t0.Add(-time.Minute), t0.Add(time.Minute))
	if len(early) != 1 {
		t.Fatalf("expected only the early record, got %d", len(early))
	}
	all := b.Store.Index().ByTimeRange(t0.Add(-time.Minute), h.Clock.Now().Add(time.Minute))
	if len(all) != 2 {
		t.Fatalf("expected both records, got %d", len(all))
	}
}

func TestIndex_PostingListIsBounded(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.Index.MaxKeysPerAttributeValue = 5
	h := newHarness(t, WithHarnessConfig(cfg))
	b := h.Assistant()

	for i := 0; i < 20; i++ {
		r := WithAttributes(
			InternalRecord("s", KindConversation, fmt.Sprintf("k%02d", i), "v"),
			map[string]string{AttrConversation: "same"})
		_, _ = b.Store.Store(r)
	}
	if got := len(b.Store.Index().ByConversation("same")); got > 5 {
		t.Fatalf("posting list grew to %d past its bound; an attribute used as a "+
			"constant would turn a lookup into a full scan", got)
	}
}

// ---------------------------------------------------------------------------
// Retrieval
// ---------------------------------------------------------------------------

func TestRetrieval_DistinguishesNegativeOutcomes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	missing := Key{Subject: "s", Kind: KindUser, Name: "never"}
	if _, err := b.Store.Retrieve(missing, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing -> %v, want ErrNotFound", err)
	}

	r, _ := b.Store.Store(InternalRecord("s", KindUser, "expiring", "v"))
	_ = b.Store.Expire(r.Key)
	if _, err := b.Store.Retrieve(r.Key, ""); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired -> %v, want ErrExpired", err)
	}

	r2, _ := b.Store.Store(InternalRecord("s", KindUser, "archiving", "v"))
	_ = b.Store.Archive(r2.Key)
	if _, err := b.Store.Retrieve(r2.Key, ""); !errors.Is(err, ErrArchived) {
		t.Fatalf("archived -> %v, want ErrArchived", err)
	}

	r3, _ := b.Store.Store(InternalRecord("s", KindUser, "redacting", "v"))
	_ = b.Store.Redact(r3.Key, RedactPolicy, "op")
	if _, err := b.Store.Retrieve(r3.Key, ""); !errors.Is(err, ErrRedacted) {
		t.Fatalf("redacted -> %v, want ErrRedacted", err)
	}
}

// TestRetrieval_UnscopedQueryIsRefused is INV-MEM-5: a query naming no subject
// would read across every subject in a shared engine.
func TestRetrieval_UnscopedQueryIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if _, err := h.Assistant().Retriever.Search(Query{}, ""); !errors.Is(err, ErrInvariant) {
		t.Fatalf("an unscoped query must be refused, got %v", err)
	}
}

// TestRetrieval_SensitivityCeilingFailsClosed asserts that a caller who forgets
// to state a ceiling gets the least, not the most.
func TestRetrieval_SensitivityCeilingFailsClosed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Grant("s", "c1")
	b := h.Assistant()

	_, _ = b.Store.Store(InternalRecord("s", KindUser, "internal", "i"))
	_, _ = b.Store.Store(PersonalRecord("s", KindUser, "personal", "p", "c1"))

	// Zero-valued MaxSensitivity is Public.
	res, err := b.Retriever.Search(Query{Subject: "s"}, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, rec := range res.Records {
		if rec.Sensitivity > Public {
			t.Fatalf("a query with no stated ceiling returned %s data", rec.Sensitivity)
		}
	}
}

func TestRetrieval_OrderIsDeterministic(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	// Identical timestamps, so ordering depends entirely on the tie-break.
	for _, name := range []string{"zebra", "alpha", "mango"} {
		_, _ = b.Store.Store(InternalRecord("s", KindUser, name, "v"))
	}

	var first []string
	for i := 0; i < 20; i++ {
		res, _ := b.Retriever.Search(Query{Subject: "s", Order: OrderRecent,
			MaxSensitivity: Internal}, "")
		got := make([]string, 0, len(res.Records))
		for _, r := range res.Records {
			got = append(got, r.Key.Name)
		}
		if first == nil {
			first = got
			continue
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d diverged: %v vs %v", i, got, first)
			}
		}
	}
}

func TestRetrieval_PlanSelectionUsesTheRightIndex(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()
	_, _ = b.Store.Store(WithAttributes(
		InternalRecord("s", KindConversation, "k", "v"),
		map[string]string{AttrConversation: "c1"}))

	cases := []struct {
		name  string
		query Query
		want  string
	}{
		{"name -> primary",
			Query{Subject: "s", Name: "k", Kinds: []Kind{KindConversation}, MaxSensitivity: Internal},
			"primary"},
		{"attribute -> secondary",
			Query{Attribute: AttrConversation, Value: "c1", MaxSensitivity: Internal},
			"secondary:" + AttrConversation},
		{"time -> time index",
			Query{Subject: "s", From: h.Clock.Now().Add(-time.Hour), MaxSensitivity: Internal},
			"time"},
		{"subject+kind -> kind scan",
			Query{Subject: "s", Kinds: []Kind{KindConversation}, MaxSensitivity: Internal},
			"primary_scan:kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := b.Retriever.Search(tc.query, "")
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if res.Index != tc.want {
				t.Fatalf("index = %q, want %q", res.Index, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

func TestMetrics_HitRateTracksRetrieval(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	r, _ := b.Store.Store(InternalRecord("s", KindUser, "k", "v"))
	for i := 0; i < 3; i++ {
		_, _ = b.Store.Retrieve(r.Key, "")
	}
	_, _ = b.Store.Retrieve(Key{Subject: "s", Kind: KindUser, Name: "absent"}, "")

	if got := h.Metrics.HitRate("exact"); got < 0.7 || got > 0.8 {
		t.Fatalf("hit rate = %v, want 0.75 (3 hits, 1 miss)", got)
	}
}

func TestMetrics_SnapshotIsStablyOrdered(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.Stores.Inc("user", "long_term")
	m.Records.Set(5)
	m.RetrieveLatency.Observe(0.001, "exact")

	first, second := m.Snapshot(), m.Snapshot()
	if len(first) != len(second) {
		t.Fatalf("snapshot length unstable: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Fatalf("snapshot order unstable at %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// TestEvents_CarryIdentifiersNotContent is invariant I7 applied to this engine.
func TestEvents_CarryIdentifiersNotContent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	secret := "the-quick-brown-fox-payload"
	_, _ = h.Assistant().Store.Store(InternalRecord("s", KindUser, "k", secret))

	for _, e := range h.Events.Events() {
		rendered := fmt.Sprintf("%+v", e)
		if strings.Contains(rendered, secret) {
			t.Fatalf("an event carried payload content: %s", rendered)
		}
	}
	if h.Events.CountOf(EventCreated) != 1 {
		t.Fatalf("expected one created event, got %d", h.Events.CountOf(EventCreated))
	}
}

func TestEvents_TopicNamingMatchesTheFrozenConvention(t *testing.T) {
	t.Parallel()
	for _, e := range []EventType{EventCreated, EventUpdated, EventDeleted,
		EventExpired, EventPromoted, EventDemoted, EventMerged, EventArchived} {
		topic := e.Topic()
		if !strings.HasPrefix(topic, "memory.record.") || !strings.HasSuffix(topic, ".v1") {
			t.Errorf("topic %q does not match <domain>.<entity>.<event>.v<major>", topic)
		}
		if strings.Contains(topic, "-") {
			t.Errorf("topic %q contains a hyphen, which collides with Prometheus "+
				"metric-name normalisation", topic)
		}
	}
}

func TestEvents_FailingPublisherDoesNotFailTheWrite(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Events.FailWith(errors.New("broker down"))

	if _, err := h.Assistant().Store.Store(InternalRecord("s", KindUser, "k", "v")); err != nil {
		t.Fatalf("a broken subscriber must not fail a memory write: %v", err)
	}
	if h.Metrics.EventsDropped.Total() == 0 {
		t.Fatal("a dropped event should be counted so the loss is visible")
	}
}

// TestPolicy_OversizedRecordIsRefused covers INV-MEM-8, which had no dedicated
// test until the Phase 10C audit noticed the gap.
//
// The cap is not about efficiency. An unbounded payload is an in-process
// memory-exhaustion vector reachable by any caller, and a 40 MB "memory" is not
// a memory — it is a document that has ended up in the wrong subsystem.
func TestPolicy_OversizedRecordIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := h.Assistant().Store

	limit := DefaultPolicy().MaxRecordBytes

	// The cap is on Value.Size(), which counts the content type and every
	// attribute as well as the payload. That is deliberate: a cap on Data alone
	// could be evaded by moving content into attributes, where it would also
	// escape the classification model (SECURITY_REVIEW R1).
	atLimit := InternalRecord("s", KindUser, "big", "")
	atLimit.Value.Data = make([]byte, limit-atLimit.Value.Size())
	if _, err := st.Store(atLimit); err != nil {
		t.Fatalf("a record exactly at the limit must be admitted: %v", err)
	}

	over := InternalRecord("s", KindUser, "bigger", "")
	over.Value.Data = make([]byte, limit+1-over.Value.Size())
	_, err := st.Store(over)
	if err == nil {
		t.Fatal("a record one byte over the limit was admitted")
	}
	if !strings.Contains(err.Error(), "INV-MEM-8") {
		t.Errorf("refusal should name the invariant it enforces, got: %v", err)
	}

	// The refused record must leave nothing behind. A rejected write that has
	// already claimed an index entry is worse than one that succeeds.
	if _, err := st.Retrieve(over.Key, ""); err == nil {
		t.Error("a refused write left a retrievable record")
	}
}
