# Lily AI product roadmap: six phases

- **Source:** the owner's six-phase product roadmap from earlier project planning. **Confirmed by the owner on 2026-11-05** and recorded here for the first time; until now it was not in the repository (`PRODUCTION-READINESS.md` §6).
- **These are product phases.** They are not the repository's engineering phases (Phases 1–19, units of execution with their own reports; `ROADMAP-INVENTORY.md` §1). **Cite them as "product Phase N"**: "Phase 1" alone is ambiguous.
- **There is no seventh phase.**
- **No phase is complete.** A phase is complete only when its exit evidence (§3) exists, not because its documents do. Every phase below has open work.
- **Unchanged by this record:**
  - the release milestones M0–M3 (`phase19/PRODUCTION-READINESS-PLAN.md` §2);
  - the accepted ADRs and frozen invariants;
  - the security gates (OD-12's independent review for high-risk decisions);
  - the spending policy (plan §4).
- **As of:** `main` `221655e`; product Phase 5's physical-device lines re-checked on 2026-11-07 at `main` `5b41b12` (#256). Task IDs are the inventory's (`phase19/ROADMAP-INVENTORY.md` §3) and the tracker's (`phase19/EXTERNAL-DEPENDENCIES.md`).

## 1. The phases, as confirmed

| Product phase | Scope (the owner's words) | Release milestones it maps to |
|---|---|---|
| **1. Engineering Foundation** | Core architecture, CI/CD, testing, security foundations and evaluation infrastructure | **M0** |
| **2. Authentication & Security** | Phone OTP, DPoP sessions, device security, account deletion, privacy, consent and memory controls | **M0** (built); **M1** (OTP delivery); **M2** (web deletion, security review, the identity decisions) |
| **3. Core Product & AI Experience** | Calling, multilingual assistant, call screening, conversation analytics, post-call reports and Android UX | **M0** (Android UX, on-device screening); **M1** (testers use screening); **M3** (the assistant, analytics, reports) |
| **4. Backend & Infrastructure** | Staging, integrations, observability, secure configuration, memory and learning pipeline, model abstraction and backend operations | **M1** (staging); **M2** (production infrastructure); **M3** (memory and learning, the model and telephony integrations) |
| **5. Real-world Validation & Improvement** | Physical-device tests, carrier testing, AI evaluation, regression, safe model improvement, versioning and rollback | **M1** (sign-in on a phone, T3); **M2** (T2–T5); **M3** (AI evaluation, safe model improvement) |
| **6. Production & Play Store Release** | Legal/privacy review, Play policies, signing, internal testing, gradual rollout and production monitoring | **M1** (signing, internal testing); **M2** (legal, Play policies, rollout, monitoring) |

The milestones keep their definitions:
- **M0:** the current baseline.
- **M1:** Play internal testing (`ROADMAP-INVENTORY.md` §4.1).
- **M2:** public production (§4.2).
- **M3:** the product capabilities (`PRODUCTION-READINESS.md` §6).

## 2. The inventory, by phase

**Every open item is placed in one phase only,** so the counts add up to the inventory's:
- 15 coding (C1–C15);
- 4 testing (T2–T5);
- 15 external (E1–E10, X1–X5);
- 6 legal (L1–L6);
- 30 owner decisions: tracker O1–O9 and O12–O17, and OD-1–OD-18 less the closed OD-11, OD-13 and OD-15.

High-risk items (H) overlap these and are listed with their phase.

**Cross-phase gates**, not counted in a phase:
- **OD-12:** an independent human reviewer. It gates every high-risk item.
- **OD-10:** the completion criteria.
- **The spending policy's category approvals** (plan §4).

### Product Phase 1: Engineering Foundation (M0)

- **Built, and verified in CI:**
  - the monorepo and its toolchains;
  - CI/CD: the `pr-*` workflows, `ci-gate`, `hardening` with race runs and fuzzing, emulator device tests on API 35/28/26, R8 functional tests, the dependency audit, image builds;
  - unit, integration and real-PostgreSQL tests;
  - security foundations: log redaction, `verify-full` TLS, fail-closed services, secrets as mounted files;
  - evaluation infrastructure: `packages/go/evaluation`, `evalstore`, `evalsubjects`, the offline speech scorer.
- **Remaining:**
  - **2 owner decisions:** OD-16 (base-image updates), OD-18 (the held Dependabot PRs #183–#185, #212);
  - **owner-held engineering work outside the inventory's counts:** engineering Phase 15 (not closed; blocker groups in `phase15/READINESS_AUDIT.md` §10.20.6), engineering Phase 16 (a blocked table, `phase16-platform/README.md` §M1), and the telephony P1 programme. Much of it is in the owner's uncommitted worktree.
- **Blocked by:** owner decisions only.

### Product Phase 2: Authentication & Security (M0 → M1 → M2)

- **Built, and verified in CI and on emulators:**
  - phone-OTP sign-in with a failover sender (fake delivery only);
  - DPoP-bound sessions and refresh;
  - Keystore device keys; the session lifecycle (C16–C18);
  - in-app account deletion; the deletion-code seam;
  - backup and transfer exclusion;
  - structural memory consent (`packages/go/memory`).
- **Coding, 10:**
  - C2, the SMS adapter (after E2);
  - C3, the deletion-code adapter;
  - C1, the web deletion page (needs OD-8 and E10);
  - C4, the Play Integrity wiring;
  - C6, attestation housekeeping;
  - C10, token revocation;
  - C11, the subscriber directory;
  - C12, device history;
  - C14, `client_id`;
  - C15, `BiometricPrompt`.
- **External, 3:** E2 (SMS and DLT), E1 (Play Integrity), E8 (the cryptographic review or penetration test).
- **Owner decisions, 18:** O1–O9, O12–O17 (the ADR ratifications, the identity policies, account deletion D1–D9), OD-4 (consent UI), OD-9, OD-14. (OD-13 closed, #258.)
- **High-risk, needing OD-12:** H1 (ADR-0032), H2 (= O17), H4–H6 (= O1, O2, O7, if classed high-risk), H7 (a refused refresh proof).
- **Blocked by:** E2, and the SMS-and-DLT category approval, block every sign-in. The rest wait on decisions and OD-12.

### Product Phase 3: Core Product & AI Experience (M0 → M1 → M3)

- **Built, and verified in CI and on emulators:**
  - **Android UX:** onboarding, Protection, the lists, the calls feed, Settings, account deletion, app language (D1–D9), navigation to specification (F25), system-bar insets (U1), large text and TalkBack checks;
  - **on-device call screening** (ADR-0002 stage 1);
  - **the six-language app packaging,** with **zero translations**;
  - the conversation and voice packages, as scaffolding.
- **Coding, 2:** C7 (server-side list sync; H3, OD-2), C8 (forwarding provisioning; X4).
- **Owner decisions, 6:**
  - OD-2 and OD-3 (allowlist sync; spam reputation);
  - OD-5, OD-6 and OD-7 (voice choice; family sharing; business analytics);
  - **OD-17 (speech, deferred).**
- **Also open:**
  - **the translations**, which need the translators and reviewers (Q0, not engaged);
  - **F26**, the Lily AI branding cleanup, which waits on the naming review;
  - **the capabilities with no task list yet:** the multilingual voice assistant, post-call reports (A27), conversation outcome analytics.
- **High-risk:** H3 (= OD-2).
- **Blocked by:**
  - **the assistant:** OD-17, X4, the telephony staging gate (P1-38, NO-GO), and the forwarding and LLM category approvals;
  - **the translations:** the owner's engagement of people.

### Product Phase 4: Backend & Infrastructure (M1 → M2 → M3)

- **Built, and verified in CI:**
  - identity and edge-api with metrics, health and trace spans (log exporter);
  - secure configuration (secret files; the release endpoint preflight);
  - the model abstraction (provider ports for speech; `packages/go/evaluation` engines);
  - the memory package.
- **Coding, 2:** C5 (FCM; E7), C13 (per-source rate limiting; E6).
- **External, 7:**
  - X5, the staging hosts, with E3 (signing-key operations) and E4 (the database, PITR);
  - E5, the tracing backend;
  - E6, the trusted-proxy model;
  - E7, FCM;
  - X4, the DID pool and carrier forwarding matrix.
- **Owner decisions, 0.** (OD-15, an unconfigured identity refusing to start in staging and production, closed, #260.)
- **Also open:**
  - the deployment manifests (`INTERNAL-TESTING-SETUP.md` C1, owner's choice);
  - the telephony gateway's staging (owner-held P1 programme);
  - **the capabilities with no task list yet:** the memory and continuous-learning pipeline, and the CPaaS and LLM integrations.
- **Blocked by:** the staging category approval (X5); the forwarding and LLM approvals; consent (OD-4) for learning.

### Product Phase 5: Real-world Validation & Improvement (M1 → M2 → M3)

- **Built, and verified:**
  - emulator regression on every relevant PR, and the API 35/28/26 device suites;
  - R8 functional tests;
  - a baseline profile (its gain is not measured);
  - the evaluation harness;
  - **the on-device checklist, partly run:** `phase19/PHYSICAL-DEVICE-TEST-CHECKLIST.md` (9 cases), on one phone (a Redmi 9 Power, Android 12) and one carrier (Airtel), 2026-11-06 and 2026-11-07. M1, M2 (Airtel only), M7 and M8 Passed; M6 is accepted with a recorded finding (`phase19/DEVICE-TEST-SESSION-2026-11-05-redmi9power.md`). The other cases are not run;
  - **the forwarding checklist, prepared and not run:** `phase19/FORWARDING-TEST-PREPARATION.md` (3 cases).
- **Testing, 4:**
  - **T2**, 12 carrier cases: 9 on-device cases, of which 5 have a result on one phone (M2 on Airtel only), and 3 needing forwarding. **T2 stays open:**
    - M2 on Jio, Vi and BSNL, M9 and M10 need more phones and SIMs;
    - M11 and M12 need a new session, with the owner's approval;
    - M3–M5 need forwarding (X4);
  - **T3**, sign-in, refresh and deletion on a phone against live hosts;
  - **T4**, Play Integrity verdicts;
  - **T5**, the key-rotation drill in staging.
- **External, 1:** X3 (phones and SIMs). **One phone with an Airtel SIM was provided by the owner and used** (2026-11-05 to 2026-11-07). More are still needed: Jio, Vi and BSNL SIMs, an Android 8 or 9 phone, and a second active SIM for the dual-SIM case. X3 stays open.
- **Also open, with no task list yet:**
  - **AI evaluation on consented recordings** (OD-17 A2, deferred);
  - **safe model improvement** (high-risk, OD-12);
  - the startup-gain measurement (needs X3);
  - **rollback:** exercising the runbooks, which are all unexercised.
- **Blocked by:** X3 for the remaining physical tests (one phone has been used; more phones and SIMs are needed); the owner's approval of a new session for M11 and M12; the hosts (X5) and E2 for T3; Play (X1) and E1 for T4; E3 for T5.

### Product Phase 6: Production & Play Store Release (M1 → M2)

- **Built, and verified in CI with a throwaway key:**
  - the signed-bundle pipeline;
  - version validation;
  - the endpoint and pin preflight;
  - Play App Signing support;
  - backup exclusion.
- **Coding, 1:** C9 (a reviewer sign-in path for Play review; OD-1).
- **External, 4:** X1 (the Play Console), X2 (the upload key and the Environment), E9 (the DPDP privacy review), E10 (the web deletion page's domain and hosting).
- **Legal, 6:** L1–L6 (the DPDP review, erasure retention, the legal hold, the privacy policy, the Data safety answers, the contacts disclosure).
- **Owner decisions, 2:** OD-1, OD-8 (the web frontend stack).
- **Also open:**
  - **the Q7 legal gate:** counsel reviews the 54 translated rows before each language ships;
  - the Play declarations: Data safety, privacy policy URL, Delete account URL, permissions, content rating;
  - the closed test, if the account is personal (14 days);
  - internal testing;
  - **gradual rollout and production monitoring** (alerting; E5);
  - **the naming review before the listing** (F26).
- **Blocked by:** the Play-registration category approval; counsel (owner engagement and spend).

### Totals

| Product phase | Coding | Testing | External | Legal | Owner decisions |
|---|---|---|---|---|---|
| 1 | 0 | 0 | 0 | 0 | 2 |
| 2 | 10 | 0 | 3 | 0 | 18 |
| 3 | 2 | 0 | 0 | 0 | 6 |
| 4 | 2 | 0 | 7 | 0 | 0 |
| 5 | 0 | 4 | 1 | 0 | 0 |
| 6 | 1 | 0 | 4 | 6 | 2 |
| Cross-phase gates | — | — | — | — | 2 (OD-10, OD-12) |
| **Total** | **15** | **4** | **15** | **6** | **30** |

**Not countable yet:** these capabilities have no task list. They need one before they can be counted:
- the multilingual voice assistant;
- post-call reports;
- conversation analytics;
- memory and learning;
- AI evaluation;
- safe model improvement.

## 3. Exit evidence, by phase

**A phase exits only with this evidence.** No phase has exited.

| Product phase | Exit evidence |
|---|---|
| 1 | OD-16 and OD-18 decided; the owner-held engineering Phase 15/16 items closed or explicitly deferred by the owner; CI green on `main` |
| 2 | A real code delivered by SMS (E2, C2), and sign-in, refresh and deletion on a physical phone against staging (T3); E8 report with findings closed; the identity decisions taken, and the high-risk ones with independent review (OD-12) |
| 3 | On-device screening recorded on physical phones (T2's 9 cases); reviewed translations for each language claimed; for the assistant, OD-17 decided and calls answered through the voice path (M5) |
| 4 | Staging deployed and verified, with health, metrics and tracing; the key-rotation drill done (T5); production infrastructure verified; integrations live only under their category approvals |
| 5 | T2 (12 cases), T3, T4 and T5 recorded on physical phones and live hosts; AI evaluation on consented recordings; a rollback exercised |
| 6 | L1–L6 and Q7 signed off by counsel; Play declarations accepted; internal testing used by testers; a staged rollout with production monitoring in place |
