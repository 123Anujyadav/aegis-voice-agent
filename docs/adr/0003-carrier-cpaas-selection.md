# ADR-0003: Carrier / CPaaS selection — Exotel primary, Plivo secondary

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Lead Staff Engineer
- **Consulted:** Legal (regulatory posture)
- **Informed:** All engineering, Finance
- **Depends on:** ADR-0002

---

## 1. Context

ADR-0002 commits us to carrier-side screening: subscribers conditionally forward
unknown calls to a DID we control, and we answer that leg and bridge its audio to
the AI pipeline. That makes the telephony provider a **critical-path dependency
with no graceful degradation** — if they are down, the product does not screen
calls.

We therefore need a provider that can (a) supply Indian DIDs in volume, (b)
terminate inbound PSTN calls to our infrastructure, (c) stream bidirectional
media in real time, and (d) do all of it under an Indian telecom licence we do
not hold.

## 2. Problem Statement

Which telephony provider (or providers) do we build on, and how do we avoid the
dependency becoming an existential single point of failure or a regulatory
liability?

The regulatory dimension is not secondary. In India, PSTN origination and
termination require a Unified Licence from the Department of Telecommunications.
Interconnecting PSTN with VoIP for termination without a licence is the "toll
bypass" prohibition, and it is enforced. **We must be a customer of a licensed
provider, not a de-facto unlicensed operator.** A provider that cannot show us
its licence is not a candidate regardless of price or API quality.

## 3. Constraints

| # | Constraint | Source |
|---|---|---|
| C1 | Provider must hold the appropriate Indian telecom licence and be able to evidence it | DoT/TRAI, ADR-0002 C5 |
| C2 | Indian local DIDs (not international) in volume, with a provisioning API | ADR-0002 |
| C3 | Bidirectional real-time media streaming, not just recording or IVR DTMF | ADR-0004 |
| C4 | Media and signalling terminate in India; call audio must not egress | ADR-0012 |
| C5 | Ingress leg latency within India ≤ 25 ms p50 / 60 ms p95 | ADR-0011 |
| C6 | Per-minute inbound cost compatible with a sub-₹200/month price point | ADR-0002 C8 |
| C7 | Must not become unswappable — a second provider must be reachable without a rewrite | Risk |
| C8 | KYC/CAF obligations on DID allocation must be satisfiable at our scale | DoT |

## 4. Considered Options

1. **Exotel** — Indian CPaaS, licensed, India-native infrastructure
2. **Plivo** — global CPaaS with substantial Indian presence and entity
3. **Twilio** — the global default
4. **Telnyx** — owns its network; strong US/EU, thin India
5. **Direct SIP trunk from a licensed Indian operator** (Airtel/Tata/Jio enterprise)
6. **Knowlarity / Ozonetel** — Indian incumbents in the cloud-telephony segment

## 5. Decision

**Primary: Exotel. Secondary: Plivo. Both behind a provider-abstraction boundary
in `telephony-gateway`, with a direct SIP trunk (Option 5) as the documented
Phase-3 exit.**

Concretely:

- Exotel carries production traffic and holds the primary DID pool.
- Plivo is provisioned, contracted, and integrated from launch — not as a
  paper contingency but as a **live secondary carrying a small continuous share
  of real traffic**, so that failover is exercised rather than theoretical.
- `telephony-gateway` defines a `CarrierProvider` port. No provider SDK type
  crosses that boundary into `session-orchestrator` or the AI tier.
- Provider selection is per-DID and runtime-switchable, so failover is a routing
  change and not a deploy.

## 6. Why This Option Was Selected

**Exotel, because of C1 and C4 before anything else.** It is an Indian company
operating Indian infrastructure under Indian licences, with DID provisioning and
KYC workflows built for the Indian regulatory environment rather than adapted to
it. For a product whose entire architecture depends on lawful inbound PSTN
termination in India, that alignment is worth more than a better API.

Supporting reasons:

- **India-domestic media path** satisfies C4 without contractual gymnastics. Audio
  never needs to leave the country to reach us.
- **Local latency** (C5). Domestic PoPs mean the ingress hop is a domestic hop.
- **DID supply and KYC at scale** (C2, C8) is a solved workflow for them and a
  novel one for a foreign provider.
- **Domestic commercial relationship.** When a carrier changes forwarding
  behaviour — the top risk in ADR-0002 — a provider with domestic carrier
  relationships can escalate. A foreign provider generally cannot.

**Plivo as secondary** because it has genuine Indian operations and entity
presence, giving a failover target that is not merely a different API but a
*different regulatory and network path*. A secondary that shares the primary's
upstream carrier is not a secondary.

## 7. Trade-offs

**Accepted.**

- **Developer experience is weaker than Twilio's.** Documentation, SDK quality,
  and error semantics are all a step down. We absorb this behind the provider
  port and compensate with our own SIPp harness (`tools/sipp-harness`) rather
  than relying on vendor tooling.
- **Dual integration cost from day one.** Two providers means two integrations,
  two contracts, two sets of quirks, and continuous traffic on the secondary.
  This is deliberate spend on an availability property.
- **Per-minute pricing is not the cheapest available.** Telnyx and direct trunking
  are cheaper per minute. We pay a premium for licence alignment and domestic
  support.
- **Vendor-shaped media API.** Streaming media over a provider WebSocket (ADR-0004)
  binds us to their framing and codec choices more tightly than raw SIP would.

**Explicitly not accepted:** we do not accept single-provider dependency, and we
do not accept a provider whose licence position is unclear.

## 8. Alternatives Rejected

**Twilio.** Rejected as primary despite being the best engineering experience of
the group. Indian local-number provisioning for a foreign-registered entity is
constrained by DoT rules, the regulatory posture for inbound PSTN termination in
India is materially more complicated than the API surface suggests, and the media
path is more likely to leave the country (C4). Retained as the **likely primary
for the first international market**, where these objections invert.

**Telnyx.** Rejected on India coverage. Owning your own network is a genuine
advantage for cost and latency — in the geographies where they own it. India is
not one of them, and the whole product is India-first.

**Knowlarity / Ozonetel.** Rejected on product fit. Both are strong in the
IVR/contact-centre segment; neither offers the low-latency bidirectional media
streaming primitive (C3) that a conversational agent needs. They are built to
play prompts and collect DTMF, which is a different product.

**Direct SIP trunk (Option 5).** Rejected *for launch*, adopted as the exit path.
It is the cheapest per minute and gives the most control over media and codecs.
It also requires operating our own SBC and media servers, negotiating enterprise
trunk contracts, meeting the operator's technical conformance requirements, and
taking on lawful-interception obligations — months of work and an ops burden we
cannot carry at launch. **Revisit at the volume trigger in §16**, where the
per-minute saving begins to exceed the operating cost.

## 9. Operational Impact

- **DID pool becomes an operated resource** with its own lifecycle: provisioning
  lead time, KYC/CAF paperwork, per-region allocation, reclamation of numbers from
  churned subscribers, and a quarantine period before reuse (a recycled DID
  receives the previous holder's calls).
- **Two providers means two runbooks** and a documented, rehearsed failover
  procedure. Failover that has never been executed does not work.
- **Provider status is a monitored dependency.** Per-provider answer-success rate,
  media-stream establishment latency, and mid-call drop rate, alerted separately
  — a provider degrading is not the same signal as our services degrading.
- **Synthetic canary calls** through each provider, on each carrier, continuously.
  This is the only way to detect a carrier-side forwarding change before
  subscribers do.
- **Contractual:** SLA, support escalation path, and a data-processing agreement
  naming India-only processing (ADR-0012).

## 10. Security Impact

- **The provider is inside our trust boundary for call audio.** They carry
  `SENSITIVE`-classified data. This requires a DPA, encryption in transit on the
  media leg (ADR-0004), and a defined breach-notification path that feeds our own
  DPDP obligations.
- **Provider credentials are high-value.** They can provision numbers and place
  outbound calls — a compromised key is both a financial and a reputational
  incident. Stored in the secret manager, rotated ≤30 days per `SECURITY.md`,
  never in an image or repo.
- **Inbound webhook authentication is mandatory.** Provider callbacks into our
  edge must be signature-verified. An unauthenticated webhook that starts an AI
  session is a free-inference vector.
- **Diversion-header trust.** We authenticate screened calls by the SIP diversion
  header (ADR-0002 §10). That header is asserted by the provider; we must confirm
  contractually and by test that they do not pass through a caller-supplied value.

## 11. Cost Impact

Telephony is the **floor cost** of a screened call and the least compressible.
Cost per screened call is dominated by inbound per-minute termination × screening
duration, plus a per-DID monthly rental that scales with subscriber count
regardless of usage.

Two consequences for the business model:

1. **DID rental is a fixed cost per subscriber.** It is incurred whether or not
   the subscriber receives a single screened call, which sets a floor under the
   viable subscription price.
2. **Duration is the lever.** Per-minute billing (typically rounded up per unit)
   means a screening interaction that resolves in 20 s versus 40 s roughly halves
   the telephony cost of that call. This is the same lever identified in ADR-0002
   §11 and reinforced in ADR-0006 — brevity is an architectural cost control.

The Phase-3 direct-trunk migration (§14) is where the per-minute component drops
materially; until then, we optimise duration and the on-device pre-filter hit rate.

## 12. Performance Impact

- Ingress leg budgeted at **25 ms p50 / 60 ms p95** (ADR-0011), predicated on a
  domestic media path. A provider that hairpins audio through Singapore or the US
  breaks the latency budget outright — this is a **hard acceptance criterion**,
  verified by measurement before launch, not a preference.
- Media-stream establishment time (answer → first audio frame available to us)
  is on the critical path for the agent's opening utterance and is measured
  per-provider.
- Codec selection is largely the provider's (ADR-0004) and directly determines
  ASR accuracy (ADR-0005).

## 13. Scalability Impact

- **Concurrent-channel limits are the binding constraint**, not requests per
  second. Provider channel capacity must be contracted ahead of peak and has lead
  time.
- **DID provisioning is not elastic.** Numbers require KYC and allocation; a
  growth spike cannot be absorbed by autoscaling. Pool headroom is a planned
  quantity with an alerting threshold.
- Multi-provider routing gives horizontal headroom: when the primary's contracted
  channels saturate, overflow routes to the secondary. This is the same mechanism
  as failover, which is why the secondary must carry continuous traffic.

## 14. Migration Strategy

**The provider port is the migration strategy.** `telephony-gateway` exposes a
`CarrierProvider` interface covering DID provisioning, inbound call notification,
answer/reject, media attach/detach, and teardown. Provider SDKs live behind it and
nowhere else.

- **Phase 1 (launch).** Exotel primary, Plivo secondary, both live.
- **Phase 2.** Traffic shifting between providers by DID cohort, exercised
  routinely. Adding a third provider is one adapter implementation.
- **Phase 3 (exit).** Direct SIP trunk with a licensed operator, using our own
  SBC. This is an additional `CarrierProvider` implementation plus real media
  infrastructure (ADR-0004 §14). Migration is per-DID-cohort, gradual, and
  reversible.
- **Rollback** at every phase is a routing-table change, not a deployment.

## 15. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Primary provider outage | Medium | Critical | Live secondary carrying continuous traffic; per-DID runtime switching |
| Provider changes media API or deprecates streaming | Medium | High | Provider port; secondary already integrated; contractual notice period |
| Regulatory action against the provider | Low | Critical | Licence evidence at contracting; secondary on a different regulatory path; direct-trunk exit documented |
| Per-minute price increase at renewal | Medium | High | Dual-sourcing creates competitive tension; direct-trunk exit is credible leverage |
| DID pool exhaustion during growth | Medium | High | Headroom alerting; provisioning lead time in the capacity plan; secondary pool |
| Recycled DID receives prior holder's calls | High | Medium | Quarantine period before reuse; reject undiverted inbound (ADR-0002 §10) |
| Media path egresses India | Low | Critical | Contractual requirement + measured verification as a launch acceptance test |
| Provider becomes unswappable through abstraction leak | Medium | High | Provider types banned outside the port; enforced by the import-boundary check in CI |

## 16. Future Review Trigger

Revisit when **any** holds:

- Monthly telephony spend exceeds **₹15 lakh**, at which point direct-trunk
  economics plausibly beat CPaaS including operating cost
- Primary provider availability falls below **99.9%** over a rolling 30 days
- Sustained concurrent-channel utilisation exceeds **70%** of contracted capacity
- Expansion to a second country (re-run this decision for that market; the answer
  is probably Twilio)
- Any provider is unable to evidence its licence position on request

## 17. References

- ADR-0002 (telephony architecture), ADR-0004 (media transport), ADR-0011
  (latency budget), ADR-0012 (privacy and residency)
- `services/go/telephony-gateway` — `CarrierProvider` port
- `tools/sipp-harness` — provider-independent conformance and load testing
- DoT Unified Licence framework; TRAI TCCCPR 2018
