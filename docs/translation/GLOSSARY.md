# Glossary: the app's terms

**For:** translators and reviewers of the Aegis Android app (see [`README.md`](README.md)).

**This is not a translation.** Each term says what it means in this app and how to handle it. The translator proposes the term for each language and the reviewer agrees it. Record the agreed term in the last table, and use it in every row.

## Terms

| English | What it means in this app | How to handle it |
|---|---|---|
| **Lily AI** | The product's name (owner, 2026-11-05), on screen since the Lily AI visual port (2026-11-09). It replaced "Aegis AI" and "Aegis" in the app's words | A name, not a word to translate. **Keep `Lily AI` in Latin letters in every translation** (Q1, owner, 2026-11-04, applied to the new name). The launcher label stays "CallScreen" until the naming review (F26) |
| **screen calls**, screening, screened, screenings | **Checking a call before the person has to deal with it.** Android's own screening decides whether a call rings or is rejected (blocking). The planned assistant answers an unknown caller the person doesn't pick up and asks who is calling and why | Not a display screen. Not medical screening. One term for "screening" in every row |
| **screen** (noun), screens | A page of the app: "this app's screens" | Different from "screening". Use a word that cannot be read as "screening" (`settings_language_intro`) |
| **assistant** | The planned automated voice that would answer unknown callers for the person. **Not available yet** | Not Google Assistant. A neutral word for a helper, not a human receptionist |
| **AI** | Artificial intelligence: "AI screening", "AI Voice Customization" | **Keep `AI` untranslated, in Latin letters** (Q3, owner, 2026-11-04) |
| **block**, blocked numbers, blocking, on-device blocking | Calls from a number the person blocked are **rejected on this phone before it rings**, even offline | Use the verb people know from phone apps for "block a number". "Blocked numbers" is also a screen's title |
| **allow**, allowed numbers | Numbers this app never blocks, even if flagged as spam | The opposite of "block" |
| **unknown caller** | A caller whose number is not in the person's contacts | Not "anonymous" or "hidden number" |
| **Caller ID & spam app** | **Android's own name** for the setting that lets an app screen calls (a "default app" role). Android asks the person to choose this app for it | **Use exactly the words Android shows** on a phone set to the language: Settings > Apps > Default apps > Caller ID & spam app |
| **call forwarding** | The mobile operator's service that sends calls the person doesn't answer to another number (here, to the assistant) | Use the term mobile operators use for this service in that language |
| **carrier** | The mobile operator (network provider) | Translate the meaning "mobile operator", the way people in India say it, not "a carrier of goods" |
| **SIM**, SIM card | The phone's SIM | Usually kept as the familiar loanword in the language's script |
| **verification code**, 6-digit code, code | The one-time code sent by SMS to sign in | **Use `OTP`** in all five translations (Q4, owner, 2026-11-04). Never for the country, area or forwarding code. Keep "6" in Western digits |
| **sign in**, sign out, signed out | Proving the phone number with a code, then staying signed in on this phone | Use the words phone apps use for signing in |
| **account**, delete account, permanently | The person's account on Aegis's servers. Deleting it cannot be undone | **Privacy text: the meaning must be exact** (`review` column) |
| **contacts access** | **Android's Contacts permission.** The app uses it only so Android can check calls from saved contacts. **It never reads or uploads contacts** | Use Android's own name for the Contacts permission. **Never translate as "reading" or "importing" contacts** |
| **spam** | Unwanted calls: marketing, robocalls | The word people in India use for unwanted calls, consistently |
| **fraud**, fraud risk, threat reports | Calls trying to cheat or deceive: scams | Distinct from spam |
| **persona** | How the planned assistant behaves with callers (strict, balanced, silent) | Translate the meaning ("style", "manner"), not a word for a character in a story |
| **label** | The person's own optional note for a number, such as "Delivery" | A short name or tag, not a product label or sticker |
| **on this phone**, on-device | Happens on the person's phone, not on Aegis's servers | Keep the contrast with "our servers" and "our service" clear |
| **our service**, our servers | Aegis's own systems on the internet | "Our": the company speaking |
| **Not available yet** | The feature **does not work yet** | Never "coming soon" and never a date: the app makes no promise |
| **App language**, phone's language | The language of the app's screens; the language the phone is set to | **Not the language of the assistant's voice.** The text says they are separate |
| **Undo** | Reverses the last change to a list | The word phone apps use for undo |

## Names that must match exactly

Some sentences tell the person where to go: "Turn it on from **Protection**, **Blocked numbers**". The translated words there must be **exactly** the translated tab name and screen title, or the person can't find them.

| Row that refers | to these rows, which it must match |
|---|---|
| `onboarding_complete_blocking_off`, `settings_task_blocking_to_do`, `settings_task_contacts_to_do` | `core_ui_tab_protection` (Protection) and `protection_list_blocked_title` (Blocked numbers) |
| `settings_deletion_kept_lists` ("Remove them in Protection") | `core_ui_tab_protection` |
| `onboarding_complete_go_to_calls` ("Go to Calls Feed") | `calls_feed_title` |
| `onboarding_complete_blocking_off`, `protection_notice_role_not_held`, `onboarding_role_ask` ("caller ID & spam app") | Android's own name for the setting (above) |
| `protection_notice_contacts_denied` ("in Settings") | **Android's** Settings app, not this app's Settings tab: the button opens Android's settings for this app (Q8) |
| `settings_language_change`, `settings_language_title` | Each other: the button opens the screen with that title |

## Agreed terms (filled by the translator, agreed by the reviewer)

| English | Hindi | Tamil | Telugu | Malayalam | Kannada |
|---|---|---|---|---|---|
| screening | | | | | |
| screen (a page) | | | | | |
| assistant | | | | | |
| block / blocked numbers | | | | | |
| allow / allowed numbers | | | | | |
| unknown caller | | | | | |
| Caller ID & spam app (Android's words) | | | | | |
| call forwarding | | | | | |
| carrier (mobile operator) | | | | | |
| verification code | | | | | |
| sign in / sign out | | | | | |
| account | | | | | |
| contacts access (Android's words) | | | | | |
| spam | | | | | |
| fraud | | | | | |
| label | | | | | |
| Not available yet | | | | | |
| App language | | | | | |
| Protection (tab) | | | | | |
| Calls (tab) | | | | | |
| Assistant (tab) | | | | | |
| Settings (tab) | | | | | |
