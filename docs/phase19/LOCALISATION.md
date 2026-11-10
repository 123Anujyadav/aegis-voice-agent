# Localisation: the Android app's words

**Status, 2026-11-04.** The app's screens are in **English only**. Every
release screen's words are in string resources, the release packages the
owner's six languages, and a subscriber can choose any of them (§4). **No
translation exists for any of the owner's other five languages** (Hindi, Tamil,
Telugu, Malayalam and Kannada), so choosing one shows English, and the picker
says "Not translated yet". Do not describe the app as supporting any language
other than English until that language's reviewed translation and on-device
tests below are done.
**Before production release, also qualified legal review** of that language's
54 second-review rows, recorded with the reviewer, date and outcome (owner
decision Q7, 2026-11-05). The translators' review is not legal approval. The translator package is ready
([`docs/translation/`](../translation/README.md)). The owner decided the
translation questions Q0–Q4 and the language UX D1–D9 on 2026-11-04
([`OWNER-DECISION-PACKET-2026-11-04.md`](OWNER-DECISION-PACKET-2026-11-04.md)).
No translator has been engaged: contacting, hiring or paying anyone needs the
owner's further approval.

## 1. Where the words are

Each module with screens owns its words in `src/main/res/values/strings.xml`.
That file is the English source of every translation. Names are prefixed by
module:

| Module | Prefix | What |
|---|---|---|
| `feature/onboarding` | `onboarding_` | A01–A14: welcome, SIM, sign-in, permissions, role, setup (#214) |
| `feature/protection` | `protection_` | A31 dashboard, A32 caller lists, the blocking notice (#215) |
| `feature/settings` | `settings_` | The setup checklist, account deletion (#216) |
| `feature/calls` | `calls_` | A20 feed, F14's A75 banner (#217) |
| `feature/assistant` | `assistant_` | Assistant home, voice setup, voice session (#218) |
| `core/ui` | `core_ui_` | Shared components: tabs, top bar, search, dialog, states (#219) |
| `app` | `app_` | A73 signed-out notice (#220); `app_name` |

The code holds resource ids, never words:
- copy objects (`AccountDeletionCopy`, `CallerListCopy`, and so on) hold `@StringRes` ids;
- composables resolve them with `stringResource` or `pluralStringResource`;
- text built from parts uses one formatted resource (`%1$s`, `%1$d`) rather than joining strings in Kotlin;
- counts use `<plurals>`.

Product names that must not be translated are `translatable="false"`: the voice products (ElevenLabs Flash, Sarvam Indic Voice, Cartesia Sonic), `app_name` and the top bar's default title.

**Not migrated, on purpose:** screens that exist only as debug-build design previews (`AegisNavGraph.includeDesignPreviews`). They are:
- all of `feature/business`, `feature/family`, `feature/screening` and `feature/summary`;
- `ConsentVaultScreen`, `FraudIntelligenceScreen` and `IncomingCallScreen`;
- the debug label in `DesignPreview.kt`.

Their copy is "intent and examples, not final strings" (UX_FREEZE §9 item 1). They move to resources when the feature is built for release. `@Preview` functions hold Android Studio sample data that never ships.

## 2. Adding a language

A translation is one file per module: `src/main/res/values-<code>/strings.xml`, with the codes `hi`, `ta`, `te`, `ml` and `kn`. **The files exist for every module and language**, empty and marked "AWAITING a human-reviewed translation" (`TranslationConsistencyTest` requires them, so a new module gets them too). Filling one needs no code change; a string it leaves out falls back to English. How to fill them is §6.

The `strings.xml` files are **not** the whole job. Before a language ships:

1. **The release must package it.** It does, for all six: `android/app/release-locales.txt` lists `en, hi, ta, te, ml, kn`, the owner's set (OD-17, applied 2026-11-02; Bengali and Marathi, which nothing depended on, were dropped). The build's `localeFilters` and `BuildConfig.PACKAGED_LOCALES` are read from that file. CI checks the built staging APK against it ("The staging APK has exactly the release languages and fits the size budget", `tools/scripts/android-apk-gate.sh`), and `TranslationConsistencyTest` fails a `values-<code>` folder for any other language, which the build would silently strip.
2. **The translation stays with people the owner approves.** No external translation API or service may receive the app's text without the owner's explicit approval (provider, cost, data sharing and privacy explained first).
3. **The checks pass** (§3), including the banned-claims scan of every `values*` folder.
4. **It is marked translated** (§4): the picker stops calling it "not translated yet", and Android's App languages page lists it.
5. **It is tested on a device in that language** (§5).

## 3. The checks every language passes

| Test | What it holds |
|---|---|
| `ReleaseCopyGuardTest` (onboarding) | No removed invented value or claim returns in any source or any `values*` folder |
| `HardCodedTextGuardTest` (app) | A migrated module writes no user-visible text in Kotlin; every module with UI is migrated, wordless, or only a design preview |
| `TranslationConsistencyTest` (app) | A translation changes only words. Every name exists in English and is translatable, placeholders match English, plurals keep `other`, and nothing is empty |
| `AccountDeletionViewModelTest` | No deletion failure message, in any language, says "nothing was deleted" |
| Spec-linked tests: `ScreeningRoleCopyTest`, `AccountDeletionCopyTest`, `ScreeningRoleBannerCopyTest`, `SignedOutCopyTest` | The English words the UX spec quotes are the shipped ones. A translation renders the same meaning; the English stays word for word |
| Large-text tests: `*LargeTextTest`, `OnboardingReachabilityTest`, `SharedComponentsTextTest` | At twice the font scale in 320 x 480 dp (onboarding: 640 x 320 dp), every action and message can be brought fully into view |
| Translation layout tests: `OnboardingTranslationLayoutTest`, `ProtectionTranslationLayoutTest`, `SettingsTranslationLayoutTest`, `CallsTranslationLayoutTest`, `AssistantTranslationLayoutTest` | Each screen in **each of the six language configurations**, with every string 50% longer in that language's own script, at twice the font scale in 320 x 480 dp: every action, message and screen title can be brought fully into view, and none is cut inside its own box (below) |
| `translation_kit` tests (`tools/i18n`, run in CI) | The worksheet tool's export, checks and import (§6) |

The large-text tests render with Robolectric's **native graphics**. The default (legacy) graphics give every glyph a fixed size and ignore the font scale, so they cannot show that text fits (#216). Twice the font scale makes English text wider and taller than the 40% growth UX_FREEZE §9 item 2 allows for. The **translation layout tests** go further with a stand-in for each translation (`core/testing` `PseudoTranslation`; never shipped, and not a translation): under the language's configuration, each string is its English followed by the language's own name ("മലയാളം") repeated until it is 50% longer, and at least four characters longer. The screens are therefore laid out with Devanagari, Tamil, Telugu, Malayalam and Kannada glyphs, which native graphics measure at their real sizes, and every action is found by its English start. Removing the screens' scrolling (a mutation) fails these tests in every language. Since 2026-11-04 they also fail text cut short inside its own box (Compose's text layout reports it): that found the shared buttons and top bar, whose fixed heights cut a wrapped label or title at twice the font scale, in English too, now `heightIn(min = …)`. **The status bar (design review U1):** the app draws edge to edge, and the top bar now starts its title and logo below the status bar and, in landscape, clear of a side navigation bar or cutout (`AegisTopAppBar`'s `windowInsets`; `SharedComponentsTextTest` and `TopBarOnDeviceTest`). A first attempt in #228 was taken out because a caller-list step then failed on the Android 15 CI emulator. The cause was the test, not the padding: that emulator is 320 × 640 dp, and 24 dp lower the new entry's row sat under the "Blocked." snackbar, so the test's tap landed on the snackbar, as a finger would. The test now waits for the snackbar to go before it taps. They do not show that a real translation reads well, fits its own lengths, or is shaped right in every word: that needs the real translation on a device (§5). None of the six languages is right-to-left, so there is no right-to-left test.

## 4. Language selection and fallback

- **Selection: built (A58l, `docs/ux/android/07-screens-settings-premium-business.md`).** Settings has an **App language** section that opens the picker: the phone's language (the default) and the six. `core/ui`'s `AppLanguage` is the list (equal to `release-locales.txt`, `AppLanguageListTest`), and `AppLanguageSetting` keeps the choice in one place per Android version:
  - **Android 13 and later:** Android's per-app language (`LocaleManager`). It persists across restarts and updates, and Android re-creates the screens on a change. A choice stored on Android 12 or earlier moves into it once after an OS upgrade (`migrate`).
  - **Android 8 to 12:** the app's preferences file `callscreen_app_language`, written with `commit()` before the activity is re-created, and applied by `MainActivity.attachBaseContext` (`wrap`), which also sets the default locale for number and date formats.
  - An unsupported or unreadable value (a language outside the six, damage) means the phone's language.
  - **An Indian language is applied with its Indian region** (`ta-IN`; English stays `en`), as Android's own language setting gives it ("Tamil (India)"), so resources and formats kept for the Indian region match. The app's translations are kept by language (`values-ta`) and match either.
  - **Android's own strings** (a system "Cancel") follow the chosen language only where the phone's Android has them. On the Android 9 and 15 emulators they did. On the Android 8 emulator image Android's own "Cancel" stayed English with both `ta` and `ta-IN`, so the device test checks them only where the platform reports Tamil, and logs what it reports. That image's own languages include Hindi but no Tamil, Telugu, Malayalam or Kannada (run 41509334002).
  - No new library: AndroidX AppCompat's `setApplicationLocales` would need AppCompat's activity and theme.
- **Android's App languages page** (`android:localeConfig`) lists a language only once it is translated; while only English is, the app declares none (`AppLanguageListTest`). The in-app picker lists all six and marks those not translated yet.
- **Not translated yet.** `AppLanguage.translated` is true only for English. A language is marked translated in the same change that adds its reviewed translation, and `TranslationConsistencyTest` then fails if any release string is missing from it, so a translated language never mixes with English.
- **Fallback.** Android resolves each string on its own: from `values-<code>` when it is there, otherwise from `values` (English). Tested with the platform's own strings, which have translations while the app's do not: in Hindi, Tamil and Malayalam, the app's words are English and Android's are translated (`AppLanguageSettingTest`, `AppLanguageOnDeviceTest`).
- **The assistant's language is separate.** Choosing a screen language never changes the language the assistant speaks or understands on calls (A43, ADR-0005).

| Test | Where | What |
|---|---|---|
| `AppLanguageTest` | core/ui, JVM | The six in order; locale matching; each name in its own script |
| `AppLanguageSettingTest` | core/ui, Robolectric, API 26, 28, 33, 35 | Persistence on disk (8–12) and in `LocaleManager` (13+); fallback; unsupported values; activity re-creation; the OS-upgrade migration |
| `AppLanguageScreenTest`, `SettingsLargeTextTest` | feature/settings | One choice of seven; the "not translated" notes; screen-reader language marks; no mixed-language names; reachable at twice the font scale in 320 x 480 dp |
| `AppLanguageListTest`, `TranslationConsistencyTest` | app | The list equals the packaged set; Android's list holds only translated languages; a translated language is complete |
| `AppLanguageOnDeviceTest` | app, emulator API 26, 28, 35 | Choose Tamil in the picker: re-created in Tamil on the same screen; Back returns to Settings; on disk or in `LocaleManager`; a new activity starts in it; the session record is untouched; back to the phone's language |
| `MinifiedAppTest.anAppLanguageWithoutATranslationStartsInEnglish` | test-minified, API 33+ | The R8 build, started with a per-app language, shows English |
| `MinifiedAppTest.aLanguageChosenInTheAppSurvivesTheProcessBeingKilled` | test-minified, API 26 and later | The R8 build: Tamil chosen in the app's own picker; the process force-stopped (checked gone) and the app started cold; the picker still has Tamil chosen, and on 13+ Android holds it as the per-app language. Restores the phone's language afterwards |

**Process death: tested on emulators** (2026-11-09): `aLanguageChosenInTheAppSurvivesTheProcessBeingKilled`, which ran locally on API 35 (the per-app language Android keeps) and API 28 (the app's own file), and runs on CI's API 35 job on every change to `test-minified`, and nightly on 35, 28 and 26. **Not tested:** a physical phone, or a reboot.

## 5. Deliverables before claiming a language

For each of Hindi, Tamil, Telugu, Malayalam and Kannada:

1. **Translation** of every module's `strings.xml`, by translators the owner approves, and reviewed against the spec-linked English.
2. **Packaging**: done for all six (`android/app/release-locales.txt`). **Selection**: done for all six (§4).
3. **Marked translated** (`AppLanguage.translated`), which makes the picker drop its "Not translated yet" note and requires every string (§4); and added to `res/xml/locales_config.xml`, declared in the manifest, for Android's App languages page.
4. **On-device tests in that language** on the release (minified) build:
   - every flow in `android/test-minified`;
   - recreation after a language change;
   - TalkBack reading;
   - the language's script rendered at twice the font scale.
5. **The voice languages are separate.** They are ADR-0005's and **OD-17**'s: the app's screen language does not make the voice assistant speak it.

## 6. Translation workflow

**The translator package is [`docs/translation/`](../translation/README.md):**
- a guide for translators and reviewers;
- a glossary;
- the questions the owner must settle first ([`QUESTIONS.md`](../translation/QUESTIONS.md));
- one worksheet per language, `docs/translation/worksheets/<code>.csv`.

The worksheets are kept equal to the English by a test. After changing any English string, run:

    python3 tools/i18n/translation_kit.py export --all docs/translation/worksheets

`tools/i18n/translation_kit.py` uses the standard library only and contacts no service. A test runs it with networking disabled, and another checks it imports no networking module.

1. `python3 tools/i18n/translation_kit.py status`: each language's progress per module. On 2026-11-04, **0/244 for each of the five**.
2. `python3 tools/i18n/translation_kit.py audit`: the English source measured.
   - **242 strings and 2 plurals: 246 worksheet rows, 1,549 words, 9,122 characters per language** (after the owner's decisions D3, D4, D6 and D7, 2026-11-04, Q5, Q6 and Q8, 2026-11-05, and the revised contacts notice, 2026-11-07: Welcome's bilingual label is not translated; the top bar's Back arrow adds "Back").
   - The workload table is in the translator guide.
3. `python3 tools/i18n/translation_kit.py export --lang hi hi.csv`: a worksheet (CSV, UTF-8). It has one row per translatable string and per plural form the language uses (CLDR: `one` and `other` for all six), with:
   - the module and the English, and its length;
   - **where it appears and how much room it has.** This comes from `tools/i18n/ui-context.csv`, where every translatable string has a kind (tab, chip, one-line field, top bar, button, heading, option, message, text, spoken-only, format), and a test requires every string to have one;
   - the comment above the string in `strings.xml`;
   - its placeholders (`%1$s`), which the translation must keep;
   - for plurals, which numbers use `one` in that language. **Hindi and Kannada use `one` for 0 and 1**; Tamil, Telugu and Malayalam only for 1;
   - **a `review` entry for the 54 rows a spec or a test quotes**, naming the document a reviewer checks the meaning against;
   - any translation already in the app.

   Strings marked `translatable="false"` (product names, `app_name`) are left out.
4. **A translator the owner approves fills the `translation` column. A second person reviews it** against the spec-linked English (§3).
5. `python3 tools/i18n/translation_kit.py import --lang hi --translated-by "<translator, date>" --reviewed-by "<reviewer, date>" --names-consented hi.csv`. `--names-consented` confirms both people agreed to their names being recorded (owner decision Q0); without it nothing is written. **It refuses the whole worksheet, and writes nothing,** if:
   - a row drops "Lily AI" (the product's name since 2026-11-05; `translation_kit.PRODUCT_NAME`) or "AI", or a sign-in-code row lacks "OTP" (owner decisions Q1, Q3, Q4, 2026-11-04);
   - any row is missing, empty, given twice, or not in English;
   - the English has changed since export;
   - a row translates an untranslatable string, or changes a placeholder;
   - a row adds a stray `%` to a formatted string;
   - a plural lacks a form the language uses;
   - a row is identical to the English without `keep_english` = `yes`;
   - a row has no letter of the language's script, or letters of another Indian script;
   - a row changes the line breaks, has spaces at either end, or contains an invisible control character;
   - the translator and the reviewer are the same person.

   **A language is therefore imported complete or not at all.** It never mixes English and the language under a "Not translated yet" label. Otherwise the import writes each module's `values-hi/strings.xml`, NFC-normalised, recording the translator and the reviewer.
6. The build's checks (§3) run on the result: banned claims, consistency and the layout tests. Then a device check in the language (§5).
7. **Only then**, mark the language translated (`AppLanguage.translated`) and add it to `res/xml/locales_config.xml` (§4, §5).

**Not allowed without the owner's explicit approval:**
- machine translation of any kind;
- sending the worksheet or the app's text to an external translation service or API, a hosted spreadsheet, or a translation platform.

The provider, cost, data sharing and privacy implications come first. The worksheet contains only the app's own interface text, never user data.
