# Owner decision packet: language UX, translation, speech evaluation

- **Status: DECIDED. The owner explicitly approved all 14 decisions (D1–D9, Q0–Q4) and the speech deferral (A1) on 2026-11-04.** The "Your decision" column records the owner's words. The "Recommendation" column is kept as it was proposed, for traceability; where the decision differs, the decision governs.
- **What the decisions authorize:** product direction, its recording, and the app changes for D4, D6 and D7. **They do not authorize** contacting, hiring or paying translators, spending money, creating external accounts, accepting third-party terms or activating external services. Each still needs the owner's separate approval.
- **Prepared:** 2026-11-04 by Claude Code, on main `91e6f1f`. It consolidates three briefs, which hold the detail:
  - [`LANGUAGE-SELECTION-DECISIONS.md`](../ux/android/reviews/LANGUAGE-SELECTION-DECISIONS.md);
  - [`APPROVAL-SUMMARY.md`](../translation/APPROVAL-SUMMARY.md);
  - [`OD-17-DECISION-BRIEF.md`](OD-17-DECISION-BRIEF.md).
- **Nothing has been done that needs your approval:**
  - no translator contacted, and no worksheet sent out;
  - no machine translation;
  - no Hugging Face account, terms, download or recording;
  - no cost incurred;
  - ADR-0005 and ADR-0007 unchanged.
- **Today the app is English only:** 0 of 244 strings are translated in each of the five other languages.

**How the decisions are carried out:** recorded in this packet and the documents each one touches. D4, D6 and D7 are built in their own PRs, with the UX specification amended in the same PR (`UX_FREEZE.md` §7). **Note on IDs:** these D1–D9 are the language-UX decisions. They are not account-deletion D1–D9 (tracker O9–O17).

---

## 1. Language UX: nine decisions (A58l App language, and Welcome's Language control)

**Blocks:**
- **Implementation:** the decision must be made before the related app change can be built.
- **Translation:** it changes or adds strings, so deciding it before translation starts avoids rework.

**Every row needs your approval** (frozen UX, `UX_FREEZE.md` §7; D8 is a product decision).

| # | Decision required | Recommendation | Reason | UX, accessibility and translation consequences | Blocks | Your decision |
|---|---|---|---|---|---|---|
| **D1** | Accept A58l, the App language screen, as specified and built | **Accept**, subject to D3–D9 | Simple and reversible. Its first line separates the screen language from the assistant's voice | **UX:** one choice of seven, applied on tap. **A11y:** one radio group; native names carry a locale span, but TalkBack's speech is **not verified on a device**. **Translation:** none new | Neither: it's built. Formally records the frozen-UX change | **Approved (2026-11-04):** accept the App Language screen design. |
| **D2** | Accept Welcome's Language control (a globe and the word, top end) | **Accept**; its label is D4 | Lets someone who can't read the screen language change it before sign-in, without competing with "Get Protected" | **UX:** off the main path. **A11y:** announced once; 48 dp target; fits at 2× text on 320 × 480 dp. **Translation:** see D4 | Neither | **Approved (2026-11-04):** accept the Welcome screen language control design. |
| **D3** | Untranslated languages: listed with a note (as built), hidden, or listed but disabled | **Keep as built** | Hiding them goes against your instruction that any of the six can be chosen; a disabled row is harder to read | **UX:** a choice may look broken while nothing changes (U3). **A11y:** the note is read with each row. **Translation:** the note disappears per language once translated | Neither | **Approved (2026-11-04):** keep untranslated languages visible with an explanatory note, clearly stating that screens remain in English until reviewed translations are available. |
| **D4** | Welcome control's label: the screen language only (as built), bilingual "भाषा · Language", or a globe with six names | **Bilingual** | It is the way out for someone who can't read the screen language. English is the app's fallback; six names wouldn't fit at large text | **UX:** two words, one line. **A11y:** the English part needs its own locale span; must still pass the 2× text tests. **Translation:** the row is translated as the screen word only, and translators must be told (Q10) | **Translation** (1 row) and implementation | **Approved (2026-11-04):** use the bilingual wording `भाषा · Language`, with TalkBack language metadata for both parts. |
| **D5** | Where A58l lives: the Settings tab section (as built), or under A58 Appearance (`02`), which is not built | **Keep as built until A58 exists**, then move it and keep a shortcut | Findable now. The spec's place doesn't exist yet | **UX:** one step fewer today. **A11y:** unchanged. **Translation:** Appearance's strings later | Neither now | **Approved (2026-11-04):** keep App language under Settings; **do not move it under Appearance.** |
| **D6** | An Up arrow in the top bar of screens opened from another, or rely on system Back (as built) | **Add app-wide** | People who don't use the Back gesture may not find the way out (U2) | **UX:** shared component, many screens. **A11y:** a 48 dp target; an on-screen way back for Switch Access and keyboard users. **Translation:** 1 new string ("Navigate up") | **Implementation**, and 1 string to translate | **Approved (2026-11-04):** add a top-bar Back arrow across the application, preserving existing navigation and screen-state behaviour. |
| **D7** | Name the phone's language in its row and in Settings, e.g. "Phone's language (English)", or keep it generic (as built) | **Name it** (the language the app resolves to); Settings also shows the name in the screen language | On an English phone "Phone's language" looks like a duplicate of "English" (U4, U6) | **UX:** clearer default. **A11y:** the named language gets a locale span. **Translation:** about 2 new strings with a placeholder | **Translation** (about 2 rows) and implementation | **Approved (2026-11-04):** use `Phone's language (English)`, the name dynamically reflecting the phone's actual language; English is not hard-coded. |
| **D8** | Is the app's screen language free, or premium? (`07` lists "multiple languages" as premium) | **Free**; "multiple languages" in premium means the assistant's voice languages | A person who can't read English must be able to use the free app | **UX:** none (built free). **A11y:** a free screen language is itself an accessibility measure. **Translation:** none | Neither today; it fixes `07`'s wording | **Approved (2026-11-04):** the screen language is free for all users. Premium restrictions may apply to assistant voice features, not to the screen language. |
| **D9** | After a choice: stay on A58l (as built), or return | **Stay**; consider a short confirmation once translations exist | A wrong tap can be fixed at once by someone who may not read the new language | **UX:** no confirmation today (U7). **A11y:** a polite live-region confirmation would tell screen-reader users. **Translation:** a confirmation adds 1 string | Neither now | **Approved (2026-11-04):** remain on the language-selection screen after a language is selected. |

**Suggested order:** D8, D4 and D7 first. D4 and D7 change strings, and D8 frames the rest.

**Also awaiting design (not one of the nine):** U8. Welcome's feature icons repeat their titles to TalkBack. The fix is to make the icons decorative.

---

## 2. Translation: Q0–Q4

**Scope, measured:** per language, 246 worksheet rows, 1,548 English words and 9,108 characters. Of those rows, 54 need a second review against a specification (deletion, privacy, the calling role, the microphone, the signed-out notice) and 28 have tight room. The figures are the same for Hindi, Tamil, Telugu, Malayalam and Kannada.

### The questions

| # | Exact question | Options | Recommendation and rationale | Rows | Your decision |
|---|---|---|---|---|---|
| **Q0** | **Who translates and who reviews each language, how the worksheets reach them, and what you'll pay** | (a) fluent speakers you already work with; (b) volunteer community reviewers; (c) a professional translator or agency. Mixing per language is possible | **(a), with (b) or (c) where you have no one.** Quickest to trust, and no third party. Ask any agency whether it uses machine translation: that needs your separate approval | All | **Approved (2026-11-04):** known fluent speakers translate, with a separate reviewer for every language, who is a different person from the translator. Nobody is contacted, hired or paid without further owner authorization; consent is obtained before their names are recorded in the repository. |
| **Q1** | **Inside a translated sentence, does the product name stay in Latin letters, or is it written in the language's script?** And separately: is the launcher name "CallScreen" or "Aegis AI"? | Latin letters · the language's script | **Latin letters ("Aegis AI")**, as the top bar shows it. Decide the launcher name separately, because it affects English too | 5 | **Approved (2026-11-04):** keep `Aegis AI` in Latin letters in every translation. |
| **Q2** | **Formal or everyday language?** | Formal or literary (e.g. Sanskritised Hindi) · everyday phone-app language | **Everyday phone-app language**, the polite "you", and loanwords ("SIM", "spam") where Android's own text uses them. The translators' lead proposes per language; you confirm | All. A later change is a full re-translation | **Approved (2026-11-04):** everyday, polite language; familiar phone-app terms; loanwords where Android's own text commonly uses them. |
| **Q3** | **Keep "AI" in Latin letters, or translate it?** | Keep "AI" · translate it | **Keep "AI":** short, fits chips and titles, widely recognised | 5 alone, plus 2 in "Aegis AI" | **Approved (2026-11-04):** keep `AI` untranslated. |
| **Q4** | **"Verification code" or "OTP" for the sign-in code?** | "Verification code" (as in English) · "OTP" · a native term chosen by the translators | **No recommendation between the terms; your choice**, applied the same way in all five languages. "OTP" is familiar in India but not everyone can expand it. Whatever you choose, it must not be reused for the country code, the area code or the forwarding code | 10 | **Approved (2026-11-04):** use `OTP` consistently in all five non-English translations. |

**Correction (2026-11-04):** Q4 covers **10** rows, not 8 as first counted. The count missed "Enter Verification Code" and "Send Verification Code" (a case-sensitive search). The import now enforces `OTP` in all 10 (`SIGN_IN_CODE_ROWS` in `tools/i18n/translation_kit.py`).

### Translator and reviewer requirements

- **Two different fluent people per language.** The import refuses a worksheet whose translator and reviewer are the same person.
- **Translator:**
  - fills the `translation` column and keeps every placeholder;
  - follows the glossary and uses Android's own words for Android's settings;
  - writes a note for anything unclear;
  - **uses no machine translation without your approval.**
- **Reviewer:**
  - reads every row against the English and the glossary;
  - checks the 54 second-review rows for **exact meaning**;
  - resolves the translator's notes.
- **On-phone checker:** a native speaker, who may be the reviewer. Goes through each screen on a phone set to the language, at large text and with TalkBack (`LOCALISATION.md` §5).
- **Whether a lawyer also reviews the deletion and privacy rows** in each language is your decision (Q7). This is not legal advice.

### Cost

**No quote exists, and no money figure is given here.**

| What | Kind | Figure |
|---|---|---|
| People's time per language | **Estimate** (planning assumption: 1,000–1,500 words a day of interface text) | Translation 1–2 days; review about 1 day; on-phone check about half a day. **About 3–4 working days per language**, about a week elapsed if all five run in parallel |
| Engineering per language | **Estimate** | About half a day: import, checks, device tests, merge |
| Fluent speakers you already know, or volunteers | Your arrangement | Possibly nothing |
| A professional or agency | **Needs a quote** | Usually 1,548 words × their per-word rate per language, plus review (per word or hourly). Get a written quote per language before approving |
| **Confirmed quotes** | — | **None** |

### Delivery and review workflow

1. You approve Q0 per language and decide Q1–Q4. Q5–Q8 and D4/D7 can follow before their rows.
2. Engineering records the decisions in the glossary and `QUESTIONS.md`. If they changed the English, it regenerates the worksheets.
3. **You, or someone you name, send the worksheets.** Claude Code doesn't contact translators.
4. The translator and the reviewer return the filled worksheet with their names and the date.
5. Engineering imports it with `--translated-by` and `--reviewed-by`. An incomplete or invalid worksheet is refused as a whole.
6. Engineering runs the layout and unit tests, the device tests on API 35, 28 and 26 and the R8 build, and merges one language per batch.
7. The on-phone check follows. Only then does that language's "Not translated yet" note disappear.

### Privacy and confidentiality

- **The worksheet holds only interface text**, with notes on where each string appears. No user data, no secrets, no credentials.
- **It is unreleased product text.** Consider a confidentiality agreement, especially with an agency.
- **Sending method:** an email attachment or a folder you control. **A hosted spreadsheet or translation platform is a third-party service** and needs your approval like any other.
- **The translator's and reviewer's names are recorded in the repository** (in the imported files' headers). Tell them, and get their agreement.

---

## 3. Speech evaluation: OD-17, approval request A1

**OD-17 itself is not being asked here.** OD-17 is the choice between open models and the paid vendors in Accepted ADR-0005 and ADR-0007; the recommendation is to measure first (option C). **A1 is the first, smallest step**, and it decides nothing about the ADRs.

### What A1 asks

**You** (not Claude Code) would:
1. create a Hugging Face account, or use your existing one;
2. **read** the access conditions of the two gated models: `ai4bharat/indic-conformer-600m-multilingual` (speech-to-text) and `ai4bharat/indic-parler-tts` (text-to-speech);
3. **accept them only if acceptable**;
4. then download the two models to a machine you control.

### The account

- **What:** a Hugging Face account. A basic account costs nothing.
- **Owner:** **you.** Claude Code doesn't create external accounts.
- **Credential:** downloading a gated model with the libraries needs an **access token**. That is a credential.
  - It stays with you.
  - Giving it to Claude Code, or storing it anywhere in this project, would need a separate approval, and it must never enter the repository or logs.

### The model conditions and terms

| | IndicConformer 600M | Indic Parler-TTS |
|---|---|---|
| Licence on the model card | MIT | Apache-2.0 |
| Gate | Conditions shown only after signing in | The same |
| Conditions read? | **No** | **No** |
| What the gate states it shares | Your account's information, with the publisher (AI4Bharat) | The same |

**The unread conditions may add terms to the licence:** for example, on attribution, permitted use, or commercial use of the named speakers' voices (Parler-TTS has 69). Reading them is the point of A1. If they are unacceptable, the open-model path (option A) is ruled out, at no cost.

### What is visible without an account (checked 2026-11-04)

Sources, all public and read without signing in:
- the two model pages;
- their public metadata (`https://huggingface.co/api/models/ai4bharat/<model>`);
- Hugging Face's documentation on gated models (`https://huggingface.co/docs/hub/models-gated`).

| | Finding | Basis |
|---|---|---|
| **Known** | Both pages show the gate heading "You need to agree to share your Hugging Face information to access this model". They say: "This repository is publicly accessible, but you have to accept the conditions to access its files and content." | The model pages |
| **Known** | **Approval is automatic** for both (`"gated": "auto"`): access is granted as soon as the request is sent, with no review by AI4Bharat | Public metadata; Hugging Face docs |
| **Known** | Hugging Face's default gate shares the requester's **username and primary email address** with the model's authors, unless they opted out. Authors may add their own form fields and prompt text | Hugging Face docs |
| **Known** | The authors **can withdraw access at any time**, without notice | Hugging Face docs |
| **Known** | **Downloading by script needs an access token** from the account. A browser download needs only the signed-in session | Hugging Face docs |
| **Known** | Licences on the cards: IndicConformer MIT, Indic Parler-TTS Apache-2.0. The Parler-TTS card also lists Creative Commons licences for its training datasets (for example CC BY 4.0) | The model pages |
| **Known** | IndicConformer's own example loads it with `trust_remote_code=True` and resamples audio to 16 kHz. Its page now recommends a newer model, Indic-Transcribe | The model page |
| **Inferred, not confirmed** | **No custom terms or extra form fields:** neither model's public metadata has `extra_gated_prompt` or `extra_gated_fields`, which is where custom terms and fields are declared. So the conditions are probably only Hugging Face's default information sharing | Public metadata. **Only signing in shows the actual form** |
| **Unknown** | The exact text of the form, and whether anything beyond username and email is asked | Needs a signed-in account (A1-read) |
| **Unknown** | Whether the authors collect the email, or only the username | Not public |
| **Unknown** | Any commercial-use or voice-use terms outside the licence and the card | Not public |

**What this changes:** probably little to accept. But the gate's actual text, and whether it adds anything, can only be confirmed by signing in. That is still A1-read, your step.

### The approval steps, kept separate

Each step needs its own approval. Approving one does not approve the next.

| Step | What it allows | Covered by |
|---|---|---|
| 1. Account and read | You sign in (or create an account) and read the two gates' text. Nothing accepted, nothing downloaded | **A1-read** |
| 2. Accept and download | You accept and download the files to a machine you control. Your username and email go to the authors | **A1** |
| 3. Execute | Loading or running either model; for IndicConformer, running its publisher's code | **A4** (review, pinned revision, isolated, no network) |
| 4. Record | Any voice recording or test call, including your own | **A2** (consent, storage and retention decided first) |
| 5. Compute | Any GPU or cloud machine | **A3** (recurring cost while running) |
| 6. Host | Running a model as a service | OD-17 (A5) |
| 7. Real calls | Any caller's audio reaching a model | OD-17, and the independent human review (OD-12) |

### What approving A1 would authorize

- Creating or using **your** Hugging Face account, and reading the two models' conditions.
- **Accepting** them, if you find them acceptable.
- **Downloading** the two models' files to a machine you control.
- Recording a summary of the conditions in `OD-17-DECISION-BRIEF.md`, from what you report.

### What A1 would not authorize

| Not authorized | Which action covers it instead |
|---|---|
| Loading IndicConformer, which runs its publisher's code (`trust_remote_code`) | **A4**, after a review, pinned and isolated |
| Recording anyone's voice, or collecting test calls | **A2**, with consent, storage and retention decided first |
| A GPU, a cloud account or any paid compute | **A3** |
| Using either model on real calls | The independent human review (**OD-12**) |
| Hosting a model | OD-17 itself (A5) |
| Changing ADR-0005 or ADR-0007 | **OD-17** (A5) |
| Claude Code creating an account, accepting terms, or holding your token | Not requested |

### Would any data be uploaded?

**No.**
- A1 downloads only.
- No recordings, no app data and no user data leave anywhere.
- Hugging Face, and through the gate the publisher, receive **your account information**.
- Hugging Face will also see the downloads themselves (normal server logs).

### Costs and hosting

| Item | Cost |
|---|---|
| The account | None for a basic account |
| Disk | Weights about **2.4 GB** (IndicConformer) and **3.6 GB** (Parler-TTS) at 32-bit, computed from their published sizes. The libraries they need (PyTorch and others) add several GB |
| Bandwidth | A one-time download of those sizes |
| Hosting | **None.** A1 hosts nothing |
| Running the models | Not part of A1. A CPU-only trial on a machine you own costs nothing extra but needs A4 and a few consented recordings (your own voice, for example). A GPU is A3 and **charges while it runs** |

**Options:**

| Option | Means |
|---|---|
| **A1-read** (smallest) | Account, and read the conditions only. Don't accept yet |
| **A1** (recommended) | Read, accept if acceptable, and download |
| **Defer** | Nothing happens. OD-17 stays undecidable on evidence |
| **Decline** | You choose option B (keep the ADRs' paid vendors, with a policy exception) without measuring |

**Recommendation: A1**, or **A1-read** if you prefer to see the conditions first. Reading them is free, and it settles G7 (the terms), which can rule option A in or out on its own.

**Owner's decision (2026-11-04): DEFER all speech model access and evaluation.**
- **Not authorized:**
  - Hugging Face sign-in for these models;
  - accepting their conditions;
  - downloading or executing them;
  - voice recordings;
  - GPU or cloud provisioning;
  - hosting or activating a speech model.
- **Any future step needs separate, explicit owner approval.**
- **OD-17 stays open.** ADR-0005 and ADR-0007 remain Accepted and unchanged.

---

## Appendix: production readiness, what else waits

From [`PRODUCTION-READINESS.md`](PRODUCTION-READINESS.md) and [`ROADMAP-INVENTORY.md`](ROADMAP-INVENTORY.md) on `91e6f1f`. **Verdict unchanged: not production-ready.**

### Remaining owner decisions, beyond this packet

| Group | Items |
|---|---|
| **High-risk** (each also needs an independent human reviewer, OD-12) | **H7** (a refused refresh proof ends the session; Proposed A + B + E). **H1** (ADR-0032 email and Google sign-in, Proposed). H2, H3 (ADR amendments, not drafted). H4–H6 (ratifying ADR-0029/0030/0031, if you classify them high-risk) |
| **OD** | **OD-12** (name the independent reviewer: it gates every high-risk item). **OD-10** (what "Phase 19 complete" means). **OD-11** (the F14 banner's sentence). **OD-13** (corrupt session file). **OD-14** (sign-out action). **OD-15** (an unconfigured identity refuses to start in staging and production). **OD-16** (base-image updates). **OD-17** (speech). **OD-18** (the held Dependabot PRs #182–#185). Also OD-1–OD-4 and OD-8 |
| **Tracker O1–O17** | ADR ratifications (O1, O2, O7), OTP backoff (O3), session lifetime (O4), SIM-swap policies and retention (O5, O6), the restricted tier (O8), account-deletion D1–D9 (O9–O17) |
| **Privacy and legal** | L1–L6: DPDP review, erasure record retention, legal hold period, privacy policy, Data safety answers, contacts disclosure |

### External dependencies

| Dependency | Unblocks |
|---|---|
| **E2** SMS/OTP provider and India DLT | Anyone signing in; web deletion |
| **X5** deployed identity and edge-api hosts (with E3 signing keys, E4 database) | Sign-in from any release build; T3 |
| **X1, X2** Play Console account and upload key | Any Play release |
| **X3, X4** physical phones, Indian SIMs, DID pool and carrier forwarding | T2 carrier tests; forwarding |
| **E1** Play Integrity project | Real integrity verdicts |
| **E5** tracing backend · **E6** trusted-proxy model · **E7** FCM · **E8** cryptographic review or penetration test · **E9** DPDP review · **E10** web deletion page hosting | As named |

### Evidence gaps

| Level | State |
|---|---|
| **Physical device** | **None.** API 36 has never run on any device. The R8 build has run only on emulators. The baseline profile's startup gain is unmeasured (X3) |
| **Staging** | **None.** No hosts exist (X5). No runbook has been exercised; signing-key rotation (T5) waits on E3 |
| **Production** | **None.** Nothing is deployed, and there is no Play release |
| **Emulator (what does exist)** | The instrumented tests and the R8 build's functional tests on API 35, 28 and 26, in CI |

### Safe work without your approval

**Little remains, and none of it is new engineering:**
- **Keeping records current.** The tracker header was refreshed to `91e6f1f` by #236 (`c07c780`).
- **Once you decide any item in this packet,** recording the decision and building what it unblocks. For example, D4 and D7 strings, D6's Up arrow, or regenerating the worksheets after Q1–Q4.

**Nothing else in the tracker is both routine and unblocked.** Claude Code is not starting new engineering just to stay busy.
