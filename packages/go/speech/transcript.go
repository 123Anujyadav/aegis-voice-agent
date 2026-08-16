package speech

import (
	"fmt"
	"time"
)

// Language is a BCP-47 tag.
//
// A string rather than a closed enum because the set is open: Tamil metadata
// must be expressible before Tamil is supported, and a provider may report a
// tag nobody anticipated. Routing consults [Capabilities], not this type, so an
// unknown tag degrades to "no provider declares it" rather than to a panic.
type Language string

// The languages the architecture is designed around. Others are legal values.
const (
	// LangEnglishIN is Indian English.
	LangEnglishIN Language = "en-IN"

	// LangHindi is Hindi in Devanagari script.
	LangHindi Language = "hi-IN"

	// LangHinglish marks code-mixed Hindi-English in Latin script.
	//
	// DELIBERATELY DISTINCT FROM hi-IN. A provider may handle Devanagari Hindi
	// well and Romanised code-mixing badly, or the reverse; collapsing them
	// would make that difference unroutable and unmeasurable.
	LangHinglish Language = "hi-Latn-IN"

	// LangTamil is Tamil. Metadata-ready; no quality claim is made.
	LangTamil Language = "ta-IN"

	// LangUnknown is an unset or unreported language.
	LangUnknown Language = ""
)

// String implements fmt.Stringer.
func (l Language) String() string {
	if l == LangUnknown {
		return "unknown"
	}
	return string(l)
}

// Label returns the language in a form safe as a metric label.
//
// Lowercased and bounded. A provider reporting a malformed tag cannot inject
// arbitrary label cardinality through it.
func (l Language) Label() string {
	s := l.String()
	if len(s) > 16 {
		return "other"
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9', r == '-':
		default:
			return "other"
		}
	}
	return s
}

// Role is who produced the speech.
type Role string

// The roles.
const (
	RoleCaller Role = "caller"
	RoleAgent  Role = "agent"
)

// Valid reports whether the role is declared.
func (r Role) Valid() bool { return r == RoleCaller || r == RoleAgent }

// String implements fmt.Stringer.
func (r Role) String() string { return string(r) }

// ProviderMeta is bounded provider metadata.
//
// # Deliberately not a map and not a vendor struct
//
// A provider response carries fields whose shape is the vendor's to change, and
// admitting them here — as a map[string]any, a json.RawMessage or an embedded
// SDK type — would put a vendor schema into the core contract. That is the
// single thing this phase exists to prevent.
//
// Three bounded fields cover what a router and an operator actually act on:
// which provider answered, which model it claims, and how long it took.
type ProviderMeta struct {
	// Provider is the authored identifier, safe as a metric label.
	Provider ProviderID

	// Model is an authored, bounded model name. Never free-form vendor output.
	Model string

	// Latency is how long the provider took to produce this segment.
	Latency time.Duration
}

// maxModelLen bounds the model name.
const maxModelLen = 64

// ConfidenceUnknown marks a provider that reports no confidence.
//
// Distinguishable from a genuine zero on purpose: a provider reporting zero
// confidence and a provider reporting nothing at all are different situations,
// and a router that conflated them would treat silence as certainty of failure.
const ConfidenceUnknown = -1.0

// TranscriptSegment is one unit of recognised speech.
//
// # It carries text, and therefore it is sensitive
//
// Text is conversation content. It never reaches a metric label, a log line at
// default level, or an event payload — [TranscriptSegment.Redacted] exists so
// that a segment can be described without being disclosed. See
// docs/speech/SECURITY_REVIEW.md.
//
// # It never carries audio
//
// There is no PCM field and there will not be one. A transcript event that
// carried the audio it was derived from would turn every transcript store into
// a recording system, which is the obligation MEDIA-PCM-1 was written to bound.
type TranscriptSegment struct {
	Session SessionID
	Turn    TurnID
	Segment SegmentID

	// Sequence orders segments within a turn. Monotonic per turn: a provider
	// that repeats or reorders is detected by this field alone.
	Sequence uint64

	// Text is what was recognised.
	Text string

	// IsFinal marks a segment that will not be revised.
	IsFinal bool

	// Confidence is 0..1, or [ConfidenceUnknown].
	Confidence float64

	// StartTime and EndTime locate the segment on the media timeline.
	StartTime time.Duration
	EndTime   time.Duration

	Language Language
	Role     Role
	Meta     ProviderMeta
}

// Duration returns the segment's span on the media timeline.
func (s TranscriptSegment) Duration() time.Duration { return s.EndTime - s.StartTime }

// Validate checks the segment, reporting every problem.
func (s TranscriptSegment) Validate() error {
	var problems []string

	if !s.Session.Valid() {
		problems = append(problems, "transcript: Session is not a valid identifier")
	}
	if !s.Turn.Valid() {
		problems = append(problems, "transcript: Turn is not a valid identifier")
	}
	if !s.Segment.Valid() {
		problems = append(problems, "transcript: Segment is not a valid identifier")
	}
	if s.Confidence != ConfidenceUnknown && (s.Confidence < 0 || s.Confidence > 1) {
		problems = append(problems, fmt.Sprintf(
			"transcript: Confidence %v is outside 0..1 and is not ConfidenceUnknown",
			s.Confidence))
	}
	if s.EndTime < s.StartTime {
		problems = append(problems, fmt.Sprintf(
			"transcript: EndTime %s precedes StartTime %s", s.EndTime, s.StartTime))
	}
	if s.Role != "" && !s.Role.Valid() {
		problems = append(problems, fmt.Sprintf("transcript: Role %q is not declared", s.Role))
	}
	if s.Meta.Provider != "" && !s.Meta.Provider.Valid() {
		problems = append(problems, "transcript: Meta.Provider is not a valid identifier")
	}
	if len(s.Meta.Model) > maxModelLen {
		problems = append(problems, fmt.Sprintf(
			"transcript: Meta.Model exceeds %d characters", maxModelLen))
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %v", ErrInvalidTranscript, problems)
	}
	return nil
}

// Redacted renders the segment WITHOUT its text.
//
// The form every log line and every event uses. Character count rather than
// content: it is enough to tell an empty result from a long one, and to spot a
// provider emitting nonsense, without recording what was said.
func (s TranscriptSegment) Redacted() string {
	return fmt.Sprintf(
		"segment %s turn=%s seq=%d final=%v chars=%d lang=%s conf=%.2f provider=%s",
		s.Segment, s.Turn, s.Sequence, s.IsFinal, len([]rune(s.Text)),
		s.Language, s.Confidence, s.Meta.Provider)
}

// String implements fmt.Stringer, redacted.
//
// Deliberately the redacted form: the commonest way transcript content reaches
// a log is somebody printing the struct with %v while debugging something else.
func (s TranscriptSegment) String() string { return s.Redacted() }
