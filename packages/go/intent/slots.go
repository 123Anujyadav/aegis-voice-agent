package intent

import (
	"sort"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
)

// ---------------------------------------------------------------------------
// What this file can and cannot do
// ---------------------------------------------------------------------------

// SLOT VALUES CANNOT BE TRANSPORTED THROUGH THIS PORT, AND THIS FILE DOES NOT
// PRETEND OTHERWISE.
//
// [conversation.Slot] carries Name, Filled, Confidence and Required — and no
// value. The frozen type says why (intent.go:32): "The value itself is NOT held
// here: slot values are caller-derived and therefore SENSITIVE, and they live in
// the context engine under its scoping and expiry rules."
//
// A classifier has no route to that context engine either. The port is
//
//	Classify(u Utterance, expect Expectation) ([]Candidate, []Slot, error)
//
// which receives no context handle, and [conversation.IntentEngine] — the only
// caller — holds cfg, classifier, clock and metrics, with no ContextEngine to
// pass (intent.go:263). Writing values is done by the conversation layer, which
// holds both, or by composition code holding Conversation.Context().
//
// So this file extracts slot SHAPE: which slots an intent needs, which appear
// to be present, how confident that is, and which are required. That is exactly
// what the frozen engine consumes — Intent.Slots is read by exactly one thing,
// MissingRequired() at intent.go:74, which feeds Complete() and therefore the
// IntentClarify decision.
//
// Values are derived internally to decide Filled, bounded while that happens,
// and discarded before return. No parallel value transport is invented here.

// ---------------------------------------------------------------------------
// The closed slot vocabulary
// ---------------------------------------------------------------------------

// Slot names this package may emit. Closed, for the same reason the intent
// vocabulary is: a name built from caller input would flow into
// Intent.Slots and onward into audit records.
const (
	SlotCallerName     = "caller_name"
	SlotCompanyName    = "company_name"
	SlotPartyName      = "party_name"
	SlotCallbackNumber = "callback_number"
	SlotTimeReference  = "time_reference"
	SlotMessageBody    = "message_body"
)

// SlotVocabulary is every slot name this package may emit, in a stable order.
func SlotVocabulary() []string {
	return []string{
		SlotCallbackNumber,
		SlotCallerName,
		SlotCompanyName,
		SlotMessageBody,
		SlotPartyName,
		SlotTimeReference,
	}
}

func inSlotVocabulary(n string) bool {
	for _, v := range SlotVocabulary() {
		if v == n {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------------

const (
	// MaxSlotsPerIntent bounds how many slots one classification returns.
	//
	// 4, because no intent in the vocabulary declares more than three and the
	// slice is copied onto Intent.Slots, which reaches an audit record. A cap
	// just above the real maximum turns a future over-specified intent into a
	// visible truncation rather than an unbounded record.
	MaxSlotsPerIntent = 4

	// MaxSlotNameLen bounds an emitted slot name.
	//
	// 32 is comfortably longer than every name in SlotVocabulary. The
	// vocabulary is closed, so this can only fire if a future edit adds an
	// over-long name — which is precisely when a bound is useful.
	MaxSlotNameLen = 32

	// maxValueTokens bounds how many tokens after an anchor are examined as a
	// candidate value.
	//
	// 6 tokens is longer than any real name, company or time phrase and short
	// enough that a caller cannot make anchor-scanning expensive by speaking a
	// long sentence after "my name is".
	maxValueTokens = 6

	// maxDigitRun bounds a digit sequence considered as a phone number.
	//
	// 15 is the E.164 maximum. Longer runs are not phone numbers, and treating
	// them as such would let an arbitrary digit string set a slot.
	maxDigitRun = 15

	// minDigitRun is the shortest run treated as a callback number. Below 7 a
	// digit run is far more likely a quantity, a date or a house number.
	minDigitRun = 7
)

// Slot detection confidences.
//
// Two levels, both documented, neither derived from a model:
//
//   - Structural: the token itself proves the class. A 7-to-15 digit run is a
//     phone number by shape, so there is nothing left to be uncertain about.
//   - Anchored: an anchor phrase was matched and a plausible value follows.
//     The anchor is certain; where the value ENDS is a heuristic, so this is
//     deliberately below 1.
const (
	confidenceStructural = 1.0
	confidenceAnchored   = 0.8
)

// ---------------------------------------------------------------------------
// Per-intent slot specifications
// ---------------------------------------------------------------------------

// slotSpec declares one slot an intent needs.
type slotSpec struct {
	Name     string
	Required bool
	// Anchors precede a value: "my name is" <value>. Empty means the slot is
	// detected structurally rather than by an anchor.
	Anchors [][]string
	// Structural, when set, detects the slot from the token run directly.
	Structural func(tokens []string) bool
}

// slotSpecsFor returns the slots an intent declares, in a stable order.
//
// Only the TOP candidate's slots are evaluated. Intent.Slots belongs to the
// resolved intent, and filling it with slots from runner-up intents would make
// Complete() ask about parameters of something the caller did not request.
func slotSpecsFor(name conversation.IntentName) []slotSpec {
	switch name {
	case IntentRequestCallback:
		return []slotSpec{
			{Name: SlotCallbackNumber, Required: true, Structural: hasPhoneLikeRun},
			{Name: SlotTimeReference, Required: false, Structural: hasTimeReference},
		}
	case IntentRequestTransfer:
		return []slotSpec{
			{Name: SlotPartyName, Required: true, Anchors: cues(
				"speak to", "talk to", "connect me to", "put me through to",
				"transfer me to", "baat karni",
			)},
		}
	case IntentCallerIdentity:
		return []slotSpec{
			{Name: SlotCallerName, Required: true, Anchors: cues(
				"this is", "my name is", "i am", "main",
			)},
			{Name: SlotCompanyName, Required: false, Anchors: cues(
				"calling from", "from",
			)},
		}
	case IntentLeaveMessage:
		return []slotSpec{
			{Name: SlotMessageBody, Required: true, Anchors: cues(
				"tell him", "tell her", "let them know", "message is",
				"the message is", "pass on",
			)},
		}
	case IntentCallPurpose:
		return []slotSpec{
			{Name: SlotTimeReference, Required: false, Structural: hasTimeReference},
		}
	default:
		// greeting, affirm, deny, repeat, hold, end_call take no parameters.
		return nil
	}
}

// ---------------------------------------------------------------------------
// Extraction
// ---------------------------------------------------------------------------

// extractSlots returns the slot shape for one intent.
//
// Takes the specs rather than looking them up so the bounds below are
// reachable from a test. With the built-in table no intent declares more than
// two slots and no name approaches MaxSlotNameLen, so both guards would
// otherwise be unverifiable defence-in-depth — mutation testing found exactly
// that and this parameter is the fix.
//
// Deterministic: specs are a slice, evaluation is in declared order, and the
// result is sorted by name before return. Values are examined to decide Filled
// and are never retained — nothing derived from caller text outlives this call.
func extractSlots(tokens []string, specs []slotSpec) []conversation.Slot {
	if len(specs) == 0 {
		return nil
	}

	out := make([]conversation.Slot, 0, len(specs))
	for _, sp := range specs {
		// A name outside the closed vocabulary, or an over-long one, is a
		// defect in this package rather than in the input — drop it rather
		// than emit it, and let the vocabulary test catch it.
		if !inSlotVocabulary(sp.Name) || len(sp.Name) > MaxSlotNameLen {
			continue
		}

		filled, conf := false, 0.0
		switch {
		case sp.Structural != nil:
			if sp.Structural(tokens) {
				filled, conf = true, confidenceStructural
			}
		case len(sp.Anchors) > 0:
			if anchoredValuePresent(tokens, sp.Anchors) {
				filled, conf = true, confidenceAnchored
			}
		}

		out = append(out, conversation.Slot{
			Name:       sp.Name,
			Filled:     filled,
			Confidence: conf,
			Required:   sp.Required,
		})
	}

	// Stable, name-ordered output. MissingRequired() sorts its own result, but
	// Intent.Slots itself reaches an audit record, and a record whose field
	// order varies between identical runs is a record that cannot be diffed.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	if len(out) > MaxSlotsPerIntent {
		out = out[:MaxSlotsPerIntent]
	}
	return out
}

// anchoredValuePresent reports whether an anchor is followed by a plausible
// value token.
//
// "my name is" alone does not fill the slot — the caller was cut off, or the
// recogniser dropped the name. Requiring at least one following token is what
// makes Filled mean "a value is present" rather than "the subject was raised".
func anchoredValuePresent(tokens []string, anchors [][]string) bool {
	for _, a := range anchors {
		for i := range tokens {
			if !matchAt(tokens, i, a) {
				continue
			}
			start := i + len(a)
			if start >= len(tokens) {
				continue // anchor with nothing after it
			}
			// Bounded look-ahead: a value is short. Scanning further would let
			// a long utterance cost more per anchor for no gain.
			end := start + maxValueTokens
			if end > len(tokens) {
				end = len(tokens)
			}
			for k := start; k < end; k++ {
				if len(tokens[k]) > 0 {
					return true
				}
			}
		}
	}
	return false
}

// hasPhoneLikeRun reports whether any token is a digit run of plausible phone
// length.
//
// Structural: a 7-to-15 digit token is a phone number by shape. Shorter runs
// are quantities or dates; longer than E.164's 15 is not a number anyone dialled.
func hasPhoneLikeRun(tokens []string) bool {
	for _, t := range tokens {
		if len(t) < minDigitRun || len(t) > maxDigitRun {
			continue
		}
		allDigits := true
		for _, r := range t {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return true
		}
	}
	return false
}

// timeWords is the closed set of tokens treated as a time reference.
//
// Deliberately small and literal. A general date parser would be a second
// system to keep correct, and the slot only needs to know whether a time was
// mentioned — the value itself lives in the context engine, not here.
var timeWords = map[string]struct{}{
	"today": {}, "tomorrow": {}, "tonight": {}, "morning": {}, "afternoon": {},
	"evening": {}, "noon": {}, "midnight": {}, "now": {}, "later": {},
	"monday": {}, "tuesday": {}, "wednesday": {}, "thursday": {},
	"friday": {}, "saturday": {}, "sunday": {},
	"am": {}, "pm": {}, "kal": {}, "aaj": {}, "subah": {}, "shaam": {},
}

// hasTimeReference reports whether the utterance mentions a time.
func hasTimeReference(tokens []string) bool {
	for _, t := range tokens {
		if _, ok := timeWords[t]; ok {
			return true
		}
	}
	return false
}
