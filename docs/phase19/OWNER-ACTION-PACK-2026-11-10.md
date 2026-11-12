# Owner action pack: the path from today to Lily AI's launch (2026-11-10)

- **As of:** `main` `1fed94f`, re-verified on 2026-11-10:
  - CI on `main` is green, and every nightly passed on 2026-11-10.
  - The open PRs are #271 (a draft, not selected; never merged), #35 and #7 (the owner's; not touched).
  - Identity and edge-api are unchanged since the OD-12 review package was written.
- **What this is:** one place for every action only the owner can take, in priority order, with exact prerequisites. It consolidates the [blocker map](BLOCKER-MAP-2026-11-10.md), the register ([`ROADMAP-INVENTORY.md`](ROADMAP-INVENTORY.md) §3), [`M1-APPROVAL-REQUESTS.md`](M1-APPROVAL-REQUESTS.md), [`INTERNAL-TESTING-SETUP.md`](INTERNAL-TESTING-SETUP.md) and the OD-12 [package](OD-12-OTP-REVIEW-PACKAGE.md) and [brief](OD-12-REVIEWER-ENGAGEMENT-BRIEF.md).
- **What this is not:**
  - **It decides nothing, and sends nothing.** No reviewer, provider, cloud account or Google account has been contacted or created.
  - **It changes no owner decision or security boundary,** and no milestone exit criterion.
  - **No price below is a quote.** Each is a public list price or a vendor's published guide, cited in `M1-APPROVAL-REQUESTS.md`, or marked unknown.
- **Naming collision:** "C1"–"C5" name both coding items (`ROADMAP-INVENTORY.md` §3.1: C1 the web deletion page, C2 the SMS adapter, C3 deletion codes, C4 Play Integrity wiring) and setup steps (`INTERNAL-TESTING-SETUP.md`: C1 deployment manifests, C2–C5 migration, configuration and health). Outside §7's R2 table, a bare C-number is a coding item.
- **Milestone:** **M0 only.** M1's exit evidence (`OD-10-DECISION-2026-11-09.md`) is:
  - a release run of `release-android.yml` from the protected Environment;
  - the `.aab` on the internal track;
  - **a tester signing in on a physical phone against staging**, and turning blocking on (T3 started).
  - None of these exists yet. **Production readiness is not claimed.**

## 0. The owner's actions, in order

| # | Action | Why first | Costs money or shares code? | Detail |
|---|---|---|---|---|
| **1** | **Decide OD-12R** (2026-11-11): the owner stopped external reviewer hiring, so how is rule 8 met? A: an independent human reviewer who is not a firm; **B (recommended, with A before M2): amend rule 8 so M1 internal testing can proceed on internal assurance evidence**; C: amend it generally (not recommended); D: no change, and M1 cannot be met. See [`OD-12-REVISED-DIRECTION-2026-11-11.md`](OD-12-REVISED-DIRECTION-2026-11-11.md) | Without it, ADR-0033, the SMS adapter on M1's critical path and every authentication decision stay blocked | B: none. A: sharing source code under a confidentiality agreement | That record, §3 |
| **2** | **Complete the naming review (F26)**, and confirm the permanent Play package name (D2) | R1's DLT templates must match the SMS text exactly, and Play fixes the package name at the first upload. Both wait for it | Trademark counsel, if used (owner's choice) | §7 |
| **3** | **Approve R1:** an SMS provider, a fallback, and India DLT registration (domestic route only) | DLT is the slowest external step. Starting it early shortens M1 the most | **Yes:** DLT entity about ₹5,900 including GST; about ₹0.10–₹0.30 per SMS (published guides) | §7 |
| **4** | **Approve R3:** the Google Play developer account | Can run in parallel; distribution to testers needs it | **Yes:** US$25, one-time | §7 |
| **5** | **Approve R2:** staging for identity and edge-api, and decide gate **A5** (how staging manifests are built) and setup step **C1** (who writes them) | M1's tester signs in against staging | **Yes:** recurring cloud costs while running (EKS control plane about US$73 a month; the rest unpriced) | §7 |
| **6** | **Decide OD-14 (sign-out)**, so it joins the review as S7 | One review instead of two | No | §6 |
| **7** | **Arrange the device sessions and hardware** (§8) | T2 and T3 need phones and SIMs | Hardware, if bought | §8 |
| **8** | **Work through the other decisions** (§6), in the order the review and the privacy review (E9) allow | They gate M2, M3 and individual features, not M1 | Some need privacy and legal review (E9, L1–L6) | §6 |

**Added 2026-11-11 (revised direction):**
- **Review draft #306 for gate A5:** the identity and edge-api staging overlay. Approving A5 lets it merge; applying it still needs R2.
- **Decide ADR-0034** (streaming speech-to-text transport). It unblocks the Google and Deepgram adapters as code, not their activation (OD-17).
- **When OD-12R is decided,** draft #301 (the delivery-only SMS adapters) follows that route. It is not activated without R1.

**Added 2026-11-12, after OD-12R (B), gate A5 and ADR-0034:**
- **Decide ADR-0033 and #301 for M1 under rule 8a:** [`ADR-0033-M1-INTERNAL-ASSURANCE.md`](ADR-0033-M1-INTERNAL-ASSURANCE.md) §3. Also choose the **SMS-pumping control (F-1)** before any real SMS: an M1 tester allowlist, a global send budget, or both.
- Gate A5 is done: the staging overlay is merged (#306), but not applied. ADR-0034 is accepted; the Google and Deepgram adapters are being built.

**The critical path to M1:** actions 1–5 in parallel. Then:
1. the reviewer's report;
2. the owner decides ADR-0033;
3. the delivery adapter (C2) is built and reviewed before merge (S5);
4. R1 is activated;
5. staging is up (R2);
6. the release run (R3);
7. a tester signs in on a physical phone.

## 1. What can proceed without the owner: nothing, today

**Checked on 2026-11-10 against the blocker map, the register and the integration matrix:** no engineering task remains that needs none of an owner approval, an external service, hardware or a model download.

| What was checked | Result |
|---|---|
| Coding items in the register (`ROADMAP-INVENTORY.md` §3.1) | All 15 wait on a decision, a review, R1–R3 or hardware |
| The integration matrix's safe preparation (§2) | Done (#290, #291, #292) |
| The local-model adapters | Fixed and pinned (#295, #296). More real-runtime coverage needs Piper installed, an Ollama model pulled or whisper.cpp built: downloads that are **not authorised** |
| Documentation of the external steps | Complete: approval requests, setup checklist, review package, engagement brief, device checklist, forwarding preparation, readiness requirements |
| Owner decisions | None of the 27 can be delegated (blocker map §6) |

**Claude Code will:**
- keep the blocker map current;
- keep this pack current when anything changes;
- **resume engineering the moment any item in §0 clears.** The work each one unblocks is listed in §6 and §7.

## 2. OD-12 first: why it is the highest-impact blocker

**Rule 8 of `ARCHITECTURE_FREEZE.md` §6.1** requires an independent human review before the owner accepts a high-risk decision. Claude Code cannot be that reviewer (rule 7), and its reviews never count (rule 9). **No high-risk decision is accepted for want of a reviewer** (rule 10).

**What one review engagement unblocks:**
- **M1's critical path:**
  - ADR-0033 (OTP Option A);
  - then the delivery-only SMS adapter (C2), reviewed before merge (S5);
  - then activation after R1.
- **Batch A, the OTP review** (the package's scope S1–S7, ready now):
  - ADR-0033 and its open questions Q2–Q10, including **the India-only country allowlist (Q2)**, the per-source and global limits, and the resend cooldown;
  - O3, OTP backoff;
  - the post-merge review of #270, the development mock (S4);
  - OD-14, sign-out, as S7 if the owner chooses option A or B;
  - OD-1, the Play reviewer sign-in path, which changes OTP sign-in: added to the scope **if** the owner wants it reviewed together.
- **Batch B, other authentication decisions** (needs its own scope; not yet packaged):
  - O1, O2 and O7: ratifying ADR-0029, ADR-0030 D30.3 and ADR-0031 (H4–H6). These are high-risk **if the owner classifies them so**.
  - O4 (session lifetime), O5 (SIM-swap policies), O8 (what a restricted session may do), O12 (step-up before deletion).
  - O17 (server-side token revocation, an ADR-0010 amendment, H2).
  - H7 (when a refused refresh proof ends the session; F1).
  - ADR-0032 (email and Google sign-in, H1), only if the owner pursues it.
- **Other high-risk decisions that need an independent review, but not necessarily a security reviewer:**
  - O9: changes a domain invariant, INV-ID-7;
  - OD-2: an ADR-0012 amendment, H3.
  - Both are privacy and legal decisions first (E9).

**A recommendation for the owner to decide:** engage Batch A now, and ask the same reviewer to quote Batch B separately. Batch A alone unblocks M1. Batch B unblocks M2's identity work, and can follow.

> **2026-11-11:** the shortlist, the recommendation, the Batch A and B comparison, the budget ceiling and the archive plan are in [`OD-12-REVIEWER-SHORTLIST.md`](OD-12-REVIEWER-SHORTLIST.md). The owner delegated reviewer discovery to Claude Code; contact, choosing the reviewer, agreements, payment and sharing code still need the owner's explicit approval.

## 3. Reviewer selection checklist

**Independence (required; the candidate signs the declaration in the brief §4):**
- [ ] Did not write, direct or approve any code or decision under review. **Not Claude Code, not the owner.**
- [ ] No financial or contractual tie to Twilio, MSG91 or any SMS or OTP provider under consideration, or the tie is disclosed and the owner accepts it.
- [ ] No relationship with the owner that would prevent an impartial report.
- [ ] Commits to reporting findings that contradict the proposed decision.

**Qualifications (required):**
- [ ] Has reviewed authentication systems that use one-time codes. References or published work to show it.
- [ ] Knows OWASP ASVS v4 chapter 2 and NIST SP 800-63B (out-of-band authenticators).
- [ ] Understands SMS threats: SIM swap, SS7 interception, SMS pumping (toll fraud), enumeration, and lockout denial of service.
- [ ] Can read Go and run Go tests. Docker is needed for the throwaway PostgreSQL, and Linux or macOS for `-race`.

**Helpful:** India's TRAI DLT rules; the DPDP Act's processor obligations; PostgreSQL concurrency; for Batch B, OAuth and JWT profiles, DPoP (RFC 9449), Play Integrity.

**Scope and deliverables (agree in writing):**
- [ ] **Batch A:** S1–S6 (the package §2), plus S7 if sign-out is chosen, plus OD-1 if the owner adds it. **Out of scope:** access tokens and DPoP, Play Integrity, account deletion (except the shared `checkCode`), and the Android app.
- [ ] **The commit under review** is fixed and named: for example `main` at the date of engagement.
- [ ] **The report gives, for each scope item:**
  - reviewed or not reviewed;
  - its conclusion;
  - findings with a severity, file and line, evidence and a recommended fix;
  - answers to the package's §7 questions;
  - a recommendation on ADR-0033 (accept, amend or reject). **The decision stays the owner's.**
- [ ] **A re-review of the fixes** for critical and high findings is included, or priced separately.

**Timeline:**
- **The review work:** an estimate of **3 to 5 reviewer-days** for S1–S6, and about 1 more for S7 (brief §5). **It is not a quote**; the reviewer gives the real figure.
- **Batch B:** not estimated; ask the reviewer to scope it.
- **Elapsed time** depends on the reviewer's availability and on the owner's contracting (selection, confidentiality agreement, engagement). **No calendar estimate is made here.**
- **What follows the report:** the owner's decision on ADR-0033, then fixes. Each fix is a change reviewed the same way.

**Cost categories (no amounts known; get written quotes):**
- [ ] The review fee: a **fixed fee** for the agreed scope, or a **day rate** with an estimate of days.
- [ ] A re-review of fixes: included, or a separate fee.
- [ ] Batch B, if engaged: a separate quote.
- [ ] Taxes on professional services (for example GST, if the reviewer is in India).
- [ ] **Optional:** the owner's own legal review of the confidentiality agreement and engagement terms.
- **Not needed:** no cloud, staging or provider account. Everything runs locally from the repository.
- **Separate decisions with their own spend:** the penetration test (E8) and the privacy review (E9). The same firm may quote them, but approving one does not approve the others.

**Confidentiality and source-code access (the owner decides before sending anything):**
- [ ] **A confidentiality agreement first,** before any code is shared.
- [ ] **How code is shared. Recommended:** an **archive of `main` at the named commit.** It is least privilege: one fixed version, no ongoing access, and nothing beyond `main`. The alternative is time-limited **read-only** access to the private repository.
- [ ] **What is shared:** the repository's `main` only. **Never** the owner's uncommitted work, local branches, credentials, staging access or provider accounts. None is needed.
- [ ] **No personal data:** the tests use synthetic values; device evidence in the repository is redacted to the last two digits.
- [ ] **Terms to agree:**
  - use of the code only for the review;
  - no use to train models;
  - return or deletion of the materials at the end;
  - the report is confidential to the owner;
  - responsible handling of any finding.
- [ ] Revoke access, if any was granted, when the engagement ends.

**Engagement steps:**
1. The owner approves the scope (Batch A; S7 if OD-14 is decided; OD-1 if wanted), the budget ceiling, the sharing method, and who signs.
2. **Claude Code has done the discovery and shortlisting** ([shortlist](OD-12-REVIEWER-SHORTLIST.md), 2026-11-11). On the owner's explicit approval, the first-contact message goes to the shortlisted candidates: sent by the owner, or by Claude Code from the owner's mailbox if authorised. **Nothing is sent without that approval.**
3. Each candidate returns a profile or CV, the signed conflict-of-interest declaration, availability, and a quote.
4. The owner selects a reviewer against this checklist, and signs the confidentiality agreement and the engagement.
5. The owner shares the archive (or read-only access) at the named commit, with the package and ADR-0033.
6. The reviewer delivers the report. **The owner** links it from ADR-0033's approval record and from OD-12, and decides ADR-0033.
7. Findings become tracked items. Claude Code implements the fixes, each reviewed before merge. **Only the owner marks OD-12 resolved.**

## 4. Outreach message (ready for the owner to send; not sent)

This updates the brief's §1 with the current commit and the two batches. The brief's channels (§2), criteria (§3) and declaration (§4) still apply.

> **Subject:** Independent security review of an OTP sign-in design (Android app, Go backend), focused engagement
>
> Hello,
>
> I am looking for an independent security reviewer for a focused review of the SMS one-time-code sign-in design of Lily AI, an Android call-screening app with a Go backend. No real SMS provider is connected yet; the aim is to have the design and code reviewed **before** one is.
>
> **Scope (Batch A):**
> - one architecture decision: the identity service generates, hashes and verifies codes, and SMS providers only deliver them (ADR-0033);
> - the existing OTP lifecycle in Go: generation, hashing, expiry, attempt limits, single use, per-number rate limits and lockout;
> - the provider-delivery boundary and failover, and a development-only mock provider;
> - possibly a small sign-out change, and a sign-in path for app-store reviewers.
>
> It is about 4,000 lines of Go in the files in scope, with tests. Everything runs locally with Go 1.26 and Docker. No staging access, credentials or provider accounts are needed.
>
> **Deliverable:** a written report with severity-rated findings (file, line, evidence and fix), a statement on each scope item, and a recommendation to accept, amend or reject the decision. A re-review of fixes for critical and high findings would be welcome.
>
> **A possible second engagement (Batch B),** quoted separately: the access-token and proof-of-possession profile (DPoP), Play Integrity at token issuance, session lifetime and SIM-swap policies.
>
> **Practicalities:** code is shared as an archive at a fixed commit, after a confidentiality agreement. The work is remote. The product launches in India first, so familiarity with TRAI DLT rules and the DPDP Act helps.
>
> **Could you send:**
> - a short profile or CV showing reviews of authentication or OTP systems;
> - a signed conflict-of-interest declaration (attached);
> - your availability;
> - a fixed-fee quote for Batch A, or a day rate with an estimate, and whether a re-review is included.
>
> Thank you,
> [owner's name and contact]

**Attach:** the brief's §4 declaration. **Do not attach code** until the confidentiality agreement is signed.

## 5. Review-readiness checklist

**Ready now (verified 2026-11-10 at `1fed94f`):**
- [x] **The decision under review:** ADR-0033, Proposed, owner-preferred.
- [x] **The package:** scope S1–S6, how to run the evidence, an AI-performed threat analysis (labelled as such), the disclosure about #270, open questions, and the handoff.
- [x] **The code in scope is unchanged** since the package was written: no change under `services/go/identity` or `services/go/edge-api` since `d3d7250`.
- [x] **Evidence on `main`:**
  - `identity-postgres` (real PostgreSQL, `-race`) passed on `063663e`, run 42826366548, with identical identity code;
  - the full CI passed on `1fed94f`;
  - the nightly runs passed on 2026-11-10.
- [x] **Draft #271 is marked not selected,** and kept for reference only.
- [x] **Secrets:** gitleaks passes on every PR and on `main`. The repository holds no credential the reviewer needs.

**The owner's, before sending (§3):**
- [ ] The scope: Batch A, plus S7 (decide OD-14 first), plus OD-1 if wanted.
- [ ] The budget ceiling, and who signs.
- [ ] The sharing method (an archive is recommended), and the confidentiality agreement.
- [ ] **The commit to review:** name it on the day. Claude Code will re-confirm that the evidence is current at that commit.

**At handover (Claude Code can prepare these on request, without sending them):**
- [ ] A one-page index of the files in scope at the named commit.
- [ ] The CI run links for that commit.

## 6. The 27 open decisions

**All 27 are open.** Each recommendation sits in the source shown; none is adopted until the owner decides.

**Review legend:**
- **A:** the OTP review (Batch A);
- **B:** another authentication review (Batch B);
- **IR:** an independent review because the decision is high-risk (not necessarily a security reviewer);
- **—:** the owner alone, or with privacy and legal review where shown.

**From the tracker (`EXTERNAL-DEPENDENCIES.md`; 15):**

| ID | Decision | Category | Independent review | Also needs | Unblocks |
|---|---|---|---|---|---|
| O1 | Ratify ADR-0029 (access token and DPoP) | Authentication | B (H4, if classified high-risk) | — | Ratification of the token profile |
| O2 | ADR-0030 D30.3 (stored subscriber ids) | Identity | B (H5, if classified high-risk) | — | C11 (subscriber directory) |
| O3 | OTP backoff (fixed lockout or exponential) | Authentication | **A** | — | An OTP policy change |
| O4 | Session lifetime (24 h or 90 days) | Session | B | Product | A session policy change |
| O5 | SIM-swap policies | Authentication | B | E7 for the FCM notice | SIM-swap controls |
| O6 | Device-history retention | Privacy | — | **E9** | C12 |
| O7 | Ratify ADR-0031 (Play Integrity at issuance) | Authentication | B (H6, if classified high-risk) | E1, OD-9 | C4; T4 (stays unresolved until then) |
| O8 | What a restricted or unverified session may do | Authentication | B | Product | Tier enforcement |
| O9 | May an erased number sign up again (D1) | Privacy, governance | **IR** (domain invariant INV-ID-7) | E9 | The deletion model |
| O12 | Step-up before deletion (D4) | Authentication | B | — | C15 |
| O13 | Web deletion approach (D5) | Privacy | Partly A (shares `checkCode`) | **E2 (R1), E10** | C3 (dormant server side, #193) |
| O14 | Device-attestation retention (D6) | Privacy | — | E9 | C6 |
| O15 | Immediate deletion or a grace period (D7) | Privacy, product | — | E9 | Policy text |
| O16 | A53d "type the last four digits" (D8) | Privacy, UX | — | — | A UX spec amendment |
| O17 | Server-side token revocation after deletion (D9) | Authentication | **IR** (H2, an ADR-0010 amendment) | — | C10 |

**Defined in other Phase 19 documents (12):**

| ID | Decision | Category | Independent review | Also needs | Unblocks |
|---|---|---|---|---|---|
| OD-1 | A sign-in path for Play's reviewers | Authentication | **A** (if added) | R3 | C9 |
| OD-2 | Server-side allowlist sync | Privacy | **IR** (H3, an ADR-0012 amendment) | E9 | C7 |
| OD-3 | Spam-reputation policy | Product | — | A reputation source (external) | Screening by reputation band |
| OD-4 | Consent UI with identity's consent records | Consent | — | E9, L1 | Consent screens; forwarding readiness |
| OD-5 | Voice choice in the app | Product | — | OD-17 | The voice feature |
| OD-6 | Family sharing | Privacy | — | Its own ADR; E9 | The feature (product Phase 3) |
| OD-7 | Business analytics | Privacy | — | Deferred | — |
| OD-8 | The frontend stack | Architecture | — | E10 (domain and hosting) | Coding item C1 (web deletion page) |
| OD-9 | Workload identity federation for the Play Integrity verifier | External (cloud) | — | E1, O7; T4 | C4 |
| **OD-12** | **Identify an independent human reviewer** | Governance | — (it *is* the reviewer) | Spend; sharing code | §2 |
| OD-14 | Sign-out: option A, B or C | Authentication | **A** (as S7) | — | Sign-out (F8) |
| OD-17 | Speech and voice languages; paid speech vendors | External | — | The speech category (deferred) | The voice pipeline (M3) |

**Counts:**
- Authentication or session: 11, all needing an independent review (A or B).
- Privacy, consent, retention or legal: 10 (two also high-risk: O9 and OD-2).
- External: 2. Product with an external input: 2. Architecture: 1. Governance: 1.
- **Batch A alone covers O3, OD-14 and OD-1, part of O13, and ADR-0033 (not a register row, but on M1's critical path).**

**Related decisions that are not register rows:**
- ADR-0033 and its questions Q2–Q10 (Q2 is the `+91`-only allowlist; the India-only domestic launch scope is recorded, not enforced);
- gate A5 and setup step C1 (staging manifests);
- F26 (naming);
- the nine open OD-10 acceptance items (staging targets; the M2 device and carrier matrix).

## 7. R1, R2 and R3: exact prerequisites

The step numbers are those of `INTERNAL-TESTING-SETUP.md`. Prices are from `M1-APPROVAL-REQUESTS.md` (public, read 2026-11-09, not quotes).

**Before R1 and R3: F26, the naming review.**
- The screen text is done (#267).
- **The launcher label and the Play listing need trademark counsel first** (an owner approval).
- **D2:** the release `applicationId` is `com.callscreen`. Whether it is free on Play is unverified, and it can never change after the first upload.

### R1: SMS provider and India DLT (the critical path)

| Step | What | Who | Prerequisite |
|---|---|---|---|
| A1 | Choose a primary SMS provider that delivers in India, and a fallback (`otpdelivery.Sender` takes up to 3) | Owner | The category approval |
| A2 | Register on a DLT platform (TRAI) as a Principal Entity, and register the sender header | Owner, with the provider | F26 |
| A3 | Register two templates, each with the code as its variable: sign-in, and account deletion | Owner | F26; the exact text |
| A4 | Provider credentials as Kubernetes Secrets **mounted as files** | SRE | R2 |
| A5 | Egress from identity to each provider's API | SRE | R2; a network approval |

- **Route:** India domestic only, with DLT. **No international fallback** (`OTP-PROVIDERS.md`).
- **Code:** a **delivery-only** adapter (ADR-0033), built only after the owner accepts ADR-0033, and reviewed before merge (S5). Activation needs all of A1–A5 and the runbook cases S1–S6.
- **Cost:** a DLT entity costs about ₹5,900 including GST; each SMS about ₹0.10–₹0.30.

### R2: staging for identity and edge-api

| Step | What | Who | Prerequisite |
|---|---|---|---|
| B1 | AWS account, EKS cluster or namespace, IAM (`ap-south-1`). Separate from the telephony programme's gate (P1-38) | Owner | The category approval |
| B2 | PostgreSQL with `sslmode=verify-full` and an explicit CA bundle | SRE | B1 |
| B3 | The public domain: two HTTPS hosts (DNS names; the release build refuses IPs) | Owner | — |
| B4 | TLS certificates, plus a backup key pair generated in advance for each host (two pins) | SRE | B3 |
| B5 | Ed25519 access-token signing key, generated offline; the private half as a mounted Secret file | SRE | — |
| B6 | identity's HMAC key (≥ 32 bytes) as a mounted Secret file | SRE | — |
| C1 (setup step) | **Deployment manifests: gate A5 (how) and who writes them are owner decisions** | Owner | — |
| C2–C5 | Migrate to `0009`; configure identity and edge-api; readiness 200 on both | SRE | B1–B6, C1 |

- **Cost:** the EKS control plane is US$0.10 per cluster-hour (about US$73 a month), **recurring**. Nodes, Aurora, the load balancer, data transfer, the domain and certificates are **unpriced**; use the AWS calculator for `ap-south-1`.
- **What it unblocks:** the release variables (hosts and pins), T3, T5 (the key-rotation drill), load and failover tests (with OD-10 §6 items 1–4), and signed-in tests on the minified build.

### R3: Google Play developer account

| Step | What | Who | Prerequisite |
|---|---|---|---|
| D1 | The Play Console developer account | Owner | The category approval |
| D2 | Confirm the permanent package name | Owner | F26 |
| D3 | Create the app, keeping Play App Signing | Owner | D1, D2 |
| D4 | Generate the upload key offline with `keytool`. It never enters the repository, CI or chat | Owner | — |
| D5 | **Create the `play-release` GitHub Environment with required reviewers, before any secret** | Owner (repository admin) | — |
| D6 | The four upload-key secrets in that Environment | Owner | D4, **D5** |
| D7 | The internal-testing track and its tester list | Owner | D3 |

- **Cost:** US$25, one-time, not refunded if verification fails.
- **Note:** `release-android.yml` has never been run. Dispatching it before D5 would create the Environment unprotected.

## 8. Hardware, devices and local runtimes

**Phones and SIMs:**

| Need | For | Milestone |
|---|---|---|
| A physical phone, with R1 and R2 | **T3:** a tester signs in against staging | **M1** |
| **The owner's phone, in a new session with the owner's approval** (app reinstall, USB debugging; the last session was cleaned up on 2026-11-07) | Device cases M11 and M12 | M2 (T2) |
| **Jio, Vi and BSNL SIMs** | M2 on those carriers (Airtel has passed) | M2 |
| A dual-SIM setup and a second SIM | M10 | M2 |
| An Android 8 or 9 phone | M9 | M2 |
| Any physical phone | Measured startup with the baseline profile | Before release |
| A DID and forwarding (X4) | Device cases M3–M5 | M3 |

**Local model runtimes** (development only; **not on M1's or M2's path**):

| Runtime | State | To get real coverage |
|---|---|---|
| openai-whisper | Verified from an isolated virtual environment. The global install is broken (NumPy 2.5 versus numba) | Optional: fix the global Python, or keep using the virtual environment |
| whisper.cpp | Never run | Build it and download a GGML model (the owner's choice) |
| Piper | Never run | Install a release and a voice (the owner's choice) |
| Ollama | The daemon runs; no model | `ollama pull <model>`, several GB (the owner's choice). Loopback only (ratified policy) |

**Production speech, TTS and LLM vendors are a separate matter:** OD-17 (deferred), the speech and LLM spending categories, and DPDP review. That is M3.

## 9. Dependencies that can be cleared independently (in parallel)

None of these waits for another:

| Track | First owner action | Clears |
|---|---|---|
| Security review | Approve the OD-12 scope, budget and sharing method | ADR-0033, then C2; Batch B later |
| Naming | F26, with trademark counsel if used | R1 templates (A3), Play package name (D2) |
| SMS | R1: provider choice and DLT entity (A1, A2) | The entity and header can start before the templates |
| Play | R3: D1, D4 and D5 | D3, D6 and D7 follow F26 |
| Staging | R2: B1 and B3, and the A5 and setup-step C1 decisions | B2, B4–B6, C2–C5 |
| Privacy | Engage the DPDP review (E9) | O6, O9, O13–O16, OD-2, OD-4, OD-6 |
| Devices | Arrange a device session; buy SIMs or phones | T2 cases M9–M12, and M2 on other carriers |
| Product | OD-14 (before the review is scoped); OD-3; OD-5 (waits for OD-17) | Sign-out (S7); screening bands |

## 10. Keeping it current

Claude Code updates this pack and the blocker map when any decision is recorded, any approval is given, or any item clears. A cleared item starts the work it unblocks automatically, through the usual isolated worktree, tests, PR, required checks and post-merge CI. **Every authentication change still waits for the independent review.**
