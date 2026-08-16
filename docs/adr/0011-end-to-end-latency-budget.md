# ADR-0011: End-to-end latency budget

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Lead Staff Engineer
- **Consulted:** —
- **Informed:** All engineering
- **Depends on:** ADR-0002, ADR-0004, ADR-0005, ADR-0006, ADR-0007

---

## 1. Context

This is the most important number in the system, and it is the reason this ADR
exists as a first-class decision rather than as a performance note appended to
another document.

Without a **written, per-hop, per-owner budget**, every team optimises locally,
every component is individually "fast enough", and the product is still slow —
because nobody owns the sum. Phase 1 flagged this explicitly: a latency budget
that exists only as an aspiration is not a constraint, and the components in
ADRs 0004 through 0007 were each specified against an allocation that this
document is the source of.

Every number quoted in those ADRs — 25 ms for the carrier hop, 120 ms for ASR
finalisation, 250 ms for LLM first token, 90 ms for TTS first byte — is
**allocated here**. They are not independent vendor benchmarks that happen to be
compatible; they are a budget divided up in advance.

## 2. Problem Statement

**How long may the caller wait between finishing their sentence and hearing the
agent begin to reply, and how is that total divided among the eleven hops that
produce it?**

The relevant metric is **response latency**: acoustic end-of-caller-speech →
first audio of the agent's reply reaching the caller's ear. Not
time-to-first-token. Not server-side processing time. The thing the human
experiences.

The human factors set the target, not the engineering:

- Natural conversational turn-taking gap between humans is roughly **200 ms**.
- Up to about **800 ms** reads as a thoughtful pause.
- Beyond roughly **1.5 s** the caller assumes the line has dropped, and starts
  talking over the agent or hangs up.
- Beyond **2.5 s** the interaction has failed regardless of what happens next.

We cannot reach 200 ms over the PSTN with a cloud LLM in the loop. We can reach
"thoughtful", and that is the target.

## 3. Constraints

| # | Constraint | Source |
|---|---|---|
| C1 | The caller is on narrowband PSTN via a forwarded leg — two carrier hops are unavoidable | ADR-0002 |
| C2 | ASR, LLM and TTS are third-party network calls with their own variance | ADR-0005/6/7 |
| C3 | Endpoint detection is ours and is a product decision, not vendor latency | ADR-0005 C6 |
| C4 | Barge-in must interrupt agent audio within one frame interval (20 ms) | ADR-0004 §12 |
| C5 | The budget must hold at peak concurrency, not only when idle | ADR-0002 §13 |
| C6 | Every hop must have exactly one owning service | Accountability |

## 4. Considered Options

1. **No formal budget** — optimise each component independently
2. **Single end-to-end SLO only** — measure the total, let teams negotiate
3. **Per-hop allocated budget with named owners** — this ADR
4. **Aggressive target (p50 ≤ 500 ms)** — requires on-device inference or a
   self-hosted co-located model stack

## 5. Decision

**Option 3. A per-hop budget with named owners, and targets set on the measured
end-to-end distribution rather than on the sum of per-hop maxima.**

### 5.1 Targets

| Metric | Target | Meaning |
|---|---|---|
| **p50** | **≤ 900 ms** | The typical turn feels like a thoughtful pause |
| **p95** | **≤ 1 500 ms** | The worst common turn is still inside the tolerance threshold |
| **p99** | **≤ 2 500 ms** | Hard ceiling — beyond this the turn has failed |
| **Barge-in** | **≤ 20 ms** | One frame interval from VAD to silence |

### 5.2 The budget

Measured from acoustic end-of-caller-speech to first agent audio at the caller's
ear.

| # | Hop | Owner | p50 | p95 | Notes |
|---|---|---|---:|---:|---|
| 1 | **Endpoint detection** — VAD silence window before a turn is declared ended | `media-relay` | 250 | 350 | **Ours, and a product decision (C3).** The largest single item and the most tunable. |
| 2 | **Carrier → gateway** — trailing frames in flight | Carrier / `telephony-gateway` | 25 | 60 | Domestic path assumption (ADR-0003 C5) |
| 3 | **Media relay ingress** — jitter buffer, resample, framing | `media-relay` | 15 | 35 | ADR-0004 C3 |
| 4 | **STT finalisation** — end-of-speech → final transcript | `asr-gateway` | 120 | 250 | ADR-0005 C1. Partials already delivered. |
| 5 | **Orchestration** — routing decision, tool pre-check, prompt assembly | `session-orchestrator` / `ai-orchestrator` | 20 | 60 | Largely overlapped (§5.3) |
| 6 | **LLM time-to-first-token** | `ai-orchestrator` | 250 | 550 | ADR-0006 C1. Tier 2 at `effort: "low"`. |
| 7 | **Sentence segmentation** — first speakable clause available | `ai-orchestrator` | 15 | 40 | ADR-0007 §5 |
| 8 | **TTS time-to-first-byte** | `tts-gateway` | 90 | 180 | ADR-0007 C1 |
| 9 | **Media relay egress** — framing, resample to 8 kHz µ-law | `media-relay` | 15 | 35 | Reduced by direct µ-law output (ADR-0007 §12) |
| 10 | **Gateway → carrier → handset** | `telephony-gateway` / Carrier | 25 | 60 | Mirror of hop 2 |
| 11 | **Playback jitter buffer** at the caller's device | Carrier (not ours) | 60 | 100 | Outside our control; budgeted, not optimisable |
| | **Serial sum** | | **885** | **1 720** | |
| | **Target** | | **≤ 900** | **≤ 1 500** | |

### 5.3 Why the p95 target is below the sum of p95s

This is the subtlety that makes the budget achievable, and it must be understood
rather than treated as arithmetic sleight of hand.

**The p95 of a sum is not the sum of the p95s.** Summing per-hop p95s assumes
every hop has its bad day simultaneously, which for largely independent hops is
vanishingly rare. The 1 720 ms serial sum is the **pessimistic bound** — useful
for reasoning about a worst case, wrong as a target.

Two structural overlaps reduce the real total further:

- **Hops 4–5 overlap hop 1.** Interim ASR results stream continuously
  (ADR-0005 §12), so Tier-1 classification and prompt assembly are usually
  complete before the endpointing window even closes. On a typical turn, hop 5
  contributes approximately zero.
- **Hops 7–8 overlap hop 6.** TTS begins synthesising the first clause while the
  LLM is still generating the rest of the reply (ADR-0007 §5). We wait for the
  first *clause*, not the first response.

The targets in §5.1 are therefore set on the **measured end-to-end
distribution**, and hop budgets are diagnostic allocations used to attribute a
breach — not numbers to be added up.

### 5.4 Escalation-path exception

Tier-3 escalation (ADR-0006 §5) runs a larger model and will exceed hop 6's
allocation. This is accepted, and it is masked rather than optimised: the agent
emits a short **filler utterance** ("Let me check that for you") from Tier 0 the
moment escalation is chosen. The caller hears a response inside the normal budget
while the escalated turn generates behind it.

Filler audio is a latency mechanism, not a personality flourish, and it is
governed by this ADR.

## 6. Why This Option Was Selected

**Because the failure mode of Option 1 is invisible and universal.** Every team
ships a component that is individually reasonable, no single team is at fault, and
the product is slow. A budget converts a diffuse quality problem into an
attributable one: when p95 breaches, the per-hop telemetry names the hop and §5.2
names its owner (C6).

- **Option 2 (end-to-end SLO only) fails at diagnosis.** Knowing the total is
  1.9 s tells nobody what to fix, and in a chain of eleven hops across six
  services that ambiguity is where weeks go.
- **Option 4 (p50 ≤ 500 ms) is not reachable with this architecture.** Hops 2, 10
  and 11 alone are ~110 ms of carrier and playback latency we do not control (C1),
  and no hosted LLM reliably delivers a first token in the remainder. Reaching
  500 ms would require on-device or co-located inference — a different product,
  costed in ADR-0006 §8 and rejected there.
- **Setting targets on the measured distribution rather than the serial sum**
  (§5.3) is what makes the budget both honest and achievable. A budget that
  demands the impossible is ignored; one that demands the merely difficult is
  enforced.

## 7. Trade-offs

**Accepted.**

- **Endpointing at 250 ms is a deliberate slowness.** It is the largest hop and we
  chose it. A shorter window makes the agent interrupt callers who paused
  mid-thought — far worse for the interaction than 100 ms of extra latency. This
  is a **product** trade-off wearing an engineering costume, and it should be
  tuned by measuring false-endpoint rate, not by minimising latency.
- **Filler utterances trade honesty for perceived speed.** The agent says
  something before it knows the answer. Confined to escalation, kept short, and
  never used to mask an ordinary slow turn — that would be padding every
  interaction to hide a problem rather than fixing it.
- **Hop 11 is budgeted but not owned.** 60–100 ms of the total is the caller's
  network and handset. We allocate it, measure around it, and cannot improve it.
- **Per-hop instrumentation has a cost** — spans, timestamps, and a trace on every
  turn. Justified: without it, §5.2 is decorative.

## 8. Alternatives Rejected

**No budget (Option 1).** Rejected for the reason in §6 — the failure is silent
and nobody owns it.

**End-to-end SLO only (Option 2).** Rejected on diagnosability. Retained *in
addition* to the per-hop budget: §5.1 is an end-to-end SLO, and §5.2 is how we
find out why it broke.

**Aggressive 500 ms target (Option 4).** Rejected as unreachable within ADR-0002's
architecture. Not rejected as undesirable — if on-device inference becomes viable
(ADR-0006 §16) this target should be revisited immediately.

**Budgeting the serial sum (1 720 ms) as the p95 target.** Rejected as too loose.
It would ratify a latency the caller experiences as broken, and it would let every
hop degrade to its individual worst case without triggering any alarm.

## 9. Operational Impact

- **Every turn is traced with a span per hop**, named for the hop numbers in
  §5.2. A p95 breach must be attributable within minutes, not investigated for
  days.
- **The SLI is the end-to-end distribution**, measured from the timestamps
  `media-relay` records at endpoint detection and at first outbound frame — not
  from a synthetic probe and not from a server-side subset.
- **Alerting is on the SLO** (§5.1), with per-hop breakdown attached to the alert
  payload so the on-call engineer opens the page already knowing the hop.
- **Per-hop budgets are burn-rate alerts, not paging alerts.** A single hop
  running hot without breaching the end-to-end SLO is a ticket; the SLO breach is
  the page.
- **`tests/eval` gates latency alongside accuracy** (ADR-0006 §9). A prompt change
  that improves quality while breaching hop 6 is a regression and fails the gate.
- **`tools/sipp-harness` measures the real chain under synthetic load** (C5).
  Latency measured on an idle system is not a measurement of anything.

## 10. Security Impact

Latency and security interact in three specific ways, all of which have caused
production incidents in comparable systems:

- **Timing is a side channel.** Response latency that varies with whether a caller
  is recognised, whether they are on a block list, or whether fraud scoring
  escalated, leaks that state to the caller. A fraudster can probe it. Tier-0
  and Tier-1 paths must be **timing-normalised** where the difference would
  disclose a security decision.
- **Latency pressure invites unsafe shortcuts.** The single most tempting
  optimisation in this system — disabling thinking to save LLM latency — silently
  breaks tool calling (ADR-0006 §2). This ADR does not authorise trading
  correctness for budget compliance, and any optimisation that does must be
  argued as its own decision.
- **Degradation must fail safe.** Under load, the correct response is to shed at
  admission (ADR-0004 §13) or to downgrade a tier (ADR-0006 §13) — never to skip
  fraud scoring or the safety layer to save milliseconds.

## 11. Cost Impact

Latency and cost are coupled in both directions, which is why this section is not
a formality:

- **Lower latency is often cheaper.** `effort: "low"` on Tier 2 (ADR-0006) reduces
  both first-token latency and token spend. Tier 0 removes the model entirely and
  is both the fastest and free. These are aligned incentives and should be
  exploited first.
- **Lower latency is sometimes much more expensive.** Fast mode on Tier 3 doubles
  the per-token rate (ADR-0006 §11). Confined to escalation precisely because
  buying latency with money does not scale to every turn.
- **Every second of latency is a second of telephony billing** (ADR-0003 §11). A
  turn that takes 1.5 s instead of 0.9 s across four turns adds nearly 2.5 s of
  metered call duration. **Latency is a direct cost line, not only a UX metric** —
  and this is the argument that wins budget for latency work.

## 12. Performance Impact

This ADR *is* the performance section for the platform. Restating the principles
that follow from §5:

- **Overlap, do not serialise.** The two overlaps in §5.3 are what make the budget
  close. Any change that serialises them — waiting for a complete transcript,
  waiting for a complete LLM response — breaks the budget by hundreds of
  milliseconds regardless of how fast the individual components are.
- **Pre-warm every stream.** ASR (ADR-0005 §12), TTS (ADR-0007 §12) and the LLM
  connection are all established before they are needed. Handshake latency inside
  the budget is pure waste.
- **The first turn is the exception that must not be one.** Cold connections, cold
  caches and an unwarmed prompt prefix make turn one the slowest. Tier 0 answering
  the opening utterance (ADR-0006 §5) exists partly to hide this while the rest of
  the pipeline warms.
- **Measure at p95 and p99, never at the mean.** A mean of 700 ms with a p99 of
  4 s is a product where one turn in a hundred fails, and callers remember the
  failure.

## 13. Scalability Impact

- **The budget must hold at peak concurrency** (C5), which is a stronger statement
  than it appears. Queueing delay is the failure mode: a service at 90%
  utilisation has a latency distribution with a long tail regardless of its
  per-request speed.
- **Therefore capacity is provisioned against the latency target, not against
  saturation.** Services on the hot path run at deliberately low utilisation.
  This is more expensive and it is the price of a p95 that holds under load.
- **Load shedding preserves the budget for admitted calls.** Refusing a new call
  at admission is better than degrading every in-flight call — consistent with
  ADR-0004 §13.
- **Autoscaling on CPU is wrong here** (ADR-0008 §13). Scale on concurrency and on
  the latency SLI itself.

## 14. Migration Strategy

The budget is versioned and changes deliberately:

1. **Phase 1 (launch).** §5.2 as allocated. Instrumentation before optimisation —
   the spans ship with the first screening call, not after.
2. **Re-baseline after 30 days of production traffic.** Launch allocations are
   estimates; production distributions are facts. Hops that consistently beat
   their allocation give budget back to hops that do not.
3. **Any reallocation is an amendment to this ADR** with the same review as the
   original. The budget is not a wiki page.
4. **Architecture changes reopen it.** Wideband PSTN (ADR-0004 §16), a faster ASR
   (ADR-0005 §16), or on-device inference each invalidate specific hops and
   require re-derivation rather than adjustment.

## 15. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| p95 breached under peak concurrency while idle measurements look fine | High | Critical | Load-measured SLI via `tools/sipp-harness`; capacity provisioned to the latency target, not to saturation |
| A vendor's latency degrades without notice | Medium | High | Per-hop telemetry attributes it immediately; secondary providers in ADR-0005/0007 |
| Endpointing tuned for latency, causing the agent to interrupt callers | Medium | High | False-endpoint rate is a gated metric alongside latency; endpointing is a product decision (§7) |
| Overlaps in §5.3 silently regress to serial execution | Medium | Critical | Overlap asserted in `tests/eval`; hop-5 and hop-7 contribution monitored as a signal |
| Timing side channel discloses fraud or block-list state | Low | High | Timing normalisation on Tier-0/Tier-1 paths (§10) |
| Filler utterances used to mask ordinary slowness | Medium | Medium | Confined to escalation by policy; filler rate is a monitored metric |
| Budget treated as the serial sum and allowed to drift to 1.7 s | Medium | High | §5.1 targets are the SLO; per-hop budgets are diagnostic only |
| First-turn latency is far worse than steady state | High | Medium | Tier 0 opening utterance; stream pre-warming; measured separately from subsequent turns |

## 16. Future Review Trigger

Revisit when **any** holds:

- Measured end-to-end **p95 exceeds 1 500 ms** for 7 consecutive days
- Measured end-to-end **p99 exceeds 2 500 ms** at any point in a rolling 24 hours
- **30 days of production traffic** have accumulated (the mandatory re-baseline,
  §14 step 2)
- Any dependent ADR's allocation changes — 0004 (media), 0005 (ASR), 0006 (LLM),
  0007 (TTS)
- On-device or co-located inference becomes viable, making Option 4 reachable
- Wideband PSTN interconnect becomes available (ADR-0004 §16)
- Barge-in latency p95 exceeds **one frame interval**

## 17. References

- ADR-0002 (telephony architecture), ADR-0003 (carrier selection), ADR-0004
  (media transport), ADR-0005 (streaming STT), ADR-0006 (LLM routing), ADR-0007
  (streaming TTS)
- `docs/architecture/voice-pipeline.md` — the same chain rendered as a diagram
- `tools/sipp-harness` — load-measured latency
- `tests/eval` — latency gates alongside accuracy and cost
