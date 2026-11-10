# Integration readiness requirements: prerequisites, dependencies, security and evidence (2026-11-10)

- **As of:** `main` `3e84a40`. Every requirement is quoted or paraphrased from a record on `main`, with its location. Where the records are silent, this file says **"no record"** rather than inventing one.
- **Companion to** [`INTEGRATION-DEPENDENCY-MATRIX.md`](INTEGRATION-DEPENDENCY-MATRIX.md), which says what exists and what gates each integration. This file says what each one must satisfy before it is activated, and what evidence proves it.
- **Decides nothing, activates nothing.** A requirement listed here as Proposed stays Proposed. ADR-0029, ADR-0031, ADR-0032 and ADR-0033 are Proposed; ADR-0028 is Proposed and conditional.
- **Naming collision:** "T1–T14" means OTP *threats* in `OD-12-OTP-REVIEW-PACKAGE.md` §5, but *test tasks* T1–T5 and emulator cases in `ROADMAP-INVENTORY.md` and `OD-10-DECISION-2026-11-09.md`. Below, "threat T9" and "test task T3" are spelled out.

## 1. Mock and local implementations stay separate from live activation

These rules already hold on `main`, and every integration below keeps them:

1. **A mock is selected only in development, explicitly.** The OTP mock is selected only when `CS_IDENTITY_ENVIRONMENT=development` is **set explicitly** (with `CS_IDENTITY_OTP_PROVIDER` unset or `mock`); an unset environment is not enough. Staging and production never select it, and identity in staging or production refuses to start unconfigured (OD-15) (`OTP-PROVIDERS.md` §1).
2. **The placeholder providers stay out of the product.** `packages/go/integrations` contacts no provider, and a guard test fails if anything else imports it (#290).
3. **Local-model adapters are development tools, not production tiers** (`PRODUCTION-READINESS-PLAN.md`; [`LOCAL-MODEL-ADAPTER-TESTING.md`](LOCAL-MODEL-ADAPTER-TESTING.md)).
4. **A live adapter is built only when explicitly selected, and refused when partly configured** (ADR-0033 lines 77–83).
5. **Code is not activation.** Merging an adapter is one step, gated by review; activating it is another. Activation needs the category approval (R1, R2, R3 or the others in `M1-APPROVAL-REQUESTS.md` and plan §4), credentials supplied by the owner as mounted Secret files, and the activation evidence below. **No activation happens without the owner's explicit approval.**

## 2. Per integration

### 2.1 SMS OTP (Twilio, MSG91, or another provider chosen under R1)

- **Prerequisites:**
  - **R1** (E2): provider, DLT Principal Entity, header and templates, the India domestic route, billing. The naming review (F26) comes before templates.
  - **OD-12:** an independent review of any adapter, before merge (package scope S5, "before merge and before activation").
  - **ADR-0033** accepted, and its open questions Q2–Q10 decided by the owner. O13 for deletion codes.
- **Implementation dependencies:**
  - In place: `otpdelivery.Provider` and `DeletionCodeProvider`, the `Sender` failover, the development mock (#270), and the delivery metrics.
  - Needed: a **delivery-only** adapter. Identity generates, hashes and verifies the code; the provider only delivers (ADR-0033 lines 24–33). Draft #271 (Twilio Verify) is not selected.
- **Security requirements:**
  - The adapter never logs the number, code, credential, header or provider response; returns fixed errors only; sends credentials only to the provider's API host over TLS; never follows redirects; and is refused when partly configured. Android never sees a credential (ADR-0033 lines 77–83).
  - The credential comes from a mounted Secret file, "never an environment value, a ConfigMap, the repository or chat" (`M1-APPROVAL-REQUESTS.md` line 56). Rotation ≤ 90 days (`SECURITY.md` line 64).
  - Egress only to the provider's host, as a network-policy approval (ADR-0033 line 177).
  - The message text matches the registered DLT template exactly (ADR-0033 line 71).
  - The provider is a data processor: its terms go to E9 and L1 (`M1-APPROVAL-REQUESTS.md` line 49).
  - **Open gap: SMS pumping (threat T9).** Only per-number limits exist. ADR-0010 calls for limits "per number, per device, and per source address" (lines 232–234). There is no country allowlist and no global budget (ADR-0033 §7; Q2, Q3).
  - Delivery receipts: not proposed (Q10). **No record** requires webhook signatures for SMS, because no SMS webhook is proposed.
- **Test evidence:**
  - Before merge (ADR-0033 lines 156–169): loopback-only unit tests; error mapping; no retries or redirects; timeout and cancellation; no request for invalid input; the body matches the template; a `Sender` failover test; the real-PostgreSQL suite; the OD-12 review.
  - Before activation: runbook cases S1–S3 on real handsets, S4 (HTTP 429 on the 4th request), S5 (the deletion template) and S6 (a log audit for numbers and codes: "No match") (`docs/runbooks/identity-provider-onboarding.md` lines 166–176).
  - For M1: test task T3 started (`OD-10-DECISION-2026-11-09.md`).
- **Launch scope: India only, domestic route.**
  - "No international-route fallback is to be used. The intended route is domestic, with DLT" (`OTP-PROVIDERS.md` line 119).
  - Deletion codes: "`+91` only (DLT)" (`ACCOUNT-DELETION.md` line 229).
  - The owner restated the India-only domestic SMS launch scope on 2026-11-10.
  - **The code does not enforce it yet:** sign-in accepts "any E.164 number" (ADR-0033 line 123). Enforcing a `+91` allowlist is ADR-0033 **Q2**, still open, and an identity change: it needs the owner's formal decision, then the OD-12 review before merge.

### 2.2 DID, telephony, CPaaS and forwarding

- **Prerequisites:** **X4** (DID pool, CPaaS contract, forwarding matrix), C8, the forwarding spending category, **P1-38** (telephony staging is NO-GO), OD-4 (consent).
- **Implementation dependencies:**
  - In place: `telephony.Provider` (frozen, unimplemented), `telephony.FakeProvider`, and `providerconformance.RunTelephony` (#292).
  - Needed: an Exotel or Plivo adapter (ADR-0003), and the media relay of ADR-0004 (AgentStream production media is NO-GO).
- **Security requirements:**
  - Authenticate a diverted call by its SIP diversion header, and "treat an undiverted inbound call as hostile" (ADR-0002 lines 177–181).
  - Per-DID and per-source rate limits, and admission control, "from day one" (ADR-0002 lines 182–184).
  - No MMI dial string from a caller or the server "without signature verification" (ADR-0002 lines 185–188).
  - "Inbound webhook authentication is mandatory": callbacks must be signature-verified (ADR-0003 lines 174–176).
  - Credentials in the secret manager, rotated ≤ 30 days (ADR-0003 lines 170–173; `SECURITY.md` line 64).
  - A DPA naming India-only processing, and a breach-notification path (ADR-0003 lines 161–169).
  - Carrier WebSocket over WSS with mutual authentication; audio never written to disk in the relay; short-lived TURN credentials (ADR-0004 lines 163–174).
  - Forwarding tests: forward only to an owned DID, record no audio, and consent and announcement before any real caller (`FORWARDING-TEST-PREPARATION.md` lines 43–48).
- **Test evidence:**
  - Device cases M3–M5, for release milestone M3 (`OD-10-DECISION-2026-11-09.md`).
  - Diversion-header passthrough confirmed "contractually and by test" (ADR-0003 lines 177–179).
  - The domestic media path "verified by measurement before launch" (ADR-0003 lines 203–206).
  - Synthetic canary calls per provider and carrier (ADR-0003 line 158).
  - `RunTelephony` passes against the adapter and a local carrier stand-in.

### 2.3 ASR (speech to text)

- **Prerequisites:** **OD-17** (deferred by the owner), the speech spending category (deferred), OD-4 (consent), and OD-12 for call audio.
- **Implementation dependencies:**
  - In place: `speech.STTProvider` (frozen), its fake, `RunSTT`, and the local whisper adapters.
  - Needed: a vendor adapter (ADR-0005: Google STT v2, Deepgram, Sarvam) and an `asr-gateway`.
- **Security requirements:**
  - Audio and transcripts are SENSITIVE. Credentials go in the secret manager, scoped to recognition only. A transcript is untrusted input to the LLM. `asr-gateway` persists no audio (ADR-0005 lines 172–185).
  - A cross-border vendor (Deepgram) is allowed only with all four of ADR-0012's conditions: consent, an audited routing decision, a DPA with no retention or training, and a minimised payload (ADR-0012 lines 136–149).
  - Open models: `trust_remote_code` needs review (`PRODUCTION-READINESS-PLAN.md` line 77).
- **Test evidence:**
  - The `tools/audio-fixtures` corpus gated in `tests/eval` (ADR-0005 lines 165–168); Sarvam is promoted only on evaluation evidence.
  - A consented test set (`SPEECH-EVALUATION-PREPARATION.md`).
  - `RunSTT` passes against the adapter. All of this is deferred with OD-17.

### 2.4 TTS (text to speech)

- **Prerequisites:** as ASR (OD-17, the speech category, OD-4).
- **Implementation dependencies:**
  - In place: `speech.TTSProvider`, its fake, `RunTTS`, and the local Piper adapter.
  - Needed: a vendor adapter (ADR-0007: ElevenLabs Flash, Cartesia, Sarvam).
- **Security requirements:**
  - Input text is PERSONAL; the output sanitiser is a security control; voice cloning is prohibited; Tier 0 identifies itself as an AI; credentials go in the secret manager (ADR-0007 lines 182–197).
  - The default vendors process outside India, so ADR-0012's four conditions apply.
- **Test evidence:** promotion on `tests/eval` evidence (ADR-0007 lines 75–76), and `RunTTS` against the adapter. **No other record.**

### 2.5 LLM

- **Prerequisites:** the LLM spending category; OD-12 and DPDP (L1, E9) before transcripts reach a model; the tier-3 fraud use waits for OD-3.
- **Implementation dependencies:**
  - In place: `runtime.Provider` (frozen), its fake, `RunLLM`, and the local Ollama adapter (not a production tier).
  - Needed: a Claude adapter for ADR-0006's four tiers, and a service that builds a `runtime.Kernel` (none does).
- **Security requirements:**
  - The transcript is hostile input, delimited; tools are read-mostly; refusals are handled; neither the prompt nor the output is logged verbatim; "no secrets in prompts, ever" (ADR-0006 lines 198–215).
  - Local voice audit: transcripts never logged, closed metric labels, no PCM persisted (`docs/voice/SECURITY_AUDIT.md` lines 172–180).
  - **No record** covers LLM data residency: ADR-0012's cross-border exception names only ASR and TTS. This gap belongs with E9 and L1 before transcripts reach any hosted model.
- **Test evidence:**
  - The `tests/eval` gates: accuracy, fraud recall and precision, refusal rate, injection resistance, p95 first-token latency and cost (ADR-0006 lines 183–187); shadow evaluation before promotion.
  - `RunLLM` against the adapter (20 ms cancellation, ADR-0011).

### 2.6 Conversation orchestration

- **Prerequisites:** the providers above (OD-17, the LLM category), P1-38, X4, OD-4.
- **Implementation dependencies:** `conversation`, `intent` and `voiceintel` are in place, and `services/go/voice` runs bridge → engine → classifier. The audio pipeline cannot be built without providers.
- **Security requirements:**
  - Utterance text is SENSITIVE and never a metric label (ADR-0016 lines 293–294).
  - Timing normalisation on Tier 0 and 1 paths (ADR-0011 lines 220–224).
  - Every spoken turn passes governance; there is no memory write path; deployments "must supply a durable auditor" (`docs/voice/GOVERNANCE_INTEGRATION.md` lines 115–142).
- **Test evidence:** `TestPipeline_EverySpokenTurnPassesGovernance` and `TestGoverned_VoiceHasNoMemoryWritePath` exist. **No record** of an activation test gate.

### 2.7 Fraud detection

- **Prerequisites:** **OD-3** (spam-reputation policy; no reputation source exists); the LLM category for tier 3. **No fraud ADR exists.**
- **Implementation dependencies:** none in place. `fraud-detection` and `fraud-engine` are scaffolds; Android's `PreFilter` reserves `SILENCE` for a reputation verdict that has no input yet.
- **Security requirements:** a fraud verdict is PERSONAL data about the caller, with 90-day retention, subject to caller rights (ADR-0012 lines 119, 280–281), and SENSITIVE content in ADR-0009 (line 88).
- **Test evidence:** the fraud recall and precision gate through the LLM evaluation (ADR-0006 lines 184, 295). **No other record.**

### 2.8 PostgreSQL

- **Prerequisites:** **R2** (E4: the database, CA bundle, multi-AZ, PITR); O2 for the subscriber directory (C11).
- **Implementation dependencies:** in place. Identity `pgstore` (migrations 0001–0009), the edge-api replay store, `evalstore` and the gateway fence. Needed: the staging instance itself and `migrate` (R2).
- **Security requirements:**
  - `sslmode=verify-full` with an explicit, absolute `sslrootcert`; no plaintext fallback; the only exception is an explicit `development` environment (ADR-0029 lines 143–150; ADR-0029 is Proposed, under delegation).
  - Customer-managed KMS keys; least-privilege per-service credentials through IRSA and short-lived tokens; backups in scope for erasure (ADR-0009 lines 213–235).
  - Aurora in `ap-south-1`, multi-AZ (ADR-0009 line 91).
- **Test evidence:**
  - Already shown against a real TLS PostgreSQL: TLS 1.2 or later in `pg_stat_ssl`; a wrong CA and a wrong hostname refused (ADR-0029 lines 287–301).
  - CI runs the real-PostgreSQL suites on every relevant change.
  - For M2: PITR and multi-AZ (E4; `OD-10-DECISION-2026-11-09.md`).

### 2.9 Cloud (AWS, EKS)

- **Prerequisites:** **R2** (X5, E3, E4; steps B1–B6), gate **A5** (how the manifests are built), **C1** (who writes them).
- **Implementation dependencies:** `infra/k8s/base` holds generic templates only; the staging overlay is not written (A5). The owner-held `rdsauth` client exists.
- **Security requirements:**
  - "IRSA everywhere; zero static cloud credentials"; private subnets; customer-managed KMS keys; region-lock SCPs; VPC endpoints; CloudTrail and flow logs kept in region (ADR-0008 lines 174–192).
  - India-only residency (ADR-0012 lines 132–134).
  - Secrets reach Kubernetes as "mounted files, not env vars" (`SECURITY.md` line 60).
  - **`MockAwsWrapper.EncryptKms` returns plaintext:** it must never stand in for KMS (#290 warns in the code).
- **Test evidence:**
  - Test task T5, the key-rotation drill in staging (`OD-10-DECISION-2026-11-09.md`).
  - A DR plan validated against `ap-south-2`, with a quarterly rehearsed failover (ADR-0008 lines 113–115, 263).
  - The staging availability, p95, failover and load targets are **open** owner decisions (OD-10 §6, items 1–4).

### 2.10 Monitoring and tracing

- **Prerequisites:** **E5** (SRE), ADR-0028's checks P1 (X-Ray accepts random W3C trace ids) and P2 (cost), then its acceptance.
- **Implementation dependencies:**
  - In place: `/metrics`, the log span exporter, and the catalogue (#291).
  - Needed: an OTLP exporter and a Collector, after ADR-0028. Alert rules and dashboards are operator artefacts (ADR-0013).
- **Security requirements:**
  - No content or per-call identifier in labels; bounded cardinality (ADR-0013 lines 49–65). Every label has a closed vocabulary ([catalogue](OBSERVABILITY-CATALOGUE.md) §2).
  - Span attributes and error text are redacted like logs (ADR-0026).
  - X-Ray only in `ap-south-1`, no cross-region export, 30-day retention, an IRSA Collector role ("no access key exists anywhere"), and a NetworkPolicy with no Ingress (ADR-0028 lines 72–104).
- **Test evidence:** P1 and P2 by the SRE procedure in a non-production account (ADR-0028 lines 129–150). Both are pending.

### 2.11 FCM push

- **Prerequisites:** **E7** (the Firebase project and credentials), **O5** (SIM-swap policies), and **OD-12**: device-token registration is an authentication endpoint.
- **Implementation dependencies:** none real. Only the placeholder `MockFcmDispatcher` exists; Android `core/notifications` is empty; `notification-fanout` is a scaffold.
- **Security requirements:**
  - ADR-0010 requires notifying the prior device over FCM on re-enrolment (lines 213–214).
  - Proposed, not decided (O5): notify every device in the history, with a "this wasn't me" action (`SIM-SWAP-CONTROLS-REVIEW.md` line 106).
  - Credentials: "prefer workload identity; otherwise a service-account key as a Secret" (`CREDENTIALS-AND-ACCOUNTS.md` line 78).
- **Test evidence:** **no record.** A test plan would be written with the O5 decision.

### 2.12 Play Integrity

- **Prerequisites:** **E1**, **O7** (ratify ADR-0031), **OD-9** (workload identity federation), O8, O14, X1. **Test task T4 stays unresolved.**
- **Implementation dependencies:** in place: the identity `playintegrity` client (never called Google) and the Android Play SDK client. The server verifier is not wired, and a test asserts that.
- **Security requirements:**
  - The verdict is bound to the DPoP proof: `requestHash = base64url(SHA-256(DPoP proof))` (ADR-0031 lines 48–53).
  - The token is "never logged, stored, traced or echoed" (ADR-0031 line 89).
  - A quota increase before launch (ADR-0031 line 147).
  - Workload identity federation from EKS; "never use a service-account key" (`PLAY-INTEGRITY-DECISION-MEMO.md` lines 23–27; Proposed).
- **Test evidence:**
  - Local-fake tests 5–8, then staging acceptance 9–11 on a Play-installed build (`PLAY-INTEGRITY-IMPLEMENTATION-HANDOFF.md` lines 130–153).
  - T4 "remains an M2 requirement and is UNRESOLVED" (`OD-10-DECISION-2026-11-09.md`).

## 3. Evidence that spans integrations (M2)

- **E8:** a penetration test of `accesstoken`, `dpop` and the refresh path, with its findings closed (`OD-10-DECISION-2026-11-09.md`; `EXTERNAL-DEPENDENCIES.md`).
- **E9:** the DPDP review, together with L1–L6.
- **Test task T2:** device cases M1, M2 and M6–M12. **Test task T3:** physical phones against staging, then production.
- Device evidence is redacted to the last two digits (`PHYSICAL-DEVICE-TEST-CHECKLIST.md` line 126). **"Accepted with finding" is not a pass.**
- **Audio retention:** opt-in and off by default, 30 days (7–90) once consented (ADR-0012 lines 95, 117). What is absolute is that `media-relay` and `asr-gateway` never persist audio (ADR-0004 lines 169–172; ADR-0005 lines 184–185).

## 4. Gaps the records leave open (recorded, not decided)

| Gap | Where it belongs |
|---|---|
| The SMS India-only scope is not enforced in code (ADR-0033 Q2) | The owner's Q2 decision, then the OD-12 review |
| No per-device or per-source OTP limit, and no global sign-in budget (threat T9) | ADR-0033 Q2 and Q3; OD-12 |
| No record covers LLM data residency | E9 and L1 |
| No fraud ADR | OD-3 |
| No FCM test plan | O5, E7 |
| ~~The local-model adapters' findings, including Ollama accepting a non-loopback host~~ **Fixed 2026-11-10** ([`LOCAL-MODEL-ADAPTER-TESTING.md`](LOCAL-MODEL-ADAPTER-TESTING.md) §5; [`OLLAMA-ENDPOINT-POLICY.md`](OLLAMA-ENDPOINT-POLICY.md)). A remote model endpoint stays a separate, high-risk decision | The owner (ratify or reclassify the loopback policy) |
