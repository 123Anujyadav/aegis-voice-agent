# Translating the Aegis Android app: translator package

**Status, 2026-11-04:**
- The app's screens are in **English only**.
- **No translator has been engaged.** That needs the owner's approval first; see "Before any work starts" below.
- **Every string is still awaiting translation** in Hindi, Tamil, Telugu, Malayalam and Kannada (0 of 243 in each).

This folder is what a translator and a reviewer receive for one language:

| File | What |
|---|---|
| This guide | What the app is, how to fill the worksheet, the rules, tone and room on screen |
| [`GLOSSARY.md`](GLOSSARY.md) | The app's terms: what each one means here and how to handle it |
| [`QUESTIONS.md`](QUESTIONS.md) | Points the owner or product must settle before or during translation |
| [`APPROVAL-SUMMARY.md`](APPROVAL-SUMMARY.md) | For the owner: the approvals needed before any work starts, the workload, and who does what |
| `worksheets/<code>.csv` | The worksheet: `hi` Hindi, `ta` Tamil, `te` Telugu, `ml` Malayalam, `kn` Kannada |

Engineers: the workflow and the checks behind it are in [`docs/phase19/LOCALISATION.md`](../phase19/LOCALISATION.md) §6.

## Before any work starts (owner)

1. **Approve the translator and the reviewer for each language.** Two different people: the import refuses a worksheet whose translator and reviewer are the same.
2. **Approve how the worksheet reaches them.** The worksheet holds only the app's interface text, with no user data. But sending it to a person, agency, hosted spreadsheet or translation platform is sharing the product's unreleased text, and needs the owner's explicit approval, as does any cost.
3. **Settle the questions in [`QUESTIONS.md`](QUESTIONS.md)** marked "before translation".

**Not allowed without the owner's explicit approval:**
- machine translation of any kind, including a translator's own tools;
- translation APIs or services.

A worksheet filled by machine and labelled as reviewed is not acceptable.

## The app, in short

Aegis is an Android app for people in India who get unwanted and fraudulent calls:
- **It blocks numbers** the person chooses, on the phone itself, before it rings, even offline.
- **An assistant is planned.** It would answer unknown callers the person doesn't pick up, through call forwarding with their mobile operator. **This is not available yet**, and the screens say so honestly.

Much of the text explains:
- what the app does and does not do;
- what it never does (it cannot hear calls; it never reads or uploads contacts);
- what is not ready yet.

**Those statements are checked against the product's specifications and must keep their exact meaning.** A friendlier or stronger translation that promises more than the English is a defect.

## Filling the worksheet

Open `worksheets/<code>.csv` in a spreadsheet as **UTF-8**, so the Indian script is shown correctly. Fill only the `translation` column, plus `keep_english` and `translator_note` where needed. Leave every other column as it is.

| Column | Meaning |
|---|---|
| `module`, `kind`, `name`, `quantity` | Which string this is. **Don't change.** |
| `english` | The English to translate. **Don't change.** |
| `where` | Where it appears on screen and how much room it has (below) |
| `english_chars` | The English's length, to compare with |
| `note` | The developers' note about the screen or the rule behind the text |
| `placeholders` | Codes such as `%1$s` that the app replaces with a number or a name. **Keep each one exactly**, moved to wherever the sentence needs it |
| `plural_rule` | For counted text: which numbers use the `one` row and which the `other` row in this language |
| `review` | Not empty: the English is quoted in a specification or held to what the app does by a test. The reviewer checks the meaning against that document |
| `translation` | **Your translation** |
| `keep_english` | Write `yes` only if the right translation is exactly the English (rare; explain in `translator_note`) |
| `translator_note` | Questions or remarks for the reviewer and the owner. Not imported |

### The rules the import checks

**A worksheet that breaks any rule is refused whole, and nothing is imported.** The tool lists every problem, so it can be fixed and sent again.

- **Every row is translated.** Don't leave rows empty, delete rows or add rows. A language is either complete or not shipped.
- **Placeholders stay exactly as they are:**
  - `%1$s` and `%1$d` keep their letters and numbers, even when moved;
  - a percent sign in a row with placeholders is written `%%`.
- **Counted text (`kind` = `plural`)** has a `one` row and an `other` row. Read `plural_rule`:
  - **in Hindi and Kannada, `one` is used for 0 and 1;**
  - in Tamil, Telugu and Malayalam, `one` is used only for 1.
- **The translation is in the language's own script:** Devanagari, Tamil, Telugu, Malayalam or Kannada.
  - Latin letters may appear (a product name, "SIM", "AI"), but there must be text in the language's script.
  - Another Indian script is refused, which catches a row pasted from the wrong language.
- **No copying the English**, unless `keep_english` is `yes`.
- **Same number of line breaks as the English** (all rows have none today). No spaces at the start or end.
- **Nothing invisible**, except the zero-width joiner and non-joiner that a script needs. No direction marks and no byte-order marks.
- **The English must not have changed since the worksheet was made.** If it has, the import says which rows; take a fresh worksheet and recheck those rows.

### Room on screen (`where`)

Text in the app wraps onto more lines when it must, and every screen scrolls. Tests show every screen still works when each text is **half as long again** as the English, in the language's own script, at **twice** the normal text size, on a small phone (320 × 480 dp). Those tests use stand-in text, not a translation. Some places are tighter:

| `where` begins | Rows | Room |
|---|---|---|
| Bottom tab label | 4 | A quarter of the screen's width under an icon. One short word |
| Short label in a small rounded chip | 17 | One line, a few words. Filters and statuses such as "Blocked", "To do" |
| Label inside a one-line text box | 7 | **Does not wrap: a long label is cut off.** Keep it short |
| Screen title in the top bar | 10 | Short. One line is best |
| Button or link | 36 | Short. Wraps only if it must |
| Heading, name of a choice | 52 | Wraps if it must |
| Sentence, paragraph, message | 105 | Wraps freely. Clarity first |
| Not shown on screen: read aloud by a screen reader | 12 | Name the thing or action plainly. TalkBack reads it to blind and low-vision users |
| A pattern that joins other text | 3 | Keep the placeholders and punctuation |

**Aim to be no longer than needed. Clarity wins over brevity in sentences, brevity wins in labels.**

## Tone and style

- **Plain, calm and exact.** People read this when a call worried them, or when deciding whether to trust the app with their calls. Short sentences. No marketing, no exclamation marks, no fear.
- **Address the person respectfully,** with the polite form of "you" used in that language's everyday phone apps. **Keep that form the same in every row.**
- **Everyday words people use on their phones over formal or literary vocabulary.** Words commonly used in English in that language on phones, such as "call" or "SIM", may stay as the familiar loanword, written in the language's script. Use one choice consistently.
- **Decided (owner, 2026-11-04):** everyday, polite language with familiar phone-app terms (Q2). `Aegis AI` and `AI` stay in Latin letters (Q1, Q3). The sign-in code is `OTP` (Q4).
- **Never add a promise.** "Blocks spam" must not become "blocks all spam"; "is not available yet" must not become "is coming soon". The English is careful about what the app does, and the translation must be too.
- **Use Android's own words for Android's own things.** The "Caller ID & spam app" setting, permissions and system screens: use the words Android shows on a phone set to that language, so the person recognises them. The glossary marks these.
- **Numbers and phone numbers stay in Western digits** (0–9), as Android shows them by default in these languages. For example "+91 98765 43210".
- **Sentence-ending punctuation:** follow the convention of Android's own text in that language. Hindi text commonly ends sentences with "।" (purna viram); the others commonly use ".". Confirm per language: `QUESTIONS.md` Q9.

## Review

**The reviewer is a second fluent speaker.** They:
1. read every row against the English, the `note` and the glossary;
2. check every row with a `review` entry against the named document. These are about deletion, privacy, what the app can hear and what it blocks, and **must keep their exact meaning**;
3. resolve each `translator_note` with the translator, or raise it with the owner.

**After import, a native speaker checks the app itself on a phone** set to the language: each screen, at a large text size, and with TalkBack reading. The checklist is `LOCALISATION.md` §5. Only then is the language marked translated in the app.

## Workload per language

Measured from the English source with `python3 tools/i18n/translation_kit.py audit`, after the owner's language decisions D3, D4 and D7 (2026-11-04). **It is the same for each of the five languages.**

| Measure | Per language |
|---|---|
| Resources (241 strings and 2 counted texts) | 243 |
| Worksheet rows (each counted text has 2 rows) | 245 |
| English words (placeholders not counted) | 1,553 |
| English characters | 9,149 |
| Rows with placeholders | 11 |
| Rows a specification or test quotes (second-review rows) | 54 |
| Rows with tight room (tabs, chips, one-line fields) | 28 |

| Module | Rows | Words |
|---|---|---|
| Onboarding (welcome, SIM, sign-in, permissions, setup) | 81 | 513 |
| Settings (setup checklist, app language, account deletion) | 54 | 476 |
| Protection (dashboard, blocked and allowed numbers) | 60 | 361 |
| Assistant | 24 | 122 |
| Calls | 10 | 45 |
| Shared components | 11 | 15 |
| App shell (signed-out notice) | 5 | 21 |

**Effort (a planning assumption, not a quote or a measurement).** Interface text is slower than prose: short strings, context to read, and a glossary to keep. Assuming a professional translator does about 1,000–1,500 words a day of interface text:

| Step | Per language | All five |
|---|---|---|
| Translation | 1–2 days | 5–10 days |
| Second review | about 1 day | about 5 days |
| On-phone check by a native speaker | about half a day | 2–3 days |

Settling the questions in `QUESTIONS.md` first avoids rework. **No cost is estimated:** it depends on the translators the owner chooses.

## Delivering a translation

Return the filled worksheet with:
- the translator's name or agency;
- the reviewer's name;
- the date.

An engineer imports it with:

    python3 tools/i18n/translation_kit.py import --lang hi --translated-by "<translator, date>" --reviewed-by "<reviewer, date>" hi.csv

That records both names in each translated file. The build then runs its own checks, and the on-phone check follows (above).
