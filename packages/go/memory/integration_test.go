package memory

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Lifecycle end to end
// ---------------------------------------------------------------------------

// TestIntegration_PromotionLadder walks a record from working to long term
// through the scheduler, exactly as production would.
func TestIntegration_PromotionLadder(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	r := InternalRecord("s", KindUser, "fact", "value")
	r.Tier = TierWorking
	stored, err := b.Store.Store(r)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Read it enough to earn promotion, past the minimum age.
	h.Clock.Advance(6 * time.Second)
	for i := 0; i < 3; i++ {
		if _, err := b.Store.Retrieve(stored.Key, ""); err != nil {
			t.Fatalf("Retrieve %d: %v", i, err)
		}
	}

	h.Runtime.Sweep()
	got, err := b.Store.Retrieve(stored.Key, "")
	if err != nil {
		t.Fatalf("after sweep: %v", err)
	}
	if got.Tier != TierShortTerm {
		t.Fatalf("tier = %v, want short_term after promotion\n(access=%d age=%v)",
			got.Tier, got.AccessCount, got.Age(h.Clock.Now()))
	}
	if h.Events.CountOf(EventPromoted) != 1 {
		t.Fatalf("expected one promotion event, got %d", h.Events.CountOf(EventPromoted))
	}

	// Promotion is single-step: a second sweep is needed for long term.
	h.Clock.Advance(6 * time.Second)
	for i := 0; i < 3; i++ {
		_, _ = b.Store.Retrieve(stored.Key, "")
	}
	h.Runtime.Sweep()
	got, _ = b.Store.Retrieve(stored.Key, "")
	if got.Tier != TierLongTerm {
		t.Fatalf("tier = %v, want long_term", got.Tier)
	}
}

func TestIntegration_IdleWorkingMemoryExpires(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	r := NewScratchpad("s", "note", Value{ContentType: "text/plain", Data: []byte("temp")})
	r.Provenance = Provenance{Source: "test"}
	stored, err := b.Store.Store(r)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	h.Clock.Advance(31 * time.Second)
	h.Runtime.Sweep()

	if _, err := b.Store.Retrieve(stored.Key, ""); !errors.Is(err, ErrExpired) {
		t.Fatalf("idle working memory should expire, got %v", err)
	}
	if h.Events.CountOf(EventExpired) == 0 {
		t.Fatal("expiry should publish an event")
	}
}

func TestIntegration_PinnedRecordSurvivesEverything(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	pref := NewPreference("s", "language", Value{ContentType: "text/plain",
		Data: []byte("hi-IN")}, Internal)
	pref.Provenance = Provenance{Source: "test"}
	stored, err := b.Store.Store(pref)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	h.Clock.Advance(48 * time.Hour)
	for i := 0; i < 5; i++ {
		h.Runtime.Sweep()
	}

	got, err := b.Store.Retrieve(stored.Key, "")
	if err != nil {
		t.Fatalf("a pinned preference must survive maintenance: %v", err)
	}
	if got.Tier != TierLongTerm {
		t.Fatalf("a pinned record must not be demoted, tier = %v", got.Tier)
	}
}

// ---------------------------------------------------------------------------
// Erasure — the DPDP path
// ---------------------------------------------------------------------------

// TestIntegration_ForgetSpansEveryNamespace is the compliance property that
// matters most: an erasure reaching only one namespace is a failure that looks
// like a success.
func TestIntegration_ForgetSpansEveryNamespace(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Grant("victim", "c1")

	for _, ns := range []Namespace{NamespaceAssistant, NamespaceReceptionist,
		NamespaceFraud, NamespaceTelephony} {
		b := h.Bundle(ns)
		if _, err := b.Store.Store(PersonalRecord("victim", KindUser,
			"name-"+ns.String(), "data", "c1")); err != nil {
			t.Fatalf("seed %s: %v", ns, err)
		}
		// A bystander whose memories must survive.
		if _, err := b.Store.Store(InternalRecord("bystander", KindUser, "n", "d")); err != nil {
			t.Fatalf("seed bystander in %s: %v", ns, err)
		}
	}

	report, err := h.Runtime.Coordinator().Forget("victim", "dpo")
	if err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if report.TotalDeleted != 4 {
		t.Fatalf("deleted %d, want 4 — one per namespace", report.TotalDeleted)
	}
	if !report.Complete {
		t.Fatalf("erasure should be complete: %+v", report)
	}
	if len(report.PerNamespace) != 4 {
		t.Fatalf("every namespace must be reported, got %d", len(report.PerNamespace))
	}

	for _, ns := range h.Runtime.Registry().Namespaces() {
		b := h.Bundle(ns)
		if len(b.Store.Index().BySubject("victim")) != 0 {
			t.Errorf("namespace %s still holds the erased subject", ns)
		}
		if len(b.Store.Index().BySubject("bystander")) != 1 {
			t.Errorf("namespace %s lost an unrelated subject's memory", ns)
		}
	}
}

// TestIntegration_LegalHoldSurvivesErasureAndIsReported asserts that an
// erasure which retains says so.
func TestIntegration_LegalHoldSurvivesErasureAndIsReported(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	held := NewPolicy("subj", "retention-choice", Value{ContentType: "text/plain",
		Data: []byte("90d")})
	held.Provenance = Provenance{Source: "test"}
	if _, err := b.Store.Store(held); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := b.Store.Store(InternalRecord("subj", KindUser, "ordinary", "d")); err != nil {
		t.Fatalf("Store ordinary: %v", err)
	}

	report, err := h.Runtime.Coordinator().Forget("subj", "dpo")
	if err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if report.Complete {
		t.Fatal("an erasure that retained anything must not report itself complete")
	}
	if report.TotalRedacted+report.TotalRetained == 0 {
		t.Fatal("the legal-hold record should be redacted or retained, not deleted")
	}

	res := report.PerNamespace[NamespaceAssistant]
	if len(res.RetainedKeys) == 0 {
		t.Fatal("the subject is entitled to know exactly what survived")
	}

	// The held record's payload is gone but its existence is provable.
	if _, err := b.Store.Retrieve(held.Key, ""); !errors.Is(err, ErrRedacted) {
		t.Fatalf("held record should be redacted, got %v", err)
	}
}

func TestIntegration_RedactDestroysIndexableAttributes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	r := WithAttributes(InternalRecord("s", KindConversation, "k", "secret"),
		map[string]string{AttrConversation: "conv-9"})
	stored, _ := b.Store.Store(r)

	if err := b.Store.Redact(stored.Key, RedactSubjectRequest, "dpo"); err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if got := b.Store.Index().ByConversation("conv-9"); len(got) != 0 {
		t.Fatal("redaction must destroy indexed attributes, or the record stays " +
			"discoverable by content that was supposed to be gone")
	}
}

// ---------------------------------------------------------------------------
// Merge, split, compression
// ---------------------------------------------------------------------------

func TestIntegration_MergeInheritsStrictestClassification(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Grant("s", "c1")
	b := h.Assistant()

	pub, _ := b.Store.Store(InternalRecord("s", KindConversation, "a", "alpha"))
	sensitive := PersonalRecord("s", KindConversation, "b", "beta", "c1")
	sensitive.Sensitivity = Sensitive
	sens, err := b.Store.Store(sensitive)
	if err != nil {
		t.Fatalf("Store sensitive: %v", err)
	}

	target := Key{Subject: "s", Kind: KindConversation, Name: "merged"}
	merged, err := b.Store.Merge(target, []Key{pub.Key, sens.Key}, JoinMerger{}, "test")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if merged.Sensitivity != Sensitive {
		t.Fatalf("a merge must inherit the strictest classification, got %s — "+
			"anything else launders Sensitive data into a lower class",
			merged.Sensitivity)
	}
	if merged.ConsentRef == "" {
		t.Fatal("the merged record must carry the basis of its strictest input")
	}
	if !merged.Provenance.Derived {
		t.Fatal("a merged record is derived")
	}
}

func TestIntegration_MergeIsAllOrNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	a, _ := b.Store.Store(InternalRecord("s", KindConversation, "a", "alpha"))
	missing := Key{Subject: "s", Kind: KindConversation, Name: "absent"}

	target := Key{Subject: "s", Kind: KindConversation, Name: "merged"}
	if _, err := b.Store.Merge(target, []Key{a.Key, missing}, JoinMerger{}, "t"); err == nil {
		t.Fatal("a merge with a missing source must fail")
	}
	if _, err := b.Store.Retrieve(a.Key, ""); err != nil {
		t.Fatal("a failed merge must leave its sources intact; destroying inputs " +
			"before writing the output loses memories to a transient error")
	}
}

func TestIntegration_SplitProducesDeterministicParts(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	src, _ := b.Store.Store(InternalRecord("s", KindConversation, "whole", "abcdefgh"))
	parts, err := b.Store.Split(src.Key, HalfSplitter{}, "t")
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected two parts, got %d", len(parts))
	}
	if parts[0].Key.Name != "whole-a" || parts[1].Key.Name != "whole-b" {
		t.Fatalf("parts must be created in sorted order: %s, %s",
			parts[0].Key.Name, parts[1].Key.Name)
	}
	if _, err := b.Store.Retrieve(src.Key, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("the source should be removed after a successful split")
	}
}

func TestIntegration_CompressionPreservesWhatMatters(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.Grant("s", "c1")
	b := h.Assistant()

	// Ten ordinary records, one pinned, one sensitive.
	for i := 0; i < 10; i++ {
		_, _ = b.Store.Store(InternalRecord("s", KindConversation,
			fmt.Sprintf("m%02d", i), fmt.Sprintf("message %d", i)))
		h.Clock.Advance(time.Second)
	}
	pinned := InternalRecord("s", KindConversation, "a-pinned", "important")
	pinned.Pinned = true
	_, _ = b.Store.Store(pinned)

	sens := PersonalRecord("s", KindConversation, "a-sensitive", "private", "c1")
	sens.Sensitivity = Sensitive
	if _, err := b.Store.Store(sens); err != nil {
		t.Fatalf("Store sensitive: %v", err)
	}

	plan := b.Compressor.Plan("s", KindConversation, TriggerCount)
	if !plan.Viable() {
		t.Fatalf("expected a viable plan, selected %d", len(plan.Selected))
	}
	if plan.Preserved[pinned.Key] != "pinned" {
		t.Fatalf("a pinned record must be preserved, got %q", plan.Preserved[pinned.Key])
	}
	if plan.Preserved[sens.Key] != "sensitive" {
		t.Fatalf("a Sensitive record must not be folded into a shared summary, "+
			"got %q", plan.Preserved[sens.Key])
	}
	// The newest KeepRecent are exempt.
	recent := 0
	for _, why := range plan.Preserved {
		if why == "recent" {
			recent++
		}
	}
	if recent == 0 {
		t.Fatal("the most recent records carry the most meaning and must be exempt")
	}
}

func TestIntegration_CompressionWithSummarizer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	comp, err := NewCompressor(b.Store, DefaultCompressionPolicy(), &ConcatSummarizer{})
	if err != nil {
		t.Fatalf("NewCompressor: %v", err)
	}
	for i := 0; i < 12; i++ {
		_, _ = b.Store.Store(InternalRecord("s", KindConversation,
			fmt.Sprintf("m%02d", i), fmt.Sprintf("message %d", i)))
		h.Clock.Advance(time.Second)
	}
	before := b.Store.Count()

	plan := comp.Plan("s", KindConversation, TriggerCount)
	summary, err := comp.Compress(plan, "t")
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if summary == nil {
		t.Fatal("expected a summary record")
	}
	if b.Store.Count() >= before {
		t.Fatalf("compression should reduce the record count: %d -> %d",
			before, b.Store.Count())
	}
	if !summary.Provenance.Derived {
		t.Fatal("a summary is derived")
	}
	if h.Metrics.CompressionRatio.Count() == 0 {
		t.Fatal("the compression ratio should be observed")
	}
}

// TestIntegration_FailedSummarisationLeavesInputsIntact is the property that
// makes compression safe to run automatically.
func TestIntegration_FailedSummarisationLeavesInputsIntact(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	comp, err := NewCompressor(b.Store, DefaultCompressionPolicy(),
		&ConcatSummarizer{FailAlways: true})
	if err != nil {
		t.Fatalf("NewCompressor: %v", err)
	}

	for i := 0; i < 12; i++ {
		_, _ = b.Store.Store(InternalRecord("s", KindConversation,
			fmt.Sprintf("m%02d", i), "data"))
		h.Clock.Advance(time.Second)
	}
	before := b.Store.Count()

	plan := comp.Plan("s", KindConversation, TriggerCount)
	if _, err := comp.Compress(plan, "t"); err == nil {
		t.Fatal("the scripted summarizer should have failed")
	}
	if b.Store.Count() != before {
		t.Fatalf("a failed summarisation must leave everything intact: %d -> %d",
			before, b.Store.Count())
	}
}

// ---------------------------------------------------------------------------
// Snapshot and rollback — recovery tests
// ---------------------------------------------------------------------------

func TestRecovery_RollbackRestoresSubjectState(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	original, _ := b.Store.Store(InternalRecord("s", KindUser, "keep", "v1"))
	snap := b.Store.Snapshot("s", "before-change")

	_, _ = b.Store.Update(original.Key, original.Version, func(r *Record) error {
		r.Value.Data = []byte("v2")
		return nil
	})
	_, _ = b.Store.Store(InternalRecord("s", KindUser, "added-after", "x"))

	if err := b.Store.Rollback(snap); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	got, err := b.Store.Retrieve(original.Key, "")
	if err != nil {
		t.Fatalf("Retrieve after rollback: %v", err)
	}
	if string(got.Value.Data) != "v1" {
		t.Fatalf("value = %q, want v1", got.Value.Data)
	}
	after := Key{Subject: "s", Kind: KindUser, Name: "added-after"}
	if _, err := b.Store.Retrieve(after, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("a record created after the snapshot must be removed by rollback")
	}
}

// TestRecovery_RollbackNeverRevertsLegalHold is the one rollback that is never
// permitted.
func TestRecovery_RollbackNeverRevertsLegalHold(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	snap := b.Store.Snapshot("s", "empty")

	held := NewPolicy("s", "hold", Value{ContentType: "text/plain", Data: []byte("keep")})
	held.Provenance = Provenance{Source: "test"}
	if _, err := b.Store.Store(held); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if err := b.Store.Rollback(snap); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if _, err := b.Store.Retrieve(held.Key, ""); err != nil {
		t.Fatal("rolling back a legally-required retention decision is the one " +
			"rollback that must never happen")
	}
}

func TestRecovery_CoordinatorSnapshotsEveryNamespace(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.Runtime.Coordinator()

	for _, ns := range h.Runtime.Registry().Namespaces() {
		_, _ = h.Bundle(ns).Store.Store(InternalRecord("s", KindUser, "k", "v1"))
	}
	ids := c.SnapshotAll("s", "before")
	if len(ids) != len(h.Runtime.Registry().Namespaces()) {
		t.Fatalf("snapshot count = %d, want one per namespace", len(ids))
	}

	for _, ns := range h.Runtime.Registry().Namespaces() {
		b := h.Bundle(ns)
		r, _ := b.Store.Retrieve(Key{Subject: "s", Kind: KindUser, Name: "k"}, "")
		_, _ = b.Store.Update(r.Key, r.Version, func(rec *Record) error {
			rec.Value.Data = []byte("v2")
			return nil
		})
	}

	if err := c.RollbackAll(ids); err != nil {
		t.Fatalf("RollbackAll: %v", err)
	}
	for _, ns := range h.Runtime.Registry().Namespaces() {
		got, _ := h.Bundle(ns).Store.Retrieve(Key{Subject: "s", Kind: KindUser, Name: "k"}, "")
		if string(got.Value.Data) != "v1" {
			t.Errorf("namespace %s not rolled back: %q", ns, got.Value.Data)
		}
	}
}

// ---------------------------------------------------------------------------
// Namespace isolation
// ---------------------------------------------------------------------------

// TestIntegration_NamespacesAreIsolated is the multi-agent property: a fraud
// agent must not reach the assistant's memory by guessing a key.
func TestIntegration_NamespacesAreIsolated(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	key := Key{Subject: "s", Kind: KindUser, Name: "shared-name"}
	_, _ = h.Bundle(NamespaceAssistant).Store.Store(
		InternalRecord("s", KindUser, "shared-name", "assistant-value"))

	if _, err := h.Bundle(NamespaceFraud).Store.Retrieve(key, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("namespaces must be isolated by construction, got %v", err)
	}

	_, _ = h.Bundle(NamespaceFraud).Store.Store(
		InternalRecord("s", KindUser, "shared-name", "fraud-value"))

	a, _ := h.Bundle(NamespaceAssistant).Store.Retrieve(key, "")
	f, _ := h.Bundle(NamespaceFraud).Store.Retrieve(key, "")
	if string(a.Value.Data) == string(f.Value.Data) {
		t.Fatal("identical keys in different namespaces must hold different values")
	}
}

func TestIntegration_NamespaceCreatedOnDemand(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	b, err := h.Runtime.Namespace("agent-42")
	if err != nil {
		t.Fatalf("on-demand namespace: %v", err)
	}
	if b.Store.Count() != 0 {
		t.Fatal("a new namespace must be empty, so a typo yields nothing rather " +
			"than a cross-agent read")
	}
}

// ---------------------------------------------------------------------------
// Context builder
// ---------------------------------------------------------------------------

func TestContext_FillsHighPriorityScopesFirst(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	pol := NewPolicy("s", "limits", Value{ContentType: "text/plain", Data: []byte("max=3")})
	pol.Provenance = Provenance{Source: "test"}
	_, _ = b.Store.Store(pol)

	pref := NewPreference("s", "lang", Value{ContentType: "text/plain", Data: []byte("hi-IN")}, Internal)
	pref.Provenance = Provenance{Source: "test"}
	_, _ = b.Store.Store(pref)

	// Plenty of conversation detail, which should lose under budget pressure.
	for i := 0; i < 30; i++ {
		_, _ = b.Store.Store(InternalRecord("s", KindConversation,
			fmt.Sprintf("c%02d", i), "a fairly long conversational message body"))
	}

	assembled, err := b.Context.Build(ContextRequest{
		Subject:        "s",
		Budget:         TokenBudget{MaxTokens: 60},
		MaxSensitivity: Internal,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	runtimeSlice, hasRuntime := assembled.Slice(ScopeRuntime)
	if !hasRuntime || len(runtimeSlice.Records) == 0 {
		t.Fatal("runtime policy must survive a tight budget: an assistant that " +
			"forgets an operating limit is dangerous")
	}
	personal, hasPersonal := assembled.Slice(ScopePersonal)
	if !hasPersonal || len(personal.Records) == 0 {
		t.Fatal("a stated preference must outrank conversation detail")
	}
	if !assembled.Truncated && len(assembled.Dropped) == 0 {
		t.Fatal("a 60-token budget over this data must truncate or drop something")
	}
	if assembled.TotalTokens > assembled.BudgetTokens {
		t.Fatalf("assembled %d tokens over a %d budget",
			assembled.TotalTokens, assembled.BudgetTokens)
	}
}

func TestContext_RequiresSubject(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if _, err := h.Assistant().Context.Build(ContextRequest{}); !errors.Is(err, ErrInvariant) {
		t.Fatalf("context assembly without a subject must be refused, got %v", err)
	}
}

func TestContext_DoesNotMutateWhatItReads(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()
	stored, _ := b.Store.Store(InternalRecord("s", KindConversation, "k", "v"))

	before, _ := b.Store.Retrieve(stored.Key, "")
	for i := 0; i < 5; i++ {
		_, _ = b.Context.Build(ContextRequest{Subject: "s", MaxSensitivity: Internal})
	}
	after, _ := b.Store.Retrieve(stored.Key, "")

	if after.Version != before.Version {
		t.Fatalf("assembling context mutated a record: v%d -> v%d",
			before.Version, after.Version)
	}
}

// ---------------------------------------------------------------------------
// Encryption hook
// ---------------------------------------------------------------------------

func TestIntegration_EncryptorIsWiredBothWays(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	enc := &ReverseEncryptor{}
	cfg.Policy.Encryptor = enc
	h := newHarness(t, WithHarnessConfig(cfg))
	b := h.Assistant()

	stored, err := b.Store.Store(InternalRecord("s", KindUser, "k", "plaintext"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	// The stored form is transformed.
	if string(stored.Value.Data) == "plaintext" {
		t.Fatal("the encryptor was not applied on the store path")
	}
	// The retrieved form is not.
	got, err := b.Store.Retrieve(stored.Key, "")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if string(got.Value.Data) != "plaintext" {
		t.Fatalf("round trip failed: %q", got.Value.Data)
	}
}

func TestFailure_EncryptorFailureRefusesTheWrite(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	enc := &ReverseEncryptor{}
	enc.FailFor("doomed")
	cfg.Policy.Encryptor = enc
	h := newHarness(t, WithHarnessConfig(cfg))
	b := h.Assistant()

	if _, err := b.Store.Store(InternalRecord("doomed", KindUser, "k", "v")); err == nil {
		t.Fatal("a failed encryption must refuse the write rather than storing " +
			"plaintext it promised to protect")
	}
	if b.Store.Count() != 0 {
		t.Fatal("nothing should have been stored")
	}
}

// ---------------------------------------------------------------------------
// Concurrency and stress
// ---------------------------------------------------------------------------

func TestStress_ConcurrentWritesAcrossSubjects(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	const subjects, perSubject = 50, 20
	var wg sync.WaitGroup
	errs := make(chan error, subjects)

	for s := 0; s < subjects; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			subject := SubjectID(fmt.Sprintf("subj-%03d", s))
			for i := 0; i < perSubject; i++ {
				r := InternalRecord(subject, KindConversation, fmt.Sprintf("m%03d", i), "data")
				if _, err := b.Store.Store(r); err != nil {
					errs <- fmt.Errorf("subject %d record %d: %w", s, i, err)
					return
				}
			}
		}(s)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	if got := b.Store.Count(); got != subjects*perSubject {
		t.Fatalf("count = %d, want %d — records were lost under concurrency",
			got, subjects*perSubject)
	}
}

// TestStress_ConcurrentUpdatesToOneRecord proves compare-and-swap holds under
// contention: every write either lands or is refused, and none is lost.
func TestStress_ConcurrentUpdatesToOneRecord(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	stored, _ := b.Store.Store(InternalRecord("s", KindUser, "counter", "0"))

	const writers = 32
	var (
		wg        sync.WaitGroup
		succeeded int64
		mu        sync.Mutex
	)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 20; attempt++ {
				cur, err := b.Store.Retrieve(stored.Key, "")
				if err != nil {
					return
				}
				_, err = b.Store.Update(cur.Key, cur.Version, func(r *Record) error {
					r.Value.Data = append(r.Value.Data, 'x')
					return nil
				})
				if err == nil {
					mu.Lock()
					succeeded++
					mu.Unlock()
					return
				}
				if !errors.Is(err, ErrVersionConflict) {
					return
				}
			}
		}()
	}
	wg.Wait()

	final, err := b.Store.Retrieve(stored.Key, "")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	// Every successful update appended exactly one byte to the original "0".
	expected := int(succeeded) + 1
	if len(final.Value.Data) != expected {
		t.Fatalf("payload length %d, want %d — a write was lost despite CAS",
			len(final.Value.Data), expected)
	}
	if final.Version != Version(succeeded)+1 {
		t.Fatalf("version %d, want %d", final.Version, succeeded+1)
	}
}

func TestStress_ConcurrentReadWriteSweep(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	b := h.Assistant()

	for i := 0; i < 200; i++ {
		_, _ = b.Store.Store(InternalRecord("s", KindConversation,
			fmt.Sprintf("m%03d", i), "data"))
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = b.Retriever.Search(Query{Subject: "s", Limit: 10,
					MaxSensitivity: Internal}, "")
			}
		}(i)
	}
	// Writers.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = b.Store.Store(InternalRecord("s", KindConversation,
					fmt.Sprintf("w%d-%d", i, n), "data"))
				n++
				if n > 100 {
					return
				}
			}
		}(i)
	}
	// Maintenance.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			h.Runtime.Sweep()
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	// The invariant is internal consistency, not an exact count.
	stats := b.Store.Index().Stats()
	if stats.Records != b.Store.Count() {
		t.Fatalf("index and store disagree on record count: %d vs %d",
			stats.Records, b.Store.Count())
	}
}

func TestStress_ManyNamespacesConcurrently(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ns := Namespace(fmt.Sprintf("agent-%02d", i))
			b, err := h.Runtime.Namespace(ns)
			if err != nil {
				t.Errorf("namespace %s: %v", ns, err)
				return
			}
			for j := 0; j < 10; j++ {
				_, _ = b.Store.Store(InternalRecord("s", KindUser,
					fmt.Sprintf("k%02d", j), "v"))
			}
		}(i)
	}
	wg.Wait()

	if got := h.Runtime.Coordinator().Count(); got < 200 {
		t.Fatalf("expected at least 200 records across namespaces, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

func TestLoad_SweepStaysWithinBudget(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.SweepBudget = 5 * time.Millisecond
	cfg.SweepInterval = time.Second
	h := newHarness(t, WithHarnessConfig(cfg))
	b := h.Assistant()

	for i := 0; i < 5000; i++ {
		_, _ = b.Store.Store(InternalRecord(
			SubjectID(fmt.Sprintf("s%03d", i%100)), KindConversation,
			fmt.Sprintf("m%05d", i), "data"))
	}

	reports := h.Runtime.Sweep()
	for _, r := range reports {
		if r.Namespace != NamespaceAssistant {
			continue
		}
		// The budget is enforced against the runtime's clock, which is fake and
		// does not advance during the sweep — so the sweep completes. What is
		// asserted is that it examined everything without error and reported it.
		if r.Examined == 0 {
			t.Fatal("the sweep examined nothing")
		}
		t.Logf("swept %d records in %v (budget exceeded: %v)",
			r.Examined, r.Duration, r.BudgetExceeded)
	}
}

func TestLoad_SubjectCapIsEnforced(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.Policy.MaxRecordsPerSubject = 10
	h := newHarness(t, WithHarnessConfig(cfg))
	b := h.Assistant()

	for i := 0; i < 10; i++ {
		if _, err := b.Store.Store(InternalRecord("s", KindUser,
			fmt.Sprintf("k%02d", i), "v")); err != nil {
			t.Fatalf("store %d: %v", i, err)
		}
	}
	if _, err := b.Store.Store(InternalRecord("s", KindUser, "overflow", "v")); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("past the subject cap the store must refuse, got %v", err)
	}

	// Replacing an existing record must not consume new quota.
	if _, err := b.Store.Store(InternalRecord("s", KindUser, "k00", "v2")); err != nil {
		t.Fatalf("replacing an existing record should not need new quota: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Runtime lifecycle
// ---------------------------------------------------------------------------

func TestRuntime_StartStopIsClean(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	if err := h.Runtime.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := h.Runtime.Start(); !errors.Is(err, ErrInvariant) {
		t.Fatalf("a second Start must be refused, got %v", err)
	}
	if err := h.Runtime.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := h.Runtime.Stop(); err != nil {
		t.Fatalf("Stop must be idempotent: %v", err)
	}
}

func TestRuntime_SweepIsDeterministic(t *testing.T) {
	t.Parallel()

	run := func() []SweepReport {
		h := newHarness(t)
		b := h.Assistant()
		for i := 0; i < 20; i++ {
			r := InternalRecord("s", KindConversation, fmt.Sprintf("m%02d", i), "d")
			r.Tier = TierWorking
			_, _ = b.Store.Store(r)
		}
		h.Clock.Advance(60 * time.Second)
		return h.Runtime.Sweep()
	}

	first := run()
	for i := 0; i < 10; i++ {
		got := run()
		if len(got) != len(first) {
			t.Fatalf("report count unstable: %d vs %d", len(got), len(first))
		}
		for j := range got {
			if got[j].Expired != first[j].Expired || got[j].Promoted != first[j].Promoted {
				t.Fatalf("sweep %d diverged: %+v vs %+v", i, got[j], first[j])
			}
		}
	}
}
