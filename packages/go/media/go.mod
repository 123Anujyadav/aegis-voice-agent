// =============================================================================
// packages/go/media — the Enterprise Media Streaming Engine.
//
// TWO FIRST-PARTY DEPENDENCIES, NO THIRD-PARTY ONES.
//
// This module requires packages/go/runtime (Phase 10A, frozen) and
// packages/go/metrics (Phase 10.5). Both are dependency-free, so the transitive
// closure of this module is the Go standard library — the property every module
// in the AI plane has held since Phase 10A.
//
// THERE IS NO MEDIA LIBRARY HERE, AND THAT IS THE POINT.
//
// No Pion, Janus, mediasoup, LiveKit, Twilio Media, Agora, Daily or Jitsi SDK.
// No RTP stack, no WebRTC stack, no codec, no resampler, no DSP library. This
// module moves audio FRAMES between a producer and a consumer, correctly and
// under backpressure, and knows nothing about how those frames reached the
// process or what is encoded in them.
//
// It notably does not require packages/go/telephony either. A media stream is
// created FOR a call, but the streaming engine does not need to know what a call
// is — it needs a stream identifier and a frame source. Coupling the two would
// make the media engine untestable without a telephony runtime, and would put
// call-lifecycle vocabulary into a buffer.
//
// WHAT THE RUNTIME DEPENDENCY BUYS. Clock (so every deadline, every drift
// measurement and every jitter window is testable without sleeping) and FSM (so
// the stream state machine is declared and validated rather than assembled from
// booleans). Reimplementing either would produce a second, subtly different
// version of a solved problem.
// =============================================================================

module github.com/callscreen/callscreen-platform/packages/go/media

go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0
)

// The module paths above are not fetchable remotes: this monorepo is private
// and unpublished. The relative replaces also keep this module buildable
// standalone with GOWORK=off, which CI relies on to prove the go.mod is
// self-sufficient rather than leaning on the workspace.
replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../runtime

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../metrics
