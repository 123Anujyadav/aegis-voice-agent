# Architecture

The frozen architecture of the CallScreen platform, as decided in ADR-0001
through ADR-0012.

> **These diagrams are derived, not authoritative.** The ADRs are the source of
> truth. A diagram that contradicts an ADR is a bug in the diagram. When a
> decision changes, the ADR is superseded first and the diagram follows in the
> same pull request.

All diagrams are **Mermaid in Markdown** — diffable, reviewable, and versioned
with the code. Binary images are not permitted here (Phase 1 §9).

---

## Reading order

Start at the top and work down. Each level assumes the one above it.

| # | Document | Answers |
|---|---|---|
| 1 | [C4 Context](c4-context.md) | Who uses the system, and what does it talk to? |
| 2 | [C4 Container](c4-container.md) | What are the deployable pieces and how do they connect? |
| 3 | [Deployment](deployment.md) | Where does it physically run? |
| 4 | [Voice Pipeline](voice-pipeline.md) | How does audio become a reply, within the latency budget? |
| 5 | [Sequence Diagrams](sequence-diagrams.md) | What happens, in order, for each significant flow? |
| 6 | [Data Flow](data-flow.md) | Where does personal data go, and under what classification? |
| 7 | [Trust Boundaries](trust-boundaries.md) | Where does trust change, and what enforces it? |
| 8 | [Threat Model](threat-model.md) | What can go wrong deliberately, and what stops it? |

---

## The four facts that explain every diagram

If you read nothing else, read these. Almost every question about why the system
is shaped this way is answered by one of them.

1. **Android will not give a third-party app the call audio** (ADR-0002).
   Everything about carrier-side screening, conditional call forwarding, DIDs and
   media relaying exists because of this single platform constraint.

2. **The caller is a Data Principal who never consented to us** (ADR-0012). This
   is why the call opens with a deterministic announcement, why audio recording
   is off by default, and why the data-flow and trust-boundary diagrams treat
   caller data as the most sensitive thing in the system.

3. **The latency budget is 900 ms p50 / 1 500 ms p95, end to end** (ADR-0011).
   Every hop in the voice pipeline has an allocation and an owner. The pipeline
   diagram is that budget rendered as a picture.

4. **Personal data stays in India** (ADR-0008, ADR-0012). This determines the
   cloud, the regions, the DR posture, and the one narrow, consented,
   audited exception for cross-border AI vendors.

---

## Notation

Used consistently across every diagram in this directory.

| Element | Meaning |
|---|---|
| **Solid arrow** `-->` | Synchronous call, request/response |
| **Dashed arrow** `-.->` | Asynchronous — event, stream, or notification |
| **Thick arrow** `==>` | Real-time media path, on the latency budget |
| **Dotted boundary** | Trust boundary — see [trust-boundaries.md](trust-boundaries.md) |
| `[Go]` `[Python]` `[Kotlin]` | Implementation language |
| 🇮🇳 | Runs inside the India residency boundary |
| 🌐 | Outside the residency boundary — consent-gated (ADR-0012 §5.4) |

---

## Status

| Aspect | State |
|---|---|
| Decisions | **Frozen** — ADR-0001 … ADR-0012 accepted |
| Implementation | Phase 2 complete — engineering foundation only, no product behaviour |
| Next | Phase 3, governed by [`ARCHITECTURE_FREEZE.md`](../../ARCHITECTURE_FREEZE.md) |
