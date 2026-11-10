# Blocker map (2026-11-10): what blocks each remaining task, and what each unblocks

- **As of:** `main` `149c97e`, re-verified on 2026-11-10. CI on `main` is green, and the latest nightly runs (2026-11-09) passed.
- **Milestones:** **M0 only.** M1 is not met (OD-10 exit evidence: a release run, the `.aab` on the internal track, and a tester signing in on a physical phone against staging).
- **What this is:** every remaining Phase 19 task, against the one thing it waits for.
  - **Sources:** `ROADMAP-INVENTORY.md` §3 (the register: **27 open owner decisions**), `OD-10-DECISION-2026-11-09.md` §6, `OWNER-DECISION-PACKET-2026-11-09.md`, and `OD-12-OTP-REVIEW-PACKAGE.md`.
  - **When a blocker clears, the work in its row starts automatically,** subject to the gates it lists.
  - **Nothing here decides anything.**
- **Unblocked engineering tasks today: none.** Rounds #262–#281 completed every task that a requirement documents and nothing gates. The one documented follow-up left, a CI build for the Python image, has a trigger that is not met (a deployed Python service; `DEPENDENCY_AUDIT.md` §13).

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
| **Android UX** (packet §3; not counted as register rows) | 3a haptics; 3b the caller lists' "cleared" state; 3c the screening-role screen after a decline (including the "If you say no" forwarding text, which is not true today) | Each choice's implementation, with the tests listed in the packet. Routine Android, not authentication |
| **F14** (not a register row; its status still reads "Open (OD-18)") | Whether Dependabot stops rebasing its PRs (`rebase-strategy`) | A one-line `dependabot.yml` change |
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
| **R1: SMS and DLT** (E2: provider, DLT entity, header, templates, India domestic route, billing) | C2 (a delivery-only adapter per ADR-0033, then OD-12 review); C3 with O13; T3 together with R2. **The critical path to M1** |
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

**All of it is done:**
- the decision briefs (packet §1–§4);
- the M1 approval requests;
- the OD-12 review package and engagement brief;
- the OTP architecture record (ADR-0033);
- the device-test checklist and evidence template;
- the forwarding-test preparation;
- the staging configuration contract (`INTERNAL-TESTING-SETUP.md` C2–C5).

**Not prepared, on purpose:**
- **implementations of choices not yet made:** the UX options, sign-out, the OTP policy;
- **staging manifests:** gate A5;
- **provider code beyond the delivery interface:** that waits on R1 and the OD-12 review.

Writing them first would pre-empt a decision or a review.
