package intent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
)

// SECURITY GUARDS — T5.
//
// SCOPE, AND WHAT IS DELIBERATELY NOT REPEATED HERE. T4 already proves the
// closed vocabularies, the bounds, the adversarial corpus, the caller-text
// sentinel, the AST forbidden-import guard and the no-package-mutable-state
// guard. Re-asserting those would inflate the count without adding coverage.
//
// This file covers the surface T4 did not:
//
//   - classifier STATE, not just output: an utterance must leave no trace in
//     the Classifier after Classify returns;
//   - the TYPE boundary: proven by reflection over what actually crosses it,
//     rather than by inspecting the values one corpus happened to produce;
//   - persistence- and execution-shaped API, which is how a "harmless" helper
//     becomes a write path;
//   - the structural ABSENCE of a metric-label path, which cannot be tested
//     behaviourally because there is nothing to observe.
//
// In package intent, not intent_test, because several guards must reach
// unexported state — proving a struct holds no caller text requires seeing the
// struct.
//
// THE DISTINCTION THIS FILE IS BUILT ON. An utterance containing "ghp_..." is
// not itself a security failure; the classifier is supposed to receive caller
// text. The failure would be that text CROSSING into somewhere it can later be
// emitted, persisted, labelled or executed. Every test below asserts on that
// crossing, not on the input.

// credentialShaped are inputs that look like secrets. They are ordinary
// utterances as far as the classifier is concerned; what matters is where they
// end up.
var credentialShaped = []string{
	"my name is AKIAIOSFODNN7EXAMPLE",
	"this is ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
	"call me back Authorization Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
	"transfer me to password hunter2",
	"tell him the api_key is sk-live-0123456789abcdefghijklmnop",
	"this is token 5f4dcc3b5aa765d61d8327deb882cf99",
	"my name is secret and the client_secret is abc123def456",
	"calling from AWS_SECRET_ACCESS_KEY wJalrXUtnFEMI K7MDENG bPxRfiCY",
}

func securityClassifier(t *testing.T) *Classifier {
	t.Helper()
	c, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// ---------------------------------------------------------------------------
// 1. Credential-shaped input must not enter classifier state
// ---------------------------------------------------------------------------

// TestSecurity_ClassifierStateIsUnchangedByCredentialShapedInput is the state
// boundary test, and it inspects the actual struct rather than trusting that
// nothing was assigned.
//
// A deep copy of the Classifier is taken before classification and compared
// afterwards with reflect.DeepEqual. If any field — including an unexported one
// added later — came to hold caller-derived data, the comparison fails.
func TestSecurity_ClassifierStateIsUnchangedByCredentialShapedInput(t *testing.T) {
	t.Parallel()

	c := securityClassifier(t)
	before := snapshotClassifier(c)

	for _, text := range credentialShaped {
		for _, e := range []conversation.Expectation{
			conversation.ExpectNothing, conversation.ExpectDisambiguation,
			conversation.ExpectYesNo, conversation.ExpectSlotValue,
		} {
			if _, _, err := c.Classify(conversation.Utterance{
				Text: text, ASRConfidence: 0.9,
			}, e); err != nil {
				t.Fatalf("Classify: %v", err)
			}
		}
	}

	after := snapshotClassifier(c)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("classifier state changed after classifying credential-shaped input\n"+
			"  before: %+v\n  after:  %+v", before, after)
	}
}

// snapshotClassifier deep-copies every field, exported or not, so a later field
// carrying caller text is detected rather than skipped.
func snapshotClassifier(c *Classifier) map[string]string {
	out := map[string]string{}
	v := reflect.ValueOf(*c)
	tp := v.Type()
	for i := 0; i < tp.NumField(); i++ {
		// %#v renders the value structurally, so a stored string appears.
		out[tp.Field(i).Name] = renderValue(v.Field(i))
	}
	return out
}

func renderValue(v reflect.Value) string {
	var b strings.Builder
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			b.WriteString(v.String())
			b.WriteByte('\x1f')
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i))
			}
		case reflect.Map:
			// Sorted so an unordered map does not make the snapshot unstable.
			keys := v.MapKeys()
			strs := make([]string, 0, len(keys))
			for _, k := range keys {
				strs = append(strs, k.String())
			}
			for i := 0; i < len(strs); i++ {
				for j := i + 1; j < len(strs); j++ {
					if strs[j] < strs[i] {
						strs[i], strs[j] = strs[j], strs[i]
					}
				}
			}
			for _, s := range strs {
				b.WriteString(s)
				b.WriteByte('\x1f')
			}
		case reflect.Ptr, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		default:
			b.WriteString(v.String())
		}
	}
	walk(v)
	return b.String()
}

// TestSecurity_ClassificationIsSideEffectFreeAcrossCalls — a secret in call N
// must not alter the outcome of call N+1. This is the observable consequence of
// holding no state, and it catches a cache the struct snapshot might miss (for
// example one stored behind a pointer that is mutated in place).
func TestSecurity_ClassificationIsSideEffectFreeAcrossCalls(t *testing.T) {
	t.Parallel()

	c := securityClassifier(t)
	probe := conversation.Utterance{Text: "please call me back on 9876543210", ASRConfidence: 0.9}

	baseCands, baseSlots, err := c.Classify(probe, conversation.ExpectNothing)
	if err != nil {
		t.Fatal(err)
	}
	baseline := renderResult(baseCands, baseSlots)

	for _, text := range credentialShaped {
		if _, _, err := c.Classify(conversation.Utterance{
			Text: text, ASRConfidence: 0.9,
		}, conversation.ExpectNothing); err != nil {
			t.Fatal(err)
		}
		cands, slots, err := c.Classify(probe, conversation.ExpectNothing)
		if err != nil {
			t.Fatal(err)
		}
		if got := renderResult(cands, slots); got != baseline {
			t.Fatalf("classifying %q changed a later unrelated result\n"+
				"  baseline: %s\n  after:    %s", text, baseline, got)
		}
	}
}

func renderResult(cs []conversation.Candidate, ss []conversation.Slot) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(string(c.Name))
		b.WriteByte('=')
		b.WriteString(strings.TrimSpace(floatStr(c.Confidence)))
		b.WriteByte('|')
	}
	b.WriteByte('#')
	for _, s := range ss {
		b.WriteString(s.Name)
		b.WriteByte('=')
		b.WriteString(floatStr(s.Confidence))
		b.WriteByte('|')
	}
	return b.String()
}

func floatStr(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// ---------------------------------------------------------------------------
// 2 + 3. The type boundary: what can cross at all
// ---------------------------------------------------------------------------

// TestSecurity_EveryStringCrossingTheBoundaryIsFromAClosedSet is the strongest
// containment statement available, and it is made by REFLECTION over the result
// rather than by checking the fields a corpus happened to populate.
//
// It walks every field of every returned Candidate and Slot, collects every
// string reachable, and requires each to be a member of a closed vocabulary. A
// new string field carrying caller text would be walked too, and would fail.
func TestSecurity_EveryStringCrossingTheBoundaryIsFromAClosedSet(t *testing.T) {
	t.Parallel()

	// Hand-declared, not read from Vocabulary()/SlotVocabulary().
	allowed := map[string]bool{
		"affirm": true, "deny": true, "greeting": true, "caller_identity": true,
		"call_purpose": true, "leave_message": true, "request_callback": true,
		"request_transfer": true, "repeat": true, "hold": true, "end_call": true,
		"caller_name": true, "company_name": true, "party_name": true,
		"callback_number": true, "time_reference": true, "message_body": true,
	}

	corpus := append([]string{}, credentialShaped...)
	corpus = append(corpus,
		"my name is zzsentinelzz and my number is 9876543210",
		"transfer me to $(curl evil.test) tomorrow",
		"tell him ../../etc/passwd and call me back on 1234567",
		strings.Repeat("call me back on 9876543210 ", 500),
	)

	c := securityClassifier(t)
	for _, text := range corpus {
		cands, slots, err := c.Classify(conversation.Utterance{
			Text: text, ASRConfidence: 0.9,
		}, conversation.ExpectNothing)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range reachableStrings(reflect.ValueOf(cands)) {
			if !allowed[s] {
				t.Errorf("Classify(%q): candidate carries uncontrolled string %q", text, s)
			}
		}
		for _, s := range reachableStrings(reflect.ValueOf(slots)) {
			if !allowed[s] {
				t.Errorf("Classify(%q): slot carries uncontrolled string %q", text, s)
			}
		}
	}
}

// reachableStrings collects every string reachable from v, at any depth.
func reachableStrings(v reflect.Value) []string {
	var out []string
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			if s := v.String(); s != "" {
				out = append(out, s)
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i))
			}
		case reflect.Ptr, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				walk(k)
				walk(v.MapIndex(k))
			}
		}
	}
	walk(v)
	return out
}

// TestSecurity_NoByteOrAudioShapedTypeCrossesTheAPI proves raw PCM cannot enter
// STRUCTURALLY rather than by attempting a conversion that does not exist.
//
// The port takes conversation.Utterance, whose only caller-derived field is a
// string. There is no []byte anywhere in the package's exported signatures, so
// there is no parameter through which audio could arrive. Asserted by AST over
// every exported declaration, so adding such a parameter later fails here.
func TestSecurity_NoByteOrAudioShapedTypeCrossesTheAPI(t *testing.T) {
	t.Parallel()

	files := parsePackage(t, 0)

	var checked int
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				return true
			}
			checked++
			var sigs []*ast.FieldList
			sigs = append(sigs, fn.Type.Params)
			if fn.Type.Results != nil {
				sigs = append(sigs, fn.Type.Results)
			}
			for _, fl := range sigs {
				if fl == nil {
					continue
				}
				for _, fld := range fl.List {
					if typeMentionsBytes(fld.Type) {
						t.Errorf("%s: exported %s has a byte-shaped type in its signature; "+
							"raw audio must have no route into this package",
							name, fn.Name.Name)
					}
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("inspected no exported functions; the guard would pass vacuously")
	}
	t.Logf("%d exported functions inspected for byte-shaped signatures", checked)

	// And the struct fields, since a []byte could arrive inside Config.
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || !ts.Name.IsExported() {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fld := range st.Fields.List {
				if typeMentionsBytes(fld.Type) {
					t.Errorf("%s: exported type %s has a byte-shaped field",
						name, ts.Name.Name)
				}
			}
			return true
		})
	}
}

// typeMentionsBytes reports whether an AST type contains []byte or a
// byte/uint8 element at any depth.
func typeMentionsBytes(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if id.Name == "byte" || id.Name == "uint8" {
				found = true
			}
		}
		return true
	})
	return found
}

// ---------------------------------------------------------------------------
// 4. Metric-label safety — proven as a structural absence
// ---------------------------------------------------------------------------

// TestSecurity_PackageHasNoMetricInstrumentPath.
//
// There is nothing to test behaviourally: this package declares no instrument
// and never calls one, so no label can be emitted from it. Inventing a metric
// purely to prove it is safe would create the very surface the property is
// about. Instead the ABSENCE is asserted, so the day an instrument is added
// this test fails and the label question is raised deliberately.
//
// The frozen conversation module owns the conversation-layer instruments and
// already labels with intent NAMES only (IntentsProposed.Inc(string(top.Name))
// at intent.go:367) — names which this package guarantees come from a closed
// vocabulary. That is the actual protection for the metric path, and it is
// upheld by the vocabulary guards rather than by anything here.
func TestSecurity_PackageHasNoMetricInstrumentPath(t *testing.T) {
	t.Parallel()

	files := parsePackage(t, parser.ImportsOnly)
	for name, f := range files {
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.HasSuffix(path, "packages/go/metrics") {
				t.Errorf("%s imports the metrics package; T5 asserted this module "+
					"emits no instrument, and adding one requires deciding the "+
					"label vocabulary first", name)
			}
		}
	}

	// And no identifier shaped like an instrument call.
	full := parsePackage(t, 0)
	for name, f := range full {
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Inc", "Observe", "Add", "Set", "Counter", "Histogram", "Gauge":
				// Add/Set are common words; report with the receiver so a
				// false positive is visibly distinguishable.
				if id, ok := sel.X.(*ast.Ident); ok {
					t.Errorf("%s: %s.%s looks like an instrument call in a package "+
						"that must emit none", name, id.Name, sel.Sel.Name)
				}
			}
			return true
		})
	}
}

// ---------------------------------------------------------------------------
// 5 + 6. Execution, governance and persistence isolation
// ---------------------------------------------------------------------------

// TestSecurity_NoPersistenceOrExecutionShapedAPI.
//
// The import guard (T4) stops the obvious route. This stops the subtle one: a
// helper named Save/Store/Execute that later acquires a body. A classifier that
// can persist is a second memory system; one that can execute has escaped the
// governance boundary entirely.
func TestSecurity_NoPersistenceOrExecutionShapedAPI(t *testing.T) {
	t.Parallel()

	forbiddenPrefixes := []string{
		"Save", "Store", "Persist", "Write", "Flush", "Commit", "Insert",
		"Upsert", "Delete", "Load", "Fetch", "Query",
		"Execute", "Exec", "Run", "Invoke", "Dispatch", "Call", "Send",
		"Post", "Get", "Do", "Connect", "Dial", "Open",
	}

	files := parsePackage(t, 0)
	var checked int
	for name, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			checked++
			for _, p := range forbiddenPrefixes {
				if strings.HasPrefix(fn.Name.Name, p) {
					t.Errorf("%s: exported %s has a persistence/execution-shaped name; "+
						"this package must neither store nor execute anything",
						name, fn.Name.Name)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("inspected no exported functions; the guard would pass vacuously")
	}
	t.Logf("%d exported functions inspected for persistence/execution shapes", checked)
}

// TestSecurity_NoExportedFunctionTypedFieldCouldBeAnExecutionHook.
//
// A func-typed exported field is a place a caller could install arbitrary
// behaviour that then runs inside classification — an execution path that no
// import guard would see. Rule.Cues and the spec table are data, deliberately,
// and the one internal func field (slotSpec.Structural) is unexported and
// populated only from this package's own table.
func TestSecurity_NoExportedFunctionTypedFieldCouldBeAnExecutionHook(t *testing.T) {
	t.Parallel()

	files := parsePackage(t, 0)
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || !ts.Name.IsExported() {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fld := range st.Fields.List {
				if _, isFunc := fld.Type.(*ast.FuncType); !isFunc {
					continue
				}
				for _, id := range fld.Names {
					if id.IsExported() {
						t.Errorf("%s: exported type %s has exported func-typed field %s, "+
							"which is an execution hook into classification",
							name, ts.Name.Name, id.Name)
					}
				}
			}
			return true
		})
	}
}

// TestSecurity_GovernanceDecisionsCannotBeConstructedHere.
//
// The classifier must not be able to make, alter or override a governance
// decision. It cannot import governance (T4's guard), and this adds the
// complementary statement: no identifier in this package refers to a governance
// or tool concept at all, so there is no partially-built path to complete.
func TestSecurity_GovernanceDecisionsCannotBeConstructedHere(t *testing.T) {
	t.Parallel()

	banned := []string{
		"governance", "Decision", "Authorization", "Permit", "Deny",
		"toolruntime", "ToolIntent", "Capability", "Grant", "Invocation",
		"memory", "Repository", "Persist",
	}

	files := parsePackage(t, 0)
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			// Identifiers only — comments are not in the AST, so this cannot be
			// tripped by prose the way a grep would be.
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, b := range banned {
				if id.Name == b {
					t.Errorf("%s: identifier %q refers to a governance/tool/memory "+
						"concept in a package that must know of none", name, id.Name)
				}
			}
			return true
		})
	}
}

// ---------------------------------------------------------------------------
// 7. Security-specific vocabulary and confidence assertions
// ---------------------------------------------------------------------------

// TestSecurity_CallerTextCanNeverBecomeAnIdentifier is the single sentence this
// file exists to prove, stated as a test: no matter what the caller says, the
// identifiers leaving this package come from closed sets and the numbers are
// bounded.
func TestSecurity_CallerTextCanNeverBecomeAnIdentifier(t *testing.T) {
	t.Parallel()

	c := securityClassifier(t)
	hostile := append([]string{}, credentialShaped...)
	hostile = append(hostile,
		"greeting_evil", "affirm\x00injected", "request_callback; DROP TABLE x",
		"caller_name=../../etc/passwd", strings.Repeat("A", 70000),
		"‮gnitirw sdrawkcab", strings.Repeat("🙂", 500),
	)

	for _, text := range hostile {
		cands, slots, err := c.Classify(conversation.Utterance{
			Text: text, ASRConfidence: 0.9,
		}, conversation.ExpectNothing)
		if err != nil {
			t.Fatalf("Classify(%q): %v", text, err)
		}
		for _, cd := range cands {
			if !inVocabulary(cd.Name) {
				t.Errorf("intent name %q escaped the closed vocabulary", cd.Name)
			}
			if cd.Confidence < 0 || cd.Confidence > 1 {
				t.Errorf("confidence %v outside [0,1]", cd.Confidence)
			}
		}
		for _, s := range slots {
			if !inSlotVocabulary(s.Name) {
				t.Errorf("slot name %q escaped the closed vocabulary", s.Name)
			}
			if s.Confidence < 0 || s.Confidence > 1 {
				t.Errorf("slot confidence %v outside [0,1]", s.Confidence)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Shared AST helper
// ---------------------------------------------------------------------------

// parsePackage parses the package's own source, excluding tests.
//
// AST rather than text search: a grep for "governance" matches this file's own
// explanatory prose, which is precisely the false positive Phase 11E hit and
// then fixed by parsing.
func parsePackage(t *testing.T, mode parser.Mode) map[string]*ast.File {
	t.Helper()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, mode)
	if err != nil {
		t.Fatalf("parsing package: %v", err)
	}

	out := map[string]*ast.File{}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			out[name] = f
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no non-test files; every AST guard would pass vacuously")
	}
	return out
}
