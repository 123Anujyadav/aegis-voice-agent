# ADR-0002: Telephony architecture — carrier-side screening with an on-device pre-filter

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Lead Staff Engineer
- **Consulted:** —
- **Informed:** All engineering, Product, Legal
- **Supersedes:** —

---

## 1. Context

The product screens inbound phone calls with an AI agent that **converses with the
caller** on the subscriber's behalf, then presents the subscriber with a
transcript, an intent classification, and a decision (answer / decline / block).

Everything downstream — media transport, latency budget, cost model, and most of
the regulatory exposure — follows from one question: **where does the audio
live?** This ADR answers it, and it is the load-bearing decision of the entire
platform. ADR-0003 through ADR-0011 are all consequences of it.

The market is India-first: Android, prepaid-dominant, price-sensitive, and served
by four carriers (Jio, Airtel, Vi, BSNL) whose supplementary-service behaviour is
not uniform.

## 2. Problem Statement

Android does not let a third-party application hear a phone call.

`CallScreeningService` (API 29+) is the API whose name suggests otherwise. What it
actually provides is a callback, before the phone rings, carrying the **caller's
number and nothing else**, plus the ability to allow, silence, reject, or send the
call to voicemail. There is no audio. There is no way to answer the call
programmatically and stream the far end. `READ_CALL_LOG`, `ANSWER_PHONE_CALLS`,
and holding the default-dialer role do not change this: the platform's audio
policy reserves the voice-call stream (`VOICE_CALL` / `VOICE_DOWNLINK`) to
privileged system applications, and `AudioRecord` against those sources fails for
an ordinary app.

Google's own Call Screen works because Google ships the Pixel system image. We do
not.

So: **how does an AI agent converse with a caller, on a stock Android handset, when
the handset cannot give us the audio?**

## 3. Constraints

| # | Constraint | Source |
|---|---|---|
| C1 | No live call audio to third-party Android apps, any OEM, any API level | Android platform |
| C2 | No root, no custom ROM, no OEM privileged partnership at launch | Product |
| C3 | Must work on a stock handset from any manufacturer sold in India | Product |
| C4 | Personal data must remain in India | ADR-0012, DPDP Act 2023 |
| C5 | Telephony origination/termination in India requires a licensed operator; we will not hold a licence at launch | DoT/TRAI |
| C6 | Subscriber must keep their existing number — number porting is a non-starter for adoption | Product |
| C7 | Caller-perceived response latency ≤ ~1.1 s p50 | ADR-0011 |
| C8 | Per-screened-minute cost must permit a sub-₹200/month consumer price point | Business |

## 4. Considered Options

1. **On-device screening via `CallScreeningService`** — decide from the number alone
2. **Carrier-side screening via conditional call forwarding** to a platform DID
3. **OEM / carrier privileged integration** — ship inside a system image or IMS stack
4. **VoIP-only** — issue each subscriber an in-app number
5. **Hybrid: on-device pre-filter + carrier-side conversational screening**

## 5. Decision

**Option 5 — a two-stage hybrid.**

**Stage 1 (on-device, free, ~0 ms).** `CallScreeningService` evaluates every
inbound call against the subscriber's contacts, a local allow/deny list, and a
cached reputation verdict. Known-good callers ring through untouched. Known-bad
callers are rejected or silenced without ever leaving the handset — no forwarding,
no server round trip, no per-minute cost, and no personal data in motion.

**Stage 2 (carrier-side, conversational).** Only *unknown* callers reach the AI.
The subscriber's carrier is configured with **conditional call forwarding on
no-reply** (`CFNRy`) to a platform-controlled DID. After a configured ring delay
the carrier forwards the call; our telephony gateway answers, bridges the audio to
the AI pipeline, and the agent converses with the caller. The subscriber sees a
live screening notification and can barge in and take the call at any point.

The forwarding condition is provisioned by the app via the standard GSM
supplementary-service MMI code (`**61*<DID>**<seconds>#`), dialled through
`ACTION_CALL` with the subscriber's explicit consent, and verified afterwards by
interrogation (`*#61#`).

## 6. Why This Option Was Selected

Because Option 1 alone cannot converse and Option 2 alone is too expensive, and
the hybrid gets the strengths of each.

- **It is the only architecture that can converse at all** on a stock handset
  (C1, C2, C3). Options 1 and 4 cannot; Option 3 is not available to us.
- **The pre-filter is what makes the economics work.** In Indian inbound-call
  traffic the large majority of calls are from saved contacts or repeat numbers.
  Screening those on-device means we pay carrier minutes only for the genuinely
  unknown tail — the difference between a viable ₹149/month product and one that
  loses money on every heavy user (C8).
- **It preserves the subscriber's number** (C6). Conditional forwarding is
  invisible to callers; they dial the same number they always have.
- **It degrades safely.** If the platform is unreachable, the carrier's forwarding
  simply fails and the call rings through to the handset as normal. The failure
  mode of our entire backend is "the phone rings" — which is the correct
  failure mode for a telephony product.
- **It contains the regulatory surface.** By terminating on a licensed operator's
  DID (ADR-0003) rather than originating telephony ourselves, we stay a customer
  of a licensed provider rather than an unlicensed operator (C5).

## 7. Trade-offs

**Accepted costs.**

- **Ring delay.** `CFNRy` fires only after the handset has rung for a configured
  interval, typically 5–10 s in 5 s increments. Callers wait longer before the
  agent answers. We set 5 s; below that, legitimate calls the subscriber wanted
  to answer get forwarded.
- **Forwarding is a per-subscriber, per-SIM, carrier-side setting.** It can be
  cleared by the carrier, by a network event, by a SIM swap, or by the subscriber
  dialling `##002#`. It must be continuously verified, not set once.
- **The forwarded leg may be billed to the subscriber** by their carrier on some
  plans. This is a real and poorly-documented cost that varies by circle and
  plan, and it is a support burden we own even though we do not control it.
- **Two legs, two costs.** Every screened call is an inbound leg to our DID plus
  the media path. We pay per minute (ADR-0003).
- **Dual-SIM.** Forwarding is per-SIM. A dual-SIM handset needs the subscriber to
  choose, and the `SubscriptionManager` view of which SIM is which is not
  reliable across OEMs.

**Rejected costs we do not pay.** No number porting. No app-to-app calling
requirement. No dependency on an OEM release cycle.

## 8. Alternatives Rejected

**Option 1 — on-device only.** Rejected on capability. It can block a spam number;
it cannot ask a caller why they are calling. It is a caller-ID product, not this
product. Retained as Stage 1 rather than discarded, because it is genuinely
excellent at the narrow thing it does.

**Option 2 — carrier-side only, no pre-filter.** Rejected on cost and on privacy.
Forwarding *every* unanswered call means paying carrier minutes for calls from the
subscriber's own mother, and routing that audio through our infrastructure for no
product benefit. Data minimisation under DPDP (ADR-0012) argues the same way:
the cheapest and most private call is the one we never see.

**Option 3 — OEM / carrier privileged integration.** The best product, and
unavailable. It requires a system-image partnership with an OEM or an IMS
integration with a carrier — 12–24 month business-development cycles, per-partner
engineering, and no coverage on handsets already in the field. Revisit as a
distribution deal once the product is proven; it is not a launch architecture.

**Option 4 — VoIP-only.** Rejected on adoption. Issuing the subscriber a new
in-app number means every caller must be told the new number. That is the number-
porting problem wearing a different hat, and it makes the product useless for
exactly the inbound calls it is meant to screen: the ones from people who only
have the old number.

## 9. Operational Impact

- **Forwarding state becomes a first-class monitored resource.** `contacts-sync`
  and a new forwarding-health job must periodically interrogate and re-assert the
  MMI configuration. A subscriber whose forwarding silently lapsed is a
  subscriber for whom the product does nothing while still billing.
- **DID pool management.** A pool of inbound numbers, allocated per region, with
  capacity headroom and a reclamation policy (ADR-0003).
- **Carrier-specific behaviour is a support matrix, not a footnote.** Jio, Airtel,
  Vi and BSNL differ in MMI acknowledgement format, ring-delay granularity, and
  VoLTE forwarding behaviour. `docs/runbooks/carrier-matrix.md` is a launch
  blocker.
- **New alerts:** forwarding-verification failure rate, ring-to-answer latency,
  DID pool exhaustion, per-carrier answer-success rate.

## 10. Security Impact

- **Our DIDs are publicly dialable.** Anyone can call them directly, bypassing the
  subscriber entirely. The gateway must authenticate calls by the SIP diversion
  header identifying the forwarding subscriber, and must refuse to start an AI
  session for a call with no valid diversion. Treat an undiverted inbound call as
  hostile.
- **Toll fraud.** A publicly-dialable DID attached to an expensive AI pipeline is
  a resource-exhaustion target. Per-DID and per-source-number rate limits, plus
  call admission control, are mandatory in `telephony-gateway` from day one.
- **MMI codes are a dangerous primitive.** `ACTION_CALL` with a `tel:` URI can
  reconfigure the subscriber's telephony. The dialled string must be constructed
  from a validated DID and never from anything caller-controlled or
  server-supplied without signature verification.
- **Caller audio is `SENSITIVE`** under the classification in
  `contracts/proto/callscreen/common/v1/annotations.proto`. It transits our
  infrastructure and is subject to ADR-0012 in full.

## 11. Cost Impact

Per screened minute we pay: inbound DID termination (ADR-0003) + STT (ADR-0005) +
LLM (ADR-0006) + TTS (ADR-0007) + egress. Telephony is the floor cost and the one
we control least.

The pre-filter is the single largest cost lever in the product. Every percentage
point of calls resolved on-device is a percentage point of carrier minutes,
inference, and storage not spent. This is why Stage 1 is architecture and not an
optimisation to add later.

Secondary lever: **screening duration**. A 20 s screening interaction costs half a
40 s one across every metered component simultaneously. The agent's brevity is a
cost control, not only a UX preference.

## 12. Performance Impact

Carrier-side screening adds two hops that on-device screening would not have:
handset → carrier → our DID on ingress, and the reverse on egress. ADR-0011
budgets these at 25 ms p50 / 60 ms p95 each way within India.

The larger perceived cost is the **ring delay** before forwarding triggers — 5 s
during which the caller hears ringback and nothing happens. This is not in the
response-latency budget (it precedes the conversation) but it is in the caller's
experience, and it is the reason the agent's first utterance must be immediate
once the leg is answered.

## 13. Scalability Impact

- Screening sessions are **long-lived and stateful** — tens of seconds each,
  holding a media path. This is a fundamentally different scaling shape from
  request/response HTTP, and it is why `telephony-gateway`, `media-relay` and
  `session-orchestrator` are separate Go services sized on concurrent sessions
  rather than requests per second.
- **Concurrency, not throughput, is the capacity unit.** Plan against peak
  concurrent screened calls.
- Traffic is sharply diurnal and correlates with telemarketing hours; peak-to-mean
  is high. Media capacity must scale on concurrency with headroom, because a
  screening session cannot be queued — a call either connects now or fails.
- DID pool size scales with subscriber count and must be provisioned ahead of
  growth; number provisioning has lead time and is not elastic.

## 14. Migration Strategy

There is nothing to migrate from. The strategy that matters is the path *out*:

1. **Phase 1 (launch).** Hybrid as described. Provider-abstracted at the
   `telephony-gateway` boundary (ADR-0003).
2. **Phase 2.** If OEM or carrier partnership becomes available, on-device or
   IMS-side screening becomes a *third* stage in the same pipeline. The
   `session-orchestrator` contract is deliberately transport-agnostic so that an
   audio source change does not reach the AI tier.
3. **Rollback.** Disabling forwarding for a subscriber restores stock behaviour
   completely. The rollback path is a single MMI code (`##61#`) and must be
   exposed prominently in the app — not buried in settings.

## 15. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Carrier changes CFNRy behaviour or blocks forwarding to our DID range | Medium | Critical | Multi-provider DID sourcing (ADR-0003); per-carrier synthetic canary calls; commercial relationship with the licensed provider |
| Forwarding silently lapses | High | High | Continuous interrogation; in-app health indicator; re-assert on SIM/network change |
| Subscriber billed for the forwarded leg | Medium | High | Per-circle documentation at onboarding; explicit consent screen naming the possibility |
| Toll fraud against public DIDs | High | High | Admission control, per-DID rate limits, diversion-header validation, anomaly alerting |
| Regulatory reinterpretation of AI answering on a subscriber's behalf | Low | Critical | Announce screening to the caller (ADR-0012); licensed provider in the path; legal review before launch |
| OEM background-execution policy kills the screening service | High | Medium | `CallScreeningService` is system-bound and exempt, but OEM aggressiveness varies — per-OEM testing matrix |
| Dual-SIM misconfiguration | Medium | Medium | Explicit SIM selection in onboarding; verify against the subscription actually forwarded |

## 16. Future Review Trigger

Revisit this ADR when **any** of the following becomes true:

- An OEM or carrier partnership offering privileged audio access is signed
- Android exposes a supported API for third-party call audio
- Measured carrier-minute cost exceeds **35%** of blended revenue per subscriber
- Forwarding-verification failure rate exceeds **2%** of active subscribers over a
  rolling 7 days
- Expansion to a market where conditional call forwarding is unavailable or where
  terminating on a third-party DID is not permitted

## 17. References

- Phase 1 Repository Foundation, assumption A1
- ADR-0003 (carrier selection), ADR-0004 (media transport), ADR-0011 (latency
  budget), ADR-0012 (privacy)
- 3GPP TS 22.082 — Call Forwarding supplementary services
- `docs/runbooks/carrier-matrix.md` — per-carrier behaviour (to be written)
