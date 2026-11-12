# Blocker map (2026-11-10): what blocks each remaining task, and what each unblocks

- **As of:** `main` `149c97e`, re-verified on 2026-11-10. **Updated the same day at `main` `52acbdf`,** after the Android UX choices 3a, 3b and 3c and F14 were decided under the owner's delegation and implemented (#283–#286). **Updated again at `main` `063663e`,** after the A09b copy fix (#288) and the three safe preparation items of the integration matrix (#289–#292). The records were re-checked: nothing in `ROADMAP-INVENTORY.md` §3 or `docs/adr` has changed since `149c97e`. ADR-0033 stays Proposed, OD-12 stays open, and **27** decisions remain. CI on `main` is green; the latest nightly runs (2026-11-09) passed.
- **Re-verified at `main` `3e84a40` (2026-11-10, later):** #288–#293 merged with green post-merge CI; the 2026-11-10 nightlies passed; the records are unchanged, and nothing in the OD-12 review scope has changed since the package was written. Added the per-integration readiness requirements and the local-model test procedure (§5). Two items for the owner, neither blocking M1:
  - **The India-only domestic SMS launch scope** (restated by the owner on 2026-11-10) is recorded, but the code does not enforce it: that is ADR-0033 **Q2**, still open, and an identity change behind OD-12.
  - **Six findings in the local-model adapters' tests and code** (`LOCAL-MODEL-ADAPTER-TESTING.md` §5) wait for the owner to confirm whether `packages/go/voice` may be edited. This machine's Whisper CLI does not currently run (a NumPy and numba version conflict); fixing it changes the owner's Python installation.
  - **Update, the same day:** the owner directed the remediation. `voice` was found not to be frozen (that document, §6). All six findings, and a seventh found during the fix, are fixed; the Ollama adapter is loopback only (`OLLAMA-ENDPOINT-POLICY.md`). **The owner ratified that policy exactly as proposed (2026-11-10)**; tests pin it. Remote inference stays a separate high-risk decision behind OD-12, and the count of open decisions stays 27. Whisper runs from an isolated virtual environment; the global Python is unchanged.
- **The owner's actions, consolidated and prioritised** (2026-11-10, at `1fed94f`): [`OWNER-ACTION-PACK-2026-11-10.md`](OWNER-ACTION-PACK-2026-11-10.md). OD-12 comes first; it lists every one of the 27 decisions with its review need, the exact R1–R3 steps, hardware, and an outreach message and checklists for the reviewer. Nothing has been sent.
- **Revised direction (2026-11-11):** the owner stopped external reviewer hiring. Nothing was sent. OD-12 stays **open**, and the new decision **OD-12R** (how rule 8 is met without a firm) is prepared in [`OD-12-REVISED-DIRECTION-2026-11-11.md`](OD-12-REVISED-DIRECTION-2026-11-11.md). Until it is decided, authentication changes stay draft. Internal security assurance (AI-performed, labelled as such) starts now.
- **2026-11-12: the owner decided OD-12R (B: governance amendment G2), approved gate A5 and accepted ADR-0034** (#309).
  - **Later on 2026-11-12:**
    - the Deepgram adapter merged (#311 `cfe744d`), with one dependency, `coder/websocket`;
    - the Google STT v2 adapter merged (#312 `5618ee5`). It is **kept out of `go.work`**, because adding it would have raised identity's `x/oauth2` and `compute/metadata`, and it is built standalone by `speech-google.yml`.
    - All four provider adapters (Claude, ElevenLabs, Deepgram, Google) are built and tested against local fakes. **None is activated.**
    - **Remaining M1 blockers:**
      - the owner's rule-8a decision on ADR-0033 and #301, plus the F-1 SMS-pumping control;
      - R1 (SMS and DLT), R2 (staging provisioning) and R3 (Play);
      - a physical phone for T3.
  - **#306** (the staging overlay) is merged, schema-validated and not applied.
  - **#301** gained startup selection and delivery metrics.
  - *(Done in round 2, above)* **M1 critical path:** your rule-8a decision on ADR-0033 and #301, and the SMS-pumping control F-1 ([`ADR-0033-M1-INTERNAL-ASSURANCE.md`](ADR-0033-M1-INTERNAL-ASSURANCE.md)); then R1, R2 and R3.
- **2026-11-12, round 2: the owner accepted ADR-0033 for M1 under rule 8a and approved both F-1 controls** (#314). **#301 merged (`0a8e88d`):**
  - the delivery-only SMS adapters and startup selection;
  - the F-1 controls. **Delivery is denied** to numbers outside the tester allowlist, and to every number once the **global hourly cap** is reached. A real provider cannot start without both (fail closed).
  - **Test evidence (mocks and local fakes only; no real SMS sent or validated):**
    - the allowlist refuses before anything is spent, and never logs a number;
    - the cap stops at exactly N and resets each hour;
    - 50 concurrent requests at cap 10 deliver exactly 10 (20 rounds);
    - 60 concurrent reservations on real PostgreSQL at limit 13 store 13;
    - two runtimes sharing one database deliver exactly the cap;
    - store errors fail closed;
    - every configuration error refuses startup;
    - every refusal is the same `503`;
    - the single install point and single send site are pinned by tests;
    - six deliberate mutations (allowlist, lock, store errors, the both-required rule, the install guards, the SQL guard) are each caught;
    - CI ran the full Go matrix with `-race` and the real-PostgreSQL `identity-postgres` suite.
    - **Incident, disclosed:** during one local mutation run, a test probably made one HTTPS request to `control.msg91.com` with a fake key. No credentials exist, so nothing could be sent. The bootstrap tests now route all outbound HTTPS to a dead loopback proxy (`816b821`), pinned by a test.
  - **Nothing is activated:** no MSG91 or Twilio account, DLT, SMS charge or staging deployment.
  - **Remaining M1 blockers:**
    - R1: SMS provider, DLT and live activation, each separately authorised;
    - R2: staging provisioning;
    - R3: Play;
    - a physical phone for T3.
  - **Before M2:** the independent human review (rule 8b, OD-12).
- **Revised-direction execution (2026-11-11, at `main` `fafd2e7`):**
  - **Merged:**
    - #299 `290cdd9`: the OD-12 record and OD-12R;
    - #300 `5daaf76`: the Claude adapter (ADR-0006), not activated;
    - #302 `c5d59ae`: internal security assurance tests and register (AI-performed; 23 of 24 threats covered, gaps G1–G5);
    - #303 `474372e`: the Play release drafts;
    - #304 `86b608e`: **Go 1.26.11 for GO-2026-6859** (`net/http`), which had failed `govulncheck` in every HTTP module;
    - #305 `b3c1728`: ADR-0034, Proposed (the streaming speech-to-text transport);
    - #307 `fafd2e7`: the ElevenLabs adapter (ADR-0007), not activated.
  - **Held drafts:**
    - **#301:** delivery-only SMS adapters (MSG91, Twilio; India `+91` only). An authentication change, held until **OD-12R** is decided;
    - **#306:** the identity and edge-api staging overlay, held for **gate A5** (ADR-0027). Nothing is applied.
  - **New owner decisions:**
    - OD-12R;
    - gate A5 on #306;
    - ADR-0034 (gRPC and a WebSocket library for streaming speech-to-text).
    - The decision count is unchanged: OD-12R refines OD-12, and A5 and ADR-0034 are gates or records, not register rows.
  - **Eligible engineering after this round:** none that is not behind one of those decisions or an external approval. Speech-to-text adapters wait for ADR-0034; activating any adapter waits for its spending category, and for DPDP review (E9) where caller data leaves India.
- *(Superseded)* **OD-12 reviewer discovery done** (2026-11-11, at the owner's direction): [`OD-12-REVIEWER-SHORTLIST.md`](OD-12-REVIEWER-SHORTLIST.md) recommends **Cure53** for Batch A, with Trail of Bits and Payatu as alternates, all on verified public evidence. A Batch A budget ceiling and a verified fixed-commit archive tool (`tools/scripts/review-archive.sh`) are in it. **Waiting for the owner's approval** of the first contact, the ceiling and archive-only sharing (§7). OD-12 stays open.
- **Milestones:** **M0 only.** M1 is not met (OD-10 exit evidence: a release run, the `.aab` on the internal track, and a tester signing in on a physical phone against staging).
- **What this is:** every remaining Phase 19 task, against the one thing it waits for.
  - **Sources:** `ROADMAP-INVENTORY.md` §3 (the register: **27 open owner decisions**), `OD-10-DECISION-2026-11-09.md` §6, `OWNER-DECISION-PACKET-2026-11-09.md`, and `OD-12-OTP-REVIEW-PACKAGE.md`.
  - **When a blocker clears, the work in its row starts automatically,** subject to the gates it lists.
  - **Nothing here decides anything.**
- **Unblocked engineering tasks today: none.** Rounds #262–#292 completed every task that a requirement documents and nothing gates, including the matrix's three safe preparation items (§5). The one documented follow-up left, a CI build for the Python image, has a trigger that is not met (a deployed Python service; `DEPENDENCY_AUDIT.md` §13).

## 1. The independent reviewer (OD-12): one gate for every authentication change

| | |
|---|---|
| **Blocks** | Accepting ADR-0033 (OTP Option A); merging any authentication change: a delivery-only SMS adapter, sign-out (OD-14 option A or B), any OTP policy change; accepting the high-risk items (ADR-0029 ratification O1, ADR-0030 D30.3 O2, ADR-0031 O7, ADR-0032, H-items); the post-merge review of #270 |
| **Clears when** | The owner engages a reviewer: [`OD-12-REVIEWER-ENGAGEMENT-BRIEF.md`](OD-12-REVIEWER-ENGAGEMENT-BRIEF.md) (send-ready). This involves repository access under confidentiality, a budget, and a signed engagement. The review is delivered (package §9), and the owner records the decisions |
| **Then eligible** | Remediation of the reviewer's findings, each as its own reviewed change; the delivery adapter, after E2 (§3); sign-out, after OD-14 |

## 2. Owner decisions (27 open; register `ROADMAP-INVENTORY.md` §3.3)

| Decision(s) | What it decides | Unblocks when decided |
|---|---|---|
| **OD-14** | Sign-out (F8): option A (this phone), B (plus revocation) or C (none) | The sign-out implementation and its tests (packet §4), then OD-12 review before merge |
| **OTP policy** (ADR-0033 Q2–Q7; not counted as register rows) | Country allowlist; global sign-in budget; whether a new request invalidates earlier codes; resend cooldown; idempotency key; tuning of the limits | Each change in identity, then OD-12 review before merge |
| **Android UX** (packet §3; not counted as register rows) | 3a haptics; 3b the caller lists' "cleared" state; 3c the screening-role screen after a decline | **Done 2026-11-10**, decided under the owner's delegation (packet §5): 3b #283 `d8531d9`; 3a #286 `3632a76`; 3c #285 `52acbdf`. **The copy question is resolved** (2026-11-10, at the owner's direction): the first-time "If you say no" line no longer promises forwarding before M3 (packet §5) |
| **F14** (not a register row) | Whether Dependabot stops rebasing its PRs (`rebase-strategy`) | **Done 2026-11-10**, decided under the owner's delegation: `rebase-strategy: disabled` for GitHub Actions only (#284 `c219eb2`; packet §5) |
| **OD-1** | A reviewer sign-in path for Play review | C9 (also needs OD-12) |
| **OD-2** | Server-side allowlist sync | C7 (high-risk, needs an ADR-0012 amendment) |
| **OD-3** | Spam-reputation policy | Screening behaviour for reputation bands |
| **OD-4** | Consent UI, with identity's consent records | Consent screens; part of forwarding readiness |
| **OD-5, OD-6, OD-7** | Voice choice, family sharing, business analytics | Those features (product Phase 3) |
| **OD-8** | The frontend stack | C1 (the web deletion page), with E10 |
| **OD-9** | Workload identity federation for the Play Integrity verifier | C4, with O7 and E1; T4 stays **unresolved** |
| **OD-12** | Identify the independent reviewer | §1 |
| **OD-17** | Speech and voice languages (deferred) | The voice pipeline (M3) |
| **O1, O2, O7** | Ratify ADR-0029; ADR-0030 D30.3; ratify ADR-0031 | C11 (O2); C4 (O7); high-risk acceptance (OD-12) |
| **O3, O4, O5, O8** | OTP backoff; session lifetime; SIM-swap policies; what a restricted session may do | Identity policy changes (OD-12 review) |
| **O6** | Device-history retention (after E9) | C12 |
| **O9, O12–O17** | Account deletion D1, D4–D9 | C15 (O12), C3 with E2 (O13), C6 (O14), C10 (O17, high-risk); the rest are policy text or no code |
| **OD-10 §6, items 1–9** | Staging targets (availability, p95, failover, load); the M2 device and carrier matrix | Load and failover tests (after R2); the M2 device acceptance plan |

## 3. External approvals (each needs its own category approval; `M1-APPROVAL-REQUESTS.md`)

| Approval | Unblocks |
|---|---|
| **R1: SMS and DLT** (E2: provider, DLT entity, header, templates, India domestic route, billing) | C2 is **built** (#301; F-1 controls included); R1 now gates only its **activation**, which needs separate authorisation. Independent review is required before M2; C3 with O13; T3 together with R2. **The critical path to M1** |
| **R2: staging** (X5, E3, E4: AWS account and IAM, the domain, TLS certificates and backup pins, the database). **Gate A5** decides how the staging manifests are built; **C1** decides who writes them | Staging manifests; `migrate`; the release variables (origins, pins); T3; T5 (key rotation drill); the load and failover tests (with OD-10 §6, items 1–4); the signed-in minified tests and a signed-in baseline profile |
| **R3: Play Console** (X1, X2): the account, package name (after the naming review, F26), the protected `play-release` Environment (D5) **before** the upload-key secrets (D6) | The first `release-android.yml` run (not run: the Environment does not exist, and dispatching would create it unprotected); the `.aab` on the internal track; the Play declarations |
| E1 (Play Integrity) | C4 and T4 (with O7 and OD-9). **T4 stays unresolved**: neither activated nor removed |
| E7 (FCM), E8 (penetration test), E9 (DPDP review), E10 (web deletion page) | C5; production issuance review; O6 and retention; C1 |
| X4 (DID pool, CPaaS, forwarding) | C8; device cases M3–M5 (milestone M3); the voice assistant (with OD-17, the LLM category approval) |

## 4. Hardware (X3) and owner-held device sessions

| Need | Unblocks |
|---|---|
| **The owner's phone, a new session with the owner's approval** (app reinstall, USB debugging; the earlier session was cleaned up on 2026-11-07) | Device cases **M11, M12** |
| **Jio, Vi and BSNL SIMs** | **M2** on those carriers |
| **A dual-SIM setup and a second SIM** | **M10** |
| **An Android 8 or 9 phone** | **M9** |
| Any physical phone | Measured startup time with the baseline profile (`BASELINE-PROFILE.md`); T3 with R1 and R2 |

## 5. Safe preparation that remains

Per integration (SMS, telephony, ASR, TTS, LLM, orchestration, fraud, PostgreSQL, cloud, monitoring, FCM, Play Integrity): [`INTEGRATION-DEPENDENCY-MATRIX.md`](INTEGRATION-DEPENDENCY-MATRIX.md) (2026-11-10), with the local preparation that is safe now and what is deliberately not prepared. What each must satisfy before activation (prerequisites, security requirements, test evidence): [`INTEGRATION-READINESS-REQUIREMENTS.md`](INTEGRATION-READINESS-REQUIREMENTS.md). The local-model adapters' test coverage and how to run their real-runtime tests: [`LOCAL-MODEL-ADAPTER-TESTING.md`](LOCAL-MODEL-ADAPTER-TESTING.md).

**All of it is done:**
- the decision briefs (packet §1–§4);
- the M1 approval requests;
- the OD-12 review package and engagement brief;
- the OTP architecture record (ADR-0033);
- the device-test checklist and evidence template;
- the forwarding-test preparation;
- the staging configuration contract (`INTERNAL-TESTING-SETUP.md` C2–C5);
- the per-integration readiness requirements and the local-model test procedure (2026-11-10);
- the integration matrix's safe items:
  - the `integrations` placeholder guard (#290);
  - the observability catalogue (#291);
  - the provider-port conformance suites for STT, TTS, LLM and telephony (#292).

**Not prepared, on purpose:**
- **implementations of choices not yet made:** sign-out, the OTP policy (the UX options 3a, 3b and 3c are now decided and implemented);
- **staging manifests:** gate A5;
- **provider code beyond the delivery interface:** that waits on R1 and the OD-12 review.

Writing them first would pre-empt a decision or a review.

## 6. The 27 open decisions, classified (2026-11-10)

The owner asked for each open register decision to be classified, and for the routine ones to be resolved under the standing delegation.

**Result: none of the 27 is a routine decision that can be delegated.** Each sits in at least one category the owner reserves, or depends on something external.

- **The owner-reserved categories** (the standing autonomy rules; `ARCHITECTURE_FREEZE.md` §6.1): authentication and identity, cryptography and session security, privacy, consent, retention and deletion, legal, production security, and T4.
- **The routine choices that could be delegated** (3a, 3b, 3c and F14) were not register rows. They are decided and implemented (§5 of the decision packet).

| Category | Decisions | Why it cannot be delegated |
|---|---|---|
| **Authentication, identity or session security: needs the independent review (OD-12) and the owner** | O1 (ratify ADR-0029), O2 (ADR-0030 D30.3, stored subscriber ids), O3 (OTP backoff), O4 (session lifetime), O5 (SIM-swap policies), O7 (ratify ADR-0031), O8 (what a restricted session may do), O12 (step-up before deletion), O17 (access-token revocation), OD-1 (a reviewer sign-in path), OD-14 (sign-out) | Each changes how identity is proved, how sessions live, or what an authenticated session may do. These are reserved, and high-risk items need rule 8's review |
| **Privacy, consent, retention, deletion or legal: the owner, with privacy and legal review (E9, L1–L6)** | O6 (device-history retention), O9 (may an erased number sign up again), O13 (the web deletion approach), O14 (attestation retention), O15 (immediate deletion or a grace period), O16 (A53d, the deletion confirmation), OD-2 (server-side allowlist sync), OD-4 (consent UI), OD-6 (family sharing), OD-7 (business analytics) | Each decides what personal data is kept, shared or deleted, or what is consented to. These are reserved categories |
| **External approval or spend** | OD-9 (workload identity federation for the Play Integrity verifier; cloud), OD-17 (speech and voice languages; paid vendors, deferred by the owner) | A cloud resource or a paid service. Both also touch reserved areas (T4; voice data) |
| **Product decision that depends on an external or deferred input** | OD-3 (spam-reputation policy), OD-5 (voice choice in the app) | OD-3 has no reputation data source in the product; a source is an external service. OD-5 depends on OD-17 (speech, deferred). Deciding either now would decide nothing that can be built |
| **Architecture (ADR) with an external dependency** | OD-8 (the frontend stack) | It decides a new technology stack (an ADR), for the web deletion page, which also needs a domain and hosting (E10, external). The owner has deferred the web dashboard |
| **Governance, which cannot be waived** | OD-12 (an independent human reviewer) | Rule 8 needs a human independent of the drafter. Claude Code cannot be that reviewer (rule 7) |

**Physical-device and carrier dependencies** are not register decisions. They are listed in §4: M2 on Jio, Vi and BSNL, M9, M10, M11 and M12.
