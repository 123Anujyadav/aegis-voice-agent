# Owner decision packet: tab navigation (F25), the remaining translation questions, readiness

- **Status: for the owner's review. Nothing in this packet is decided.** Every "Your decision" cell is empty, and every recommendation is Claude Code's proposal.
- **Prepared:** 2026-11-05 by Claude Code, on main `33022a5`. AI-prepared material (`ARCHITECTURE_FREEZE.md` §6.1 rule 9).
- **Builds on:** [`OWNER-DECISION-PACKET-2026-11-04.md`](OWNER-DECISION-PACKET-2026-11-04.md), whose decisions (language UX D1–D9, translation Q0–Q4, the speech deferral) are approved and built.
- **Nothing here changes behaviour.**
  - No navigation, copy, ADR or invariant is changed.
  - No translator is contacted, and no account or service is used.
  - Speech-model evaluation stays deferred.

---

## 1. F25: Back on a tab root

### The two sources, exactly

**The UX specification**, `docs/ux/android/02-navigation-graph.md`. The navigation graphs are **frozen** (`UX_FREEZE.md` §1).
- **§2.1:** "Each tab owns an independent back stack. Switching tabs preserves the other tabs' stacks. Re-selecting the active tab pops its stack to root; re-selecting at root scrolls to top; at top it is a no-op — never a bounce."
- **§2.4:** "Any tab root | **Exits the app.** Never cycles to Calls first — a back button that changes tabs is disorienting".
- **§2.6:** "Tab back stacks | Tab switches, backgrounding | Yes" (preserved and restored).

**The implementation**, `android/app/src/main/kotlin/com/callscreen/app/navigation/AegisNavGraph.kt`, `handleTabClick`:
- **Each tab is plain `navigate(route)`,** with no `popUpTo`, `launchSingleTop` or saved state. It has been unchanged since the repository's first commit (`acaceae`, 2026-08-16). So:
  - every tab switch adds an entry, and Back walks back through the tabs visited;
  - re-selecting the current tab adds a **duplicate** entry, where the specification says it pops to the root;
  - tabs do not keep separate stacks.
- **Related, also as built:** only Calls, Protection and Assistant show the bottom bar. **Settings (the task centre) has none**, so it is left by Back, or now the top-bar arrow.

**D6** (approved 2026-11-04): "Add a top-bar Back arrow across the application, preserving existing navigation and screen-state behaviour."
- As built (#239), the arrow **is** Android's Back for the screen it is on.
- It therefore shows on a tab root reached from another tab, and returns to that tab, exactly as Back does.
- `TopBarBackTest.theArrowFollowsTheBackStackAsBuilt` holds this.

### Can they be reconciled without a decision? No

- **Keeping D6's behaviour and the specification together is impossible.** The arrow mirrors Back, and Back as built cycles tabs, which §2.4 forbids.
- **Making the code follow the specification** changes user-visible navigation, which D6 asked to preserve, and which you asked not be changed silently.
- **Making the specification follow the code** changes a frozen navigation graph. `UX_FREEZE.md` §7 requires that change in `docs/ux/` with a design and engineering review.

**Either way, it is your decision.**

**What stays true under every option:** the arrow is Android's Back for its screen (D6). Only where Back goes changes.

### Options

| Option | What changes | Consequences |
|---|---|---|
| **A. Build the specification** | Tab selection uses Navigation's tab pattern: pop to the start, save and restore each tab's state, single-top. Back on a tab root leaves the app. Re-selecting the current tab pops it to its root | **Matches the frozen §2.1, §2.4 and §2.6.** No duplicate entries. The arrow disappears from tab roots, because Back has nowhere in the app to go. `TopBarBackTest`'s tab test is rewritten to the specified behaviour. **Users' Back from a tab changes** (today it cycles). No ADR or invariant affected. Settings still lacks a bottom bar: that is a separate gap, not part of this option |
| **B. Amend the specification to the build** | `02` §2.1, §2.4 and §2.6 rewritten to "Back returns through the tabs visited". The duplicate on re-selection stays unless also fixed | No code change. **Reverses a frozen rule** whose stated reason is that "a back button that changes tabs is disorienting". Needs design and engineering review (`UX_FREEZE.md` §7). Tab state is not preserved per tab |
| **C. Keep the recorded deviation** | Nothing. F25 stays open in `PRODUCTION-READINESS.md`, and `02` §2.4 keeps its note | The specification and the app keep disagreeing. Any future navigation work starts from a known conflict |

**Recommendation: A**, as its own change after your approval.
- It is the specified, frozen behaviour.
- It removes the duplicate entries.
- It keeps D6 intact: the arrow still is Back.

It would be built with device tests on API 35, 28 and 26, and the R8 build's tests. **Whether Settings should have the bottom bar** is a separate question, raised here but not asked.

| Decision | Your decision |
|---|---|
| F25: A, B or C | |

---

## 2. The remaining translation questions

The exact questions are in [`docs/translation/QUESTIONS.md`](../translation/QUESTIONS.md). The table quotes them. **"Options" lists only what that file already offers.**

**Why they matter: the import is all-or-nothing.** A language's worksheet must have every row translated before it can be imported. So Q5, Q6 and Q8 hold up **every** language until their rows are settled, or translated as they stand.

| # | Who decides (`QUESTIONS.md`) | When | Exact question | Existing options | Current implementation | Recommendation (not a decision) | Your decision |
|---|---|---|---|---|---|---|---|
| **Q5** | Owner (OD-17) | Before those rows | "The planned-voice rows describe undecided vendors, and one is inaccurate" | (a) fix or remove the English labels when OD-17 is decided, and translate after; (b) translate them as they are, if you want a language shipped first | 6 rows, 17 words, in `feature/assistant` `strings.xml`. They label three voices on voice setup that cannot be chosen ("Not available yet"). "Hindi Devanagari Native Accent" calls a script an accent. **OD-17 is deferred**, so (a) has no date | **Correct the inaccurate English label now** (it is copy, and copy is not frozen). **Translate the six rows as they are**, so they don't block every language. Revisit them when OD-17 is decided. If you prefer, hold them, but no language can then be completed | |
| **Q6** | Owner (OD-11) | Before that row | "The F14 banner's first sentence may change" | (a) decide OD-11 before translating this row; (b) translate it now and again after the amendment | `calls_role_lost`: "Another app is now screening your calls. Numbers you've blocked here won't be blocked until you switch back." It is false when the subscriber turned screening off. Appendix A of `ROADMAP-INVENTORY.md` holds the ready amendment: "On-device blocking is off. …" | **Decide OD-11 first:** approving Appendix A's wording settles the row before anyone translates it. It is one row and a copy change | |
| **Q7** | Owner | Before | "Legal review of the deletion and privacy rows": whether a lawyer, as well as the second translator, reviews them in each language | Lawyer review in each language · the second translator's review only | 54 rows carry a `review` entry: account deletion 32, the calling role (A09b) 11, the signed-out notice (A73) 5, contacts never read or uploaded 3, the F14 banner 2, the microphone 1. Their English is held to the specifications by tests | **Your call, and not legal advice.** The 32 deletion rows say what is erased and kept. They are the strongest case for a lawyer, and the DPDP review (E9, L1) is the natural place to ask | |
| **Q8** | **Product** | Before that row | "'in Settings': Android's or the app's" | (a) change the English to "in your phone's settings"; (b) until then, translate as Android's settings | `protection_notice_contacts_denied`: "Contacts access is off. You can turn it on in Settings." Its button opens **Android's** page for this app (`ACTION_APPLICATION_DETAILS_SETTINGS`, `BlockingNotice.kt`) | **(a): change the English** to "in your phone's settings". It says where the button goes. It is a copy change, and a test-held wording updates with it | |
| **Q9** | **The translators' lead** (during translation) | During | "Sentence-ending punctuation per language": Hindi commonly "।", the others "." | Follow Android's own text in each language, consistently | Nothing to change in the app. The import checks placeholders and line breaks, not punctuation | **Not yours to decide** under `QUESTIONS.md`. Listed for completeness. The reviewer checks consistency (§3) | — |
| **Q11** | Owner | Separate | "Text outside the app": the sign-in SMS, the assistant's voice prompts, the Play Store listing, debug-only previews | None defined. `QUESTIONS.md` notes each depends elsewhere: the SMS on the provider decision and DLT templates per language (E2), the voice prompts on OD-17, the Play listing on X1 | None of these is in the app's worksheets. The debug previews are not migrated (`LOCALISATION.md` §1) | **No decision needed now.** `QUESTIONS.md`: "None of these blocks the app's translation, but a language is not fully supported until the person's whole experience is in it." Revisit with E2, OD-17 and X1 | — |
| **Launcher name** | Owner (raised in Q1) | Separate; affects English | "settle the launcher name ('CallScreen' or 'Aegis AI') separately. It affects English too." | "CallScreen" · "Aegis AI" | `app_name` = **CallScreen** (`android/app/src/main/res/values/strings.xml`, `translatable="false"`). The launcher and Android's own screens use it: the app list, App languages, permission prompts, the "Caller ID & spam app" choice. **The app's screens say "Aegis AI"**: the top bar, Welcome, the splash. Q1 keeps "Aegis AI" in Latin letters in translations. The application id `com.callscreen` and the `callscreen://` scheme are not shown to users | **"Aegis AI"**, so the phone and the app name the product the same way. A subscriber asked by Android to choose a "Caller ID & spam app" otherwise looks for "CallScreen" after reading "Aegis AI" throughout. One string changes, plus its tests. **Not changed:** the application id. **Check the name's availability for Play and trademark before choosing** (X1; not legal advice) | |

---

## 3. Translation readiness

### Verified on 2026-11-05 (main `33022a5`), offline

| Check | Result |
|---|---|
| The five committed worksheets equal a fresh export of the English (`RepositoryTest`) and the translation kit's tests | **25 tests pass** |
| Each committed worksheet, run through the import's checks | **hi, ta, te, ml, kn: 246 rows each.** Every row is refused only for "no translation". There are no unknown, duplicate, stale or malformed rows |
| A complete worksheet passes: a **validation stand-in** filled each row with its placeholders, the owner's fixed terms (Aegis AI, AI, OTP) and the language's own name. It was held in memory and **never written**, and **it is not a translation** | **0 problems in each of the five languages.** A filled, reviewed worksheet in the expected form can be imported |

**Not done:**
- no translation;
- no machine translation;
- no worksheet sent out;
- no translator contacted;
- no external account or platform.

### Workload per language

From `python3 tools/i18n/translation_kit.py audit`. It is the same for all five.

| Measure | Per language | All five |
|---|---|---|
| Resources (242 strings, 2 plurals) | 244 | — |
| Worksheet rows (each plural has `one` and `other`) | **246** | 1,230 |
| English words / characters | **1,554** / 9,153 | 7,770 / 45,765 |
| Second-review rows (exact meaning) | 54 | 270 |
| Tight room: chips 17, one-line fields 7, tabs 4 | 28 | 140 |
| Rows with placeholders | 11 | 55 |
| **Rows waiting on a decision in §2** | 8: Q5 6, Q6 1, Q8 1 | 40 |
| **Rows that can be translated now** | **238** | 1,190 |

| Module | Rows | Words |
|---|---|---|
| Onboarding | 81 | 513 |
| Settings | 54 | 476 |
| Protection | 60 | 361 |
| Assistant | 24 | 122 |
| Calls | 10 | 45 |
| Shared components | 12 | 16 |
| App shell | 5 | 21 |

**Effort.** This is a planning assumption from `docs/translation/README.md`, not a quote or a measurement: about 1,000–1,500 words a day. Translation takes 1–2 days, review about 1 day, and the on-phone check about half a day: **about 3–4 working days per language.** **No cost is known:** no quote exists.

### Reviewer checklist, for each language

The second fluent speaker, who is not the translator, checks:

1. **Every row against its English, its `note` and its `where`.** The `where` column gives the room the text has.
2. **The 54 second-review rows, word for word, against the named document:**
   - **account deletion** (32): `docs/phase19/ACCOUNT-DELETION.md`. What is erased and what is kept must not soften or widen;
   - **the calling role** (11) and **the F14 banner** (2): `06` A09b and `08` F14;
   - **the signed-out notice** (5): A73;
   - **contacts** (3): never "read" or "imported";
   - **the microphone** (1): the app never uses it.
3. **The owner's fixed terms.** The import also refuses a departure:
   - `Aegis AI` and `AI` in Latin letters (Q1, Q3);
   - `OTP` in all 10 sign-in-code rows (Q4), and never for the country, area or forwarding codes.
4. **Register (Q2):** everyday and polite, with Android's own words for Android's settings. "Caller ID & spam app" must match the phone's own wording exactly.
5. **Names that must match:** the glossary's table. For example, "Protection" and "Blocked numbers" inside a sentence must equal the tab and screen titles.
6. **Placeholders and plurals:** `%1$s` and `%1$d` are kept, and both plural forms are filled. The import checks both.
7. **Tight room:** the 28 chip, field and tab rows read naturally when short.
8. **Punctuation (Q9):** consistent, following Android's own text in that language.
9. **Each translator's note** is resolved, or raised with the owner.
10. **Then the on-phone check by a native speaker.** Every screen, at a large text size, with TalkBack (`docs/phase19/LOCALISATION.md` §5).

**Before an import:** both people agree to their names being recorded (`--names-consented`, Q0).

---

## 4. Governance and readiness inventory

Sources:
- [`ROADMAP-INVENTORY.md`](ROADMAP-INVENTORY.md) §3.3–§3.7;
- [`EXTERNAL-DEPENDENCIES.md`](EXTERNAL-DEPENDENCIES.md) §2;
- [`PRODUCTION-READINESS.md`](PRODUCTION-READINESS.md) §4;
- [`ACCOUNT-DELETION.md`](ACCOUNT-DELETION.md) §5.

**No item below is decided by this packet.**

**What the status values mean:**
- **Proposed:** a written proposal awaits a decision.
- **Owner:** awaits your decision.
- **Review:** also needs an independent human reviewer, who does not exist yet (OD-12).
- **External:** awaits an outside dependency.
- **Current build:** what the app or service does today, pending the decision.

### The gate

**OD-12, an independent human reviewer, is not identified.**
- Without one, every high-risk item stays Proposed:
  - H1–H3 and H7;
  - H4–H6, if you class them high-risk;
  - O9, which amends invariant INV-ID-7.
- **Naming a reviewer unblocks more than any other single decision.**

### Items

| # | Subject | Status | Blocked by | Current build |
|---|---|---|---|---|
| **H7** | When a refused refresh proof ends the session (F1, with F11) | **Proposed** (A + B + E in `F1-REFRESH-PROOF-REJECTION.md`); Owner + **Review** | OD-12 | Any refused proof signs out locally; identity revokes nothing |
| OD-10 | What "Phase 19 complete" means | Owner | — | — |
| OD-11 | F14 banner's first sentence | **Proposed** (Appendix A, ready to apply); Owner | — | Current sentence (false when screening is simply turned off) |
| OD-12 | Name an independent human reviewer | Owner | — | **Gates every high-risk item** |
| OD-13 | Recover a corrupt session file | Owner (recommendation in readiness §4.1) | — | The crashes are guarded; the file is not recovered |
| OD-14 | A sign-out action | Owner. A server logout route would be high-risk (**Review**) | OD-12 for the server part | No sign-out in the UI |
| OD-15 | Unconfigured identity refuses to start in staging and production | Owner | — | It boots "inactive" and reports ready |
| OD-16 | Keeping base images current: Renovate (an **external app**) or Dependabot `docker` PRs | Owner (an external integration if Renovate) | — | Neither active |
| OD-17 | Speech providers | **Deferred by the owner, 2026-11-04.** ADR-0005 and ADR-0007 unchanged | Your separate approval for any step | No speech model; the voice rows are labels only |
| OD-18 | Held Dependabot PRs | Owner | — | **Open now: #183, #184, #185, #212.** #182 was closed by Dependabot on 2026-11-01 and replaced by #212 (the same bump). `ROADMAP-INVENTORY.md` still says "#182–#185" |
| O1, O2, O7 (= H4–H6) | Ratify ADR-0029, ADR-0030 D30.3, ADR-0031 | Owner; **Review** if you class them high-risk | OD-12 (if high-risk) | The ADRs' status is "Proposed: decided under owner delegation" (2026-10-23/24), awaiting ratification. ADR-0029's deviations: `ADR-0029-DEVIATION-ANALYSIS.md` |
| O3 | OTP backoff | Owner | — | A fixed 15-minute lockout (`identity.go`), the recommended option |
| O4 | Session lifetime: 24 h or 90 days | Owner (product) | — | 24 h from sign-in (`identity.go`) |
| O5 | SIM-swap policies | Owner | — | — |
| O6 | Device-history retention | Owner | E9 (privacy review) | — |
| O8 | What a restricted or unverified session may do | Owner (product) | — | The tier is recorded and audited, never enforced |
| O9 (deletion D1) | Can an erased number sign up again? | Owner + privacy review; amends **invariant INV-ID-7** (**Review**) | OD-12, E9 | Allowed: **the code conflicts with INV-ID-7** |
| O10 (D2), O11 (D3) | Erasure-record retention; audit-log legal hold | Owner on legal advice; O11 needs an ADR-0012 amendment | E9 | Content kept; no end date |
| O12 (D4) | Step-up before deletion | Owner | — | No SMS step-up |
| O13 (D5) | Web deletion approach | Owner | E2, E10 | Server side built and **dormant** (#193) |
| O14 (D6) | Device-attestation retention | Owner | — | None written yet |
| O15 (D7) | Immediate deletion or a grace period | Owner | — | Immediate |
| O16 (D8) | A53d "type the last four digits" | Owner (UX spec) | — | An explicit confirmation dialog. The specification cannot be built as written |
| O17 (D9) = H2 | Revoke access tokens after deletion | Owner + ADR-0010 amendment (**Review**) | OD-12 | The app destroys its key after a confirmed deletion (#192) |
| H1 | ADR-0032: email code and Google sign-in | **Proposed** (#200); Owner + **Review** | OD-12 | Not built |
| H3 | ADR-0012 amendment for server-side list sync (OD-2) | Not drafted; **Review** | OD-12 | Lists stay on the phone |
| L1–L6 | DPDP review, erasure retention, legal hold, privacy policy, Data safety, contacts disclosure | **External:** legal and privacy review (E9) | E9 | None written |
| **F25** | Back on a tab root | Owner (§1) | — | Back cycles through tabs |

### Verified, and not verified

| | State |
|---|---|
| **Implemented and verified (CI and emulator)** | The owner's 2026-11-04 decisions: D3, D4 and D7 (#237), D6 (#239) and the import's term checks (#238). Device tests passed on API 35, 28 and 26, with the R8 build's functional tests and launch check (runs in `PRODUCTION-READINESS.md` §3.9) |
| **External dependencies** | E2 SMS provider and DLT; X5 staging hosts (with E3 keys, E4 database); X1 and X2 Play Console and upload key; X3 and X4 phones, SIMs and carrier forwarding; E1, E5–E10 |
| **Not tested on physical hardware** | **Everything.** No phone has run the app (X3). API 36 has never run on any device |
| **Not tested in staging or production** | **Everything.** No hosts exist (X5); nothing is deployed; no Play release |

**Production readiness: not ready.**

---

## What happens after you decide

- **F25 A:** the tab change is built in one PR, with device tests.
- **F25 B:** `02` is amended, with design and engineering review.
- **Q5, Q6, Q8, launcher:** the English copy changes and the worksheets are regenerated, in one PR per decision.
- **Q7:** recorded in `QUESTIONS.md`.
- **Translators:** still not contacted. Engaging anyone needs your further approval.

---

## 5. Proposed by the owner on 2026-11-05: awaiting final confirmation

- **Status: PROPOSED. Not approved, not recorded elsewhere, not built.** These are the owner's proposed choices, written as the exact entries that would be recorded on confirmation.
- **The "Your decision" cells above stay empty until then.**
- **Nothing in the app, the specifications, the worksheets or the ADRs has changed.**

### F25: navigation, Option A

**Entry to record:**
> **F25, Option A (owner, ‹date of confirmation›):** build the frozen navigation specification, `docs/ux/android/02-navigation-graph.md` §2.1, §2.4 and §2.6:
> - Back on a tab root exits the app;
> - each tab keeps its own back stack, preserved across tab switches and backgrounding;
> - Back never cycles through previously selected tabs;
> - selecting the current tab adds no duplicate entry: it pops that tab to its root, and at the root scrolls to the top (§2.1);
> - the top-bar Back arrow mirrors Android's Back.
>
> **This narrowly supersedes D6's "preserving existing navigation and screen-state behaviour"**, for tab selection and Back on tab roots only. All of D6's other requirements stand:
> - the arrow is Android's Back for the screen it is on;
> - it shows only where Back stays in the app, and so not on a tab root;
> - it has a 48 dp target;
> - screen readers announce it as "Back";
> - it mirrors in a right-to-left language.

**Specification wording to record** (`02` §2.4). The paragraph "As built, switching tabs adds to the back stack…" becomes:
> **Tab roots (F25, owner, ‹date›).** Each tab keeps its own back stack, and Back on any tab root leaves the app, so a tab root shows no Back arrow.

**Implementation impact:**
- **`AegisNavGraph.handleTabClick`:** tab selection saves and restores each tab's stack, single-top.
  - **Care point:** Navigation's usual tab pattern keeps the first tab, Calls, under the others. Back from Protection would then show Calls first, which §2.4 forbids. Each tab root must therefore be the only entry beneath its own screens.
- **Settings needs the bottom bar** (`TaskCenterScreen`). Settings shows none today. Under A it becomes a tab root where Back exits, so without the bar someone in Settings could not reach another tab. §2.1 ("Bottom navigation, four destinations, labels always visible") requires it. **This is part of A as proposed; please confirm it explicitly.**
- **Re-selection at a root scrolls to the top** (§2.1): the Calls, Protection, Assistant and Settings roots each need to respond to it.
- **Tests:**
  - `TopBarBackTest.theArrowFollowsTheBackStackAsBuilt` is rewritten to the specified behaviour;
  - new tests: Back on each tab root finishes the activity; switching tabs preserves and restores each stack, also across re-creation; re-selection pops to the root, with no duplicates;
  - device tests on API 35, 28 and 26, and the R8 build's tests.
- **`PRODUCTION-READINESS.md` F25:** becomes "Fixed (#‹PR›)".

**Dependencies:** none external. No ADR or invariant is affected.

### Q5: the planned-voice labels

**Entry to record (`QUESTIONS.md` Q5):**
> **Decided (owner, ‹date›):**
> - correct `assistant_voice_sarvam_accent` from "Hindi Devanagari Native Accent" (a script is not an accent) to **"Native Hindi Accent"**;
> - the six planned-voice rows are translated as they stand, **independently of OD-17**, which stays deferred;
> - they are revisited only if OD-17 changes the planned voices;
> - ADR-0005 and ADR-0007 are unchanged.

**Repository change:** `android/feature/assistant/src/main/res/values/strings.xml`:

    <string name="assistant_voice_sarvam_accent">Native Hindi Accent</string>

**Impact:**
- **Display:** voice setup shows the corrected label. The voice still cannot be chosen ("Not available yet").
- **Tests:** none quote the old label.
- **Worksheets:** regenerated, still 246 rows, 1,553 words.

**Dependencies:** none.

### Q6: the F14 / A75 role-lost banner (closes OD-11)

**Proposed wording** (`ROADMAP-INVENTORY.md` Appendix A):
> **"On-device blocking is off. Numbers you've blocked here won't be blocked until you switch back."**

**Exact repository changes:**
- **`docs/ux/android/06-screens-onboarding-auth-permissions.md`, A75:**
  - the trigger becomes "The app no longer holds the call screening role: another app took it, or the subscriber turned it off (Android does not tell an app which).";
  - **Copy** becomes the wording above.
- **`docs/ux/android/08-user-flows.md`, F14:**
  - the first box becomes "This app loses ROLE_CALL_SCREENING (another app takes it, or the subscriber turns it off)";
  - the banner text matches A75.
- **`android/feature/calls/src/main/res/values/strings.xml`:** `calls_role_lost` becomes the wording above. `ScreeningRoleBannerCopyTest`, which reads the specification, keeps the two equal.
- **`ScreeningRoleStatesTest`** (device): its banner string becomes "On-device blocking is off."

**Factual implications:**
- **It is true in both cases Android allows:** another app took the role, or the subscriber turned screening off. Android does not tell the app which.
- **It no longer claims another app is screening the calls,** which the app cannot know.
- **It names exactly what stopped:** this app blocking the numbers on its blocklist, on the phone.
- **It does not claim all screening stopped.** Forwarding-based screening, once built, is unaffected. Today forwarding is "Not available yet", so nothing else of this app is screening.
- **It matches the app's other wording** for the same state: "Blocking is off." and "On-device blocking is off.".
- **The "Switch back" action is unchanged:** it re-requests the role.

**Kept as historical evidence:** the run reports that quote the old sentence (`ANDROID-SCREENING-DEVICE-VALIDATION.md`, `RELEASE-READINESS-ASSESSMENT.md`) stay as they are.

**Copy wording is not frozen** (`UX_FREEZE.md` §1). Two of the 54 second-review rows change, before any legal review (Q7).

### Q7: legal review of the 54 rows

**Entry to record (`QUESTIONS.md` Q7):**
> **Decided (owner, ‹date›):** before any translated language is released to production, **qualified legal counsel** reviews that language's 54 second-review rows against the English and the documents they cite:
> - account deletion 32;
> - the calling role 11;
> - the signed-out notice 5;
> - contacts 3;
> - the F14 banner 2;
> - the microphone 1.
>
> **The second translator's review checks meaning; it is not legal approval.** Engaging counsel, and its scope and cost, need the owner's separate approval.

**Repository change:** `docs/phase19/LOCALISATION.md` §5's release checklist gains: "Legal review of the 54 second-review rows recorded: reviewer, date, outcome (Q7)." Track it with L1 (E9).

**Impact:**
- **No code change.**
- **Does not block** translating, importing or internal testing of a language.
- **Blocks only that language's production release.**

**Dependencies:** counsel (external; engagement and cost need your approval); E9 and L1.

### Q8: "in your phone's settings"

**Entry to record (`QUESTIONS.md` Q8):**
> **Decided (owner, ‹date›):** the English says where the button goes.

**Repository change:** `android/feature/protection/src/main/res/values/strings.xml`:

    <string name="protection_notice_contacts_denied">Contacts access is off. You can turn it on in your phone\'s settings.</string>

**Impact:**
- **Behaviour:** the button is unchanged; it opens Android's page for this app.
- **Tests:** none quote the old wording.
- **Worksheets:** regenerated (+2 words).

**Dependencies:** none.

### Q9: punctuation

**Entry to record (`QUESTIONS.md` Q9):**
> **Delegated (owner, ‹date›):**
> - the translators' lead chooses sentence-ending punctuation for each language, following Android's own text in that language, and applies it consistently;
> - the reviewer checks the consistency.

**Impact:** none.

### Q11: text outside the app

**Entry to record (`QUESTIONS.md` Q11):**
> **Deferred (owner, ‹date›)** until the relevant services and surfaces are approved:
> - the sign-in SMS and its DLT templates per language (E2);
> - the assistant's voice prompts (OD-17, deferred);
> - the Play Store listing (X1).
>
> Debug-only design previews are not shipped and not translated. **Until then, a language is described as covering the app's screens only.**

**Impact:** none.

### Launcher name: "Aegis AI"

**Entry to record (`QUESTIONS.md` Q1, launcher name):**
> **Decided (owner, ‹date›):** the app's name on the phone is **"Aegis AI"**, as on its own screens.
>
> **A preliminary availability check is not clearance:**
> - **Preliminary check:** a search of public sources: Google Play, the web, and the Indian trademark registry's public search. It is done without an account, and finds only obvious conflicts.
> - **Legal or trademark clearance:** qualified trademark counsel's search and opinion, for India and any other market. It needs the owner's separate approval and may cost money.
>
> The rename ships after the preliminary check. **Clearance is required before the Play listing (X1) and before production.**

**Repository change:** `android/app/src/main/res/values/strings.xml`:

    <string name="app_name" translatable="false">Aegis AI</string>

**Impact:**
- **Where users see it:** Android shows "Aegis AI" for the launcher icon, App info, App languages, the permission prompts and the "Caller ID & spam app" choice.
- **Unchanged:** the application id `com.callscreen`, the `callscreen://` link scheme and internal theme names. Users don't see any of them.
- **Tests:** none quote the label.
- **Documentation:** the architecture documents that call the product "CallScreen" are outside this change.

**Dependencies:**
- the preliminary check: not done; yours, or engineering's with your OK;
- counsel's clearance: external, needs your approval;
- X1 for the Play name.

### On confirmation

Each change is built in its own focused PR, with tests and regenerated worksheets where needed, and merged when its checks pass:
- F25;
- Q5 and Q8 (copy);
- Q6 (OD-11);
- the launcher name.

The records:
- **Q7, Q9 and Q11:** recorded in `QUESTIONS.md`;
- **Q7's release gate:** added to `LOCALISATION.md` §5.
