// Package voice orchestrates the Aegis AI end-to-end voice loop and hosts the
// local provider adapters.
//
// # What this is
//
// The layer that turns eight frozen contracts into one working conversation:
//
//	media.Frame in
//	  → audiointel  (is somebody talking, did they stop, did they interrupt)
//	  → speech      (recognition, through a provider port)
//	  → conversation (what should happen next — a Plan, not text)
//	  → governance  (may it happen)
//	  → runtime     (generate the text)
//	  → speech      (synthesis, through a provider port)
//	  → media.Frame out
//
// This package owns the ORCHESTRATION of that loop and the ADAPTERS at its two
// ends. It owns none of the layers themselves.
//
// # What this is not
//
// There is no recognition, no synthesis and no language model in this package.
// There is no SIP, RTP, WebRTC or carrier. There is no second router, no second
// memory system, no second governance engine and no reimplementation of the
// conversation engine.
//
// There is no cloud SDK and no API key. Phase 11E is local-first by
// requirement: the loop runs against processes on a developer's own machine and
// needs no paid credential. Cloud adapters come later and will be written
// against exactly the ports the local ones implement.
//
// # The conversation engine is a planner, not a generator
//
// This is the single most important thing to understand before reading the
// orchestrator. [conversation.Engine] decides WHAT should happen next and
// returns a Plan; it never calls a model and produces no text. Generation is
// [runtime.Kernel.Generate].
//
// So a turn is not "ask the conversation engine for a reply". It is: hand it a
// transcript, receive a plan, put that plan through governance, and only then
// generate. Anything that shortcut that would be bypassing the gate.
//
// # Providers live behind ports that already existed
//
// [speech.STTProvider] and [speech.TTSProvider] are Phase 11C's, and their own
// documentation names Whisper as an intended adapter. [runtime.Provider] is
// Phase 10A's. This package adds no new provider port, because adding one would
// mean two vocabularies for the same idea.
//
//	whispercpp  → speech.STTProvider
//	whispercli  → speech.STTProvider
//	piper       → speech.TTSProvider
//	ollama      → runtime.Provider
//
// Each adapter subpackage imports the port it implements and the standard
// library. Nothing else — not this package, not conversation, not governance,
// not each other.
//
// # Routing already existed too
//
// [speech.ProviderRouter] implements primary and secondary tiers, a circuit
// breaker, health reporting, provider-switch notification and capability
// matching. This package registers providers with it and reads its health; it
// does not contain a second routing engine, because two would eventually
// disagree about which provider is live.
//
// What this package adds is a DESCRIPTOR layer — model identity, version,
// declared languages, streaming and cancellation support — which the router has
// no place to hold and an operator needs.
//
// # Local providers are external processes, and processes leak
//
// A crashed child that nobody reaped, a goroutine blocked forever on a pipe
// nobody drains, a stderr buffer that grows until the host does — every one of
// those is a production incident that looks like a memory leak. providers/process
// exists so that supervision is written once, and every adapter that spawns
// anything uses it.
//
// Ollama is the exception and is documented as one: its daemon is externally
// managed and the adapter is an HTTP client. That also removes the injection
// surface entirely on that path, because no caller text ever reaches an argv.
//
// # Honest failure
//
// A missing binary or model produces [ErrProviderUnavailable] carrying the exact
// path that was checked and what to install. No adapter fabricates a transcript,
// no adapter fabricates audio, and no test in this package passes by pretending
// a provider ran.
//
// Where the local runtime is genuinely absent the end-to-end test reports that
// and says what is missing. A green test that proved nothing would be worse than
// a red one.
//
// # Budgets: what is frozen and what is merely observed
//
// Verified against the sources rather than assumed, because this phase must not
// invent an SLA:
//
//   - ABORT / BARGE-IN, 20 ms — FROZEN and applicable. ADR-0011 §5.1, ADR-0004
//     §12, and restated in runtime.Provider's own contract: a provider that
//     ignores cancellation cannot meet it. Asserted here.
//   - LLM TIME-TO-FIRST-TOKEN, 250 ms p50 / 550 ms p95 — FROZEN (ADR-0011 §5.2
//     hop 6, ADR-0006 C1) but set for claude-sonnet-5 over a network. A local
//     model on a laptop CPU is NOT held to it. Measured and reported as an
//     observation against that reference, never asserted.
//   - Any local-model latency target — DOES NOT EXIST, and this phase does not
//     create one.
//
// # ADR-0006 and the local model
//
// ADR-0006 freezes a four-tier ladder on Claude with exact model identifiers,
// and explicitly REJECTED self-hosted open-weight models as an option.
//
// Phase 11E requires a loop that runs with no API key. The two are not in
// conflict because they govern different things: ADR-0006 is production model
// routing, and this is a development loop. The Ollama adapter is
// development-only, is not registrable as an ADR-0006 tier, claims to satisfy
// no clause of it, and nothing here amends it.
package voice
