// Package telephony implements the Aegis AI telephony runtime and call
// lifecycle.
//
// # What this is
//
// Every phone call the platform handles is a [CallSession] managed by a
// [TelephonyRuntime]. This package owns the call's state machine, its identity,
// its context, its lifecycle transitions, the events it publishes, and its
// recovery after a crash.
//
// It is the layer between a carrier and the AI:
//
//	Carrier  →  Provider Adapter  →  Telephony Runtime  →  Conversation Engine
//	                                                    →  Tool Runtime
//	                                                    →  Governance
//	                                                    →  Memory
//
// # What this is not
//
// There is no SIP, no RTP, no WebRTC, no audio, no codec, no speech and no
// model. Not "not yet" — those belong to other layers and their absence here is
// the design.
//
// The distinction that matters: this package knows a call is CONNECTED. It does
// not know what is being said, how the audio is carried, or which codec
// negotiated. A provider adapter tells the runtime that media is up; the
// runtime records that the call reached [StateConnected] and tells everyone who
// needs to know. If this package ever needs to parse an SDP body, something has
// gone wrong upstream of it.
//
// # No implicit transitions
//
// The brief's hardest requirement, and the one that shapes everything else.
//
// A call is in exactly one of fifteen states, and every legal move between them
// is declared in [transitionSpec]. A transition that is not declared is refused
// at run time with [ErrInvalidTransition]; a state that is unreachable, or a
// terminal state with an outgoing edge, is refused at CONSTRUCTION by
// runtime.NewFSM. There is no code path that assigns a state directly.
//
// This is why the package uses the frozen runtime's FSM rather than its own
// switch statement. A switch encodes transitions in the places that perform
// them, so "can a held call be transferred" is answered by reading every call
// site. A declared table answers it in one place, and the answer is testable —
// see TestState_TransitionTableIsComplete.
//
// # Provider agnostic
//
// [Provider] is an interface with four methods and no telephony vocabulary
// beyond dial, answer, reject and hangup. A provider identifier is authored, not
// generated, so it appears in configuration and metric labels as a stable
// string. The runtime never branches on which provider it is talking to: a
// carrier that cannot transfer declares the absence of [CapTransfer] and the
// runtime refuses the transfer generically, rather than carrying a special case
// per carrier.
//
// # Concurrency
//
// A runtime holds thousands of concurrent sessions. The registry is sharded by
// call identifier, so two calls on different shards contend for nothing. Each
// session carries its own lock and its own FSM. Nothing in this package is
// package-level and mutable: two runtimes in one process share no state, which
// is what lets the test suite run parallel and what makes horizontal scaling a
// deployment decision rather than a code change.
//
// # Determinism
//
// Every clock is injected. No timeout is measured against wall time that a test
// cannot control, and no identifier is derived from the current time. A
// lifecycle test advances a [runtime.FakeClock] and observes an expiry, in
// microseconds and without sleeping.
package telephony
