# Localisation: the Android app's words

**Status, 2026-11-02.** The app ships in **English only**. Every release
screen's words are in string resources, which is the precondition for
translation. **No translation exists for any of the owner's other five
languages** (Hindi, Tamil, Telugu, Malayalam and Kannada). No locale
switching has been built. Fallback is Android's standard per-string fallback
to English, and it has not been tested with a translation, because none
exists. Do not describe the app as supporting any language other than English
until that language's translation, locale selection and on-device tests below
are done.

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
| `app` | `app_` | A73 signed-out notice; `app_name` |

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

A translation is one file per module: `src/main/res/values-<code>/strings.xml`, with the codes `hi`, `ta`, `te`, `ml` and `kn`. It needs no code change. A string it leaves out falls back to English.

The `strings.xml` files are **not** the whole job. Before a language ships:

1. **The release must package it.** `localeFilters` in `AndroidApplicationConventionPlugin.kt` is `en, hi, bn, ta, te, mr`, so a Malayalam or Kannada translation would be **stripped from the APK**. Changing the set is owner decision **OD-17**.
2. **The translation stays with people the owner approves.** No external translation API or service may receive the app's text without the owner's explicit approval (provider, cost, data sharing and privacy explained first).
3. **The checks pass** (§3), including the banned-claims scan of every `values*` folder.
4. **Locale selection exists** (§4), and it lists only translated languages.
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

The large-text tests render with Robolectric's **native graphics**. The default (legacy) graphics give every glyph a fixed size and ignore the font scale, so they cannot show that text fits (#216). Twice the font scale makes English text wider and taller than the 40% growth UX_FREEZE §9 item 2 allows for. It does not test Indic scripts' line heights or shaping, which needs real translations (§5).

## 4. Locale selection and fallback: not built

- **Selection.** Nothing in the app chooses a language. It follows the phone's language, and with only English it always shows English. Two options exist:
  - Android 13+ per-app languages: `generateLocaleConfig`, which lists the packaged locales in system settings;
  - an in-app picker: AndroidX AppCompat `setApplicationLocales`, which also covers Android 8–12.

  Either must list only languages that have been translated and tested, so it waits for the first translation. The in-app picker needs a UX spec.
- **Fallback.** Android resolves each string on its own: from `values-<code>` when it is there, otherwise from `values` (English). A half-translated screen therefore mixes languages, so a language should ship with every release screen translated. Untested until a translation exists.

## 5. Deliverables before claiming a language

For each of Hindi, Tamil, Telugu, Malayalam and Kannada:

1. **Translation** of every module's `strings.xml`, by translators the owner approves, and reviewed against the spec-linked English.
2. **Packaging**: the language is in `localeFilters` (OD-17).
3. **Locale selection** (§4), listing only that language once it is complete.
4. **On-device tests in that language** on the release (minified) build:
   - every flow in `android/test-minified`;
   - recreation after a language change;
   - TalkBack reading;
   - the language's script rendered at twice the font scale.
5. **The voice languages are separate.** They are ADR-0005's and **OD-17**'s: the app's screen language does not make the voice assistant speak it.
