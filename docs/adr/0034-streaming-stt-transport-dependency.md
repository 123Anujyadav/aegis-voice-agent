# ADR-0034: Transport dependency for streaming speech-to-text

- **Status:** Proposed
- **Date:** 2026-11-11
- **Deciders:** the repository owner (see `README.md`, "Approval")
- **Independent human review:** proposed as **not high-risk** (a transport library for vendors already accepted in ADR-0005; §Approval record). The owner's classification is final (rule 6).
- **Consulted:** none
- **Informed:** —

---

## Context

- **ADR-0005 (Accepted)** routes speech-to-text through an `asr-gateway`:
  - Google Cloud Speech-to-Text v2 (`telephony` model, `asia-south1`) as primary;
  - Deepgram Nova-3 as secondary and failover (cross-border, so consent-gated: ADR-0012);
  - Sarvam in shadow evaluation.
- **Recognition must stream** (ADR-0011's latency budget). The frozen port `speech.STTProvider` is streaming.
- **Neither transport exists in the repository:**
  - **Google STT v2 streams only over gRPC** (`StreamingRecognize`); its REST API has no streaming recognition.
  - **Deepgram streams over WebSocket.**
  - The Go standard library has neither a gRPC nor a WebSocket client.
  - The repository uses no gRPC or WebSocket library today (checked on `main`).
- **Policy:** a third-party dependency needs its own recorded reason (ADR-0026 D5). ADR-0015's precedent is to contain it to the adapter module, verify the transitive set with `go list -deps`, and keep every other module free of it.
- **The other AI adapters avoid the question:** `packages/go/aiproviders` already holds the Claude adapter (#300) and the ElevenLabs adapter, both standard-library HTTP. **Speech-to-text cannot avoid it.**
- **What this ADR is not:**
  - it activates nothing;
  - speech stays deferred by the owner (OD-17);
  - adding the dependency lets the adapter be written and tested against local fakes, and nothing more.

## Decision Drivers

| # | Driver | Source |
|---|---|---|
| D1 | The primary recogniser (Google, in-country) must be reachable; a secondary-only design forces cross-border by default | ADR-0005 C4, ADR-0012 |
| D2 | The fewest, smallest and best-maintained dependencies, contained to one module | ADR-0015, ADR-0026 D5 |
| D3 | No hand-written implementation of a security-sensitive protocol | §Options |
| D4 | Licences compatible with the repository (`DEPENDENCY_AUDIT.md` §9, §12) | — |

## Considered Options

1. **A. gRPC and Google's generated Speech v2 client** (`google.golang.org/grpc`, `cloud.google.com/go/speech`, with their protobuf and auth dependencies), for Google STT v2.
2. **B. A small WebSocket client library** (for example `github.com/coder/websocket`, ISC licence, no transitive dependencies), for Deepgram, and for Sarvam if its streaming API is WebSocket (**unverified**).
3. **C. Write a WebSocket (RFC 6455) client in the repository.**
4. **D. Batch REST only, no streaming.**

## Decision Outcome

**Proposed: A and B, each in its own module boundary, each added only when its adapter is written.**
- **A** is the only way to stream to the primary recogniser (D1).
- **B** is the smallest path to the secondary (D2).
- **C is rejected:** framing, masking, fragmentation and close handling are security-sensitive (D3), and a maintained library is safer.
- **D is rejected:** it breaks ADR-0011's latency budget, and ADR-0005's streaming decision.

**Conditions:**
- **Contained:** the libraries are required only by the speech-adapter module (`packages/go/aiproviders`, or a separate `speechproviders` module if A's dependency set is large: decide at implementation). `go list -deps` shows no other module gains them.
- **Pinned:** exact versions, with licence checks recorded in `DEPENDENCY_AUDIT.md`. The versions are **chosen at implementation**, not here.
- **Same rules as the other adapters:**
  - credentials from mounted files;
  - TLS to the vendor's host only;
  - no redirects;
  - no audio or transcript in logs or errors;
  - `providerconformance.RunSTT` passes against local fakes.
  - Google uses workload identity, not a service-account key (as `PLAY-INTEGRITY-DECISION-MEMO.md` recommends for Google APIs).
- **Not activated:** use needs the speech spending category (deferred, OD-17), consent (OD-4), and ADR-0012's conditions for Deepgram.

### Consequences

- **Good:** both ADR-0005 vendors become implementable and testable now. Each dependency is contained and reviewed once.
- **Bad:**
  - **A brings a large transitive set** (gRPC, protobuf, Google auth). That is review and update load: `govulncheck` and Dependabot cover it, but it is real work.
  - **B is a second, smaller dependency.**
- **Neutral:** Sarvam's streaming transport is unverified. If it is WebSocket, B covers it; otherwise it needs its own note.

### Confidence

- **Medium.** The transports are the vendors' documented ones as understood on 2026-11-11, but **not verified against a live API**.
- **The concrete libraries and versions are chosen at implementation,** with `go list -deps` and licence evidence.

### Revisit Trigger

- The owner changes ADR-0005's vendors under OD-17.
- A vendor ships a plain-HTTP streaming API.
- A's transitive set proves unacceptable at implementation.

## Options in Detail

### Option A: gRPC and Google's Speech v2 client
- **Good:**
  - the only streaming path to the in-country primary;
  - a vendor-maintained client;
  - gRPC is mature and widely reviewed.
- **Bad:** a large dependency tree; Google-specific types must not cross the `speech` port (they do not: the adapter maps them).

### Option B: a small WebSocket library
- **Good:** small, with no transitive dependencies (to be confirmed with `go list -deps` at implementation); serves Deepgram and possibly Sarvam.
- **Bad:** Deepgram is cross-border, so it is never the default.

### Option C: an in-house WebSocket client
- **Good:** no dependency.
- **Bad:** security-sensitive protocol code with no external review; a maintenance burden. **Rejected.**

### Option D: batch REST only
- **Good:** standard library only.
- **Bad:** no partials and whole-utterance latency; breaks ADR-0011. **Rejected.**

## Approval record

| Field | Record |
|---|---|
| Decision | — (Proposed) |
| Classification | Proposed **not high-risk**: it adds transport libraries for vendors already accepted in ADR-0005, and changes no authentication model, trust boundary, invariant or personal-data flow (the flow was decided in ADR-0005 and ADR-0012). **The owner's classification is final** |
| Owner decision | — |
| Independent human review | Not required if the classification stands |
| AI involvement | Drafted by Claude Code (AI-performed) |
| Does not | Activate any vendor; approve the speech spending category; decide OD-17 |
| Supersedes | nothing |

## References

- ADR-0005, ADR-0011, ADR-0012, ADR-0015, ADR-0026 (D5)
- `docs/phase19/INTEGRATION-DEPENDENCY-MATRIX.md` (ASR row); `docs/phase19/INTEGRATION-READINESS-REQUIREMENTS.md` §2.3
