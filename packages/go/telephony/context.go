package telephony

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Direction is which way a call is going.
type Direction string

// The call directions.
const (
	// DirectionInbound is a call arriving from the carrier. The screening case,
	// and the one this platform exists for.
	DirectionInbound Direction = "inbound"

	// DirectionOutbound is a call the platform originates.
	DirectionOutbound Direction = "outbound"
)

// Valid reports whether the direction is one of the two.
func (d Direction) Valid() bool {
	return d == DirectionInbound || d == DirectionOutbound
}

// String implements fmt.Stringer.
func (d Direction) String() string { return string(d) }

// Channel is the bearer a call arrives on.
//
// Named but not implemented: this runtime does not carry media on any of them.
// The value selects policy and routing upstream and appears as a metric label.
type Channel string

// The channels.
const (
	ChannelVoice  Channel = "voice"
	ChannelVideo  Channel = "video"
	ChannelSIP    Channel = "sip"
	ChannelPSTN   Channel = "pstn"
	ChannelWebRTC Channel = "webrtc"
)

// AllChannels returns every channel.
func AllChannels() []Channel {
	return []Channel{ChannelVoice, ChannelVideo, ChannelSIP, ChannelPSTN, ChannelWebRTC}
}

// Valid reports whether the channel is known.
func (c Channel) Valid() bool {
	for _, k := range AllChannels() {
		if c == k {
			return true
		}
	}
	return false
}

// String implements fmt.Stringer.
func (c Channel) String() string { return string(c) }

// Capability is something a provider can be asked to do.
//
// Declared per provider so the runtime refuses an unsupported operation
// generically rather than carrying a branch per carrier. A carrier that cannot
// transfer omits [CapTransfer]; the runtime returns
// [ErrCapabilityUnsupported] and no code anywhere names that carrier.
type Capability string

// The capabilities.
const (
	CapDial     Capability = "dial"
	CapAnswer   Capability = "answer"
	CapReject   Capability = "reject"
	CapHangup   Capability = "hangup"
	CapHold     Capability = "hold"
	CapMute     Capability = "mute"
	CapTransfer Capability = "transfer"
	CapRecord   Capability = "record"
	CapDTMF     Capability = "dtmf"
)

// AllCapabilities returns every capability.
func AllCapabilities() []Capability {
	return []Capability{CapDial, CapAnswer, CapReject, CapHangup,
		CapHold, CapMute, CapTransfer, CapRecord, CapDTMF}
}

// String implements fmt.Stringer.
func (c Capability) String() string { return string(c) }

// Capabilities is a set.
type Capabilities struct{ set map[Capability]bool }

// NewCapabilities builds a capability set.
func NewCapabilities(caps ...Capability) Capabilities {
	s := Capabilities{set: make(map[Capability]bool, len(caps))}
	for _, c := range caps {
		s.set[c] = true
	}
	return s
}

// Has reports whether the capability is present.
//
// A zero-value Capabilities has nothing, which is the safe default: an
// unconfigured provider supports no optional operation rather than all of them.
func (c Capabilities) Has(cap Capability) bool { return c.set[cap] }

// List returns the capabilities, sorted.
func (c Capabilities) List() []Capability {
	out := make([]Capability, 0, len(c.set))
	for k := range c.set {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Len returns the number of capabilities.
func (c Capabilities) Len() int { return len(c.set) }

// Endpoint identifies one end of a call.
//
// # It does not hold a phone number
//
// The field is [Endpoint.Ref] — an OPAQUE REFERENCE minted upstream, not an
// E.164 number. The telephony runtime does not need the number to manage a
// call's lifecycle, and every place a number could sit here is a place it would
// reach a snapshot, a Kafka event, a metric label and a log line.
//
// Frozen invariant I7 says events carry identifiers, never content. A caller's
// number is the most sensitive identifier the platform touches: it is personal
// data under the DPDP Act, it is the primary key of a person's identity to a
// telecom, and Kafka cannot delete an individual record. Holding it here would
// make an erasure request unsatisfiable for as long as the topic retains.
//
// The resolution from Ref to a number belongs to the identity service, behind
// an access check, on a path that is audited.
type Endpoint struct {
	// Ref is the opaque upstream reference for this party.
	Ref string
	// Display is a coarse, non-identifying label for operators — "unknown",
	// "contact", "blocked". Never a name or a number.
	Display string
	// Country is the ISO-3166 alpha-2 code, retained because routing and
	// regulatory policy are country-scoped. Two characters cannot identify a
	// person.
	Country string
}

// Valid reports whether the endpoint is usable.
func (e Endpoint) Valid() bool { return e.Ref != "" }

// String renders the endpoint without disclosing anything.
func (e Endpoint) String() string {
	if e.Display != "" {
		return fmt.Sprintf("%s(%s)", e.Display, e.Country)
	}
	return "ref:" + e.Ref
}

// CallContext is everything known about a call that does not change.
//
// IMMUTABLE ONCE THE SESSION EXISTS. Mutable per-call state lives in
// [CallSession]; this is the description of what the call IS. Splitting them is
// what lets a snapshot be taken without a lock on the parts that never move,
// and what makes "did the provider change mid-call" a question with an obvious
// answer: no, it cannot.
type CallContext struct {
	// Caller and Callee are the two ends.
	Caller Endpoint
	Callee Endpoint

	// Direction is inbound or outbound.
	Direction Direction

	// Channel is the bearer.
	Channel Channel

	// Provider is who is carrying the call.
	Provider ProviderID

	// Capabilities is what that provider can be asked to do for this call.
	// Carried per call rather than read from the provider registry at use time,
	// so a provider reconfigured mid-call cannot change what an in-flight call
	// is allowed to do.
	Capabilities Capabilities

	// Metadata is free-form provider detail.
	//
	// BOUNDED AND NON-SENSITIVE. Validated for size, because this reaches
	// snapshots and events. A provider adapter must not place a number, a name
	// or any audio reference here — see [CallContext.Validate].
	Metadata map[string]string

	// Tags classify the call for routing and policy: "vip", "unknown-caller",
	// "after-hours". Coarse by construction.
	Tags []string
}

// Validation bounds. Deliberately small: everything here reaches a durable
// event stream, and a bound chosen to be generous is a bound that stops
// bounding anything.
const (
	maxMetadataEntries = 32
	maxMetadataKeyLen  = 64
	maxMetadataValLen  = 256
	maxTags            = 16
	maxTagLen          = 48
)

// Validate checks the context, returning every problem.
func (c CallContext) Validate() error {
	var problems []string

	if !c.Caller.Valid() {
		problems = append(problems, "context: Caller.Ref is required")
	}
	if !c.Callee.Valid() {
		problems = append(problems, "context: Callee.Ref is required")
	}
	if !c.Direction.Valid() {
		problems = append(problems, fmt.Sprintf(
			"context: Direction %q must be inbound or outbound", c.Direction))
	}
	if !c.Channel.Valid() {
		problems = append(problems, fmt.Sprintf("context: Channel %q is unknown", c.Channel))
	}
	if !c.Provider.Valid() {
		problems = append(problems, fmt.Sprintf(
			"context: Provider %q must be lowercase alphanumerics, hyphen or "+
				"underscore — it becomes a metric label and a topic segment", c.Provider))
	}

	if len(c.Metadata) > maxMetadataEntries {
		problems = append(problems, fmt.Sprintf(
			"context: %d metadata entries exceeds the %d cap; this reaches a "+
				"durable event stream", len(c.Metadata), maxMetadataEntries))
	}
	for k, v := range c.Metadata {
		if len(k) > maxMetadataKeyLen {
			problems = append(problems, fmt.Sprintf(
				"context: metadata key %q exceeds %d characters", truncate(k), maxMetadataKeyLen))
		}
		if len(v) > maxMetadataValLen {
			problems = append(problems, fmt.Sprintf(
				"context: metadata value for %q exceeds %d characters", truncate(k), maxMetadataValLen))
		}
	}

	if len(c.Tags) > maxTags {
		problems = append(problems, fmt.Sprintf(
			"context: %d tags exceeds the %d cap", len(c.Tags), maxTags))
	}
	for _, t := range c.Tags {
		if t == "" {
			problems = append(problems, "context: empty tag")
			continue
		}
		if len(t) > maxTagLen {
			problems = append(problems, fmt.Sprintf(
				"context: tag %q exceeds %d characters", truncate(t), maxTagLen))
		}
	}

	if len(problems) > 0 {
		return &ConfigError{Problems: problems}
	}
	return nil
}

// HasTag reports whether the tag is present.
func (c CallContext) HasTag(tag string) bool {
	for _, t := range c.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Clone returns a deep copy.
//
// Maps and slices are copied so a caller mutating what it passed in cannot
// reach into a live session — the class of bug that produces a call whose
// provider appears to change while it is connected.
func (c CallContext) Clone() CallContext {
	out := c
	if c.Metadata != nil {
		out.Metadata = make(map[string]string, len(c.Metadata))
		for k, v := range c.Metadata {
			out.Metadata[k] = v
		}
	}
	out.Tags = append([]string(nil), c.Tags...)
	out.Capabilities = NewCapabilities(c.Capabilities.List()...)
	return out
}

// String renders the context for a log line, disclosing nothing.
func (c CallContext) String() string {
	return fmt.Sprintf("%s %s/%s via %s [%s]",
		c.Direction, c.Caller, c.Callee, c.Provider, strings.Join(c.Tags, ","))
}

func truncate(s string) string {
	const max = 32
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// ---------------------------------------------------------------------------
// Provider port
// ---------------------------------------------------------------------------

// Provider is the carrier-facing port.
//
// NOTHING IN THIS PACKAGE IMPLEMENTS IT. A Twilio, Exotel or SIP adapter
// satisfies this interface in a sibling module, and this runtime never learns
// which one it is talking to.
//
// Four verbs and no telephony vocabulary beyond them. There is deliberately no
// SDP, no codec negotiation, no media description and no DTMF payload in these
// signatures: the moment one appears, this module has acquired an opinion about
// how calls are carried, and the provider-agnostic claim stops being true.
//
// Implementations must be safe for concurrent use and must not block
// indefinitely — the runtime bounds every call with a context deadline, but a
// provider that ignores cancellation holds a goroutine per call.
type Provider interface {
	// ID identifies the provider. Authored and stable.
	ID() ProviderID

	// Capabilities reports what this provider supports.
	Capabilities() Capabilities

	// Dial originates an outbound call and returns once the carrier has
	// accepted the request — NOT once the far end answers.
	Dial(ctx context.Context, c CallContext) error

	// Answer accepts an inbound call.
	Answer(ctx context.Context, id CallID) error

	// Reject declines an inbound call with a bounded reason code.
	Reject(ctx context.Context, id CallID, reason string) error

	// Hangup terminates an established call.
	Hangup(ctx context.Context, id CallID, reason string) error
}
