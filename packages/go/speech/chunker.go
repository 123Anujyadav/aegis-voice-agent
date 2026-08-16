package speech

import (
	"strings"
	"sync"
	"unicode"
)

// Chunk is one speakable unit of text.
type Chunk struct {
	// Sequence orders chunks within one synthesis stream. Monotonic and
	// gapless, because TTS output ordering is reconstructed from it.
	Sequence uint64

	// Text is the chunk, trimmed of leading and trailing whitespace.
	Text string

	// Terminator is the punctuation that ended the chunk, or zero when the
	// chunk was ended by the length bound or by end of stream.
	Terminator rune

	// IsFinal marks the last chunk of a stream, so a synthesiser knows to close
	// rather than wait for more text.
	IsFinal bool
}

// ChunkConfig configures the chunker.
type ChunkConfig struct {
	// MinChars is the fewest non-space runes a chunk may contain.
	//
	// Guards against a stray terminator emitting an empty or single-character
	// chunk, which costs a whole synthesis round trip to say nothing.
	MinChars int

	// MaxChars forces a break in text that never terminates.
	//
	// Without it a single unterminated clause holds all audio until the
	// generator finishes — the failure ADR-0007 describes as "well over a
	// second of dead air before the caller hears anything".
	MaxChars int

	// Abbreviations are tokens that end in a period without ending a sentence.
	// Matched case-sensitively against the token preceding the period.
	Abbreviations []string
}

// DefaultChunkConfig returns the telephony baseline.
func DefaultChunkConfig() ChunkConfig {
	return ChunkConfig{
		MinChars: 2,
		// 240 runes is roughly 15 seconds of speech — long enough that a normal
		// sentence is never split mid-clause, short enough that a runaway
		// generation still produces audio promptly.
		MaxChars: 240,
		// "No" is deliberately ABSENT even though it abbreviates "number".
		// A voice agent says "No." as a complete answer far more often than it
		// says "No. 5", and suppressing the boundary there merges the refusal
		// into the following sentence — which is both wrong and the more
		// audible of the two errors.
		Abbreviations: []string{
			"Dr", "Mr", "Mrs", "Ms", "Prof", "Sr", "Jr", "St",
			"vs", "etc", "Rs", "approx", "Inc", "Ltd", "Pvt",
		},
	}
}

func (c ChunkConfig) validate() []string {
	var problems []string
	if c.MinChars < 0 {
		problems = append(problems, "chunk: MinChars must not be negative")
	}
	if c.MaxChars <= 0 {
		problems = append(problems, "chunk: MaxChars must be positive; without it one "+
			"unterminated clause delays every frame of audio")
	}
	if c.MaxChars > 0 && c.MinChars > c.MaxChars {
		problems = append(problems, "chunk: MinChars exceeds MaxChars")
	}
	return problems
}

// Sentence terminators.
//
// The Devanagari danda (U+0964) and double danda (U+0965) are sentence
// terminators in Hindi exactly as the period is in English. Omitting them —
// which any splitter written for English alone does — means Hindi text is never
// segmented and the entire reply is synthesised as one chunk, which misses the
// latency budget rather than merely reading badly.
const (
	danda       = '।'
	doubleDanda = '॥'
)

// unambiguousTerminator reports whether r always ends a sentence.
//
// Only the period is ambiguous. '?' and '!' do not appear inside numbers,
// abbreviations or URLs in any script this handles, and the danda is used for
// nothing else at all. Treating them as unambiguous is what keeps the
// suppression rules below small enough to reason about.
func unambiguousTerminator(r rune) bool {
	return r == '?' || r == '!' || r == danda || r == doubleDanda
}

// Chunker is a deterministic sentence and clause boundary detector.
//
// # Streaming and one-shot produce identical output
//
// Text arrives from a generator a token at a time. The chunker buffers until a
// boundary is provably reached, so feeding a string one rune at a time yields
// exactly the chunks that feeding it whole would. Without that property, TTS
// output would depend on network packet boundaries.
//
// # Rune-based throughout
//
// Devanagari is multi-byte, and byte indexing would split a grapheme and
// corrupt the text. Every index here is into a []rune.
//
// # It never splits blindly on a period
//
// A period ends a sentence only when the next rune is whitespace or end of
// input, the preceding token is not a known abbreviation, and the preceding
// token is not a single letter. That is what keeps 1234.56, example.co.in,
// 022.2222.3333, "Dr." and "A. K. Sharma" intact.
type Chunker struct {
	cfg ChunkConfig
	// abbrev is the configured list as a set, for O(1) lookup.
	abbrev map[string]struct{}

	mu  sync.Mutex
	buf []rune
	seq uint64
}

// NewChunker builds a chunker.
func NewChunker(cfg ChunkConfig) (*Chunker, error) {
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Problems: problems}
	}
	set := make(map[string]struct{}, len(cfg.Abbreviations))
	for _, a := range cfg.Abbreviations {
		set[a] = struct{}{}
	}
	return &Chunker{cfg: cfg, abbrev: set, buf: make([]rune, 0, cfg.MaxChars+16)}, nil
}

// Push feeds text and returns every chunk that became complete.
//
// Returns nil when no boundary was reached, which is the common case while a
// generator is mid-sentence.
func (c *Chunker) Push(text string) []Chunk {
	if text == "" {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.buf = append(c.buf, []rune(text)...)

	var out []Chunk
	for {
		cut, term, ok := c.boundaryLocked(false)
		if !ok {
			return out
		}
		chunk, emitted := c.emitLocked(cut, term, false)
		if emitted {
			out = append(out, chunk)
		}
	}
}

// Flush emits whatever remains, marking the last chunk final.
//
// Called at end of generation. A generator that stops mid-sentence still has
// its words spoken; the alternative is silently dropping the tail.
func (c *Chunker) Flush() []Chunk {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []Chunk
	for {
		cut, term, ok := c.boundaryLocked(true)
		if !ok {
			break
		}
		if chunk, emitted := c.emitLocked(cut, term, false); emitted {
			out = append(out, chunk)
		}
	}

	// Whatever is left is the tail.
	if rest := strings.TrimSpace(string(c.buf)); rest != "" {
		chunk := Chunk{Sequence: c.seq, Text: rest, IsFinal: true}
		c.seq++
		c.buf = c.buf[:0]
		out = append(out, chunk)
		return out
	}
	c.buf = c.buf[:0]

	if len(out) > 0 {
		out[len(out)-1].IsFinal = true
	}
	return out
}

// Reset clears the buffer and the sequence, for reuse across turns.
func (c *Chunker) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = c.buf[:0]
	c.seq = 0
}

// emitLocked cuts the buffer at cut and returns the chunk. Caller holds lock.
//
// Returns false when the cut would produce a chunk below MinChars, in which
// case the text is discarded rather than synthesised — a chunk of "." costs a
// full round trip to say nothing.
func (c *Chunker) emitLocked(cut int, term rune, final bool) (Chunk, bool) {
	text := strings.TrimSpace(string(c.buf[:cut]))
	c.buf = c.buf[:copy(c.buf, c.buf[cut:])]

	if countNonSpace(text) < c.cfg.MinChars {
		return Chunk{}, false
	}

	chunk := Chunk{Sequence: c.seq, Text: text, Terminator: term, IsFinal: final}
	c.seq++
	return chunk, true
}

// boundaryLocked finds the next chunk boundary. Caller holds the lock.
//
// atEnd reports whether the buffer is known to be the end of the stream, which
// only Flush can know. See periodTerminatesLocked for why it matters.
//
// Returns the cut index (exclusive), the terminator that caused it, and whether
// a boundary was found at all.
func (c *Chunker) boundaryLocked(atEnd bool) (int, rune, bool) {
	for i := 0; i < len(c.buf); i++ {
		r := c.buf[i]

		// '?', '!' and the dandas are unambiguous, so they cut immediately even
		// at the very end of the buffer. Waiting for the next rune would add a
		// generator round trip of latency to every question the agent asks.
		if unambiguousTerminator(r) {
			return i + 1, r, true
		}
		if r == '.' && c.periodTerminatesLocked(i, atEnd) {
			return i + 1, r, true
		}
	}

	// No terminator. Force a break if the buffer has outgrown the bound.
	if len(c.buf) > c.cfg.MaxChars {
		if cut := lastSpaceAtOrBefore(c.buf, c.cfg.MaxChars); cut > 0 {
			return cut, 0, true
		}
		return c.cfg.MaxChars, 0, true
	}
	return 0, 0, false
}

// periodTerminatesLocked decides whether the period at index i ends a sentence.
// Caller holds the lock.
//
// atEnd reports whether the buffer is the whole remaining stream.
func (c *Chunker) periodTerminatesLocked(i int, atEnd bool) bool {
	// Rule 1: the next rune must be whitespace, or genuine end of input.
	//
	// This single rule is what protects decimals (1234.56), URLs
	// (example.co.in) and dotted phone numbers (022.2222.3333): in all three
	// the period is followed immediately by a digit or a letter.
	//
	// # Why atEnd exists
	//
	// A period at the END of the buffer is UNDECIDABLE while text is still
	// streaming: "1234." looks like a terminated sentence until "56" arrives.
	// Deciding it early makes streaming output differ from one-shot output,
	// which would make TTS chunking depend on network packet boundaries.
	// Only Flush knows the stream is over, so only Flush passes atEnd.
	if i+1 < len(c.buf) {
		if !unicode.IsSpace(c.buf[i+1]) {
			return false
		}
	} else if !atEnd {
		return false
	}

	// Rules 2 and 3 look at the token immediately before the period.
	tok := tokenBefore(c.buf, i)
	if tok == "" {
		return false
	}

	// Rule 2: a known abbreviation does not end a sentence.
	if _, ok := c.abbrev[tok]; ok {
		return false
	}

	// Rule 3: a single letter is an initial — "A. K. Sharma".
	if len([]rune(tok)) == 1 && unicode.IsLetter([]rune(tok)[0]) {
		return false
	}

	return true
}

// tokenBefore returns the run of non-space runes immediately preceding index i.
func tokenBefore(buf []rune, i int) string {
	end := i
	start := end
	for start > 0 && !unicode.IsSpace(buf[start-1]) {
		start--
	}
	if start == end {
		return ""
	}
	return string(buf[start:end])
}

// lastSpaceAtOrBefore returns the index just past the last space at or before
// limit, or 0 when there is none.
func lastSpaceAtOrBefore(buf []rune, limit int) int {
	if limit > len(buf) {
		limit = len(buf)
	}
	for i := limit; i > 0; i-- {
		if unicode.IsSpace(buf[i-1]) {
			return i
		}
	}
	return 0
}

// countNonSpace counts runes that are not whitespace.
func countNonSpace(s string) int {
	var n int
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}
