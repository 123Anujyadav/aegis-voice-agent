// Package intent implements [conversation.IntentClassifier].
//
// conversation owns the intent engine, the confidence thresholds, the
// clarification policy and the bounded context. It deliberately owns no
// classifier — see intent.go:101 in that module. This package is that
// classifier, plugged into the existing port via conversation.WithClassifier.
// It introduces no engine, no second intent model and no second context store.
//
// The implementation is deterministic and offline: no model, no network, no
// credential. Given the same utterance, expectation and configuration it
// returns the same result every time, which is what lets the conversation
// layer be tested without a model present.
//
// WHAT THIS PACKAGE DOES NOT DO. It scores; it does not decide. Every
// threshold — accept, reject, ambiguity margin, minimum ASR confidence —
// belongs to [conversation.IntentConfig] and is applied by
// [conversation.IntentEngine] after this package returns. Re-applying any of
// them here would be a second confidence policy, and when the two disagreed
// nothing would say which was authoritative — see
// docs/adr/0016-intent-classification.md.
package intent

import (
	"github.com/callscreen/callscreen-platform/packages/go/conversation"
)

// Config configures a [Classifier].
//
// Everything that affects the result is here, so "same input, same config,
// same output" is a property of a value rather than of the process.
type Config struct {
	// Rules is the lexicon. Empty means [DefaultRules].
	//
	// Every rule's Name must be in [Vocabulary]; a rule naming anything else is
	// rejected at construction rather than silently emitting an out-of-
	// vocabulary intent at runtime.
	Rules []Rule

	// MaxCandidates bounds the returned slice. Zero means
	// [DefaultMaxCandidates].
	//
	// A bound matters because the engine copies the whole slice into
	// Intent.Alternatives, which reaches an audit record.
	MaxCandidates int
}

// DefaultMaxCandidates bounds how many alternatives are returned.
//
// Five is comfortably more than the two the ambiguity margin needs, and far
// fewer than the vocabulary, so a garbled utterance that weakly matches
// everything cannot produce an alternatives list as long as the lexicon.
const DefaultMaxCandidates = 5

// DefaultConfig returns the built-in configuration.
func DefaultConfig() Config {
	return Config{Rules: DefaultRules(), MaxCandidates: DefaultMaxCandidates}
}

// Classifier is a deterministic lexical [conversation.IntentClassifier].
//
// Immutable after construction and safe for concurrent use: Classify reads the
// rules and writes nothing. There is no cache, no history and no package-level
// mutable state, so one session's utterance cannot influence another's result.
type Classifier struct {
	rules []Rule
	maxN  int
}

// Compile-time proof that this satisfies the FROZEN port.
//
// If conversation ever changes the interface, this module stops compiling here
// rather than at some distant call site — and because conversation is frozen,
// that would be a contract change requiring its own decision.
var _ conversation.IntentClassifier = (*Classifier)(nil)

// New builds a classifier from cfg.
//
// Returns an error rather than silently correcting a bad lexicon: a rule
// naming an intent outside [Vocabulary] is precisely the defect the closed
// vocabulary exists to prevent, and discovering it at startup beats
// discovering it in an audit record.
func New(cfg Config) (*Classifier, error) {
	rules := cfg.Rules
	if len(rules) == 0 {
		rules = DefaultRules()
	}
	for _, r := range rules {
		if !inVocabulary(r.Name) {
			return nil, &ConfigError{Problems: []string{
				"intent: rule names " + string(r.Name) +
					", which is not in the closed vocabulary",
			}}
		}
		if len(r.Cues) == 0 {
			return nil, &ConfigError{Problems: []string{
				"intent: rule " + string(r.Name) + " has no cues",
			}}
		}
		if r.Saturation < 0 {
			return nil, &ConfigError{Problems: []string{
				"intent: rule " + string(r.Name) + " has a negative saturation",
			}}
		}
	}

	maxN := cfg.MaxCandidates
	if maxN <= 0 {
		maxN = DefaultMaxCandidates
	}

	// Copy so a caller mutating its slice afterwards cannot change how an
	// already-built classifier behaves.
	own := make([]Rule, len(rules))
	copy(own, rules)
	return &Classifier{rules: own, maxN: maxN}, nil
}

// MustNew is New, panicking on a bad configuration. For tests and for wiring
// that has no error path.
func MustNew(cfg Config) *Classifier {
	c, err := New(cfg)
	if err != nil {
		panic(err)
	}
	return c
}

// Classify implements [conversation.IntentClassifier].
//
// HOW THE SCORE IS PRODUCED, exactly and reproducibly:
//
//  1. The utterance is tokenised: ASCII lowercased, split on anything that is
//     not a letter or digit, capped at [maxTokens].
//  2. For each rule, every cue is matched against the token run. A cue matches
//     at most once however often it occurs — repeating "message message
//     message" is not three times the evidence that a message was meant.
//  3. Matched cues contribute [cueWeight]: 1 for a single token, len+1 for a
//     phrase, because word order carries meaning a bag of words does not.
//  4. confidence = min(evidence / saturation, 1.0), so it is bounded in [0,1]
//     by construction rather than by a clamp bolted on afterwards.
//  5. Rules with no matched cue produce no candidate. They are absent, not
//     zero-scored — see the note on unknown below.
//
// The result depends on the utterance text, the expectation and the
// configuration, and on nothing else. No clock, no randomness, no map
// iteration, no environment, no I/O.
//
// ASRConfidence and Truncated are deliberately NOT used to weight the score.
// The engine already gates on ASR confidence via MinASRConfidence
// (intent.go:309) before this is ever called, and inventing a second,
// differently shaped weighting here would be a competing confidence policy.
// Truncation is left alone for the same reason: any penalty would be a number
// chosen here with nothing to justify its size. Both are recorded as
// limitations rather than papered over with a plausible-looking coefficient.
//
// UNKNOWN IS AN EMPTY SLICE, NOT AN INTENT. Returning no candidates is the
// contract's documented way to say "nothing recognised", and the engine turns
// it into the configured fallback. Emitting a low-scored "unknown" intent
// instead would be a different claim — that something WAS recognised, weakly —
// and would produce a clarification about a topic the caller never raised, the
// exact failure intent.go:298 warns about.
func (c *Classifier) Classify(
	u conversation.Utterance,
	expect conversation.Expectation,
) ([]conversation.Candidate, []conversation.Slot, error) {
	tokens := tokenize(u.Text)
	if len(tokens) == 0 {
		return nil, nil, nil
	}

	// Iterating a slice, never a map: map order is randomised per process and
	// would make the same utterance classify differently between runs.
	out := make([]conversation.Candidate, 0, len(c.rules))
	for _, r := range c.rules {
		evidence, phraseHit := score(tokens, r)
		if evidence == 0 {
			continue
		}
		// Expectation rule, and the only one: while answering a question that
		// targets a specific slot, a single incidental keyword is far more
		// likely to be part of the answer than a new request. "Monday" in reply
		// to "which day?" should fill a slot, not start a new intent. A
		// phrase-level cue still counts, so a caller who genuinely changes
		// direction mid-answer is still heard.
		if expect == conversation.ExpectSlotValue && !phraseHit {
			continue
		}

		sat := r.Saturation
		if sat == 0 {
			sat = defaultSaturation
		}
		conf := evidence / sat
		if conf > 1 {
			conf = 1
		}
		out = append(out, conversation.Candidate{Name: r.Name, Confidence: conf})
	}

	if len(out) == 0 {
		return nil, nil, nil
	}

	sortCandidates(out)
	if len(out) > c.maxN {
		out = out[:c.maxN]
	}

	// Slots belong to the RESOLVED intent, so only the top candidate's are
	// evaluated. Returning slots from runner-up intents would make the frozen
	// Complete() ask the caller for parameters of something they did not
	// request.
	//
	// Only shape is returned — Name, Filled, Confidence, Required. Values are
	// examined inside extractSlots to decide Filled and are discarded there;
	// conversation.Slot has nowhere to put one, and this package invents no
	// second transport. See the header of slots.go.
	slots := extractSlots(tokens, slotSpecsFor(out[0].Name))

	return out, slots, nil
}

// score returns the evidence weight for one rule and whether any matched cue
// was a phrase.
//
// Each cue counts at most once.
func score(tokens []string, r Rule) (evidence float64, phraseHit bool) {
	for _, cue := range r.Cues {
		for i := range tokens {
			if matchAt(tokens, i, cue) {
				evidence += cueWeight(cue)
				if len(cue) > 1 {
					phraseHit = true
				}
				break
			}
		}
	}
	return evidence, phraseHit
}

// ConfigError reports one or more configuration problems.
//
// Every problem, not the first: a lexicon with four bad rules should take one
// fix-and-restart cycle, not four.
type ConfigError struct{ Problems []string }

func (e *ConfigError) Error() string {
	if len(e.Problems) == 1 {
		return e.Problems[0]
	}
	msg := "intent: configuration problems:"
	for _, p := range e.Problems {
		msg += "\n  - " + p
	}
	return msg
}
