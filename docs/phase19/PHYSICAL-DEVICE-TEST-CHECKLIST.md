# Physical-device test checklist: the 9 on-device carrier cases

- **Status, 2026-11-05: NOT RUN.** No physical phone or SIM is available yet (X3). **Nothing here has passed.**
- **Every result row stays "Not run" until someone runs it on a real phone and records the evidence.** Emulator runs never count here.
- **What it covers:** T2's cases M1, M2 and M6–M12, from `ANDROID-SCREENING-DEVICE-VALIDATION.md` §5, made executable. Their "Expected" text is that document's, unchanged, with today's screen wording.
- **What it does not cover:** M3–M5 need call forwarding, a DID pool and the deployed gateway. They are prepared separately in [`FORWARDING-TEST-PREPARATION.md`](FORWARDING-TEST-PREPARATION.md) and **must not be run**.
- **Why these 9 need no integration:** blocking happens on the phone. The debug build opens Protection directly with its start-destination extra (debug builds only), as the emulator tests do. So these cases need:
  - **no sign-in**, so no SMS provider;
  - **no staging**;
  - **no Play Console;**
  - **no forwarding**, no DID and no paid service.
- **Who runs it:** the owner, or someone the owner names, on their own phones. Claude Code prepared it, and can analyse the evidence once it is recorded.

## 1. What is needed

### Phones

| Phone | For | Requirement |
|---|---|---|
| **P1, the test phone** | M1, M2, M6, M7, M8, M11, M12 | **Android 10 or later** (the call-screening role, API 29+). Developer options and USB debugging on. **Ideally a mainstream OEM sold in India** |
| **P2, an older phone** | M9 | **Android 8.0–8.1 or 9** (no call-screening role) |
| **P3, a dual-SIM phone** | M10 | Android 10+, two active SIMs. May be P1, if P1 is dual-SIM |
| **P4, an OEM phone** | M12 | Android 10+, with the OEM's own battery and auto-start controls: **Xiaomi, Vivo, Oppo or Realme**. May be P1, if P1 is one of these |
| **C, the caller** | Every case | Any phone that can place calls, and hide its number for M11. Not the test phone |

### SIMs

| | Requirement |
|---|---|
| **Test phone** | An active Indian SIM with voice service. **For M2, one SIM per carrier: Jio, Airtel, Vi, BSNL**, each used in turn in P1 |
| **Caller** | An active SIM. **Its number is "X" in the cases.** Use a number the tester owns and controls, never a third party's |
| **Allowlist case Y, unknown Z** | Used only by the forwarding cases (M3–M5); not needed here |

**Cost:**
- **Owner-provided hardware.** Calls between the tester's own SIMs use their plans' allowance.
- **No call to any paid or premium number.**
- **Recharges or new SIMs are the owner's spend, per the spending policy** (`PRODUCTION-READINESS-PLAN.md` §4).

### Computer

- The Android SDK's `platform-tools` (`adb`).
- This repository, to build the debug APK.

## 2. Build and install

```sh
cd android
./gradlew :app:assembleDebug
# The APK: app/build/outputs/apk/debug/app-debug.apk  (application id com.callscreen.debug)

adb devices                              # the phone is listed as "device" (accept the USB-debugging prompt)
adb install -r app/build/outputs/apk/debug/app-debug.apk
adb shell getprop ro.build.version.release   # record the Android version
adb shell getprop ro.product.manufacturer    # record the maker
adb shell getprop ro.product.model           # record the model
```

**Open a screen directly** (debug builds only; a release build ignores the extra):

```sh
# Protection
adb shell am start -n com.callscreen.debug/com.callscreen.app.MainActivity --es startDestination protection_dashboard
# Calls feed (for the F14 banner)
adb shell am start -n com.callscreen.debug/com.callscreen.app.MainActivity --es startDestination calls_feed
```

**The call-screening role: use the app first.** Protection → "Turn on blocking" → Android's dialog → choose this app. Real users do this, so it is part of the test. **If that dialog cannot be reached,** the same as the emulator tests:

```sh
adb shell cmd role get-role-holders android.app.role.CALL_SCREENING
adb shell cmd role add-role-holder android.app.role.CALL_SCREENING com.callscreen.debug 0
```

**Contacts access** (M6, M7):

```sh
adb shell pm revoke com.callscreen.debug android.permission.READ_CONTACTS   # M6: no access
adb shell pm grant  com.callscreen.debug android.permission.READ_CONTACTS   # M7: access (or "Allow contacts access" in the app)
```

**Block X in the app:** Protection → Blocked numbers → "Add number" → enter X with its area code → "Block".

## 3. Evidence: what to capture, and how

For each case, record:
- the date and time;
- the phone (maker, model, Android version) and the carrier;
- the case number and exactly what happened;
- the evidence.

**Screenshots:**

```sh
adb exec-out screencap -p > M1-p1-airtel-blocklist.png
```

**What to capture:** the app's blocklist, the system call log entry, and any banner or notice. **Record what the caller hears in words** (M2), for example "busy tone after one ring". Never record the call itself.

**The system call log:** a screenshot of the phone's own call log ("Blocked"). Prefer that to querying it with `adb`, which returns raw numbers.

**Logs:** only if a case fails, and only the app's process:

```sh
adb logcat -d --pid=$(adb shell pidof com.callscreen.debug) > M1-fail.log
```

**Read logs before keeping them, and delete any that hold a phone number.**

**Privacy, before keeping any evidence:**
- **Redact every phone number** in screenshots and notes, except the last two digits: "+91 98•••••• 21". Only the tester's own numbers are used, but evidence travels.
- **No contacts other than the test entries.** On a personal phone, use a separate Android user or work profile, or a spare phone.
- **No recordings of calls, ever.**
- **Keep evidence out of the repository unless redacted.** Results go in §5 below; images go where the owner chooses.

## 4. The cases

**Before every case:** the app holds the call-screening role (unless the case says otherwise), and X is on the blocklist.

| # | Phone | Setup | Steps | Expected (from `ANDROID-SCREENING-DEVICE-VALIDATION.md` §5) | Evidence |
|---|---|---|---|---|---|
| **M1** | P1 | App holds the role; X blocked | C calls P1 from X | **P1 does not ring; the system call log shows "blocked"** | Screenshot of the blocklist and of P1's call log |
| **M2** | P1, **once per carrier**: Jio, Airtel, Vi, BSNL | As M1; **no forwarding set on the SIM** (check with the carrier settings screen; dial nothing) | C calls from X; the tester **listens on C** | **The caller hears busy or call-ended, not voicemail or ringing on.** Record exactly what is heard, per carrier | One note per carrier: what C heard, for how long |
| **M6** | P1 | X saved as a contact on P1 **twice**: national form (`98…`) and `+91` form; X blocked; **contacts access revoked** | C calls from X | **P1 rings** (Android does not ask the app about saved contacts). **The blocklist shows** "Numbers saved in your contacts ring without being checked…" | Screenshot of the notice; note that it rang |
| **M7** | P1 | As M6, then "Allow contacts access" in the app (or `pm grant`) | C calls from X | **Blocked** | Call-log screenshot |
| **M8** | P1 | The app held the role (open the app once while it holds it: it records that on every return to the foreground, #195), **then another app takes it**: Settings → Apps → Default apps → Caller ID & spam app → the OEM's dialer or another spam app | C calls from X; then open the calls feed | **P1 rings. The F14 banner appears:** "On-device blocking is off. Numbers you've blocked here won't be blocked until you switch back." **"Switch back" restores blocking:** call again and it is blocked | Screenshots of the banner and of the call log after "Switch back" |
| **M9** | **P2** (Android 8 or 9) | Install; open Protection | Onboard; open Blocked numbers; C calls from X | **The role request (A09b) is skipped.** The notice reads "This version of Android doesn't let apps block calls, so numbers on this list aren't blocked on this phone." **X rings** | Screenshot of the notice; note that it rang |
| **M10** | **P3** (dual-SIM) | Block X | C calls SIM 1, then SIM 2, from X | **Blocked on both SIMs** (the role is per phone). Note any OEM difference | Call-log screenshot for each SIM |
| **M11** | P1 | X blocked | C hides its number (its own setting, or the carrier's prefix for one call), then calls | **P1 rings.** Android does not pass restricted numbers to the service | Note that it rang; call-log screenshot ("Private" or similar) |
| **M12** | **P4** (OEM) | X blocked; then let the OEM stop the app: swipe it from recents, turn on the battery saver, and turn auto-start off for it | C calls from X | **Still blocked** (Android binds the service for each call). **Record any OEM that prevents it**, and which setting did | Screenshots of the OEM settings used, and of the call log |

**If a case does not match "Expected":**
- record what happened, exactly, with the evidence;
- **do not retry until it passes.**

A mismatch is a finding. Claude Code can then investigate it from the evidence.

## 5. Results (filled in by the tester)

| # | Phone (maker, model, Android) | Carrier | Date | Result | Evidence (redacted) | Notes |
|---|---|---|---|---|---|---|
| M1 | | | | **Not run** | | |
| M2 (Jio) | | Jio | | **Not run** | | |
| M2 (Airtel) | | Airtel | | **Not run** | | |
| M2 (Vi) | | Vi | | **Not run** | | |
| M2 (BSNL) | | BSNL | | **Not run** | | |
| M6 | | | | **Not run** | | |
| M7 | | | | **Not run** | | |
| M8 | | | | **Not run** | | |
| M9 | | | | **Not run** | | |
| M10 | | | | **Not run** | | |
| M11 | | | | **Not run** | | |
| M12 | | | | **Not run** | | |

## 6. Cleanup, after every session

1. **Remove the test numbers** from the blocklist and the allowlist (in the app, or uninstall below).
2. **Give the call-screening role back** to the phone's previous holder: Settings → Apps → Default apps → Caller ID & spam app. Or:

   ```sh
   adb shell cmd role remove-role-holder android.app.role.CALL_SCREENING com.callscreen.debug 0
   ```

3. **Restore contacts access and the OEM settings** changed for M6, M7 and M12. Delete the test contacts.
4. **Undo any number hiding on C.**
5. **Uninstall:** `adb uninstall com.callscreen.debug`. That deletes the app's lists and settings.
6. **Turn off USB debugging** when finished.
7. **Delete unredacted screenshots and logs.**
