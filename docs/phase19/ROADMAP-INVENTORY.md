# Roadmap inventory: phases, remaining work and release prerequisites

- **As of:** 2026-10-26, `main` `93fe2cb` (post-merge CI 156/156 green), plus #194.
- **Method:** every count below is of items **defined in a named document or verified in the repository**. Nothing is estimated. Where documents conflict, the conflict is reported (§1), with the source treated as authoritative.
- **Not claimed:** that Phase 19 is complete, or that the app is ready for Google Play.

## 1. Phase vocabulary and conflicts

The repository uses three different meanings of "phase". None defines a phase after 19.

| Vocabulary | Where | Meaning | Status |
|---|---|---|---|
| **Project phases** 1–5, 10A–10F, 10.5, 11A–11E, 12–16, 18, 19 | `ARCHITECTURE_FREEZE.md`, `DOMAIN_FREEZE.md`, `UX_FREEZE.md`, `docs/phase13`–`phase19`, `docs/hardening`, `docs/evaluation` | Units of execution, each with its own reports | See §2 |
| **Rollout stages** "Phase 1 (launch) / Phase 2 / Phase 3 (exit)" | ADR-0002 to ADR-0009 (one list per ADR) | How each component evolves **after** launch (for example, ADR-0003: Exotel and Plivo at launch, then traffic shifting, then a direct SIP trunk) | Not project phases. No code task is scheduled against them. |
| **Telephony programme** "Phase 19, P1-xx" | `docs/telephony/P1-40`…`P1-50` on `main`; P1-01…P1-39 and P1-49, P1-51…P1-53 **only in the owner's uncommitted worktree** | The telephony-gateway fence and staging programme | Owner-held. Its gate (P1-38) records staging **NO-GO** with items A1–A12 unapproved. |

**Conflicts found:**
1. **"Phase 19" names two programmes:** the telephony P1 programme and this directory's identity, Android and release programme. **Authoritative:** for telephony, the P1 documents and `docs/phase15/READINESS_AUDIT.md` §10.20.6; for release work, `docs/phase19/` (this file, `RELEASE-READINESS-ASSESSMENT.md`, `EXTERNAL-DEPENDENCIES.md`). This inventory covers the release programme. The telephony programme's own task list is not on `main`, so it is **not counted** here.
2. **Decision IDs collide:**
   - Phase 15 and the telephony programme use D8–D11 (`READINESS_AUDIT.md` §10.20.6; P1-38);
   - `ACCOUNT-DELETION.md` §5 uses D1–D9.

   **To disambiguate:** cite account-deletion decisions by their tracker IDs, O9–O17 (`EXTERNAL-DEPENDENCIES.md` §2), which are unique.
3. **No phase defines completion criteria.** Searching the repository for exit, completion or acceptance criteria finds none for Phases 15, 16, 18 or 19. Phase 19's working definition so far is the readiness blockers (`RELEASE-READINESS-ASSESSMENT.md` §7–§8) and the tracker. **Defining them is an owner decision** (OD-10 below).
4. **Earlier phases are open, not closed:**
   - Phase 15 is "NOT closed" (`READINESS_AUDIT.md` §10.20.6: blocker groups 1, 3, 4, 5, 6, 8, 9, plus #35 and #44);
   - Phase 16 is open, with a "Blocked, with the decision each needs" table of 7 rows (`docs/phase16-platform/README.md` §M1);
   - Phase 18 is ADR-0029's ratification brief, still pending (O1).
5. **Stale statements:**
   - `ARCHITECTURE_FREEZE.md` says compileSdk 35, but the build is on 36 since #147. It is a frozen historical record, so it is not edited.
   - Readiness §8.3 said migrations "through `0008`". It is `0009` since #193, and is corrected in #194.

## 2. Where the project is

- **Current phase:** Phase 19, the release programme (`docs/phase19/`). **67 PRs have been merged in it:** #123–#181 and #186–#193, from `git log d764117..93fe2cb`.
- **Completion criteria:** none defined (conflict 3).
- **Proposed criteria, NOT approved (OD-10):** Phase 19 is complete when:
  - a Play **internal-testing** release can be built by `release-android.yml` from a protected Environment;
  - testers can sign in and use on-device screening against deployed staging hosts;
  - every §4.1 internal-testing item is done.
- **Later phases defined in the repository:** **none** (no "Phase 20" or later appears anywhere). The only defined later stages are the per-component rollout stages in ADR-0002 to ADR-0009 (Phase 2 and Phase 3 per component). They are not tasks, and are not counted.

## 3. Phase 19 remaining work, by category

### 3.1 Coding: 15 open, all blocked or conditional

| # | Task | Blocked on | Source |
|---|---|---|---|
| C1 | Web deletion page | Frontend stack (owner) and E10 | `ACCOUNT-DELETION.md` §3.0 |
| C2 | Sign-in SMS provider adapter (`OtpSender`) | E2 | Readiness §7 |
| C3 | Deletion-code provider adapter (`DeletionCodeSender`) and budget configuration | E2, O13, a budget value | Tracker §3 |
| C4 | Wire the Play Integrity verifier into identity | O7 (ADR-0031), E1, memo §12 B1 | Tracker §3; readiness §7 |
| C5 | FCM integration | E7 | Readiness §7 |
| C6 | Attestation housekeeping | O14 | Tracker §3 |
| C7 | Server-side call-list sync | OD-2 (needs an ADR-0012 amendment) | Readiness §2b; validation doc §6 |
| C8 | Forwarding provisioning | DID pool and carrier matrix (X4) | Readiness §2a, §8.1 |
| C9 | A reviewer sign-in path for Play review | OD-1 | Readiness §8.1 |
| C10 | Server-side access-token revocation | O17 (ADR-0010 amendment) | `ACCOUNT-DELETION.md` §5 D9 |
| C11 | `pgstore` subscriber directory (D30.3) | O2 | Tracker §3 |
| C12 | Device-history table and re-enrolment detection | O6, E9 | Tracker §3 |
| C13 | Per-source rate-limiting design | E6 | Tracker §3 |
| C14 | `client_id` rollout | O1, only if `client_id` is chosen | Tracker §3 |
| C15 | `BiometricPrompt` before the deletion confirmation | O12, only if screen lock is chosen | Tracker §3 |

**Completed, not counted above:** the release endpoint preflight (#194), and the F14 "held before" fix (#195).

### 3.2 Testing: 4 open, 0 unblocked

| # | Task | State | Source |
|---|---|---|---|
| T1 | Device (instrumented) coverage of A09b, Protection, the blocklist notice, the F14 banner and the Settings checklist | **Done.** Most of it was already done by #181; this row had been stale (validation doc §4.3, corrected). #195 adds the Settings checklist, the role lost before the feed was opened (a defect, fixed), the role revoked with no holder, and Android 9's checklist and feed. **Emulators: API 35 16/16, API 28 12/12.** | Validation doc §4.3 |
| T2 | Physical phone, SIM and carrier sheet M1–M12 (**12 cases**, including forwarding and Indian number presentation) | Blocked: X3, X4 | Validation doc §5 |
| T3 | Sign-in, refresh and deletion on a device against live hosts | Blocked: X5, E2 | Readiness §8.4 |
| T4 | Play Integrity verdicts from a Play-installed build | Blocked: E1, X1 | Readiness §8.4 |
| T5 | Run the signing-key rotation runbook once in staging | Blocked: E3 (SRE) | Tracker E3 |

### 3.3 Owner decisions: 26 pending (none approved)

- **Tracker O1–O17 (17),** less O10 and O11, which are in §3.4: **15.**
  - O9–O17 are the account-deletion decisions D1–D9.
  - No row records an approval (checked: the tracker contains no "approved", "ratified" or "decided" entry).
- **Defined in other Phase 19 documents and not in the tracker (11):**

| # | Decision | Source |
|---|---|---|
| OD-1 | A reviewer sign-in path for Play review (sign-in is SMS-only; any test bypass must not work in production) | Readiness §8.1 |
| OD-2 | Server-side allowlist sync (keep local, or sync hashed after an ADR-0012 amendment) | Validation doc §6; readiness §2b |
| OD-3 | Spam-reputation policy (band to reject, silence or screen) | Validation doc §6 |
| OD-4 | Consent UI, only with identity's consent records | Prototype audit §3 |
| OD-5 | Voice choice in the app (no vendor names) | Prototype audit §3 |
| OD-6 | Family sharing (needs its own ADR) | Prototype audit §3 |
| OD-7 | Business analytics (defer) | Prototype audit §3 |
| OD-8 | The frontend stack (also blocks C1) | Readiness §4, §7 |
| OD-9 | Workload identity federation approval for the verifier (memo §12 B1) | Readiness §7; Play Integrity memo §12 |
| OD-10 | Phase 19 completion criteria (§2) | This inventory, conflict 3 |
| OD-11 | The F14 banner's first sentence, "Another app is now screening your calls", is a UX-spec text the app cannot verify, and it is false when the subscriber simply turned screening off. Recommended: amend F14 to a sentence true in both cases, for example "This app is no longer screening your calls." | Validation doc §4.3 (#195) |

### 3.4 Privacy and legal review: 6 pending

| # | Item | Source |
|---|---|---|
| L1 | The DPDP privacy review (E9), including the data inventory | Readiness §8.2 |
| L2 | The erasure record's content and retention (O10 / D2) | `ACCOUNT-DELETION.md` §5 |
| L3 | A concrete period for the audit log's legal hold (O11 / D3) | `ACCOUNT-DELETION.md` §5 |
| L4 | The privacy policy text | Data inventory §6 Q6 |
| L5 | The Data safety answers (collected, not collected, Play Integrity, encryption) | Data inventory §6 Q2–Q5 |
| L6 | The contacts-permission disclosure (policy and Play declaration) | Validation doc §6 |

### 3.5 External dependencies: 15 pending

All costs are **unverified** unless the cited document says otherwise. None was created or used.

| # | Resource | Why | Local alternative |
|---|---|---|---|
| E1 | Google Play Integrity: Cloud project and verifier credential | Real verdicts | Fake decoder in tests |
| E2 | SMS/OTP provider and India DLT, including a **deletion** template | Anyone can sign in; web deletion | Fake and stdout senders (tests only) |
| E3 | Signing-key operations (SRE) | Production token issuance | Generated test keys |
| E4 | Database: CA bundle, multi-AZ, point-in-time recovery | Running outside development | Disposable PostgreSQL |
| E5 | Tracing backend (X-Ray) | Production spans | Log exporter |
| E6 | Trusted-proxy model | Per-source limits | — |
| E7 | FCM project | Notifications | Interface and mock |
| E8 | Cryptographic review or penetration test | Reviewed production issuance | One internal review |
| E9 | DPDP privacy review | L1–L6 | — |
| E10 | Web deletion page: domain, hosting, TLS | Play's web deletion requirement | Local identity and the fake sender |
| X1 | Play Console developer account, app, Play App Signing | Any Play release | — |
| X2 | Upload key, and the `play-release` Environment's secrets **and its endpoint variables** (#194) | `release-android.yml` | A throwaway key (CI) |
| X3 | Physical Android phones and Indian SIMs on each carrier | T2, T3 | Emulator (not carrier evidence) |
| X4 | DID pool and carrier forwarding matrix | Forwarding (C8), M3–M5 | — |
| X5 | Deployed identity and edge-api hosts (migrations through `0009`) and their certificate pins | Sign-in from any release; T3 | Local binaries in CI (`android-identity-contract`) |

### 3.6 Totals (Phase 19, release programme)

| Category | Open | Of which unblocked now |
|---|---|---|
| Coding | 15 | 0 |
| Testing | 4 (T2 has 12 cases) | 0 |
| Owner decisions | 26 | — |
| Privacy/legal reviews | 6 | — |
| External dependencies | 15 | — |

**Not counted:**
- **Phase 15** (7 blocker groups plus #35 and #44) and **Phase 16** (7 blocked rows, plus its own decision log): they are tracked in their own documents, and none is a Play prerequisite.
- **The telephony programme:** owner-held, uncommitted.

## 4. What each release needs

### 4.1 Play internal testing (the next milestone)

For a release that testers can actually use: sign in, then set up screening. The milestone needs:
- **Code:** nothing more is required.
  - The release path is complete and tested: signing, version validation, and, since #194, the refusal to build without hosts and pins.
- **External:**
  - X1 and X2: Play Console, the upload key and the Environment's secrets and variables;
  - X5 with E3 and E4: staging hosts, keys, the database and pins;
  - **E2**: without an SMS provider nobody can sign in, and the app stops at phone verification.
- **Owner decisions:** none of D1–D9 blocks an internal-testing build. The owner chooses the providers and hosts for E2 and X5. These are choices, not policy decisions.
- **Verify at submission:** whether Play requires the Data safety form, a privacy policy URL or the account-deletion URL before an internal-testing track can be published. **Not verified here.**

### 4.2 Public production

Everything in §4.1, plus:
- **Code:**
  - C1, the web deletion page;
  - C3, the deletion-code adapter, after O13;
  - C9, the reviewer path, after OD-1;
  - C4, only if Play Integrity is to be enforced (O7, O8).
- **Testing:** T1–T5, including the 12 carrier cases.
- **Decisions:**
  - D1 (O9);
  - D5 (O13);
  - O1 (ADR-0029);
  - OD-1;
  - OD-10;
  - the Play Integrity decisions, if it is used: O7, O8 and OD-9.
- **Privacy/legal:** L1–L6.
- **External:** E8 and E10.
- **Play Console:** the Data safety form, the privacy policy URL, the Delete account URL, app access instructions, content rating, and permission declarations.

### 4.3 The smallest set of decisions to unblock the next milestone

1. **E2:** choose an SMS/OTP provider and begin India DLT registration. It blocks every sign-in. It is an external dependency, and its cost is unverified.
2. **X5:** choose where staging identity and edge-api run, so their origins and pins can be set as the Environment variables (#194).
3. **OD-10:** approve Phase 19's completion criteria (§2), so that "done" is defined.

Nothing else is needed before internal testing. D1–D9 and the other owner decisions can wait for production. **No testing task is unblocked any more** (T1 is done, #195). Every remaining test needs a physical device, live hosts or an external account.
