# Localisation: the Android app's words

**Status, 2026-11-02.** The app's screens are in **English only**. Every
release screen's words are in string resources, the release packages the
owner's six languages, and a subscriber can choose any of them (§4). **No
translation exists for any of the owner's other five languages** (Hindi, Tamil,
Telugu, Malayalam and Kannada), so choosing one shows English, and the picker
says "Not translated yet". Do not describe the app as supporting any language
other than English until that language's reviewed translation and on-device
tests below are done.

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
| Translation layout tests: `OnboardingTranslationLayoutTest`, `ProtectionTranslationLayoutTest`, `SettingsTranslationLayoutTest`, `CallsTranslationLayoutTest`, `AssistantTranslationLayoutTest` | Each screen in **each of the six language configurations**, with every string 50% longer in that language's own script, at twice the font scale in 320 x 480 dp: every action and message can be brought fully into view (below) |
| `translation_kit` tests (`tools/i18n`, run in CI) | The worksheet tool's export, checks and import (§6) |

The large-text tests render with Robolectric's **native graphics**. The default (legacy) graphics give every glyph a fixed size and ignore the font scale, so they cannot show that text fits (#216). Twice the font scale makes English text wider and taller than the 40% growth UX_FREEZE §9 item 2 allows for. The **translation layout tests** go further with a stand-in for each translation (`core/testing` `PseudoTranslation`; never shipped, and not a translation): under the language's configuration, each string is its English followed by the language's own name ("മലയാളം") repeated until it is 50% longer, and at least four characters longer. The screens are therefore laid out with Devanagari, Tamil, Telugu, Malayalam and Kannada glyphs, which native graphics measure at their real sizes, and every action is found by its English start. Removing the screens' scrolling (a mutation) fails these tests in every language. They do not show that a real translation reads well, fits its own lengths, or is shaped right in every word: that needs the real translation on a device (§5). None of the six languages is right-to-left, so there is no right-to-left test.

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

**Not tested:** real process death on a device. The next process reads the same file or Android setting that these tests read.

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

`tools/i18n/translation_kit.py` (standard library only; it contacts no service):

1. `python3 tools/i18n/translation_kit.py status`: each language's progress per module. On 2026-11-03, **0/244 for each of the five** (243 before Welcome's Language label).
2. `python3 tools/i18n/translation_kit.py export --lang hi hi.csv`: a worksheet (CSV, UTF-8). It has one row per translatable string and per plural form the language uses (CLDR: `one` and `other` for all six), with:
   - the module and the English;
   - the comment above the string in `strings.xml`, which says where it appears and what the spec requires;
   - its placeholders (`%1$s`), which the translation must keep;
   - any translation already in the app.

   Strings marked `translatable="false"` (product names, `app_name`) are left out.
3. **A translator the owner approves fills the `translation` column. A second person reviews it** against the spec-linked English (§3). A row left empty stays English.
4. `python3 tools/i18n/translation_kit.py import --lang hi --reviewed-by "<translator or agency, date>" hi.csv`:
   - **It refuses the worksheet** if a row changes a placeholder, names a string English lacks, translates an untranslatable one, or gives a plural without `other`.
   - Otherwise it writes each module's `values-hi/strings.xml`, recording who reviewed it.
5. The build's checks (§3) run on the result: banned claims, consistency and the layout tests. Then a device check in the language (§5).
6. **Only when every string is translated and reviewed**, mark the language translated (`AppLanguage.translated`) and add it to `res/xml/locales_config.xml` (§4, §5).

**Not allowed without the owner's explicit approval:**
- machine translation of any kind;
- sending the worksheet or the app's text to an external translation service or API, a hosted spreadsheet, or a translation platform.

The provider, cost, data sharing and privacy implications come first. The worksheet contains only the app's own interface text, never user data.
