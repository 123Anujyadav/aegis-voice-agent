# Android call screening: device validation

- **As of:** 2026-10-25, `main` at `35ee4bb` (#167–#175).
- **Scope:** ADR-0002 Stage 1, the on-device pre-filter.
- **Contents:**
  - §1–§2: what was run on an Android emulator, and the exact results;
  - §3: what that proves and what it does not;
  - §4: findings;
  - §5: the manual test sheet for a physical phone, SIM and carrier;
  - §6: the decisions still open.
- **Privacy:** every number used was synthetic (`98765000NN`), and numbers are masked below. No real contact, call or account was used.

## 1. Environment

| Item | Value |
|---|---|
| Device | Android Emulator 37.1.11.0, AVD `Pixel_9_API_35`. Image `system-images/android-35/google_apis_playstore/x86_64`, build `google/sdk_gphone64_x86_64/emu64xa:15/AE3A.240806.036/12592187:user/release-keys` |
| Android | **15 (API 35)**. It was run `-read-only -no-snapshot -memory 2048`, so the AVD was not modified. Acceleration: WHPX. |
| App | `com.callscreen.debug`, `versionName 0.1.0-dev-debug`, `minSdk 26`, `targetSdk 36`. Built locally from `35ee4bb` with `./gradlew :app:assembleDebug` (AGP installed SDK Platform 36 and Build-Tools 35). |
| Not available | No physical phone (no USB device), no SIM, no Android 8/9 system image, and no identity server (so sign-in is disabled in this build). |

**How the cases were driven:**
- **The role:** `adb shell cmd role add-role-holder|remove-role-holder android.app.role.CALL_SCREENING <pkg> 0`. This is the same role state the A09b request produces.
- **The stored list:** written in the app's own format to `no_backup/caller_list.json` with `run-as` (a debug build).
- **Calls:** `adb emu gsm call <n>` and `adb emu gsm cancel <n>`, through the emulator's modem and **Android's real Telecom and call-screening stack**.
- **Results:**
  - the call state from `dumpsys telephony.registry` (0 idle, 1 ringing);
  - the call log (`type` 6 blocked or 3 missed, `block_reason` 1 = `BLOCK_REASON_CALL_SCREENING_SERVICE`);
  - Telecom's per-call event log (`dumpsys telecom`: `SCREENING_BOUND`, `SCREENING_COMPLETED`);
  - the crash buffer (`logcat -b crash`).

## 2. Results (all executed; all as expected)

| # | Case | Result |
|---|---|---|
| T1 | Cold start, then Welcome → SIM → phone verification | Launched in 4.4 s, no crash. Verification shows "Sign-in is not available in this build" with sending disabled. That is correct for an unconfigured build, and it stops navigation here (§4.3). |
| T2 | Grant the role | The app is the only `CALL_SCREENING` holder. |
| T3 | Blocked number calls | **Never rang** (state 0). Call log: type 6, block_reason 1. Telecom: `SCREENING_BOUND` our service, then `Reject`. |
| T4 | Allowed number; unknown number | Both **rang** (state 1) and were logged missed (type 3). |
| T5 | App force-stopped, then the blocked number again | Blocked. The cold process read the stored list. |
| T6 | List file replaced with `not a list` | The blocked number **rang** (screened, not rejected), and the file was **unchanged** afterwards (#168's rule). |
| T7 | Valid list restored | Blocked again. |
| T8 | Role revoked | The blocked number **rang**. |
| T9 | Role given to Google Phone (`com.google.android.dialer` accepted it) | Our app lost the role, and the blocked number **rang**: blocks stop silently. This is what F14 (#172) and the blocklist notice (#175) report. Restoring the role restored blocking. |
| T10a | Blocked **saved contact**, no `READ_CONTACTS` | **Rang.** Telecom: `Allow, contact exists`, with **no `SCREENING_BOUND`**: Android did not ask the app. |
| T10b | Same, `READ_CONTACTS` granted | **Blocked.** Telecom: `SCREENING_BOUND`, then `Reject, contact exists`. This confirms #175's premise. |
| T10c | Saved contact not on the list, access granted | Rang (the app allowed it). |
| T11 | Short-code caller (not normalisable) | Rang. Our service answered `Allow` (it is never rejected). |
| T12 | International caller not on the list | Rang (`Allow`). |
| — | Timing | From service bind to answer: 1–65 ms. Whole Telecom filtering: 34–78 ms warm, about 0.7 s on a cold start. Android's limit is 5 s. |
| — | Crashes over the whole session | `logcat -b crash` empty; no `FATAL EXCEPTION`. |

Automated suites (JVM and Robolectric) for the same code ran green in CI on every PR #167–#175. They were not re-run locally.

## 3. What this proves, and what it does not

**Proves, on Android 15's real framework:**
- The manifest entry, the role binding and the decisions are correct.
- A rejected call is logged as blocked by the screening service.
- Persistence across an app restart.
- The corrupted-file rule.
- Losing the role stops blocking.
- The contacts-permission behaviour.
- The deadline margin.

**Does not prove:**
- **Carrier behaviour.** The emulator modem is not a network: nothing here shows what Jio, Airtel, Vi or BSNL do with a rejected call, or with an unanswered allowed one.
- **Other versions and OEMs:** Android 8/9, or OEM builds.
- **The Protection, onboarding role and F14 screens on a device** (unreachable, §4.3).
- **Real SIMs, real forwarding (CFNRy) or the AI path.**

## 4. Findings

### 4.1 Placeholder data shown as fact (release blocker)

Several prototype screens show fixed values as if they were measured or detected. On this emulator, with no SIM, the app said:
- "Carrier: Jio 5G India", "DETECTED", "Conditional Call Forwarding Supported" (`SimDetectionScreen.kt:85,93,108`).
- Protection: "Aegis Score 98/100", "Optimal", "Spam Blocked 142", "AI Screenings 89" (`ProtectionDashboardScreen.kt:87,93,111-118`). It also says "Zero unverified scam breaches detected" (`:97`).
- The calls feed shows `sampleCalls` (`CallsFeedScreen.kt:54`).
- Welcome claims the AI "filters spam before your phone rings" and "scans caller speech vectors". On Android the AI answers only after the carrier's ring delay (ADR-0002 §5), and no such scanning exists (`WelcomeScreen.kt:87,92`).

A release must not show invented status: it misleads users, and Play's policies on deceptive behaviour apply. **Recommendation:** before any public track, replace each value with real data or an honest empty state, and remove the unsupported claims. Wording and product claims are the owner's; the code change is routine once the wording is chosen.

### 4.2 Contact matching depends on the number's format

On this emulator (region not India), a contact saved as `+91…` was not matched to a call presented as a 10-digit national number, because Android's own `phone_lookup` failed. Telecom therefore treated the caller as a non-contact. On an Indian SIM, Android should match these. That is a real-device check (M6 below).

### 4.3 The screening screens cannot be reached in a build without identity

Onboarding stops at phone verification, so A09b, Protection (A32/A33/A32d), the blocklist notice and the F14 banner were **not exercised on the device**. Their automated coverage is Robolectric UI tests. Device coverage needs a staging build with identity hosts (external), or an instrumented test that hosts these screens directly (a routine follow-up).

### 4.4 Repository hygiene

`android/.kotlin/sessions/*.salive` were tracked, and every build deletes them. Fixed by #176.

## 5. Manual test sheet: physical phone, SIM and carrier

Every case below is **BLOCKED**: no physical phone, Indian SIM, carrier account, platform DID or deployed backend is available to Claude. **Do not place paid calls without the owner's authorisation.** Record Android version, OEM and model, carrier and circle, and date, and never record real numbers.

| # | Setup | Steps | Expected | Observed |
|---|---|---|---|---|
| M1 | Android 10+ phone, app holds the role, number X blocked | Call from X | Phone does not ring; system call log shows "blocked" | BLOCKED |
| M2 | As M1, per carrier (Jio, Airtel, Vi, BSNL); **no** forwarding set | Call from X; caller listens | Caller hears busy or call-ended, **not** voicemail or ringing on (record exactly what is heard) | BLOCKED |
| M3 | As M2 **with** CFNRy to a test DID (needs the DID pool, external) | Call from X | Rejected on the handset. Does the carrier forward a rejected call (to voicemail or the DID)? If it does, ADR-0002's "no forwarding for blocked" fails on that carrier | BLOCKED |
| M4 | Number Y on the allowlist, CFNRy on | Call from Y; answer once, then leave unanswered once | Rings locally both times. Unanswered: the carrier forwards it to the DID (§2b gap, expected today) | BLOCKED |
| M5 | Unknown number Z, CFNRy on | Call from Z; do not answer | Rings, then is forwarded after the ring delay; the AI answers (needs the deployed gateway) | BLOCKED |
| M6 | X saved as a contact in national and in `+91` form, blocked, no contacts access | Call from X | Rings (Android does not ask the app). The blocklist shows the "saved contacts" notice | BLOCKED |
| M7 | As M6, then "Allow contacts access" granted | Call from X | Blocked | BLOCKED |
| M8 | Role taken by the OEM dialer or another spam app | Call from X; open the calls feed | Rings. The F14 banner appears; "Switch back" restores blocking | BLOCKED |
| M9 | Android 8 or 9 phone | Onboard; open Blocked numbers; call from X | A09b skipped; notice "This version of Android doesn't let apps block calls…"; X rings | BLOCKED (an Android 8/9 emulator would cover the app side; no image installed) |
| M10 | Dual-SIM phone | Block X; call each SIM from X | Blocked on both SIMs (the role is per phone); note OEM differences | BLOCKED |
| M11 | Withheld caller ID | Call with the number hidden | Rings: Android does not pass restricted numbers to the service | BLOCKED |
| M12 | OEM battery and "auto-start" restrictions (Xiaomi, Vivo, Oppo, Realme) | App killed by the OEM; call from X | Still blocked (Android binds the service per call); record any OEM that prevents binding | BLOCKED |

## 6. Decisions still open (recommendation and consequence; nothing here is approved)

| Decision | Recommendation | Consequence of each option |
|---|---|---|
| **Spam-reputation policy** (fraud domain: band to `REJECT`, `SILENCE` or `SCREEN`) | Decide only when a reputation source exists. Then: never `REJECT` on reputation alone, `SILENCE` for the highest band at most, and `SCREEN` otherwise. | *Reject on reputation:* fastest, but a false positive loses a wanted call with no trace beyond the call log. *Silence:* the call still rings silently, and an unanswered call is forwarded to paid screening (§2a). *Screen (today):* costs AI minutes, but never loses a call. |
| **Server-side allowlist sync** (ADR-0012) | Keep local (readiness §2b). Sync **only the allowlist, as keyed hashes**, only after an ADR-0012 amendment adds a purpose, consent and retention. | *Local:* an unanswered allowed caller is screened by the AI, which the app now states. *Sync:* the gateway can treat allowed callers differently, but it is new server-side personal data, needs an endpoint, consent records and an owner decision on what the gateway then does. Blocklist and contacts: never needed. |
| **Contacts permission disclosure** | The privacy policy and Play's permission declaration must state: requested only on the subscriber's request; used only so Android passes calls from saved contacts to the screening service; contacts are never read or uploaded. Confirm in the DPDP review that no `CONTACT_SYNC` consent applies. | *Omitted from the policy:* a Play review and DPDP risk. *Not requesting the permission at all:* saved contacts can never be blocked (T10a). |
| **Play Integrity (ADR-0031, O7, O8, B1)** | Unchanged from `PLAY-INTEGRITY-DECISION-MEMO.md` §12: ratify O7 and the proposed verdict values; approve B1 (WIF, no key); decide O8 tiers before any enforcement. The verifier stays dormant (#164). | *Ratify plus B1:* the wiring PR can follow once SRE provides S1–S3 and the owner does C1–C5. *Not ratified:* every session stays `unverified`; nothing is refused for integrity. |
| **Placeholder data (§4.1)** | Replace it before any public track. | *Ship as is:* invented status shown to users, a policy and trust risk. *Replace:* needs the owner's wording for each empty state. |
