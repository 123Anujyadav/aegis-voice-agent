# Integration dependency matrix (2026-11-10)

- **As of:** `main` `5fb7b33`. Compiled from the code and records on `main` only (`git show`, `ls-tree`, `grep`); key facts spot-checked. **Updated at `main` `063663e`:** the three safe preparation items in §2 are done (#290, #291, #292). No governing record in §1 changed status.
- **What it is:** for each external integration, the governing decisions, what code exists, whether it runs, what gates it, which milestone needs it, and what local preparation is possible.
- **Decides nothing, and activates nothing.**
- **Companion documents:**
  - [`BLOCKER-MAP-2026-11-10.md`](BLOCKER-MAP-2026-11-10.md): the blockers;
  - [`M1-APPROVAL-REQUESTS.md`](M1-APPROVAL-REQUESTS.md): the M1 approvals;
  - `PRODUCTION-READINESS-PLAN.md` §3–§4: the readiness matrix and the spending categories.
  - [`INTEGRATION-READINESS-REQUIREMENTS.md`](INTEGRATION-READINESS-REQUIREMENTS.md): for each integration, the prerequisites, implementation dependencies, security requirements and test evidence, with citations (2026-11-10).
  - [`LOCAL-MODEL-ADAPTER-TESTING.md`](LOCAL-MODEL-ADAPTER-TESTING.md): what the Whisper, Piper and Ollama adapters' tests cover, and how to run the real-runtime tests.

**Words used below:**
- **Port:** the interface the product codes against.
- **Real:** a client that would call an external API. **None has ever been called.**
- **Local:** a real adapter to a local binary or daemon, with no external API.
- **Fake:** a test double.
- **Placeholder:** `packages/go/integrations`, whose package comment says: "None of the implementations here contacts a provider: each returns a fixed or synthetic result." Nothing imports it, and a guard test keeps it that way (#290).
- **Conformance:** `packages/go/providerconformance` (#292), behavioural suites for the four provider ports. Every future adapter for those ports has a ready acceptance test.

## 1. Matrix

| Integration | Governing records (status) | Port | Real / local client | Fake or mock | Runs in a service? | Gated by | Milestone |
|---|---|---|---|---|---|---|---|
| **SMS OTP** (Twilio, MSG91) | ADR-0010 (Accepted) §5(a), §9; **ADR-0033 (Proposed, owner-preferred; OD-12)**: delivery-only providers; ADR-0032 (Proposed, high-risk) | `otpdelivery.Provider` / `DeletionCodeProvider` (identity) | **None.** Draft #271 (Twilio Verify) is not selected | `otpdelivery.MockProvider` (development only); `fakeProvider` in tests | Yes: the mock, in development only. Elsewhere there is no delivery | **R1** (E2: provider, DLT, domestic route, billing); F26 before templates; **OD-12** for any adapter; ADR-0033 Q2–Q10; O13 for deletion codes | **M1**, the critical path (C2); deletion codes M2 (C3) |
| **DID, telephony and CPaaS; forwarding** | ADR-0002 (Accepted) CFNRy to a platform DID; ADR-0003 (Accepted) Exotel primary, Plivo secondary; ADR-0004 (Accepted; AgentStream production media NO-GO per Phase 15 audit) | `telephony.Provider` (frozen; "NOTHING IN THIS PACKAGE IMPLEMENTS IT") | None. `telephony-gateway` holds only the PostgreSQL fence and RDS IAM auth (owner-held) | `telephony.FakeProvider`; placeholders `ExotelClient` and `PlivoClient` (no network; `HealthCheck` always nil). Conformance: `RunTelephony` | No carrier path anywhere; Android shows forwarding "Not available yet" | **X4** (DID pool, forwarding matrix), C8, the forwarding spending category, **P1-38** (telephony staging NO-GO), OD-4 | **M3** (device cases M3–M5) |
| **ASR** (speech to text) | ADR-0005 (Accepted): Google STT v2, Deepgram, Sarvam; **OD-17 deferred** (owner, 2026-11-04) | `speech.STTProvider` (frozen) | **Local:** `voice/providers/whispercpp`, `whispercli`; no vendor client | `speech.FakeSTTProvider`; placeholders return fixed text. Conformance: `RunSTT` | No: `services/go/voice` builds no pipeline | **OD-17**; the speech category ("Deferred"); OD-12 for call audio; consent (OD-4) | M3 |
| **TTS** | ADR-0007 (Accepted): ElevenLabs Flash, Cartesia, Sarvam; OD-17 | `speech.TTSProvider` (frozen) | **Local:** `voice/providers/piper` | `speech.FakeTTSProvider`; the ElevenLabs placeholder returns non-audio bytes. Conformance: `RunTTS` | No | As ASR | M3 |
| **LLM** | ADR-0006 (Accepted): four tiers on Claude | `runtime.Provider` (frozen; 20 ms cancellation) | **Local:** `voice/providers/ollama` (explicitly not a production tier); no vendor client | `runtime.FakeProvider`. Conformance: `RunLLM` | No: no service builds a `runtime.Kernel` | The LLM spending category; OD-12 and DPDP (L1) for transcripts | M3 |
| **Conversation orchestration** | ADR-0016, ADR-0017 (Accepted); ADR-0006, ADR-0011 | `conversation` (frozen), `intent`, `voiceintel`, `voice.Pipeline` | Deterministic, in-process | Extensive tests in `services/go/voice` | `services/go/voice` runs bridge → engine → classifier; the **audio** pipeline cannot be built without providers | The providers above (OD-17, LLM), P1-38, X4, OD-4 | M3 |
| **Fraud detection** | **No fraud ADR.** Mentioned in ADR-0006 (tier 3, eval gate), ADR-0009, ADR-0002, ADR-0011 | None | None. `fraud-detection` and `fraud-engine` are scaffolds; `governance.RiskLevel` runs no model | None | No. Android's `PreFilter` reserves `SILENCE` for a reputation verdict, with no input yet | **OD-3** (spam-reputation policy; no reputation source exists), the LLM approval for tier 3 | M3 (product capability) |
| **PostgreSQL** | ADR-0009, ADR-0015 (Accepted); ADR-0029 (Proposed, under delegation: TLS) | `pgx` pools, `pgshared` | **Real:** identity `pgstore` (migrations 0001–0009), edge-api replay store, `evalstore`, the gateway fence | Throwaway digest-pinned containers in CI (`identity-postgres`, `gateway-postgres`, `evalstore-postgres`, `android-identity-contract`) | Yes: identity, edge-api, telephony-gateway | **E4** (CA bundle, multi-AZ, PITR), **R2**; O2 for the subscriber directory (C11) | M1 (staging); M2 (PITR, multi-AZ) |
| **Cloud** (AWS, EKS) | ADR-0008 (Accepted): `ap-south-1`, EKS; ADR-0027 (Accepted): one `infra/k8s` tree; A2 (Helm), the kubeconform policy and gate **A5** not decided | — | **Real:** `telephony-gateway/internal/rdsauth` (STS, RDS IAM; owner-held). `infra/k8s/base` holds generic templates only | `rdsauth` fake STS; placeholder `MockAwsWrapper` (`EncryptKms` returns **plaintext** with a "KMS_ENC:" prefix) | Nothing deployed | **R2** (X5, E3, E4, B1–B6); **A5**, C1 (who writes the manifests) | M1 (staging); M2 (production) |
| **Monitoring and tracing** | ADR-0013 (Accepted, Prometheus text); ADR-0026 (Accepted, W3C tracing); **ADR-0028 (Proposed, conditional: X-Ray; P1, P2 pending)** | `metrics` (frozen), `metricsexport`, `telemetry` | `/metrics` on edge-api, identity and telephony-gateway; spans to a **log exporter** only; **no OTLP exporter**; no alert rules (ADR-0013: operator artefacts). Catalogue: [`OBSERVABILITY-CATALOGUE.md`](OBSERVABILITY-CATALOGUE.md) | `RecordingTracer`; fuzzed `traceparent` | Yes, metrics and log spans | **E5** (SRE: P1 X-Ray ids, P2 cost), ADR-0028 acceptance | M2 (operations) |
| **FCM push** | ADR-0010 (Accepted) §8: notify the prior device on re-enrolment | `integrations.FcmPushDispatcher` (placeholder) | None; Android `core/notifications` is empty; no Firebase SDK | `MockFcmDispatcher` | No; `notification-fanout` is a scaffold | **E7** (Firebase project and credentials); **O5** (SIM-swap policies); device-token registration is an auth endpoint (**OD-12**) | Product Phase 4; not an M1 or M2 code item |
| **Play Integrity** | ADR-0010 (Accepted) §5(b); ADR-0031 (Proposed, under delegation; O7) | `app.IntegrityVerifier` (identity); Android `PlayIntegrityTokens` | **Real:** identity `playintegrity` (decode client and WIF token source; "has NEVER called Google"); Android Play SDK client | A local fake of Google; `TestPlayIntegrity_IsNotWiredIntoTheServer` | Android: wired only when the project number is set (unset). Server: not wired | **E1, O7, OD-9, O8, O14, X1**; **T4 stays unresolved** | M2 (T4); optional for M1 |

## 2. Local preparation, by safety

**Safe now:** local, provider-independent, and pre-empting no decision or review. **All three are done (2026-11-10).**
1. **Contain the `integrations` placeholders.** **Done:** #290 `3d832a7`.
   - A guard test that fails if any other module imports the package.
   - Comments that point to the canonical ports.
   - A loud warning that `MockAwsWrapper.EncryptKms` does not encrypt.
   - It changes no behaviour, and removes nothing.
2. **An observability catalogue.** **Done:** [`OBSERVABILITY-CATALOGUE.md`](OBSERVABILITY-CATALOGUE.md), #291 `7e82fd8`. It lists every metric, span and health check the wired services expose, taken from the code. It is documentation of existing behaviour. It decides no backend and no rule location.
3. **A provider-port conformance suite.** **Done:** `packages/go/providerconformance`, #292 `063663e`.
   - Reusable behavioural tests for `speech.STTProvider` and `TTSProvider`, `runtime.Provider` and `telephony.Provider`.
   - **They run against the ports' fakes only.** The local adapters in `packages/go/voice` (whisper.cpp, Piper, Ollama) are not run against them: those adapters need local binaries and models that CI does not have.
   - It lives in a new, non-frozen module, and **imports the frozen ports without editing them.**
   - It names no vendor, and needs no model, account or spend.
   - Every future adapter then has a ready acceptance test.

**Not prepared, on purpose:**

| Item | Why not |
|---|---|
| A model-free speech evaluation driver | It risks pre-empting the owner's OD-17 deferral of speech evaluation |
| An OTP adapter test kit; the OTP metrics hook; the stale identity comment at `app/identity.go:1299` | Inside identity: OD-12 |
| Any vendor adapter (Twilio, MSG91, Exotel, Plivo, Google, Deepgram, ElevenLabs, Cartesia, Sarvam, Anthropic, Firebase) | The provider and spending approvals; OD-12 for authentication |
| The OTLP exporter | ADR-0028 acceptance, after P1 and P2 |
| Kubernetes manifests | Gate A5, and C1 |
| Play Integrity server wiring | O7, E1, OD-9; T4 unresolved |
| Fraud scoring | OD-3 |
| Forwarding provisioning | X4 |
| A voice pipeline in a service | The providers. Phase 14 rejected in-process fakes in non-test `voice` code |
| Fixing `packages/go/persistence` | An ADR-0015 decision |

## 3. Observations recorded, not acted on

- **The `telephony-gateway` and its `rdsauth` client** belong to the owner's telephony programme, which is held and partly uncommitted. This repository's work does not edit them.
- **`packages/go/integrations`** has a stale reference at `services/go/identity/internal/app/identity.go:1299` ("see packages/go/integrations, whose provider clients place no calls"), although `otpdelivery` now exists. It is an identity comment, so it is left to an OD-12-reviewed change.
- **The frozen modules** (13; `docs/phase14/DEPENDENCY_MAP.md` §13) are imported, never edited. `packages/go/voice`'s freeze status is recorded inconsistently (`phase13/LIMITATIONS.md` versus `phase14/DEPENDENCY_MAP.md`), so it is treated as do-not-edit.
