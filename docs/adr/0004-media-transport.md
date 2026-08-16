# ADR-0004: Media transport — provider WebSocket streaming at the carrier leg, WebRTC at the app leg

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Lead Staff Engineer
- **Consulted:** —
- **Informed:** All engineering
- **Depends on:** ADR-0002, ADR-0003

---

## 1. Context

ADR-0002 established that the AI converses with the caller over a forwarded PSTN
leg. ADR-0003 selected Exotel and Plivo to carry that leg. This ADR decides how
audio actually moves between the carrier and our AI pipeline, and how the
subscriber listens in or takes over.

There are two distinct media paths, and conflating them is the most common design
error in this class of product:

- **The carrier leg** — caller ↔ PSTN ↔ provider ↔ our `media-relay`. Narrowband,
  lossy, latency-critical, and not under our control.
- **The app leg** — `media-relay` ↔ subscriber's Android handset, for live-listen
  and barge-in takeover. Wideband, over mobile data, and entirely under our
  control.

They have different codecs, different failure modes, and different transports.

## 2. Problem Statement

How does bidirectional audio flow between the PSTN and the AI pipeline with
sufficient fidelity for accurate ASR and low enough latency to sustain natural
turn-taking, and how does the subscriber monitor and seize a live screening?

The fidelity question is not cosmetic. Indian PSTN delivers **8 kHz narrowband**,
typically G.711 A-law on fixed interconnects and AMR-NB over VoLTE. That is the
audio ASR must work on (ADR-0005), and no downstream cleverness recovers
information the codec discarded. The transport decision therefore sets a hard
ceiling on recognition accuracy.

## 3. Constraints

| # | Constraint | Source |
|---|---|---|
| C1 | Carrier-leg audio is 8 kHz narrowband, codec chosen by the network | PSTN/VoLTE |
| C2 | Bidirectional streaming with sub-frame-level control — we must be able to interrupt our own playback mid-utterance | Barge-in, ADR-0011 |
| C3 | Media relay hop budgeted at 15 ms p50 / 35 ms p95 each way | ADR-0011 |
| C4 | Call audio must not egress India | ADR-0012 |
| C5 | Must not require operating an SBC or media server at launch | ADR-0003 §8 |
| C6 | App leg must traverse Indian mobile NAT and carrier middleboxes reliably | Product |
| C7 | Transport must be swappable without touching the AI tier | ADR-0003 C7 |

## 4. Considered Options

1. **Provider WebSocket media streaming** — Exotel/Plivo stream raw PCM/µ-law frames over a WebSocket to our endpoint
2. **SIP + RTP with our own media server** (FreeSWITCH / Asterisk / Drachtio + rtpengine)
3. **WebRTC end-to-end**, with the provider bridging PSTN↔WebRTC
4. **Provider "media fork" / recording callback** — receive audio after the fact
5. **Hybrid: (1) for the carrier leg, WebRTC for the app leg**

## 5. Decision

**Option 5.**

**Carrier leg — provider WebSocket media streaming.** On call answer, the
provider opens a WebSocket to `media-relay` and streams inbound caller audio as
base64 frames (8 kHz, µ-law or L16 depending on provider); we stream synthesised
audio back on the same socket. Frame cadence is 20 ms.

**App leg — WebRTC.** When the subscriber opens the live-screening view,
`media-relay` establishes a WebRTC session to the handset carrying a mixed
downstream (caller + agent) and, on takeover, an upstream from the subscriber's
microphone. Signalling rides the existing authenticated connection to `edge-api`;
media uses SRTP with ICE/TURN.

**`media-relay` owns an internal audio bus** — a codec- and transport-neutral
frame representation (L16 mono, 16 kHz internally, with resampling at both
edges). Neither `session-orchestrator` nor the AI tier ever sees a provider frame
format, a WebSocket, or an RTP packet.

## 6. Why This Option Was Selected

**For the carrier leg, because it is the only option that satisfies C5 without
sacrificing C2.** Provider WebSocket streaming gives us genuine bidirectional
frame-level access — the thing barge-in requires — while the provider retains the
SBC, the codec negotiation, the NAT traversal, and the carrier interconnect. We
get the primitive we need and none of the operational burden we cannot yet carry.

- **Barge-in works** (C2). Because we push frames rather than handing over an
  audio file, we can stop mid-utterance the instant VAD detects the caller
  speaking. A prompt-playback API cannot do this, and barge-in is the single
  largest contributor to a conversation feeling natural.
- **It is trivially swappable** (C7). A WebSocket carrying PCM frames is close to
  the minimum viable coupling to a provider. The adapter is small; the internal
  bus is the real interface.
- **Domestic path** (C4) follows from ADR-0003's provider choice.

**For the app leg, WebRTC, because C6 is a real problem.** Indian mobile networks
are heavily NATed with variable middlebox behaviour. WebRTC's ICE, STUN and TURN
machinery exists precisely to solve this, and re-inventing it over a raw socket
would be a months-long detour to a worse result. WebRTC also gives us
Opus — wideband, so the subscriber hears the agent clearly even though the caller
leg is narrowband — plus echo cancellation and jitter buffering on the handset for
free.

## 7. Trade-offs

**Accepted.**

- **Codec is the provider's choice, not ours** (C1). We receive whatever the
  carrier negotiated, transcoded by the provider. We cannot request wideband from
  the PSTN because the PSTN does not have it. ASR accuracy is capped accordingly
  (ADR-0005 §7).
- **WebSocket over TCP means head-of-line blocking.** A retransmission stalls the
  frame stream where RTP over UDP would simply drop and continue. For a 20 ms
  frame cadence on a domestic path this is tolerable; on a degraded path it
  manifests as a latency spike rather than a glitch, which is arguably worse for
  turn-taking. Mitigated by jitter-buffer tuning and by treating stall duration
  as a first-class metric.
- **Two transports, two codebases** in `media-relay`. Deliberate: they have
  genuinely different requirements.
- **No control over PSTN-side packet loss concealment.** The provider's SBC does
  it, and quality varies.

## 8. Alternatives Rejected

**Option 2 — SIP + RTP with our own media server.** The technically superior
answer and the eventual destination. Rejected for launch on C5: it requires
operating an SBC, handling SIP signalling and NAT traversal against four carriers,
managing RTP port ranges at scale, and meeting an operator's conformance
requirements. That is a dedicated team's work. Retained as the Phase-3 exit
alongside the direct SIP trunk in ADR-0003 §14 — the two migrate together, because
owning the trunk without owning the media makes no sense.

**Option 3 — WebRTC end-to-end.** Rejected for the carrier leg. The provider
would bridge PSTN↔WebRTC on our behalf, adding a transcode and a hop for no gain:
the audio is still 8 kHz narrowband at origin, so we pay WebRTC's setup latency
and complexity to carry the same information. WebRTC earns its keep on the app
leg, where the network is hostile and the audio is genuinely wideband.

**Option 4 — media fork / recording callback.** Rejected outright. Post-hoc audio
cannot support a conversation. Listed only because provider documentation
frequently presents it as the media integration, and it is not.

## 9. Operational Impact

- **`media-relay` is a stateful, long-lived-connection service.** It holds a
  WebSocket and possibly a WebRTC peer connection per active screening for the
  duration of the call. Deployment must drain rather than terminate: killing a
  pod drops live calls. This is why the graceful-shutdown lifecycle in
  `packages/go/platform` marks readiness false *before* closing listeners.
- **TURN infrastructure** must be operated for the app leg, in-region, with its
  own capacity planning and credential rotation.
- **New golden signals:** frame arrival jitter, WebSocket stall duration, frames
  dropped, transcode error rate, WebRTC ICE failure rate, TURN relay ratio.
- **`tools/sipp-harness` and `tools/audio-fixtures` are load-bearing here.** Media
  behaviour cannot be tested by unit tests; it needs synthetic calls carrying
  known audio under injected loss and jitter.

## 10. Security Impact

- **Carrier leg WebSocket must be WSS with mutual authentication.** The provider
  authenticates to us; we verify. An unauthenticated media socket is a free
  inference channel and an audio-injection vector.
- **App leg uses SRTP with DTLS key exchange** — mandatory in WebRTC, and the
  reason the subscriber's live-listen stream is not interceptable on a hostile
  Wi-Fi network.
- **`media-relay` handles `SENSITIVE` data continuously.** Audio frames are never
  written to disk in the relay. Recording, where consented, is performed by
  `transcript-service` against a separate policy-gated path (ADR-0012), not as a
  side effect of relaying.
- **TURN credentials must be short-lived and per-session.** Long-lived TURN
  credentials are an open relay.
- **Resource exhaustion.** A media socket is expensive. Admission control at
  `telephony-gateway` (ADR-0002 §10) must bound concurrent sessions before
  `media-relay` is reached.

## 11. Cost Impact

Media transport itself is bandwidth and compute rather than per-minute vendor
billing, so it is a smaller line than telephony (ADR-0003 §11) or inference
(ADR-0006 §11) — but it is not free:

- **Sustained bandwidth per session** is modest (8 kHz narrowband) on the carrier
  leg, materially higher on the app leg when live-listen is active. Live-listen
  is therefore an occasionally-used feature by design, not an always-on one.
- **TURN relay traffic is the expensive tail.** Sessions that fail to establish a
  direct path relay all media through our TURN servers. Relay ratio is a cost
  metric, not merely a quality metric.
- **`media-relay` is memory- and connection-bound rather than CPU-bound**, which
  makes it cheap to scale horizontally but sensitive to per-instance connection
  limits.

## 12. Performance Impact

Budgeted at **15 ms p50 / 35 ms p95** each way (ADR-0011), covering jitter buffer,
resample, and framing. The components:

- **Jitter buffer** is the dominant and deliberately-chosen cost. Too small and
  frames arrive late and are discarded; too large and we add pure latency. Target
  a small adaptive buffer and treat its depth as a tuned parameter per provider.
- **Resampling** 8 kHz ↔ 16 kHz at both edges is cheap but not free, and must not
  allocate per frame.
- **The barge-in path is the latency-critical one.** From VAD detecting caller
  speech to our outbound frame stream stopping must be a single frame interval.
  Any queue between the VAD and the output is added interruption latency.

## 13. Scalability Impact

- **Capacity unit is concurrent sessions**, consistent with ADR-0002 §13. Each
  session holds at minimum one WebSocket; with live-listen, a WebRTC peer
  connection as well.
- **Connections, not requests, saturate the service.** Scale on connection count
  and file-descriptor headroom.
- **Sessions cannot be shed gracefully under load** — dropping a live call is a
  visible product failure. Backpressure therefore has to be applied at admission
  (refuse the new call) rather than at the media layer.
- Horizontal scaling is straightforward because sessions are independent; the
  constraint is that a session is pinned to one relay instance for its lifetime,
  so rebalancing only affects new calls.

## 14. Migration Strategy

**The internal audio bus is the migration strategy.** `media-relay` converts every
external representation to the internal frame format at its edge. Changing
transports is an edge change.

- **Phase 1 (launch).** Provider WebSocket + WebRTC as decided.
- **Phase 2.** Second provider's WebSocket adapter (ADR-0003) — an additional edge
  implementation, no change to the bus or above.
- **Phase 3 (exit).** SIP/RTP against our own SBC, migrating in lockstep with the
  direct trunk in ADR-0003 §14. This adds an RTP edge implementation and real
  media infrastructure; the AI tier is unaffected.
- **Rollback** at every phase is per-DID routing, as in ADR-0003.

## 15. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| WebSocket head-of-line stall degrades turn-taking | Medium | High | Stall-duration metric with alerting; adaptive jitter buffer; RTP exit path |
| Provider changes frame format or cadence | Medium | High | Edge adapter isolates it; secondary provider already integrated; contractual notice |
| Narrowband audio caps ASR accuracy below product requirement | Medium | Critical | Codec-matched ASR model (ADR-0005); golden audio fixtures; measured before launch |
| WebRTC fails to establish on Indian mobile networks | Medium | Medium | TURN fallback; relay-ratio monitoring; live-listen degrades to transcript-only |
| Pod termination drops live calls | High if unmitigated | High | Drain-before-close lifecycle; long termination grace; PDBs sized for concurrency |
| TURN abused as an open relay | Low | High | Short-lived per-session credentials; egress restrictions |
| Barge-in latency exceeds one frame interval | Medium | High | No queueing between VAD and output; measured as a dedicated SLI |

## 16. Future Review Trigger

Revisit when **any** holds:

- WebSocket stall duration p99 exceeds **60 ms** sustained
- Direct SIP trunk adopted per ADR-0003 §16 (the two migrate together)
- Wideband PSTN interconnect (G.722 / EVS) becomes generally available on Indian
  carrier forwarding paths — this would raise the ASR accuracy ceiling and
  justifies revisiting codec handling end to end
- TURN relay ratio exceeds **25%** of app-leg sessions
- Live-listen adoption exceeds **20%** of screened calls, changing the bandwidth
  cost profile materially

## 17. References

- ADR-0002 (telephony architecture), ADR-0003 (carrier selection), ADR-0005
  (streaming STT), ADR-0011 (latency budget), ADR-0012 (privacy)
- `services/go/media-relay` — internal audio bus and transport edges
- `tools/sipp-harness`, `tools/audio-fixtures`
- RFC 3550 (RTP), RFC 8825 (WebRTC overview), RFC 8656 (TURN)
