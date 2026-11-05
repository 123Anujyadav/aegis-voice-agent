# Production readiness and external dependency plan

- **As of:** 2026-11-05, `main` `9de65aa`. Prepared by Claude Code from the repository and public sources, cited inline.
- **Verdict: not production-ready.** All evidence is from CI and emulators (API 35, 28 and 26). **None** is from a physical device, staging or production (`PRODUCTION-READINESS.md` §1).
- **Nothing was contacted, created or bought.** No provider, account, credential, integration or infrastructure. This plan decides nothing: every owner decision stays the owner's.

## 0. The six-phase roadmap is not in the repository

`PRODUCTION-READINESS.md` §6 records that the product capabilities "belong to the owner's six-phase product roadmap… That roadmap is not recorded in this repository". A search finds no other definition. "Phase" in the repository means something else:
- engineering units 1–19;
- the per-component rollout stages in ADR-0002 to ADR-0009;
- the telephony P1 programme (`ROADMAP-INVENTORY.md` §1).

The "six phases" in `docs/hardening/` were an earlier hardening programme.

**So this plan does not invent phases.** It uses the milestones the repository **does** define, in order:
- **M0**, the current baseline;
- **M1**, Play internal testing (`ROADMAP-INVENTORY.md` §4.1);
- **M2**, public production (§4.2);
- **M3**, the product capabilities (`PRODUCTION-READINESS.md` §6).

The last column of §2 is left for the owner to place each milestone in its phase.

**Owner action 1:** record the six phases, their names and scope, so each milestone and task can be placed.

## 1. Counts, from the repository's records

The inventory's counts are as of 2026-11-01 (`ROADMAP-INVENTORY.md` §3.6), adjusted for the decisions taken since.

| Category | Open | Unblocked now | Since 2026-11-01 |
|---|---|---|---|
| Coding (C1–C15) | **15** | **0** | — (C16–C24 done) |
| Testing (T2–T5; T2 is 12 carrier cases) | **4** | **0** | T1 done |
| Owner decisions (tracker O1–O17 less O10, O11; OD-1–OD-18) | **32** | — | OD-11 closed (#245) |
| Privacy and legal reviews (L1–L6) | **6** | — | Q7 adds counsel review of the 54 translated rows as a production gate |
| External dependencies (E1–E10, X1–X5) | **15** | — | — |
| High-risk decisions needing an independent reviewer | **4** (H1, H2, H3, H7), **+3 conditional** (H4–H6) | **0**: no reviewer named (OD-12) | — |
| New since the inventory | F26 branding (trademark counsel); translators (Q0; not engaged); **the policy-scope question (§4)** | — | — |

**Claude Code has no routine, unblocked coding or testing task.** Every open item waits on an owner decision, an external party, an independent reviewer or counsel.

## 2. Milestones

| | **M0 Baseline (now)** | **M1 Play internal testing** | **M2 Public production** | **M3 Product capabilities** |
|---|---|---|---|---|
| **Complete** | Identity and edge-api; the Android app (onboarding, on-device screening, lists, settings, deletion, six-language packaging and language choice, navigation per spec); the release pipeline (R8, signing check, endpoint preflight); CI (unit, race, fuzz, real PostgreSQL, emulators API 35/28/26, R8 functional tests, dependency audit) | **Code: nothing more is required** (§4.1). The release refuses to build without hosts and pins (#194) | — | Package scaffolding only (`PRODUCTION-READINESS.md` §6) |
| **Remaining** | — | **External:** E2 (SMS and DLT); X5 with E3, E4 (staging hosts, keys, database, pins); X1, X2 (Play Console, upload key, Environment). **Engineering after E2 is chosen:** C2 (the SMS adapter), deployment manifests (C1 of `INTERNAL-TESTING-SETUP.md`) | Code: C1 (web deletion), C3 (deletion-code adapter), C9 (reviewer path), C4 if Play Integrity is enforced. Tests: T2 (12 cases), T3, T4, T5. Decisions: O9, O13, O1, OD-1, OD-10, and O7, O8, OD-9 if Play Integrity is used. L1–L6, E8, E10. Play Console declarations. Q7 counsel review per translated language. F26 branding | Voice assistant (forwarding, speech, LLM); post-call reports; evaluation; analytics; memory; learning; model improvement |
| **Blocked by** | — | E2 (critical path), X5, X1/X2, **the policy-scope question (§4)** | M1, OD-12 (for H-items), E8, E9, E10, counsel | OD-17 (deferred), X4, the telephony staging NO-GO (P1-38), OD-4 (consent), ADR-0003/0006 under the policy question (§4) |
| **Exact remaining count** | 0 | **3 external items** (E2, X5+E3+E4, X1+X2) **+ 2 engineering items** after them (C2 adapter, manifests) | 3 coding (C1, C3, C9) + C4† + 4 testing (T2–T5) + 5 decisions (O9, O13, O1, OD-1, OD-10) + 3† (O7, O8, OD-9) + 6 legal + 2 external (E8, E10) + Play declarations | Not countable: no task list exists for them (§6 lists capabilities, not tasks) |
| **Exit evidence** | Met: CI and emulator only | A release run of `release-android.yml` from the protected Environment; the `.aab` on the internal track; **a tester signs in on a physical phone against staging** and turns blocking on (T3 started) | All of M1, plus: T2 (12 cases) and T3–T5 recorded on physical phones and staging; L1–L6 signed off; E8 report; the Play declarations accepted; production deploy verified; the counsel sign-off per translated language (Q7) | Each capability's own acceptance, defined when its tasks are |
| **Owner's phase** | *(owner to assign)* | *(owner to assign)* | *(owner to assign)* | *(owner to assign)* |

† Only if Play Integrity is to be enforced.

## 3. Integration-readiness matrix

| | **SMS/OTP (E2)** | **Staging hosting (X5, E3, E4)** | **Play Console (X1)** | **Signing / upload key (X2)** | **Physical devices and SIMs (X3, X4)** | **Speech and AI (ADR-0005/0006/0007; CPaaS ADR-0003)** |
|---|---|---|---|---|---|---|
| **Why** | Nobody can sign in without a delivered code (ADR-0010 MSISDN identity) | Testers' phones need reachable identity and edge-api hosts; the release needs their origins and pins | Distribution; internal, closed and production tracks; Play App Signing | Signs the `.aab` that Play accepts (Google keeps the app-signing key) | Evidence on real phones and carriers: T2's 12 cases, T3 | The voice assistant: forwarding, transcription, replies, speech |
| **Needed at** | M1 | M1 | M1 | M1 | Most of T2 now (below); all at M2 | M3 |
| **Account and credentials** | A provider account with India delivery, and a fallback (ADR-0010 §9); a DLT Principal Entity registration, a sender header and two templates (sign-in, deletion); credentials as mounted Secret files | AWS account (ADR-0008: `ap-south-1`, EKS, Aurora); IAM/IRSA; a domain; TLS certificates **and** backup keys (two pins); Ed25519 signing key and HMAC key (self-generated) | A Google Play developer account; identity verification; the app; testers' Google accounts | Upload key made offline with `keytool`; four `play-release` Environment secrets; Environment protection first | Android phones (Android 10+, one Android 8/9, one dual-SIM, OEMs listed in M12); SIMs on Jio, Airtel, Vi, BSNL; for M3–M5 a DID pool and CFNRy forwarding | Vendor accounts per the Accepted ADRs (Google STT, Deepgram, ElevenLabs, Cartesia, Sarvam, Anthropic, Exotel/Plivo), or open models (OD-17 option A) with GPU hosting |
| **Configuration** | Adapter C2 (written once a provider is chosen); egress from identity (network-policy change); deletion-code budget value | `INTERNAL-TESTING-SETUP.md` B–C: schema to `0009`, `CS_IDENTITY_*` and `CS_EDGE_API_*` values, health checks | Package name, Play App Signing, internal track, tester list | `ANDROID_UPLOAD_KEYSTORE_BASE64`, `…_PASSWORD`, `…_KEY_ALIAS`, `…_KEY_PASSWORD`; then the release variables (origins, pins) | A debug or staging APK installed by the owner; the M1–M12 sheet (`ANDROID-SCREENING-DEVICE-VALIDATION.md` §5) | Adapters behind the existing `STTProvider`/`TTSProvider` ports; latency budgets (ADR-0011) |
| **Owner actions** | Choose providers (A1); DLT registration and templates (A2, A3); approve egress (A5) | Authorise the account, cluster and IAM (B1); choose the domain (B3); decide the manifests (C1) | Register; **personal or organisation account** (below); confirm the permanent package name | Generate the key; protect the Environment; add the secrets | Provide the phones and SIMs; approve any paid calls; arrange the DID pool for M3–M5 | Decide OD-17 (deferred); answer the policy question (§4); OD-4 consent; OD-12 reviewer |
| **Free or local alternative** | Tests' fake senders and the harness's stdout sender: **not a delivery path**. Email sign-in (ADR-0032) avoids SMS for sign-in but is **Proposed and high-risk** (H1; needs OD-12) and still needs an email sender | CI's local binaries on a disposable PostgreSQL behind a TLS proxy: **not reachable by testers' phones**. A non-AWS host would deviate from ADR-0008 | **For device evidence only:** install the debug or staging APK directly with `adb`, no Play needed. Not a distribution path; no Play Integrity verdicts | The CI throwaway key (tests only) | **9 of the 12 carrier cases (M1, M2, M6–M12) need only a phone, a SIM and the debug APK.** The debug build opens Protection directly (start-destination extra, debug only), as the emulator tests do, so no sign-in, Play or staging is needed. M3–M5 need the DID pool and the gateway | Local models for development (Whisper, Piper, Ollama), not production. Open models need GPU hosting (`SPEECH-EVALUATION-PREPARATION.md` §3) |
| **Cost** | **Per-message SMS:** unknown. **DLT entity registration:** about ₹5,900 including GST per operator portal, per providers' guides ([Exotel](https://developer.exotel.com/docs/sms-support/dlt-registration), [SMSGatewayHub](https://crm.smsgatewayhub.com/knowledge-base/article/dlt-registration-charges-cost-breakdown-in-india)); **not verified on the official portals** | **Unknown:** EKS, Aurora, load balancer, data transfer; **recurring while running.** Domain and certificates unknown | **US$25, one-time** ([Google Play help](https://support.google.com/googleplay/android-developer/answer/6112435)) | None (self-generated) | Phones and SIM plans: unknown; prepaid recharges recurring. DID pool: unknown, recurring | Per-minute or per-token vendor pricing, or GPU hosting: unknown, recurring |
| **Privacy and security** | Phone numbers go to the provider (a processor under DPDP; L1); codes in transit; credentials as Secret files only; DLT template text is registered publicly | Holds personal data (numbers as keyed pseudonyms, sessions); `verify-full` TLS; India residency (ADR-0012); key handling per runbooks; T5 rotation drill | Developer identity verification; testers' Google accounts; the Data safety form and privacy policy (L4, L5) | **The upload key is the release's root of trust:** never in Git, CI logs or chat; Environment protection before any secret | Test calls involve real numbers; don't use personal contacts' numbers; record results without personal data | **Call audio is personal data** (ADR-0012); vendor processing needs DPDP review and the independent review (OD-12); `trust_remote_code` review for open models |
| **Permitted now?** | Owner's choice (§4) | Owner's choice (§4) | Owner's choice (§4) | Yes, once X1 exists (no cost) | Yes: owner-provided hardware | **No:** OD-17 deferred by the owner on 2026-11-04 |

**Play Console, personal or organisation (owner action).** Personal developer accounts created after 2023-11-13 must run a **closed test with opted-in testers for 14 days** before they can apply for production access. Google's help page states 20 testers; several guides still say 12. Organisation accounts are exempt, but need organisation verification ([Google Play help](https://support.google.com/googleplay/android-developer/answer/14151465)). **This adds at least 14 days to M2 on a personal account.** Check the current figure in Play Console.

**Before registering anything with the product's name** (F26, owner decision 2026-11-05: the product is **Lily AI**), the naming review should precede:
- **the DLT templates:** the deletion template suggested in `ACCOUNT-DELETION.md` §3.1 says "Aegis", and SMS text must match its registered template;
- **the Play listing;**
- **the permanent package name**, which Play fixes at the first upload.

A preliminary public search found other "Lily AI" products, so **trademark counsel** is needed (owner approval).

## 4. A policy question that blocks the critical path

The owner's policy of 2026-11-01, as recorded (`SPEECH-PROVIDER-POLICY-CONFLICT.md` §1): "Free and open-source only: no paid API, SDK, subscription or cloud service."

It is recorded only against the speech ADRs (OD-17). **Read as written, it also rules out:**
- per-message SMS (E2), and no free OTP delivery path exists for India;
- AWS staging and production (ADR-0008, Accepted);
- the Play Console fee (X1);
- the CPaaS (ADR-0003) and the LLM (ADR-0006).

Without some paid services, **neither M1 nor M2 is reachable.**

**The owner decides.** For example:
- a spending envelope, per service or overall, that each item is approved against;
- or exceptions by category: delivery, hosting, store, voice.

Claude Code makes no choice here. Until it is answered, every item above stays at "owner's choice".

## 5. Who does what

| Work | Claude Code, autonomously | Owner | Independent human reviewer | Qualified legal counsel | External party |
|---|---|---|---|---|---|
| SMS (E2) | The adapter (C2) and its tests, after the provider is chosen; budget wiring | Provider, contract, DLT, templates, spend | — | DPDP processor terms (L1) | Provider; DLT portal |
| Staging (X5) | Manifests, if the owner asks for them to be written; configuration docs; the release variables' checks | Account, cluster, IAM, domain, spend | — | — | Cloud provider; SRE |
| Play (X1, X2) | Release runs once the Environment exists; evidence records | Account, identity verification, package name, key, secrets, tracks, testers | — | Privacy policy, Data safety (L4, L5) | Google |
| Device evidence (T2, T3) | The test sheets, scripts, and analysis of the results the owner records | Phones, SIMs, running the cases, any paid calls | — | — | Carriers |
| High-risk decisions (H1–H3, H7; H4–H6 if classed) | Proposals and analyses, labelled AI-performed | Decision | **Required** (OD-12) | Where privacy | — |
| Legal (L1–L6, Q7 counsel gate) | Inventories and drafts for counsel to review, never as legal advice | Engagement and spend | — | **Required** | — |
| Translation | Worksheets, validation, import after review | Translators and reviewers (Q0), consent, spend | — | Q7 rows before production | Translators |
| Branding (F26) | The cleanup PR, after the naming review | Naming decision | — | **Trademark clearance** | — |
| Security review (E8) | Scope and evidence pack | Commission and spend | — | — | Penetration tester |

## 6. Evidence to declare production-ready

Production-ready means **every** row below is met and recorded with its evidence. CI and emulator runs alone never qualify.

| Area | Required evidence |
|---|---|
| Sign-in, refresh, deletion | T3 on physical phones against staging, then production; the deletion code delivered (E2), and the web deletion page live (E10, C1) |
| On-device screening | T2: all 12 cases on physical phones, per carrier (Jio, Airtel, Vi, BSNL), including OEM restrictions and dual-SIM |
| Release | A signed `.aab` from the protected Environment; Play App Signing; Play declarations accepted (Data safety, privacy policy URL, Delete account URL, permissions, content rating) |
| Security | E8 report with findings closed; T5 key-rotation drill in staging; high-risk decisions approved with the independent review (OD-12) |
| Privacy and legal | L1–L6 signed off by counsel and the DPDP review (E9); Q7 sign-off for each translated language shipped |
| Operations | Staging and production deploys verified; health, metrics and alerting (E5 tracing as decided); database PITR (E4) |
| Languages | A language is called supported only with reviewed translation, the on-phone check, and Q7 |
| Completion criteria | OD-10 approved, so "done" is defined |

## 7. Critical path

1. **Today, in parallel, all owner actions:**
   - record the six phases (§0);
   - answer the policy question (§4);
   - **choose the SMS provider and start DLT** (the slowest step), after the naming review for the template text;
   - authorise staging and choose the domain;
   - register Play (personal or organisation) and confirm the package name;
   - **provide one Android phone and SIMs.**
2. **As soon as a phone exists:** T2's 9 on-device cases with the debug APK. This is the first physical-device evidence, and needs none of the integrations.
3. **When the provider is chosen:** Claude Code writes adapter C2. SRE deploys staging, and pins and release variables follow.
4. **M1:** the first release run, upload to internal testing, and T3 on physical phones.
5. **M2:**
   - the closed-test period, if the account is personal;
   - L1–L6 and E8;
   - C1 and E10;
   - the decisions in §2;
   - OD-12 for the high-risk items;
   - production deploy and verification.
6. **M3:** after OD-17, X4, the telephony staging gate and the policy question.

## 8. Owner actions, in order

1. Record the six-phase roadmap (names and scope).
2. Answer the policy-scope question (§4): what may be paid for, within what envelope.
3. Name an independent human reviewer (OD-12).
4. Provide a physical Android phone and Indian SIMs (X3); T2's 9 on-device cases can then start.
5. Approve trademark counsel for "Lily AI" (F26), before DLT templates, the Play listing and the package name.
6. Choose the SMS provider(s), and start DLT registration with the reviewed template text (E2).
7. Authorise staging: AWS account, cluster, IAM and domain (X5, B1, B3); decide the manifests (C1).
8. Register the Play developer account (personal or organisation), confirm the package name, create the upload key and Environment (X1, X2).
9. Approve OD-10, Phase 19's completion criteria.
10. For M2: engage counsel for L1–L6 and Q7; commission E8; decide O9, O13, O1, OD-1 and the Play Integrity items.
11. When ready: approve translators (Q0) and, separately, any speech-model step (OD-17).
