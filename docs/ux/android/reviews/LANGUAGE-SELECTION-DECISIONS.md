# Decisions for the owner: the App language screen (A58l) and Welcome's Language control

- **Status: DECIDED by the owner on 2026-11-04: all nine approved.** The record is [`OWNER-DECISION-PACKET-2026-11-04.md`](../../../phase19/OWNER-DECISION-PACKET-2026-11-04.md). This page keeps the analysis and recommendations as proposed; where a decision differs from a recommendation, the decision governs:
  - **D5:** App language stays under Settings and is **not** moved under Appearance;
  - **D7:** the name in brackets is the phone's actual language;
  - **D4:** the label is the fixed bilingual `भाषा · Language`.
- **D6 clarification (2026-11-05, F25):** tab selection and Back on tab roots follow the frozen navigation specification. The rest of D6 stands (`../../../phase19/OWNER-DECISION-PACKET-2026-11-05.md` §5).
- **Built:** D3, D4 and D7 in #237 (`996d555`); D6 in #239 (`4d5f532`). D1, D2, D5, D8 and D9 needed no app change. Tests: `AppLanguageScreenTest`, `HonestOnboardingUiTest`, `TopBarBackTest`, `SharedComponentsTextTest`, `AppLanguageOnDeviceTest`, `TopBarOnDeviceTest`.
- **Prepared:** 2026-11-04 by Claude Code, on main `7ed8f2f`, from the design review packet [`LANGUAGE-SELECTION-REVIEW.md`](LANGUAGE-SELECTION-REVIEW.md). The packet has the captures, the specification and the evidence. This page is the short form for deciding.
- **Every decision needs approval:** each one accepts or changes frozen UX (`UX_FREEZE.md` §7), or is a product decision (D8). A recommendation is only a recommendation.

## Summary

| # | Decision | Recommendation | Who decides | Blocks |
|---|---|---|---|---|
| D1 | Accept A58l as specified | **Accept**, subject to D3–D9 | Owner and design | Nothing today; the screen is built |
| D2 | Accept Welcome's Language control | **Accept**; its label is D4 | Owner and design | Nothing today |
| D3 | Untranslated languages in the list | **Keep listed, with the note** | Owner | Nothing today |
| D4 | Welcome control's label | **Bilingual**: the screens' word plus "Language" | Design, then the translators | `onboarding_welcome_language` (Q10) |
| D5 | Where A58l lives in Settings | **Keep the Settings section** until A58 exists | Design | Nothing today |
| D6 | An Up arrow in the top bar | **Add app-wide** | Design | Every screen with a top bar |
| D7 | Name the phone's language | **Name it**, e.g. "Phone's language (English)" | Design | Two new strings to translate |
| D8 | Is the app's screen language premium? | **Free**; "multiple languages" in premium means the assistant's voice | Owner (product) | `07`'s premium strategy wording |
| D9 | After a choice: stay or return | **Stay**, as built | Design | Nothing today |

**Suggested order:** D8, D4 and D7 first. D4 and D7 change strings, so settling them before translation starts saves rework. The others can follow in any order.

**Engineering, not a decision:** issue U1, the top bar under the status bar, is an implementation defect. Its fix is engineering's. U8, Welcome's feature icons announced twice, is fixed only if design agrees: it is listed at the end.

---

## D1. Accept A58l as specified

- **Current behaviour:**
  - one choice among seven rows: Phone's language (the default), then English, Hindi, Tamil, Telugu, Malayalam and Kannada, each in its own script with its name in the screens' language beneath;
  - a choice applies on tap and re-creates the screens; the place in the app and the session are kept.
- **Usability:** simple and reversible. The first line says the app's language is not the assistant's voice language, which stops people expecting the voice to change.
- **Recommendation:** accept, subject to D3–D9.
- **Accessibility:** the rows are one radio group; each language's own name carries a locale span, so TalkBack reads it with that language's voice where the phone has one. **TalkBack's actual speech has not been checked on a device.**
- **Localization:** the native names are fixed, not translated; the names beneath are translated rows.
- **Approval:** required (frozen UX, `UX_FREEZE.md` §7).

## D2. Accept Welcome's Language control

- **Current behaviour:** a globe and the word "Language" at the top end of Welcome (A01). It opens A58l before anything else is asked; Back returns to Welcome.
- **Usability:** a person who can't read the phone's language can change the app's before sign-in. At the top end it is out of the main path and doesn't compete with "Get Protected".
- **Recommendation:** accept. Its label is decided in D4.
- **Accessibility:** one clickable node announced once (the globe is decorative), with a 48 dp touch target. It stays on one line at twice the text size on a 320 × 480 dp screen.
- **Localization:** see D4. The label is the one word that must work for someone who can't read the language the screens are in.
- **Approval:** required (frozen UX).

## D3. Untranslated languages in the list

- **Current behaviour:** all six languages are listed. Five say "Not translated yet. Screens stay in English." and can still be chosen; choosing one changes nothing visible today (issue U3).
- **Usability:**
  - **listed with the note (as built):** honest, and the choice is kept for when the translation arrives. It may look broken, since nothing changes;
  - **hidden until translated:** no dead choices, but the six languages can't be chosen at all, which goes against your instruction that any of them can be chosen;
  - **listed but disabled:** shows what is coming, but a greyed row is harder to read, and it can't be chosen ahead of the translation.
- **Recommendation:** keep as built. Review again when the first translation lands, after which the note disappears from that language.
- **Accessibility:** the note is read with each row, so a screen-reader user hears why nothing changes.
- **Localization:** the note itself is a translated row; once a language is translated, its row loses the note.
- **Approval:** required.

## D4. Welcome control's label

- **Current behaviour:** "Language" in the language the screens are shown in.
- **Usability:** this control is the way out for a person who can't read the screens' language. Once translations exist, the word will be in a script that person may not read (issue U5).
- **Options:**
  - **the screens' word only (as built);**
  - **bilingual:** the screens' word plus "Language" in English, e.g. "भाषा · Language";
  - **a globe with the six native names.**
- **Recommendation:** bilingual. English is the app's fallback language, and the one most likely to be read alongside each of the others. Two words still fit on one line; six names would not, at a large text size.
- **Accessibility:**
  - TalkBack should read each part in its own language, so the English part needs a locale span, as A58l's native names have;
  - the touch target stays 48 dp; the layout tests must still pass at twice the text size with the longer label.
- **Localization:** the row `onboarding_welcome_language` is translated as the screens' word only, and the app adds the English. Translators must be told (`docs/translation/QUESTIONS.md` Q10). Decide before translation starts.
- **Approval:** required (frozen UX).

## D5. Where A58l lives in Settings

- **Current behaviour:** the Settings tab, today the setup checklist, has an App language section above Account. The specification (`02`) puts A58l under A58 Appearance, which is not built.
- **Usability:** the section is easy to find now. Moving it later under Appearance matches the specification but adds a step.
- **Recommendation:** keep it as built until A58 exists. Then move A58l under Appearance and keep the section as a shortcut.
- **Accessibility:** no change; the section's button opens the same screen.
- **Localization:** no new strings now. A move later adds Appearance's own strings.
- **Approval:** required (a difference from `02`).

## D6. An Up arrow in the top bar

- **Current behaviour:** no screen with the shared top bar has an Up or Back control. Return is by Android's Back gesture or button (issue U2).
- **Usability:** a person who doesn't use the Back gesture, or doesn't know it, may not find the way out. That includes many first-time smartphone users.
- **Recommendation:** add an Up arrow app-wide on screens opened from another (A58l, account deletion, the blocked and allowed numbers). It is a shared-component change, so it is design's decision, not engineering's.
- **Accessibility:** the arrow needs a 48 dp target and a translated description, "Navigate up", in each language. It gives Switch Access and keyboard users an on-screen way back.
- **Localization:** one new string. In the six languages the arrow points left, since none of them is right-to-left.
- **Approval:** required (frozen UX, app-wide).

## D7. Name the phone's language

- **Current behaviour:** the first row says "Phone's language" without saying which language that is, and the Settings section shows only the native name of a choice (issues U4, U6).
- **Usability:** on an English phone, "Phone's language" looks like a duplicate of "English". A person can't tell what the default will give them.
- **Recommendation:** name it, as the language the app actually resolves to: "Phone's language (English)" when the phone's language has no translation, "Phone's language (தமிழ்)" once Tamil does. The Settings section also shows the name in the screens' language, as A58l's rows do.
- **Accessibility:** the named language carries a locale span, as the rows do.
- **Localization:** about two strings with a placeholder. Decide before translation starts.
- **Approval:** required.

## D8. Is the app's screen language premium?

- **Current behaviour:** A58l is built free for everyone. `07`'s premium strategy lists "multiple languages" as a premium feature, without saying which languages.
- **Usability:** a person who can't read English must be able to use the app at all. Charging for the screen language would lock them out of the free tier.
- **Recommendation:** confirm the screen language is free, and that "multiple languages" in premium means the assistant's voice languages.
- **Accessibility:** a free screen language is itself an accessibility measure for people who can't read English.
- **Localization:** none; only `07`'s wording would change.
- **Approval:** required (a product and pricing decision).

## D9. After a choice: stay or return

- **Current behaviour:** the person stays on A58l, re-created in the new language. There is no confirmation (issue U7).
- **Usability:** staying lets a wrong tap be corrected at once, by someone who may not read the new language. Returning would show them a screen they can't read, with no list to fix it.
- **Recommendation:** stay, as built. Once translations exist, consider a short confirmation in the new language.
- **Accessibility:** the re-creation moves TalkBack's focus. A confirmation announced as a polite live region would tell a screen-reader user that the choice applied.
- **Localization:** a confirmation would add one string per language.
- **Approval:** required.

## Not one of the nine: U8

**Welcome's feature icons repeat their titles**, so TalkBack reads "Assistant screening" twice. The fix is to make the icons decorative. It changes what TalkBack announces on a frozen screen, so it waits for design's agreement.

## After the decisions

- **An accepted decision** is written into `docs/ux/` and built in the same PR, as `UX_FREEZE.md` §7 requires. The layout tests and `AppLanguageOnDeviceTest` cover it.
- **D4 and D7 change strings:** the worksheets are regenerated, and their rows reach the translators with the rest.
