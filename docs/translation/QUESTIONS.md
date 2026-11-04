# Translation: questions to settle

**For:** the owner, and the translators' lead once one is approved.

These came from auditing the English source (244 resources, 246 worksheet rows) on 2026-11-04. Each item says:
- who decides it;
- whether it must be settled **before translation** starts or can be settled **during** it;
- which rows it affects;
- a recommendation. A recommendation is not a decision.

**None is decided. Nothing in the app has been changed for them.** The owner's short form, with the workload and who does what, is [`APPROVAL-SUMMARY.md`](APPROVAL-SUMMARY.md).

| # | Decides | When | Question |
|---|---|---|---|
| Q0 | Owner | Before | Translators, reviewers, cost and how the worksheet is shared |
| Q1 | Owner | Before | The product's name, and its script in translated sentences |
| Q2 | Owner, with the translators' lead | Before | Register: formal or everyday, and loanwords |
| Q3 | Owner | Before | "AI": keep or translate |
| Q4 | Owner | Before | "Verification code" or "OTP" |
| Q5 | Owner (OD-17) | Before those rows | The planned-voice rows describe undecided vendors, and one is inaccurate |
| Q6 | Owner (OD-11) | Before that row | The F14 banner's first sentence may change |
| Q7 | Owner | Before | Legal review of the deletion and privacy rows |
| Q8 | Product | Before that row | "in Settings": Android's or the app's |
| Q9 | Translators' lead | During | Sentence-ending punctuation per language |
| Q10 | Design review | Before that row | The Welcome screen's "Language" button in a language the person may not read |
| Q11 | Owner | Separate | The sign-in SMS and other text outside the app |

## Q0. Translators, reviewers and sharing (before)

**Rows:** all.

The owner decides:
- who translates and who reviews each language (two different people);
- how the worksheet reaches them;
- the cost.

Before approval, the owner should see the usual details: the provider, the cost and any recurring charges, the data shared and its privacy implications. Here the data is the app's interface text only: **no user data, no secrets.** But it is unreleased product text, and the owner may want confidentiality terms.

**Free alternatives:**
- fluent speakers the owner already works with;
- volunteer community reviewers.

Either way, they are two people per language and their names are recorded on import.

## Q1. The product's name (before)

**Rows:** `onboarding_welcome_title` ("Welcome to Aegis AI"), `onboarding_phone_subtitle_number`, `settings_deletion_entry_description` ("your Aegis account"), `core_ui_logo` and `onboarding_splash_logo_description` ("Aegis Shield Logo").

The English uses three names:
- **"CallScreen"**: the launcher label, `app_name`;
- **"Aegis AI"**: the top bar and onboarding;
- **"Aegis"**: "Aegis account".

The names themselves are not translated (`translatable="false"`). What the translator needs to know is **whether a name inside a translated sentence stays in Latin letters or is written in the language's script.**

**Recommendation:**
- keep the name in Latin letters inside every translated sentence, as the top bar shows it;
- settle the launcher name ("CallScreen" or "Aegis AI") separately. It affects English too.

## Q2. Register and loanwords (before)

**Rows:** all.

Should the text use:
- formal or literary vocabulary, for example Sanskritised Hindi; or
- the everyday language of phone apps, where words such as "call", "SIM", "spam" and "block" are often used as loanwords?

**Recommendation:**
- everyday phone-app language;
- the polite "you" throughout;
- loanwords where Android's own text in that language uses them;
- the same choice in every row.

The translators' lead proposes; the owner confirms per language.

## Q3. "AI" (before)

**Rows:** `onboarding_ai_title` ("AI Screening Persona"), `assistant_voice_setup_title` ("AI Voice Customization"), `calls_filter_ai_screened` ("AI Screened"), `protection_dashboard_screenings` ("AI screenings").

Should "AI" stay as the Latin abbreviation or be translated?

**Recommendation:** keep "AI" in Latin letters. It is short, it fits the chips and titles, and it is widely recognised. The owner may prefer the language's own term.

## Q4. "Verification code" or "OTP" (before)

**Rows:** `onboarding_phone_title_code`, `onboarding_phone_label_code`, `onboarding_phone_resend`, `onboarding_phone_send`, the code messages, and `settings_deletion_deleted_sessions` ("pending verification codes").

In India the one-time sign-in code is widely called "OTP". The English says "verification code" and "6-digit code".

**Recommendation:** decide one term for all five languages. "OTP" is familiar, but it is an abbreviation some people don't expand.

## Q5. The planned-voice rows (before those rows)

**Rows (6):** `assistant_voice_*_style` and `assistant_voice_*_accent`.

These describe three vendor voices, which can't be chosen today:
- ElevenLabs "Calm Professional", "English & Hindi (Neutral Tone)";
- Sarvam "Authoritative", "Hindi Devanagari Native Accent";
- Cartesia "Friendly Receptionist", "Clear Indian English Accent".

Two problems:
- **The vendors are undecided.** They are those of Accepted ADR-0005 and ADR-0007, which conflict with the free-only rule (**OD-17**).
- **One label is inaccurate.** "Hindi Devanagari Native Accent": Devanagari is a script, not an accent.

**Recommendation:** fix or remove these English labels when OD-17 is decided, and translate them after. They are 6 rows and 17 words. Until then they would hold up each language's completion, so translate them as they are only if the owner wants a language shipped first.

## Q6. The F14 banner (before that row)

**Row:** `calls_role_lost` ("Another app is now screening your calls. …").

**OD-11** proposes amending this sentence, which is a UX-spec text: the app can't verify it, and it is false when the person simply turned screening off. The amendment is not applied.

**Recommendation:** decide OD-11 before translating this row, or translate it now and again after the amendment.

## Q7. Legal review of deletion and privacy rows (before)

**Rows:** the 54 rows with a `review` entry. Most important are `settings_deletion_*` (what account deletion does and keeps), `protection_notice_contacts` and `settings_task_contacts_done` (contacts are never read or uploaded), and `assistant_no_microphone`.

These are statements about personal data. A second translator's review checks the meaning. **Whether a lawyer must also review them in each language is the owner's decision.**

This is not legal advice: India's data-protection rules may bear on which languages privacy information has to be offered in.

## Q8. "in Settings" (before that row)

**Row:** `protection_notice_contacts_denied` ("Contacts access is off. You can turn it on in Settings.").

Its button opens **Android's** settings page for this app, but the app also has a **Settings** tab. In English the sentence can be read either way; a translation must choose.

**Recommendation:**
- product decides whether to change the English to "in your phone's settings";
- until then, translate as Android's settings.

## Q9. Sentence-ending punctuation (during)

Hindi text commonly ends sentences with "।" (purna viram); Tamil, Telugu, Malayalam and Kannada commonly use ".".

**Recommendation:** follow what Android's own text uses in each language, consistently.

## Q10. The "Language" button on Welcome (before that row)

**Row:** `onboarding_welcome_language`.

This button exists so a person can change the app's language before signing in. If the phone's language is, say, Tamil, and the app shows Tamil, the button says "Language" in Tamil, which is fine. But it is also the way out for someone who **cannot** read the language the app is showing. Many apps show the word in two languages, for example "भाषा / Language".

This is a design question, raised in the design review packet (`docs/ux/android/reviews/LANGUAGE-SELECTION-REVIEW.md`). It decides whether this row is translated alone or kept bilingual.

## Q11. Text outside the app (separate)

The worksheet holds only the app's own screens. Not included:
- **The sign-in SMS.** It is sent by the server. In India its text must match a DLT-registered template, and each language is a separate template. That depends on the SMS provider decision, which is not made.
- **The assistant's voice prompts.** Those are ADR-0005 and OD-17's, and are separate from the app's screen language.
- **The Play Store listing.**
- **Debug-only design-preview screens.** They are not migrated (`LOCALISATION.md` §1).

None of these blocks the app's translation, but a language is not fully supported until the person's whole experience is in it.
