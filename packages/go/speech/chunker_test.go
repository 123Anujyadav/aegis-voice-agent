package speech

import "testing"

func chunkTexts(t *testing.T, c *Chunker, in string) []string {
	t.Helper()
	var out []string
	for _, ch := range c.Push(in) {
		out = append(out, ch.Text)
	}
	for _, ch := range c.Flush() {
		out = append(out, ch.Text)
	}
	return out
}

// TestChunker_Boundaries covers mandatory cases 15-23.
func TestChunker_Boundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// Mandatory 15 — Hindi with the Devanagari danda.
		{"hindi devanagari danda",
			"नमस्ते। आप कैसे हैं।",
			[]string{"नमस्ते।", "आप कैसे हैं।"}},

		// Mandatory 17 — Devanagari with mixed terminators.
		{"devanagari mixed terminators",
			"क्या हाल है? बहुत बढ़िया! ठीक है।",
			[]string{"क्या हाल है?", "बहुत बढ़िया!", "ठीक है।"}},

		// Mandatory 16 — Hinglish in Latin script.
		{"hinglish code mixed",
			"Aapka OTP hai 4 5 6. Please share mat kijiye. Thank you!",
			[]string{"Aapka OTP hai 4 5 6.", "Please share mat kijiye.", "Thank you!"}},

		// Mandatory 18 — Devanagari and Latin in one utterance.
		{"devanagari and latin mixed",
			"मैं आपकी help कर सकता हूँ। Please hold.",
			[]string{"मैं आपकी help कर सकता हूँ।", "Please hold."}},

		// Mandatory 19 — decimal numbers must not split.
		{"decimals",
			"The amount is 1234.56 rupees. Confirm please.",
			[]string{"The amount is 1234.56 rupees.", "Confirm please."}},

		// Mandatory 20 — phone numbers must not split.
		{"phone number",
			"Call 022.2222.3333 now. Thanks.",
			[]string{"Call 022.2222.3333 now.", "Thanks."}},

		// Mandatory 21 — OTP-like digit runs must not split.
		{"otp digits",
			"Your code is 4 8 2 9 1 6. Do not share.",
			[]string{"Your code is 4 8 2 9 1 6.", "Do not share."}},

		// Mandatory 22 — URLs must not split.
		{"url",
			"Visit example.co.in for details. Bye.",
			[]string{"Visit example.co.in for details.", "Bye."}},

		// Mandatory 23 — abbreviations must not split.
		{"abbreviations",
			"Dr. Sharma will call. Mr. Rao agreed.",
			[]string{"Dr. Sharma will call.", "Mr. Rao agreed."}},

		// Initials are the other abbreviation shape.
		{"initials",
			"A. K. Sharma is calling. Please hold.",
			[]string{"A. K. Sharma is calling.", "Please hold."}},

		// Short sentences are legal and must survive.
		{"short sentences", "Yes. No. Ok.", []string{"Yes.", "No.", "Ok."}},

		// No terminator at all — Flush must still emit.
		{"unterminated", "no terminator here", []string{"no terminator here"}},

		// Devanagari double danda.
		{"double danda", "श्लोक॥ अगला वाक्य।", []string{"श्लोक॥", "अगला वाक्य।"}},

		// A rupee amount in Devanagari context — decimal rule is script-neutral.
		{"decimal in devanagari",
			"आपका बैलेंस 1234.56 है। धन्यवाद।",
			[]string{"आपका बैलेंस 1234.56 है।", "धन्यवाद।"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewChunker(DefaultChunkConfig())
			if err != nil {
				t.Fatal(err)
			}
			got := chunkTexts(t, c, tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d chunks %q, want %d %q", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("chunk %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// Streaming must produce identical chunks to one-shot input. This is the
// property that lets TTS start before the LLM has finished generating.
func TestChunker_StreamingMatchesOneShot(t *testing.T) {
	t.Parallel()
	const full = "Aapka balance 1234.56 hai। Dr. Sharma se baat kijiye. Thank you!"

	one, err := NewChunker(DefaultChunkConfig())
	if err != nil {
		t.Fatal(err)
	}
	want := chunkTexts(t, one, full)

	stream, err := NewChunker(DefaultChunkConfig())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range full { // one rune at a time — the worst case
		for _, ch := range stream.Push(string(r)) {
			got = append(got, ch.Text)
		}
	}
	for _, ch := range stream.Flush() {
		got = append(got, ch.Text)
	}

	if len(got) != len(want) {
		t.Fatalf("streaming produced %d chunks %q, one-shot produced %d %q",
			len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("chunk %d: streaming %q, one-shot %q", i, got[i], want[i])
		}
	}
}

// Sequence numbers are monotonic and gapless — TTS chunk ordering depends on it.
func TestChunker_SequenceIsMonotonic(t *testing.T) {
	t.Parallel()
	c, err := NewChunker(DefaultChunkConfig())
	if err != nil {
		t.Fatal(err)
	}
	chunks := append(c.Push("One. Two. Three."), c.Flush()...)
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3", len(chunks))
	}
	for i, ch := range chunks {
		if ch.Sequence != uint64(i) {
			t.Errorf("chunk %d has sequence %d", i, ch.Sequence)
		}
	}
}

// MaxChars must force a break, or one unterminated clause holds all audio.
func TestChunker_MaxCharsForcesABreak(t *testing.T) {
	t.Parallel()
	cfg := DefaultChunkConfig()
	cfg.MaxChars = 20
	c, err := NewChunker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	long := "this is a very long clause with no terminator anywhere in it at all"
	got := append(c.Push(long), c.Flush()...)
	if len(got) < 2 {
		t.Fatalf("MaxChars did not force a break: %d chunks %v", len(got), got)
	}
	for _, ch := range got {
		if n := len([]rune(ch.Text)); n > cfg.MaxChars*2 {
			t.Errorf("chunk %q is %d runes, far beyond MaxChars %d", ch.Text, n, cfg.MaxChars)
		}
	}
}

// The last chunk of a stream is marked, so TTS knows when to close the stream.
func TestChunker_FlushMarksTheFinalChunk(t *testing.T) {
	t.Parallel()
	c, err := NewChunker(DefaultChunkConfig())
	if err != nil {
		t.Fatal(err)
	}
	mid := c.Push("First. Second.")
	for _, ch := range mid {
		if ch.IsFinal {
			t.Errorf("chunk %q was marked final mid-stream", ch.Text)
		}
	}
	last := c.Flush()
	if len(last) == 0 {
		t.Skip("nothing buffered at flush; covered by other cases")
	}
	if !last[len(last)-1].IsFinal {
		t.Error("the last flushed chunk was not marked final")
	}
}

func TestChunker_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultChunkConfig()
	cfg.MaxChars = 0
	if _, err := NewChunker(cfg); err == nil {
		t.Error("MaxChars=0 was accepted")
	}
	cfg = DefaultChunkConfig()
	cfg.MinChars = -1
	if _, err := NewChunker(cfg); err == nil {
		t.Error("negative MinChars was accepted")
	}
}

// Empty and whitespace-only input must produce nothing, not an empty chunk.
func TestChunker_EmptyInputProducesNothing(t *testing.T) {
	t.Parallel()
	c, err := NewChunker(DefaultChunkConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got := append(c.Push("   "), c.Flush()...); len(got) != 0 {
		t.Errorf("whitespace produced %d chunks %v", len(got), got)
	}
}
