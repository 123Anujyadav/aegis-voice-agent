# Translation: what the owner needs to approve

- **Status, 2026-11-04:**
  - the app's screens are in **English only**: 0 of 244 strings are translated in each of Hindi, Tamil, Telugu, Malayalam and Kannada;
  - **no translator or reviewer has been contacted, engaged or sent anything;**
  - no machine translation has been used.
- **What this page is:** the short form of [`QUESTIONS.md`](QUESTIONS.md) Q0–Q4, with the workload and who does what. The details are in [`README.md`](README.md) (the translator's guide), [`GLOSSARY.md`](GLOSSARY.md) and the five worksheets in `worksheets/`.
- **Decided by the owner on 2026-11-04: Q0–Q4 approved** ([`OWNER-DECISION-PACKET-2026-11-04.md`](../phase19/OWNER-DECISION-PACKET-2026-11-04.md)):
  - known fluent speakers translate, each language with a separate reviewer;
  - `Aegis AI` and `AI` stay in Latin letters;
  - everyday, polite language;
  - `OTP` in all five.
- **Not authorized by those decisions:** contacting, hiring or paying anyone, or any hosted translation platform. Each needs the owner's further approval. Consent is needed before anyone's name is recorded.
- **Prepared by Claude Code.** The recommendations below are kept as proposed.

**The six languages stay English, Hindi, Tamil, Telugu, Malayalam and Kannada.** English is the source and is complete; the other five each need the steps below.

## 1. Approvals before any work starts

| # | You decide | Recommendation | Why it is needed first |
|---|---|---|---|
| **Q0** | **Who translates and who reviews each language**, how they receive the worksheet, and any cost | Two fluent people per language whom you already trust; see §2 | Nothing can start without it. The import refuses a worksheet whose translator and reviewer are the same person |
| **Q1** | **The product's name** inside translated sentences: Latin letters or the language's script | **Latin letters** ("Aegis AI"), as the top bar shows it. Decide the launcher name, "CallScreen" or "Aegis AI", separately: it affects English too | 5 rows name the product. Changing it later means re-reviewing them in all five languages |
| **Q2** | **Register:** formal or everyday language | **Everyday phone-app language**, the polite "you", and loanwords ("SIM", "spam") where Android's own text in that language uses them. The translators' lead proposes per language; you confirm | Every row. A change of register later is a full re-translation |
| **Q3** | **"AI":** keep the Latin letters or translate | **Keep "AI"**: it is short, fits the chips and titles, and is widely recognised | 5 rows use "AI" alone, and 2 more in the name "Aegis AI" (Q1) |
| **Q4** | **"Verification code" or "OTP"** for the sign-in code | **One term in all five languages, chosen by you.** "OTP" is familiar in India, but some people don't know what it stands for. Keep "6" in Western digits | 8 rows. The translation must also not reuse that word for the other codes: the country code, the area code and the operator's forwarding code |

**Also before the rows they affect** (they need not hold up the rest):

| Item | Rows | Depends on |
|---|---|---|
| Q5: the planned-voice labels, one of which is inaccurate | 6 | **OD-17** (speech providers) |
| Q6: the "another app is screening your calls" banner | 1 | **OD-11** |
| Q7: whether a lawyer reviews the deletion and privacy rows in each language | 54 rows have a second review either way | Your decision |
| Q8: "in Settings" means Android's settings | 1 | Product |
| Q10 and design D4: Welcome's "Language" control, translated alone or bilingual | 1 | Design ([`LANGUAGE-SELECTION-DECISIONS.md`](../ux/android/reviews/LANGUAGE-SELECTION-DECISIONS.md)) |
| Design D7: naming the phone's language | about 2 new rows | Design |

## 2. Q0 in detail: translators, reviewers and sharing

**Fill in one row per language when you approve it:**

| Language | Translator | Reviewer (a different person) | On-phone check by | How the worksheet is shared | Cost | Approved on |
|---|---|---|---|---|---|---|
| Hindi | | | | | | |
| Tamil | | | | | | |
| Telugu | | | | | | |
| Malayalam | | | | | | |
| Kannada | | | | | | |

**Options for who:**

| Option | Cost | Notes |
|---|---|---|
| **Fluent speakers you already work with** (recommended) | None, or what you agree | Quickest to trust. They need the guide and glossary, and an hour's briefing |
| **Volunteer community reviewers** | None | Slower and less predictable. Suits the reviewer role better than the translator's |
| **A professional translator or agency** | A quote per language; no figure is estimated here | Ask whether they use machine translation: **that is not allowed without your separate approval**, including their own tools |

**What is shared, and the privacy side:**
- **The worksheet holds only the app's interface text**, with notes on where each string appears. It contains **no user data and no secrets.**
- It is **unreleased product text.** You may want confidentiality terms, especially with an agency.
- **How it travels is your choice:** an email attachment, a shared folder you control, or a hosted spreadsheet or translation platform. **A hosted platform is a third-party service and needs your approval like any other.**
- **The import records the translator's and reviewer's names** in the repository. Tell them so, and get their agreement.

## 3. Workload

Measured from the English source (`python3 tools/i18n/translation_kit.py audit`). **It is the same for each language.**

| Measure | Per language | All five |
|---|---|---|
| Worksheet rows | 246 | 1,230 |
| English words | 1,548 | 7,740 |
| Rows with a second review against a specification (deletion, privacy, the calling role, the microphone, the signed-out notice) | 54 | 270 |
| Rows with tight room (tabs, chips, one-line fields) | 28 | 140 |
| Rows with placeholders | 10 | 50 |

**Effort.** This is a planning assumption, not a quote or a measurement: about 1,000–1,500 words a day of interface text.

| Step | Per language | All five, one after another | All five in parallel |
|---|---|---|---|
| Translation | 1–2 days | 5–10 days | 1–2 days |
| Second review | about 1 day | about 5 days | about 1 day |
| On-phone check by a native speaker | about half a day | 2–3 days | about half a day |
| **Engineering per batch:** import, checks, device tests, merge | about half a day | 2–3 days | 2–3 days |

**A language takes about 3–4 working days of people's time.** Five translator pairs working in parallel could finish in about a week of elapsed time, once the questions in §1 are settled.

## 4. Who does what

| Role | Does | Doesn't |
|---|---|---|
| **Owner** | Approves the people, the sharing method and any cost (Q0). Decides Q1–Q4, and Q5–Q8 when their rows come up. Decides whether a language is shipped | — |
| **Translator** | Fills the `translation` column, keeps every placeholder, follows the glossary, and writes a `translator_note` for anything unclear | Use machine translation without your approval |
| **Reviewer** (a second fluent speaker) | Reads every row against the English, the note and the glossary. Checks the 54 second-review rows against the named document: their meaning must be exact. Resolves each translator's note | Review their own translation |
| **On-phone checker** (a native speaker; may be the reviewer) | Goes through each screen on a phone set to the language, at a large text size and with TalkBack (`docs/phase19/LOCALISATION.md` §5) | — |
| **Engineering (Claude Code)** | Imports a returned worksheet with `--translated-by` and `--reviewed-by`. The import refuses an incomplete or invalid worksheet as a whole. Runs the layout and unit tests, the device tests on Android 8, 9 and 15 and the R8 build, and merges each language as its own batch | Contact anyone, send a worksheet out, translate, or mark any text as reviewed |

**A language is marked translated in the app only after all three people have finished**, and the import's checks and the device tests pass.

## 5. What happens after you approve

1. You approve Q0 for one or more languages, and decide Q1–Q4.
2. Engineering records your decisions in `QUESTIONS.md` and the glossary, and regenerates the worksheets if a decision changed the English. The worksheets are always kept equal to the English by a test.
3. **You, or someone you name, send the worksheets.** Claude Code does not contact translators.
4. The translator and reviewer return the filled worksheet with their names and the date.
5. Engineering imports, checks, tests on devices and merges, one language at a time.
6. The on-phone check; then that language's "Not translated yet" note disappears from the app.
