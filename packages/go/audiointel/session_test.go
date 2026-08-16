package audiointel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	media "github.com/callscreen/callscreen-platform/packages/go/media"
)

func TestRuntime_OpenAndClose(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	ctx := context.Background()

	if h.Runtime.Live() != 0 {
		t.Fatalf("a fresh runtime reports %d live sessions", h.Runtime.Live())
	}

	s := h.OpenInbound(t)

	if h.Runtime.Live() != 1 {
		t.Errorf("Live() = %d after opening one session, want 1", h.Runtime.Live())
	}
	if !h.Runtime.Registry().Has(s.ID()) {
		t.Error("the session is not in the registry")
	}
	if got, err := h.Runtime.Get(s.ID()); err != nil || got != s {
		t.Errorf("Get returned (%v, %v), want the session", got, err)
	}

	if err := s.Close(ctx, ReasonClosedByCaller); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !s.Closed() {
		t.Error("the session does not report itself closed")
	}
	if h.Runtime.Live() != 0 {
		t.Errorf("Live() = %d after closing the only session; closing must free "+
			"its registry slot or an idle process eventually refuses new calls "+
			"for capacity", h.Runtime.Live())
	}
	if _, err := h.Runtime.Get(s.ID()); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Get on a closed session returned %v, want ErrSessionNotFound", err)
	}

	// Idempotent.
	if err := s.Close(ctx, ReasonClosedByCaller); err != nil {
		t.Errorf("a second Close returned %v", err)
	}
}

// TestRuntime_RefusesBeyondCapacity is the §19 bound at the runtime level.
func TestRuntime_RefusesBeyondCapacity(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	ctx := context.Background()

	sessions := make([]*Session, 0, h.Config.MaxSessions)
	for i := 0; i < h.Config.MaxSessions; i++ {
		sessions = append(sessions, h.OpenInbound(t))
	}

	_, err := h.Runtime.Open(ctx, SessionContext{
		Direction: DirectionInbound, Format: h.Config.Format,
	})
	if !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("opening beyond capacity returned %v, want ErrAtCapacity", err)
	}

	// Closing one frees exactly one slot.
	if err := sessions[0].Close(ctx, ReasonClosedByCaller); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Runtime.Open(ctx, SessionContext{
		Direction: DirectionInbound, Format: h.Config.Format,
	}); err != nil {
		t.Errorf("opening after freeing a slot returned %v", err)
	}
}

func TestRuntime_RefusesAfterStop(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	ctx := context.Background()

	live := make([]*Session, 3)
	for i := range live {
		live[i] = h.OpenInbound(t)
	}

	closed, err := h.Runtime.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if closed != 3 {
		t.Errorf("Stop closed %d sessions, want 3", closed)
	}
	for i, s := range live {
		if !s.Closed() {
			t.Errorf("session %d is still open after Stop", i)
		}
	}

	if _, err := h.Runtime.Open(ctx, SessionContext{
		Direction: DirectionInbound, Format: h.Config.Format,
	}); !errors.Is(err, ErrRuntimeStopped) {
		t.Errorf("opening after Stop returned %v, want ErrRuntimeStopped", err)
	}

	// Idempotent.
	if n, err := h.Runtime.Stop(ctx); n != 0 || err != nil {
		t.Errorf("a second Stop returned (%d, %v), want (0, nil)", n, err)
	}
}

func TestRuntime_RefusesMismatchedFormat(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)

	_, err := h.Runtime.Open(context.Background(), SessionContext{
		Direction: DirectionInbound,
		Format:    media.PCM16Mono16k(),
	})
	if !errors.Is(err, ErrFormatMismatch) {
		t.Errorf("a mismatched format returned %v, want ErrFormatMismatch", err)
	}
}

func TestRuntime_RefusesInvalidSessionContext(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	ctx := context.Background()

	cases := []struct {
		name string
		sc   SessionContext
	}{
		{"no direction", SessionContext{Format: h.Config.Format}},
		{"bad direction", SessionContext{Direction: "sideways", Format: h.Config.Format}},
		{"stereo", SessionContext{
			Direction: DirectionInbound,
			Format: media.AudioFormat{Format: media.FormatPCM16,
				Layout: media.LayoutStereo, Rate: media.Rate8kHz, Codec: media.CodecPCM},
		}},
		{"malformed call identifier", SessionContext{
			Call: "Call With Spaces", Direction: DirectionInbound, Format: h.Config.Format,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := h.Runtime.Open(ctx, tc.sc); err == nil {
				t.Error("an invalid session context was accepted")
			}
		})
	}
}

// TestSession_RefusesFramesAfterClose pins the closed-session contract.
func TestSession_RefusesFramesAfterClose(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	ctx := context.Background()
	s := h.OpenInbound(t)

	frames := h.Gen.NormalSpeech(1)
	if _, err := s.Analyze(ctx, frames[0], ConversationState{}, h.Controller, nil); err != nil {
		t.Fatalf("analysing before close: %v", err)
	}

	if err := s.Close(ctx, ReasonClosedByCaller); err != nil {
		t.Fatal(err)
	}

	more := h.Gen.NormalSpeech(1)
	if _, err := s.Analyze(ctx, more[0], ConversationState{}, h.Controller, nil); !errors.Is(err, ErrSessionClosed) {
		t.Errorf("analysing after close returned %v, want ErrSessionClosed", err)
	}
	if got := s.Stats().Refused; got != 1 {
		t.Errorf("Refused = %d after one post-close frame, want 1", got)
	}
}

// TestSession_IsIsolated is §20's session isolation requirement, driven in
// parallel.
//
// # Isolation is structural here, and this checks the structure holds
//
// Two sessions share no detector, no window, no floor estimate and no lock, so
// there is no path from one's state to another's. The failure this guards
// against is a future change introducing package-level state — a shared
// analyser, a cached window, a global floor — which would show up as sessions
// fed different audio reaching the same conclusions.
func TestSession_IsIsolated(t *testing.T) {
	t.Parallel()

	const sessions = 24

	cfg := TestConfig(testFormat())
	cfg.MaxSessions = sessions
	h := NewHarness(t, WithHarnessConfig(cfg))
	ctx := context.Background()

	type result struct {
		index  int
		speech bool
		runs   int
	}
	results := make(chan result, sessions)

	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			s, err := h.Runtime.Open(ctx, SessionContext{
				Call:      CallID(fmt.Sprintf("call-%d", index)),
				Direction: DirectionInbound,
				Format:    cfg.Format,
			})
			if err != nil {
				t.Errorf("session %d: %v", index, err)
				return
			}
			defer func() { _ = s.Close(ctx, ReasonClosedByCaller) }()

			// Each session gets its OWN generator, and half of them are fed
			// nothing but silence. A session that reported speech without being
			// given any is reading somebody else's state.
			g := NewSignalGenerator(cfg.Format, cfg.FrameInterval)
			frames := WarmupFrames(g, cfg)
			if index%2 == 0 {
				frames = append(frames, g.NormalSpeech(40)...)
			} else {
				frames = append(frames, g.Silence(40)...)
			}

			var runs int
			for _, f := range frames {
				a, err := s.Analyze(ctx, f, ConversationState{}, nil, nil)
				if err != nil {
					t.Errorf("session %d: %v", index, err)
					return
				}
				if a.VAD.OnsetConfirmed {
					runs++
				}
			}
			results <- result{index: index, speech: index%2 == 0, runs: runs}
		}(i)
	}

	wg.Wait()
	close(results)

	var got int
	for r := range results {
		got++
		switch {
		case r.speech && r.runs != 1:
			t.Errorf("session %d was given speech and reported %d runs, want 1",
				r.index, r.runs)
		case !r.speech && r.runs != 0:
			t.Errorf("session %d was given only silence and reported %d speech "+
				"runs; it is seeing another session's audio", r.index, r.runs)
		}
	}
	if got != sessions {
		t.Errorf("%d sessions reported, want %d", got, sessions)
	}
	if h.Runtime.Live() != 0 {
		t.Errorf("%d sessions still live after all closed", h.Runtime.Live())
	}
}

// TestSession_ConcurrentLifecycleIsSafe drives analysis, stats and close from
// different goroutines.
//
// The mutex exists for exactly this: a supervising goroutine calling Stats or
// Close must never observe a half-updated analyser.
func TestSession_ConcurrentLifecycleIsSafe(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	ctx := context.Background()
	s := h.OpenInbound(t)

	g := NewSignalGenerator(h.Config.Format, h.Config.FrameInterval)
	frames := append(WarmupFrames(g, h.Config), g.NormalSpeech(400)...)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, f := range frames {
			// Errors are expected once the closer wins the race.
			_, _ = s.Analyze(ctx, f, ConversationState{}, h.Controller, nil)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			_ = s.Stats()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = s.Closed()
		}
		_ = s.Close(ctx, ReasonClosedByCaller)
	}()

	wg.Wait()

	if !s.Closed() {
		t.Error("the session is not closed")
	}
	if h.Runtime.Live() != 0 {
		t.Errorf("%d sessions live after the only one closed", h.Runtime.Live())
	}
}

// TestSession_PublishesTheExpectedEvents walks a whole turn and checks what
// reached the event port.
func TestSession_PublishesTheExpectedEvents(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	s := h.OpenInbound(t)

	frames := WarmupFrames(h.Gen, h.Config)
	frames = append(frames, h.Gen.NormalSpeech(30)...)
	frames = append(frames, h.Gen.Silence(60)...)

	h.Drive(t, s, frames, ConversationState{Turn: "turn-1"})

	want := []AudioEventType{
		EventSpeechStarted,
		EventSpeechDetected,
		EventSpeechStopped,
		EventSilenceStarted,
		EventEndpointCandidate,
		EventEndpointConfirmed,
	}
	for _, tp := range want {
		if n := h.Events.Count(tp); n == 0 {
			t.Errorf("no %s event was published", tp)
		}
	}

	// Each of these describes a once-per-turn fact and must not repeat.
	for _, tp := range []AudioEventType{
		EventSpeechStarted, EventSpeechDetected, EventSpeechStopped,
		EventEndpointConfirmed,
	} {
		if n := h.Events.Count(tp); n != 1 {
			t.Errorf("%s was published %d times for one turn, want 1", tp, n)
		}
	}

	// Every event must carry its identifiers and be in sequence.
	events := h.Events.ForSession(s.ID())
	if len(events) == 0 {
		t.Fatal("no events at all")
	}
	for i, e := range events {
		if e.Session != s.ID() {
			t.Errorf("event %d carries session %q, want %q", i, e.Session, s.ID())
		}
		if e.Call != "call-test" {
			t.Errorf("event %d carries call %q, want call-test", i, e.Call)
		}
		if e.Turn != "turn-1" {
			t.Errorf("event %d carries turn %q, want turn-1", i, e.Turn)
		}
		if i > 0 && e.Sequence <= events[i-1].Sequence {
			t.Errorf("event %d has sequence %d, not above the previous %d",
				i, e.Sequence, events[i-1].Sequence)
		}
		if !e.Type.Valid() {
			t.Errorf("event %d has undeclared type %q", i, e.Type)
		}
	}
}

// TestSession_MetricLabelsAreFromTheDeclaredVocabulary is the runtime check
// behind §17.
//
// The metrics test proves the label NAMES are bounded. This proves the VALUES
// are: after driving real audio, every series key must be a constant from
// classifications.go.
func TestSession_MetricLabelsAreFromTheDeclaredVocabulary(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	ctx := context.Background()
	s := h.OpenInbound(t)

	// Enough variety to touch most of the instruments.
	frames := WarmupFrames(h.Gen, h.Config)
	frames = append(frames, h.Gen.NormalSpeech(30)...)
	frames = append(frames, h.Gen.Silence(40)...)
	frames = append(frames, h.Gen.Noise(0.05, 30)...)
	frames = append(frames, h.Gen.Clipped(30)...)
	frames = append(frames, h.Gen.Silence(40)...)

	h.Drive(t, s, frames, ConversationState{AgentSpeaking: true})
	_ = s.Close(ctx, ReasonClosedByCaller)

	vocabulary := map[string]bool{}
	add := func(vs ...string) {
		for _, v := range vs {
			vocabulary[v] = true
		}
	}
	for _, v := range AllDirections() {
		add(string(v))
	}
	for _, v := range AllVADStates() {
		add(string(v))
	}
	for _, v := range AllSilenceClasses() {
		add(string(v))
	}
	for _, v := range AllQualityClasses() {
		add(string(v))
	}
	for _, v := range AllOverlapStates() {
		add(string(v))
	}
	for _, v := range AllBargeInOutcomes() {
		add(string(v))
	}
	for _, v := range AllContinuityFaults() {
		add(string(v))
	}
	for _, v := range AllNoiseClasses() {
		add(string(v))
	}
	add(allReasonCodes()...)
	add(allVerdictCodes()...)
	add("full_chain", "unspecified")

	for _, sample := range h.Metrics.Snapshot() {
		for _, label := range sample.Labels {
			if label == "" {
				continue
			}
			if !vocabulary[label] {
				t.Errorf("metric %q carries label value %q, which is not in the "+
					"declared vocabulary; every label must be a constant from "+
					"classifications.go", sample.Name, label)
			}
		}
	}
}

// TestSession_StatsReflectTheTurn checks the pulled-on-demand detail.
func TestSession_StatsReflectTheTurn(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	s := h.OpenInbound(t)

	frames := WarmupFrames(h.Gen, h.Config)
	frames = append(frames, h.Gen.NormalSpeech(30)...)
	frames = append(frames, h.Gen.Silence(60)...)

	h.Drive(t, s, frames, ConversationState{})
	stats := s.Stats()

	if stats.ID != s.ID() {
		t.Errorf("Stats.ID = %q, want %q", stats.ID, s.ID())
	}
	if stats.Frames != uint64(len(frames)) {
		t.Errorf("Frames = %d, want %d", stats.Frames, len(frames))
	}
	if stats.SpeechRuns != 1 {
		t.Errorf("SpeechRuns = %d, want 1", stats.SpeechRuns)
	}
	if stats.Endpoints != 1 {
		t.Errorf("Endpoints = %d, want 1", stats.Endpoints)
	}
	if stats.SpeechTime <= 0 {
		t.Error("SpeechTime is zero after a turn of speech")
	}
	if stats.String() == "" {
		t.Error("Stats renders empty")
	}
}

// TestSession_ResetKeepsTheLifetimeCounters guards a specific mistake: a
// recovered session is the SAME session, and zeroing its history would make an
// incident look like it happened to a fresh call.
func TestSession_ResetKeepsTheLifetimeCounters(t *testing.T) {
	t.Parallel()

	h := NewHarness(t)
	s := h.OpenInbound(t)

	frames := WarmupFrames(h.Gen, h.Config)
	frames = append(frames, h.Gen.NormalSpeech(30)...)
	h.Drive(t, s, frames, ConversationState{})

	before := s.Stats()
	if before.SpeechRuns == 0 {
		t.Fatal("setup: no speech run was recorded")
	}

	s.Reset()
	after := s.Stats()

	if after.SpeechRuns != before.SpeechRuns {
		t.Errorf("SpeechRuns went from %d to %d across a Reset",
			before.SpeechRuns, after.SpeechRuns)
	}
	if after.Frames != before.Frames {
		t.Errorf("Frames went from %d to %d across a Reset",
			before.Frames, after.Frames)
	}
	// But the detectors did restart.
	if after.VADState != VADUncertain {
		t.Errorf("VADState = %s after a Reset, want %s", after.VADState, VADUncertain)
	}
}

// TestRegistry_ShardsSpreadTheLoad checks the hash actually distributes.
//
// A registry with every session on one shard has the contention profile of a
// single mutex while looking sharded, and nothing about the API would reveal
// it.
func TestRegistry_ShardsSpreadTheLoad(t *testing.T) {
	t.Parallel()

	r := NewSessionRegistry()
	const n = registryShards * 20

	for i := 0; i < n; i++ {
		if err := r.Register(&Session{id: NewSessionID()}); err != nil {
			t.Fatal(err)
		}
	}

	depths := r.ShardDepths()
	var occupied, deepest int
	for _, d := range depths {
		if d > 0 {
			occupied++
		}
		if d > deepest {
			deepest = d
		}
	}

	if occupied < registryShards/2 {
		t.Errorf("%d of %d shards occupied by %d sessions; the hash is not "+
			"spreading", occupied, registryShards, n)
	}
	// With 20 per shard on average, one holding a quarter of everything means
	// the hash is clustering badly.
	if deepest > n/4 {
		t.Errorf("the deepest shard holds %d of %d sessions", deepest, n)
	}
	if r.Len() != n {
		t.Errorf("Len() = %d, want %d", r.Len(), n)
	}
}

func TestRegistry_RefusesDuplicates(t *testing.T) {
	t.Parallel()

	r := NewSessionRegistry()
	s := &Session{id: NewSessionID()}

	if err := r.Register(s); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(s); !errors.Is(err, ErrSessionExists) {
		t.Errorf("registering a duplicate returned %v, want ErrSessionExists", err)
	}
	if r.Len() != 1 {
		t.Errorf("Len() = %d after a refused duplicate, want 1", r.Len())
	}

	if !r.Remove(s.id) {
		t.Error("Remove reported the session absent")
	}
	if r.Remove(s.id) {
		t.Error("Remove reported a second removal as successful")
	}
	if r.Len() != 0 {
		t.Errorf("Len() = %d after removal, want 0", r.Len())
	}
}

func TestRegistry_ConcurrentAccessIsSafe(t *testing.T) {
	t.Parallel()

	r := NewSessionRegistry()
	const workers = 16
	const each = 50

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				s := &Session{id: NewSessionID()}
				if err := r.Register(s); err != nil {
					t.Errorf("Register: %v", err)
					return
				}
				if !r.Has(s.id) {
					t.Error("a just-registered session is not present")
					return
				}
				r.Each(func(*Session) bool { return true })
				if !r.Remove(s.id) {
					t.Error("Remove reported a registered session absent")
					return
				}
			}
		}()
	}
	wg.Wait()

	if r.Len() != 0 {
		t.Errorf("Len() = %d after every session was removed", r.Len())
	}
	if r.Total() != workers*each {
		t.Errorf("Total() = %d, want %d", r.Total(), workers*each)
	}
}

// BenchmarkSession_Analyze measures the complete per-frame cost through the
// public entry point, including the lock, the metrics and the event checks.
func BenchmarkSession_Analyze(b *testing.B) {
	h := NewHarness(b)
	ctx := context.Background()
	s := h.OpenInbound(b)

	h.Drive(b, s, WarmupFrames(h.Gen, h.Config), ConversationState{})
	frames := h.Gen.NormalSpeech(128)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Analyze(ctx, frames[i%len(frames)],
			ConversationState{}, h.Controller, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSession_ConcurrentSessions measures how the engine scales across
// sessions, which is the §19 question that matters for capacity planning.
func BenchmarkSession_ConcurrentSessions(b *testing.B) {
	for _, count := range []int{1, 8, 64, 256} {
		b.Run(fmt.Sprintf("sessions=%d", count), func(b *testing.B) {
			cfg := TestConfig(testFormat())
			cfg.MaxSessions = count
			h := NewHarness(b, WithHarnessConfig(cfg))
			ctx := context.Background()

			sessions := make([]*Session, count)
			frames := make([][]media.Frame, count)
			for i := range sessions {
				sessions[i] = h.OpenInbound(b)
				g := NewSignalGenerator(cfg.Format, cfg.FrameInterval)
				h.Drive(b, sessions[i], WarmupFrames(g, cfg), ConversationState{})
				frames[i] = g.NormalSpeech(64)
			}

			b.ReportAllocs()
			b.ResetTimer()

			var wg sync.WaitGroup
			perSession := b.N / count
			if perSession < 1 {
				perSession = 1
			}
			for i := range sessions {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					s, f := sessions[index], frames[index]
					for n := 0; n < perSession; n++ {
						_, _ = s.Analyze(ctx, f[n%len(f)], ConversationState{}, nil, nil)
					}
				}(i)
			}
			wg.Wait()
		})
	}
}

// BenchmarkSession_AnalyzeWithoutRecording isolates the engine's own
// allocation behaviour from the test harness's.
//
// BenchmarkSession_Analyze uses a RecordingEventPublisher, which RETAINS every
// event it is given and therefore grows a slice — a few bytes per frame that
// belong to the recorder, not to the analysis. A production deployment
// publishes to a broker adapter or discards. This measures the path with
// nothing retaining, which is what §19's bounded-hot-path claim is about.
func BenchmarkSession_AnalyzeWithoutRecording(b *testing.B) {
	cfg := TestConfig(testFormat())
	runtime, err := New(cfg, WithEventPublisher(NopEventPublisher{}))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()

	s, err := runtime.Open(ctx, SessionContext{
		Direction: DirectionInbound, Format: cfg.Format,
	})
	if err != nil {
		b.Fatal(err)
	}

	g := NewSignalGenerator(cfg.Format, cfg.FrameInterval)
	for _, f := range WarmupFrames(g, cfg) {
		if _, err := s.Analyze(ctx, f, ConversationState{}, nil, nil); err != nil {
			b.Fatal(err)
		}
	}
	frames := g.NormalSpeech(128)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Analyze(ctx, frames[i%len(frames)],
			ConversationState{}, nil, nil); err != nil {
			b.Fatal(err)
		}
	}
}
