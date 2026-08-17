package intent

import (
	"sort"
	"strings"

	"github.com/callscreen/callscreen-platform/packages/go/conversation"
)

// ---------------------------------------------------------------------------
// The closed vocabulary
// ---------------------------------------------------------------------------

// The intent vocabulary this classifier can emit.
//
// CLOSED, AND CLOSING IT IS THIS PACKAGE'S JOB. The conversation.IntentName
// type is an open `string` (intent.go:14) with only four reserved constants, so the
// frozen port does not and cannot bound the vocabulary. If a classifier
// invents a name, the conversation engine will faithfully carry it into a
// plan, a metric label and an audit record. Every name below is declared here,
// and [Vocabulary] is the single authoritative list.
//
// Two names are NOT redeclared: affirm and deny already exist in the frozen
// module as [conversation.IntentAffirm] and [conversation.IntentDeny], and
// minting second spellings of them is exactly the duplication this phase
// avoids elsewhere.
const (
	// IntentGreeting is an opening pleasantry with no request attached.
	IntentGreeting conversation.IntentName = "greeting"

	// IntentCallerIdentity is the caller saying who they are, or asking who
	// they have reached.
	IntentCallerIdentity conversation.IntentName = "caller_identity"

	// IntentCallPurpose is the caller stating why they are calling.
	IntentCallPurpose conversation.IntentName = "call_purpose"

	// IntentLeaveMessage is a request to leave a message.
	IntentLeaveMessage conversation.IntentName = "leave_message"

	// IntentRequestCallback is a request to be called back, or an offer to
	// call back later.
	IntentRequestCallback conversation.IntentName = "request_callback"

	// IntentRequestTransfer is a request to reach the person being screened.
	IntentRequestTransfer conversation.IntentName = "request_transfer"

	// IntentRepeat is a request to repeat what was just said.
	IntentRepeat conversation.IntentName = "repeat"

	// IntentHold is a request to wait.
	IntentHold conversation.IntentName = "hold"

	// IntentEndCall is a request to end the call.
	IntentEndCall conversation.IntentName = "end_call"
)

// Vocabulary is every name this classifier may emit, in a stable order.
//
// Used by the classifier's own guard and by tests. A name not in this list
// must never appear in a [conversation.Candidate] this package returns.
func Vocabulary() []conversation.IntentName {
	return []conversation.IntentName{
		conversation.IntentAffirm,
		conversation.IntentDeny,
		IntentCallPurpose,
		IntentCallerIdentity,
		IntentEndCall,
		IntentGreeting,
		IntentHold,
		IntentLeaveMessage,
		IntentRepeat,
		IntentRequestCallback,
		IntentRequestTransfer,
	}
}

// inVocabulary reports whether a name is emittable.
func inVocabulary(n conversation.IntentName) bool {
	for _, v := range Vocabulary() {
		if v == n {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

// Rule is one intent's lexical evidence.
//
// Cues are matched against the tokenised utterance. A cue of one token matches
// that token; a cue of several tokens matches only as a contiguous run, which
// is what makes "call back" evidence for a callback request while "call" alone
// is barely evidence of anything.
type Rule struct {
	// Name is the intent this rule proposes. Must be in [Vocabulary].
	Name conversation.IntentName

	// Cues are the phrases that count as evidence. Each is pre-tokenised.
	Cues [][]string

	// Saturation is the evidence weight at which confidence reaches 1.0.
	// Below it, confidence is the ratio. Zero means [defaultSaturation].
	Saturation float64
}

// defaultSaturation is the evidence weight at which an intent is fully
// confident — 3.0, chosen so that ONE strong multi-token phrase saturates a
// rule while a single incidental keyword does not. With the weighting in
// [cueWeight], a two-token phrase scores 3.0 and a lone token scores 1.0 — so
// "call me back" is decisive and a stray "back" is 0.33, which lands below the
// frozen RejectThreshold of 0.45 and is therefore discarded by the engine
// rather than by a second policy here.
const defaultSaturation = 3.0

// cueWeight is the evidence a matched cue contributes.
//
// A single token is weak evidence and scores 1. A multi-token phrase is much
// stronger — word order carries meaning that bag-of-words does not — and
// scores len+1, so two tokens give 3 and three give 4. The +1 is what makes a
// two-token phrase decisive at the default saturation rather than merely twice
// a single word.
func cueWeight(cue []string) float64 {
	if len(cue) <= 1 {
		return 1
	}
	return float64(len(cue) + 1)
}

// DefaultRules returns the built-in lexicon, in a stable order.
//
// SCOPE, STATED HONESTLY. This is a deterministic keyword lexicon for a
// call-screening receptionist, not natural-language understanding. It
// recognises what it is told to recognise and nothing else, and its ceiling is
// the phrase list below. A caller who phrases a request in words absent from
// this list gets no candidates, which the engine turns into the fallback
// intent — the correct outcome, and the reason this list is a starting point
// rather than a claim.
//
// English plus common Hindi/Hinglish forms, matching the frozen engine's own
// yes/no lexicon (intent.go:410) and the India-first posture of ADR-0003 and
// ADR-0012.
func DefaultRules() []Rule {
	return []Rule{
		{Name: conversation.IntentAffirm, Cues: cues(
			"yes", "yeah", "yep", "yup", "sure", "correct", "right", "ok",
			"okay", "confirm", "haan", "haa", "ji", "jee",
		)},
		{Name: conversation.IntentDeny, Cues: cues(
			"no", "nope", "nah", "wrong", "incorrect", "nahi", "nahin", "na",
		)},
		{Name: IntentGreeting, Cues: cues(
			"hello", "hi", "hey", "namaste", "good morning", "good afternoon",
			"good evening",
		)},
		{Name: IntentCallerIdentity, Cues: cues(
			"this is", "my name is", "speaking", "calling from", "i am",
			"who is this", "who am i speaking to", "main",
		)},
		{Name: IntentCallPurpose, Cues: cues(
			"calling about", "calling regarding", "reason for", "regarding",
			"about the", "with respect to", "kaam",
		)},
		{Name: IntentLeaveMessage, Cues: cues(
			"leave a message", "take a message", "pass on", "tell him",
			"tell her", "let them know", "message",
		)},
		{Name: IntentRequestCallback, Cues: cues(
			"call me back", "call back", "callback", "ring me", "return my call",
			"reach me", "wapas call",
		)},
		{Name: IntentRequestTransfer, Cues: cues(
			"put me through", "transfer me", "speak to", "talk to", "connect me",
			"is he available", "is she available", "baat karni",
		)},
		{Name: IntentRepeat, Cues: cues(
			"say that again", "repeat that", "repeat", "pardon", "come again",
			"phir se",
		)},
		{Name: IntentHold, Cues: cues(
			"hold on", "hang on", "one moment", "just a second", "wait",
			"ruko", "ek minute",
		)},
		{Name: IntentEndCall, Cues: cues(
			"goodbye", "good bye", "bye", "hang up", "end the call",
			"that is all", "nothing else", "alvida",
		)},
	}
}

// cues tokenises each phrase once, at construction.
func cues(phrases ...string) [][]string {
	out := make([][]string, 0, len(phrases))
	for _, p := range phrases {
		if t := tokenize(p); len(t) > 0 {
			out = append(out, t)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Tokenisation
// ---------------------------------------------------------------------------

// maxTokens bounds how much of an utterance is examined.
//
// An utterance arrives from a recogniser and is therefore caller-controlled in
// length. Scoring is linear in tokens, so an unbounded utterance is unbounded
// work on the turn's critical path. 512 tokens is far beyond any real spoken
// turn; anything past it is discarded rather than truncating mid-analysis.
const maxTokens = 512

// tokenize lowercases ASCII and splits on anything that is not a letter or
// digit — written here rather than reused because the frozen module's
// equivalent is unexported. Punctuation is stripped so "hello." matches
// "hello"; digits are kept because a callback number or a time is a token a
// cue may reference.
//
// The returned tokens are derived from SENSITIVE text and must not escape this
// package — they are scored and discarded within one call.
func tokenize(s string) []string {
	var (
		out []string
		cur strings.Builder
	)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			cur.WriteRune(r + ('a' - 'A'))
		default:
			flush()
			if len(out) >= maxTokens {
				return out
			}
		}
	}
	flush()
	if len(out) > maxTokens {
		return out[:maxTokens]
	}
	return out
}

// matchAt reports whether cue appears at tokens[i].
func matchAt(tokens []string, i int, cue []string) bool {
	if i+len(cue) > len(tokens) {
		return false
	}
	for k, c := range cue {
		if tokens[i+k] != c {
			return false
		}
	}
	return true
}

// sortCandidates orders candidates highest-confidence first.
//
// The tie-break is lexicographic by name, ASCENDING — deliberately identical
// to the frozen engine's own rule at intent.go:348-356. The engine re-sorts
// what a classifier returns, so a different rule here would mean the order
// this package promises and the order the engine acts on could differ.
// Matching it makes the two agree by construction.
func sortCandidates(cs []conversation.Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Confidence != cs[j].Confidence {
			return cs[i].Confidence > cs[j].Confidence
		}
		return cs[i].Name < cs[j].Name
	})
}
