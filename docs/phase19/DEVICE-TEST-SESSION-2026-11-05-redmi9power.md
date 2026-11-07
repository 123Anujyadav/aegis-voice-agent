# Device test session record: 2026-11-05, Redmi 9 Power (Android 12, Airtel)

**Prepared by Claude Code; filled in from the tester's reports and the evidence in §3c.** Run so far: **M1 Passed** (§3b), **M2 on Airtel Passed** (§3f), **M6: accepted with a recorded finding** (§3g: passed on a later attempt; the initial attempt failed), and **M7 Passed** (§3h). **M2 is not complete:** Jio, Vi and BSNL are blocked, with no SIMs. Every other case is not run.

This is the record for one session, made from [`DEVICE-TEST-EVIDENCE-TEMPLATE.md`](DEVICE-TEST-EVIDENCE-TEMPLATE.md). The cases and their expected results are in [`PHYSICAL-DEVICE-TEST-CHECKLIST.md`](PHYSICAL-DEVICE-TEST-CHECKLIST.md).

**When a case counts as passed:**
- someone ran it on a physical phone;
- the observed result matches "Expected";
- the redacted evidence is recorded: file names and SHA-256 here (§3c), the images kept outside the repository.

**Rules for filling it in:**
- **Fill in only what was actually done.** Leave a case "Not run" rather than estimate it.
- **Commit only after the redaction check (§4) is complete.** Unredacted evidence never goes into the repository.

## 1. Session

| Field | Value |
|---|---|
| Date and time (IST) | Started 2026-11-05; the M1 call 2026-11-06 00:45:39; the M2 (Airtel) call 2026-11-07 09:19; the M6 calls 2026-11-07 09:59:31, 10:05:11 and 10:15:22, and the M6 retest 11:13:26; the M7 call 2026-11-07 14:40:59 |
| Tester | Anuj Kumar |
| Location (city only) | |
| Repository commit the APK was built from (`git rev-parse --short HEAD`) | `627e571` (`627e5712284760148ab8a9d05a2761039fbdcc45`, main, 2026-11-05 13:06 IST) |
| APK | `app-debug.apk` |
| APK SHA-256 (`sha256sum app/build/outputs/apk/debug/app-debug.apk`) | `868a05884799b97afade0b29d52b1de025b301513d041562e64cb261f617c39f` (18,679,367 bytes; built 2026-11-05 13:23 IST; Gradle 8.12, JDK 21.0.12; debug-signed, certificate SHA-256 `71963d36…5441d79a`) |
| App version (`adb shell dumpsys package com.callscreen.debug \| grep versionName`) | `com.callscreen.debug`, versionName `0.1.0-dev-debug`, versionCode 1, targetSdk 36 (from the APK; confirm on the phone) |

## 2. Devices and SIMs

Record one row per phone used. **Write SIM numbers as "+91 ••••••••NN"**, with the last two digits only.

| Role | Maker | Model | Android (`getprop ro.build.version.release`) | Dual-SIM? | Carrier(s) and SIM (redacted) |
|---|---|---|---|---|---|
| P1, test phone | Xiaomi | Redmi 9 Power | 12 (confirm with getprop) | Yes (two slots); one SIM fitted | Airtel, +91 ••••••••NN |
| P2, Android 8/9 | — | not available | — | — | — |
| P3, dual-SIM | (P1, if a second SIM is fitted) | | | | |
| P4, OEM | Xiaomi (P1 is also P4) | Redmi 9 Power | | | |
| C, caller (X) | | | | | |

## 3. Cases run

**Result** is one of:
- **Passed:** observed matches expected, with evidence attached;
- **Failed:** observed differs from expected; record exactly how;
- **Blocked:** it could not be run, and why;
- **Not run.**

| Case | Phone | Carrier | Time | Steps followed as written? (yes, or what differed) | Observed | Result | Evidence files (redacted) |
|---|---|---|---|---|---|---|---|
| M1 | P1 (Redmi 9 Power, Android 12) | Airtel | 2026-11-06 00:45:39 IST (Telecom `TC@198`) | **Attempt 2: yes,** except that the images are camera photos of the screen, not screenshots (§3b). Attempt 1 differed: X was saved in P1's contacts and contacts access was denied (§3a) | **Attempt 2:** P1 did not ring. The app's service was bound and answered Reject; Android rejected the call; the Phone app shows "Blocked call" (§3b) | **Passed** | `M1-p1-airtel-calllog-redacted.png`, `M1-p1-airtel-blocklist-redacted.png`, `M1-p1-airtel-telecom-redacted.txt` (§3c) |
| M2 (Jio) | | Jio | | | | Blocked: no Jio SIM | |
| M2 (Airtel) | P1 (Redmi 9 Power, Android 12) | Airtel | 2026-11-07 09:19 IST | Yes: forwarding checked off before the call (§3f) | **What the caller heard:** "The number is currently busy." (busy), for about 2 s; then the call ended by itself. P1 did not ring | **Passed** (Airtel only) | The tester's note (§3f); no image or log |
| M2 (Vi) | | Vi | | | | Blocked: no Vi SIM | |
| M2 (BSNL) | | BSNL | | | | Blocked: no BSNL SIM | |
| M6 | P1 (Redmi 9 Power, Android 12) | Airtel | 2026-11-07, from 09:59:31 IST (Telecom `TC@218`, then `TC@220`, `TC@221`, and the owner-approved retest `TC@222` at 11:13:26) | Yes: X saved twice before the first call, contacts access denied (§3g) | **Initial attempt (09:59):** P1 did not ring. Android consulted the app and the call was rejected. **Later calls** (10:05, 10:15, and the retest at 11:13): P1 rang, showing "M6 test A"; Android did not consult the app; all missed, ended by the caller. The notice matched (§3g) | **Accepted with finding — Passed on a later attempt; the initial attempt failed (TC@218); root cause inconclusive.** (owner, 2026-11-07) | `M6-p1-airtel-telecom-redacted.txt`, `M6-p1-airtel-telecom-TC220-redacted.txt`, `M6-p1-airtel-telecom-TC221-redacted.txt`, `M6-retest-p1-airtel-telecom-TC222-redacted.txt`, `M6-p1-airtel-blocklist-notice-redacted.png`, `M6-p1-airtel-blocklist-notice-after-call-redacted.png` (§3g) |
| M7 | P1 (Redmi 9 Power, Android 12) | Airtel | 2026-11-07 14:40:59 IST (Telecom `TC@228`) | Yes: as M6 (X saved twice, unchanged), then "Allow contacts access" in the app and Android's dialog (granted 14:39:51) (§3h) | P1 did not ring. Android reported "contact exists", consulted the app, which rejected the call; the Phone app shows "Blocked call, 2:40 pm" for "M6 test A" (§3h) | **Passed** | `M7-p1-airtel-telecom-TC228-redacted.txt`, `M7-p1-airtel-calldetails-redacted.png` (§3h) |
| M8 | | | | | | Not run | |
| M9 | | | | | | Blocked: needs an Android 8/9 phone (P1 is Android 12) | |
| M10 (SIM 1) | | | | | | Not run: needs a second active SIM in P1 | |
| M10 (SIM 2) | | | | | | Not run: needs a second active SIM in P1 | |
| M11 | | | | | | Not run | |
| M12 | | | | | **OEM settings changed:** | Not run | |

### 3a. M1 attempts, as reported by the tester

**Setup, both attempts:**
- `adb shell cmd role get-role-holders android.app.role.CALL_SCREENING` printed `com.callscreen.debug`.
- X, the second phone's number, was on the app's blocklist.
- The app was opened on Protection with the debug `startDestination` extra (checklist §2), because sign-in is not configured in this build.

| Attempt | Setup | Observed | Assessment |
|---|---|---|---|
| **1** | **X was saved in P1's contacts; contacts access (READ_CONTACTS) denied** | P1 rang. No "Blocked" entry in the call log | **Not an M1 failure:** the setup differed from M1. It matches what Android documents, and what M6 expects: Android does not pass a saved contact's call to the app without contacts access. **Not an M6 pass either:** M6 needs X saved twice (national and `+91` forms), and the blocklist notice screenshot |
| **2** | The test contact removed from P1's contacts; nothing else changed | **P1 did not ring;** the call log shows it as blocked | **Matches M1** (§3b) |

**Attempt 2, system evidence.** Telecom's events for the attempt-2 call, `TC@198` (§3c):
- 00:45:39.355 `SCREENING_BOUND`: `com.callscreen.debug/…PreFilterScreeningService`. Android bound the app's service for this call.
- 00:45:39.359 `SCREENING_COMPLETED`: `[Reject, logged, mCallBlockReason = 1, …]`.
  - "Reject": the app answered block.
  - "logged": the call is to be added to the call log.
  - Block reason 1 is Android's `CallLog.Calls.BLOCK_REASON_CALL_SCREENING_SERVICE`.
- 00:45:39.361 `REQUEST_REJECT` (internal), then 00:45:39.436 `SET_DISCONNECTED`, reason `INCOMING_REJECTED`. Android ended the call on the handset.

### 3b. M1: verdict and how the evidence was checked

**M1 Passed** on 2026-11-06. Both halves of "Expected" are evidenced for the attempt-2 call (`TC@198`, 2026-11-06 00:45:39 IST):

| Expected | Evidence | Check |
|---|---|---|
| **P1 does not ring** | The tester observed no ringing. Telecom (`M1-p1-airtel-telecom-redacted.txt`): the call arrived at 00:45:39.047; the app answered `Reject` 0.3 s later; the call was disconnected `INCOMING_REJECTED` 0.4 s after it arrived. The excerpt's `SET_RINGING` (00:45:39.150) is Telecom's state for any incoming call, set before filtering; Telecom starts the ringer only for a call that filtering allows, and this one was rejected | Consistent |
| **The system call log shows "Blocked"** | `M1-p1-airtel-calllog-redacted.png`: the Phone app's page for the number, "Recent activity: **Blocked call**, 11 min ago", number ending `39` | The photo was saved at 00:57:25; "11 min ago" places the call at about 00:45–00:46, matching Telecom's 00:45:39 |
| (Evidence asked for) the blocklist | `M1-p1-airtel-blocklist-redacted.png`: Blocked numbers, one entry ending `39`, "Added by you · 05-Nov-2026" | The same last two digits as the blocked call |

**What the Telecom excerpt shows:**
- Telecom keeps the phone's last 10 calls; **exactly one** was rejected, `TC@198`. It is the only call in the excerpt.
- **The other "Allow" event** (a different call) was excluded, on the owner's instruction, and not examined. M6 exercises the saved-contact path directly.
- **`BLOCK_CHECK_FINISHED (not blocked [Allow, …])`** in the excerpt is Android's own blocked-numbers check, which the call passed. The block came from this app's service (`SCREENING_BOUND` names `com.callscreen.debug/…PreFilterScreeningService`), not from Android's own list.

**Deviations from the steps as written:**
- **The images are camera photos of P1's screen, not screenshots.** They show the same screens; the checklist asks for screenshots.
- **The call-log image is the Phone app's page for the number** (its "Recent activity"), not the Recents list. It is the same call-log entry.

**Left blank:** the C row (§2) and the location (§1). Neither is needed for M1's verdict, and nothing is guessed.

### 3c. Evidence files

**No image is committed** (owner, 2026-11-06). The files are kept by the tester in `%USERPROFILE%\Documents\lily-m1-redacted\`, outside the repository. The unredacted originals are not evidence: they are kept in the same folder under names beginning `UNREDACTED-DO-NOT-SHARE-`, unchanged, and are deleted at cleanup (§4).

| File | Shows | SHA-256 |
|---|---|---|
| `M1-p1-airtel-calllog-redacted.png` | The Phone app: "Blocked call, 11 min ago" for a number ending `39` | `1f2e12023aa05514fae1bbb21dc3aa05daaa9f9b360ca550be980a14932d5264` |
| `M1-p1-airtel-blocklist-redacted.png` | The app's Blocked numbers: one entry ending `39`, and the contacts-access explanation | `d607651cf09c3f39fc0ae1702f842ef8f6f9d1624f93e7b0b2e02210942eb153` |
| `M1-p1-airtel-telecom-redacted.txt` | Telecom's events for `TC@198` only (29 lines) | `72cd2ffa1e690e64609759f96deaaefe257f7096e978f97afdd7ac6308c4cbb1` |

**How they were made (Claude Code, 2026-11-06):**
- **Images:** copies of the tester's photos, with solid black boxes drawn into a new, flattened PNG with no metadata, so nothing under a box survives. Covered: the first ten characters of each number (`+91` and eight digits), and the caller-ID name the Phone app showed. Kept: the last two digits, "Blocked call", "11 min ago" and the blocklist entry's date.
- **Checked:** each box is solid black in the saved file; both images were inspected whole and at 1.5× zoom on every number row; the source photos' SHA-256 and timestamps were unchanged afterwards.
- **Telecom:** `adb shell dumpsys telecom` was piped straight into a filter that kept only the rejected call's block, replaced the `tel:` handle and app-name fields with `[redacted]`, and masked any run of 7 or more digits to its last two. The full dump was never written to disk. The excerpt contains no run of 7 or more digits.
- **The call time** is Telecom's (`06-Nov-2026 12:45:39 am`; the phone's time zone is `Asia/Kolkata`). The phone's call log was not queried.

### 3d. Not M1 evidence: the Calls feed

After attempt 2, the app's Calls feed showed "No call data yet". **That is by design in this build**, and is neither evidence for M1 nor against it:
- the screening service answers Android and records nothing (`PreFilterScreeningService.onScreenCall`);
- the feed is given no calls (`CallsFeedRoute` passes none; `CallsFeedScreen`'s `calls` defaults to empty);
- the app has no call-log permission.

**A blocked call is recorded only in Android's call log,** which is what M1 checks. An in-app call history is not built yet (product Phase 3).

**The blocklist's contacts-access explanation** is the notice M6 checks for. It is not an M6 pass: M6 needs its own setup and call.

### 3e. Not covered by M1, and still not run

- **M6:** saved contact, no contacts access. M1's attempt 1 was consistent with it, but was not M6. M6 itself was run on 2026-11-07 (§3g).
- **M7:** saved contact, contacts access granted, expected Blocked. Run on 2026-11-07 and Passed (§3h).
- **The contact behaviour is observed on this one phone only.** It is not established for other phones or OEMs.

### 3f. M2 on Airtel: what the caller heard

**Expected** (checklist §4): "The caller hears busy or call-ended, not voicemail or ringing on. Record exactly what is heard, per carrier."

**Observed** (the tester's report, 2026-11-07):

| | |
|---|---|
| Start | 09:19 IST |
| Test phone | P1, Airtel SIM, the app holding the role and X blocked (as M1) |
| Forwarding (setup) | Checked by the tester before the call on P1's call-forwarding settings, with nothing dialled: **always, when busy, when unanswered and when unreachable were all off** |
| What C heard | Busy: **"The number is currently busy."** |
| For how long | About 2 seconds |
| How it ended | The call ended by itself |
| P1 rang? | No |

**Assessment:**

| Criterion | Result |
|---|---|
| The caller hears busy or call-ended | **Met:** a busy announcement, then the call ended |
| Not voicemail | **Met:** no voicemail answered |
| Not ringing on | **Met:** no ringing continued; the call ended after about 2 s |

**M2 on Airtel: Passed.** The evidence is the tester's note above, as the checklist asks ("one note per carrier: what C heard, for how long"). No image or log was taken.

**Setup check:** the tester confirmed, 2026-11-07, that all four Airtel call-forwarding options were off before the call (above). The evidence for it is that confirmation; no screenshot was taken.

**Not recorded:** a Telecom excerpt for this call. It was optional.

**M2 is not complete.** Its other three carriers are blocked: there is no Jio, Vi or BSNL SIM. Airtel's result says nothing about them.

### 3g. M6: the call sequence of 2026-11-07 (accepted with a recorded finding)

**Expected** (checklist §4, as worded at the time of the run): "**P1 rings** (Android does not ask the app about saved contacts). **The blocklist shows** "Numbers saved in your contacts ring without being checked…""

**Setup**, checked read-only just before the calls:
- the app held the role;
- X was blocked (one entry, ending `39`);
- contacts access (READ_CONTACTS) was **denied** (`granted=false`, set by the user);
- X was saved twice in P1's contacts, as the tester reported, **both saved before the first call** (tester's confirmation):
  - "M6 test A": stored on the **phone (device)**, in the **10-digit national** form;
  - "M6 test B": stored in the **Google account**, in the **`+91`** form, and added on the Redmi itself;
  - the exact save and edit times of both are **unknown**.

The contacts themselves were not read.

**The calls, in order:**

| # | Time (IST) | Telecom | What happened | Status |
|---|---|---|---|---|
| **1** | 09:59:31 | `TC@218` | **The tester's intentional M6 call, the initial attempt.** Android's own check did **not** report the caller as a contact (`BLOCK_CHECK_FINISHED (not blocked [Allow, logged, notified])`, with no "contact exists"). It **consulted the app** (`SCREENING_BOUND` names `com.callscreen.debug/…PreFilterScreeningService`); the app answered `[Reject, logged, mCallBlockReason = 1]`; the call was disconnected `INCOMING_REJECTED` 0.33 s after it arrived. It was rejected before any ringer started (no `START_RINGER`); it is not a missed call | **Does not match Expected: the initial-attempt finding** |
| **2** | 10:05:11 | `TC@220` | A subsequent call. Android **reported the contact** (`BLOCK_CHECK_FINISHED` and `FILTERING_COMPLETED`: `[Allow, logged, notified, contact exists]`) and did **not** bind the app's service. **Tester:** the Redmi rang with "M6 test A" on the incoming screen, unanswered, for an estimated 20–30 s. **Telecom, separately:** `START_RINGER` 10:05:12.17, `STOP_RINGER` 10:05:16.43 (about 4.3 s of ringer activity); disconnected `INCOMING_MISSED`, ended by the caller | A subsequent observation that matches Expected. **It does not replace the initial attempt** |
| **3** | 10:15:22 | `TC@221` | A further subsequent call. Android **reported the contact** (`BLOCK_CHECK_FINISHED` and `FILTERING_COMPLETED`: `[Allow, logged, notified, contact exists]`) and did **not** bind the app's service. **Tester:** the Redmi rang with "M6 test A" on the incoming screen; the tester watched it ring for about 5 s and did not wait for the end. **Telecom, separately:** `START_RINGER` 10:15:22.75, `STOP_RINGER` 10:15:32.90 (about 10.1 s of ringer activity); disconnected `INCOMING_MISSED`, ended by the caller | A subsequent observation that matches Expected. **It does not replace the initial attempt** |
| **4** | 11:13:26 | `TC@222` | **The owner-approved retest** (one call; below). Android **reported the contact** (`BLOCK_CHECK_FINISHED` and `FILTERING_COMPLETED`: `[Allow, logged, notified, contact exists]`) and did **not** consult the app (no `SCREENING_BOUND`, `SCREENING_COMPLETED` or `REQUEST_REJECT`). **Owner:** the Redmi rang, showing "M6 test A", for about 30 s. **Telecom, separately:** `START_RINGER` 11:13:27.18, `STOP_RINGER` 11:13:56.45 (about 29.3 s of ringer activity); disconnected `INCOMING_MISSED`, ended by the caller | A fresh observation that matches Expected; the latest. **It does not replace the initial attempt** |

**The Blocked numbers notice** (the second half of Expected) was captured at 10:01 (between calls 1 and 2) and at 10:08 (after call 2). Both read, in the wording of the time: "Numbers saved in your contacts ring without being checked, so blocking one of them here has no effect. To block saved contacts too, allow contacts access. This app doesn't read or upload your contacts: Android uses the permission only to let it check calls from them." This matches the checklist's wording, with "Allow contacts access" below it.

**First assessment (after calls 1–3; superseded by the owner's final decision, below): M6 not Passed, an unresolved finding.**
- **On the initial attempt, the result did not match Expected.** The phone did not ring: Android consulted the app for a caller saved twice in contacts, without contacts access, and the call was blocked.
- **The checklist's rule:** "If a case does not match 'Expected': record what happened, exactly, with the evidence; do not retry until it passes." Calls 2 and 3 are recorded as what happened. **They do not turn the initial attempt into a pass.**
- **What the evidence shows:**
  - The calls differ in **Android's own contact check**: "contact exists" is absent on call 1 and present on calls 2 and 3.
  - Telecom masks the caller's number, so it does not by itself show who called. Calls 2 and 3 are tied to X by the tester's observation of "M6 test A". Call 1 is tied to X by the app's rejection: its blocklist holds only X.
  - **Why** it differed is not established. The contacts were not read, and nothing on the phone was changed between the calls by this testing.
  - The app's notice says saved contacts "ring without being checked". On call 1 that did not hold.

**The evidence**, kept by the tester in `%USERPROFILE%\Documents\lily-m1-redacted\`, outside the repository:

| File | Shows | SHA-256 |
|---|---|---|
| `M6-p1-airtel-telecom-redacted.txt` | Telecom's events for `TC@218` only (call 1) | `8d63c75094f851eb6dbc0709bd3b5b83b25887f0e92569ae592828dffd8d3380` |
| `M6-p1-airtel-telecom-TC220-redacted.txt` | Telecom's events for `TC@220` only (call 2) | `334d53e0345388262bc124bd20c31d610010e6f1336db4efcdcda0edf16a3b7a` |
| `M6-p1-airtel-telecom-TC221-redacted.txt` | Telecom's events for `TC@221` only (call 3) | `5c8c8b199afcf328794634fae950b690dbbcaaf2fe43d26029a4a27691162792` |
| `M6-retest-p1-airtel-telecom-TC222-redacted.txt` | Telecom's events for `TC@222` only (call 4, the retest) | `9e0983d8a2c6b33782b95c31ddee82b30bd2959900ec7e7c7bf482e8c6f99d3d` |
| `M6-p1-airtel-blocklist-notice-redacted.png` | The Blocked numbers notice at 10:01; the entry shows only `39` | `0c5cb7992c0ab66012976cbe810ebb8484dde4be87dd315e8ebc32284e760efa` |
| `M6-p1-airtel-blocklist-notice-after-call-redacted.png` | The same screen at 10:08 | `5331b573be7b78f7d42379d7319a22ac521171ad22c41164c06547944d29e86f` |

**How the evidence was made:**
- **Screenshots:** taken with `adb exec-out screencap` after opening Blocked numbers with the debug start-destination (delivered with `--activity-single-top`), so nothing was written to the phone.
  - The redacted copies are new, flattened PNGs with no metadata. A solid box covers all but the last two digits; each box was confirmed solid, and the copies were viewed whole.
  - The unredacted originals are kept under names beginning `UNREDACTED-DO-NOT-SHARE-M6-`, unchanged.
  - A first capture showed the wrong screen (the Assistant tab: no personal data, not evidence) and was deleted.
- **Telecom:** each excerpt was filtered from `dumpsys telecom` in memory, for one call ID, never saving the full dump.
  - `tel:` handles and app-name fields are replaced with `[redacted]`; Android itself also masks the caller handle.
  - No excerpt holds a run of 7 or more digits.
  - Call 3's excerpt was taken later, with the owner's approval of one read-only Telecom check: only call headers after `TC@220` were listed (one: `TC@221`), then only that call was extracted.
- **Not inspected:** `TC@219`, an outgoing call from P1 at 10:03:30. It is not part of the test.

**Investigation of call 1** (2026-11-07; read-only: the app's source, and the Telecom excerpts above. No contacts read, no calls placed, nothing on the phone changed):

**Facts:**
- **The app's code is the code on the phone:** nothing under `android/` changed between the APK's commit `627e571` and `main` `bb44d9e`.
- **When Android hands the app a call, the app's decision is fixed:**
  - `PreFilterScreeningService.onScreenCall` (`android/core/telephony/src/main/kotlin/com/callscreen/core/telephony/screening/PreFilterScreeningService.kt:46-56`) builds `PreFilter(callerLists, NO_CONTACTS)`.
  - `NO_CONTACTS` is `ContactLookup { false }` (line 66): the app never reads contacts, so it cannot tell a contact from a non-contact, with or without contacts access.
  - `PreFilter.decide` (`…/prefilter/PreFilter.kt:69-76`) checks the blocklist (line 73) before any contact check.
  - `PhoneNumbers.normalise` (`…/prefilter/PhoneNumbers.kt:52-58`) maps the `+91`, `91`, `0` and 10-digit forms to the same number.
  - **Tests:** `PreFilterTest` ("a number matches its list in any form the handset presents it"; "an explicit block wins over a contact") and `ScreeningServiceTest` ("a blocked number, however Android presents it, is rejected").
  - **So every call from X that Android hands the app is rejected.** Call 1 was.
- **Whether the app is consulted is Android's decision,** made from its own contact check (the manifest note: `android/core/telephony/src/main/AndroidManifest.xml:11-13`).
  - "contact exists" was absent on call 1 and present on calls 2 and 3.
  - The app was bound only on call 1.
- **Android's check completed on all three calls,** with no sign of a timeout:
  - the voicemail-routing step took 30, 17 and 24 ms;
  - the block check took 61, 62 and 80 ms.
  - So on call 1 the lookup finished and did not report a contact.
- **Telecom masks the caller's number,** so whether the network presented X in the same form on all three calls cannot be checked.

**Hypotheses** (none established):
- **H1, timing or indexing:** Android's lookup had not yet seen the contacts at 09:59, for example because one was saved or edited just before, or the Google-account contact was still syncing. **H1 can be neither confirmed nor ruled out, because the save and edit times are unknown.** Against it, A is stored on the device, where a saved contact is normally found at once.
- **H2, number form:** Android normally matches numbers on their trailing digits, so the national entry (A) and the `+91` entry (B) should each match X in any form. The same two contacts matched on calls 2 and 3, with nothing reported changed. Number form alone therefore does not explain the difference, unless the network presented X differently on call 1, which cannot be checked. Plausibility: low.
- **H3, the phone's software** (MIUI, Android 12, the Google Phone app): a device-specific lookup or caching behaviour. No evidence either way.

**Conclusion:**
- **Not a reproducible product defect:** the app acted deterministically, as designed and tested, on the call Android handed it.
- **The root cause is inconclusive:** Android's own contact check gave different answers for the same caller and the same two contacts within six minutes, for a reason not established.
- **The notice** ("…ring without being checked, so blocking one of them here has no effect") did not hold on call 1. The owner approved revised wording (below).

**Owner decisions, 2026-11-07:**
1. **M6 assessment: Unresolved / Not Passed.** *Superseded by the owner's final decision, after the retest (below).*
   - Call 1 (`TC@218`) remains the initial-attempt finding.
   - Calls 2 and 3 (`TC@220`, `TC@221`) are subsequent observations; they do not replace the initial attempt.
   - The root cause remains inconclusive. The test contacts' save and edit times remain unknown.
2. **No further diagnostic calls are authorised.** (The owner later approved exactly one retest, call 4, below.)
3. **The contacts notice is revised** (approved wording). In `protection_notice_contacts` (`android/feature/protection/src/main/res/values/strings.xml`), the first two sentences change; the last two, about privacy, are unchanged:
   - **was:** "Numbers saved in your contacts ring without being checked, so blocking one of them here has no effect. To block saved contacts too, allow contacts access."
   - **now:** "Contacts may be allowed to ring without being checked by this app. To help block numbers saved in your contacts, allow contacts access."
   - "To help" does not promise that every saved contact will be recognised once contacts access is allowed.
   - It says "this app", not a product name: the brand review (F26) is pending and the other screens still say "Aegis AI" (owner, 2026-11-07).
   - The app's behaviour is unchanged: no permission is added or requested, and the call-screening logic is not touched.
   - **The calls above were made with the previous wording.**
4. **Translations:** the revised English is in the five translation worksheets (`docs/translation/worksheets/`). **Hindi, Tamil, Telugu, Malayalam and Kannada are pending human translation**, as is every string; until then the app shows the English.

**The retest, 2026-11-07** (exactly one call, approved by the owner to investigate the finding):
- **Read-only preflight**, just before the call: the same install (`0.1.0-dev-debug`, the `627e571` APK); the app held the role; READ_CONTACTS `granted=false` and app-ops `ignore`; X blocked (one entry, ending `39`); the Airtel SIM loaded; no call in progress; the "always forward" indicator off; no call since `TC@221`. The app's process had been restarted by the phone since the earlier calls (a new process ID); the install, role and permission were unchanged.
- **The owner confirmed:** both test contacts unchanged ("M6 test A": phone, 10-digit national; "M6 test B": Google account, `+91`); the caller setup unchanged; all four Airtel forwarding options (always, busy, unanswered, unreachable) off.
- **The call:** `TC@222`, 11:13:26 IST, Airtel, the Redmi 9 Power (Android 12, MIUI 14): call 4 in the table above. The owner confirmed "M6 test A" on the incoming screen (stored on the phone, 10-digit national form), the Redmi ringing for about 30 s, and no recording on either phone.
- **The evidence:** `M6-retest-p1-airtel-telecom-TC222-redacted.txt` (SHA-256 `9e0983d8a2c6b33782b95c31ddee82b30bd2959900ec7e7c7bf482e8c6f99d3d`, verified on disk): `TC@222` only, filtered from `dumpsys telecom` in memory, handles redacted, no run of 7 or more digits.
- **About the cause:** an hour after call 1, Android again recognised the contact. That is consistent with H1 (call 1's lookup not yet seeing the contacts), but does not prove it.

**The owner's final decision, 2026-11-07** (superseding decision 1 above): **Accepted with finding — Passed on a later attempt; the initial attempt failed (TC@218); root cause inconclusive.**
- The owner accepted M6 after reviewing the four observations.
- **Call 1 (`TC@218`) remains an initial-attempt failure;** it is kept here unchanged.
- **Three subsequent observations matched Expected** (`TC@220`, `TC@221`, `TC@222`); `TC@222` is the latest.
- **Why Android's contact lookup differed on call 1 remains unknown,** and the test contacts' save and edit times remain unknown.
- **This acceptance does not establish the root cause,** and does not show the same behaviour on other Android devices, OEMs or carriers.
- **No further diagnostic calls are authorised.**

### 3h. M7: a saved contact with contacts access, 2026-11-07 (Passed)

**Expected** (checklist §4): "**M7** | P1 | As M6, then "Allow contacts access" in the app (or `pm grant`) | C calls from X | **Blocked** | Call-log screenshot". **The owner's pass criterion** (stricter than the checklist): all three of (1) Android reports "contact exists"; (2) the app's screening service is consulted and rejects the call; (3) the Phone app's call history shows "Blocked".

**Setup** (read-only preflight just before; the contacts were not read):
- the same install (`0.1.0-dev-debug`, the `627e571` APK); the app held the role; X blocked (one entry, ending `39`); the Airtel SIM loaded; no call in progress;
- X saved twice, unchanged since M6 ("M6 test A": phone, 10-digit national; "M6 test B": Google account, `+91`), as the owner confirmed;
- caller ID shown on X, and call recording disabled on both phones, as the owner confirmed;
- **forwarding:** all four Airtel options confirmed off by the owner at 11:13; on the day, the "always forward" indicator was read off (`mCallForwarding=false`); **the busy, unanswered and unreachable options were not re-queried** (owner's instruction).

**Contacts access granted, the one change** (owner-approved): before it, `READ_CONTACTS` `granted=false` (flags `USER_SET`) and app-ops `ignore`. The owner opened Blocked numbers, tapped **"Allow contacts access"** and approved Android's own permission dialog (no `pm grant`). Afterwards, read-only: `READ_CONTACTS` `granted=true` for user 0, at 14:39:51 (the `granted=false` line belongs to MIUI's second-space user 999, where the app is not installed); app-ops "Default mode: allow"; the role still held; the app still running (the same process) with no crash or freeze.

**The call:** exactly one, `TC@228`, 2026-11-07 14:40:59 IST, the first incoming call after `TC@227`.

| Condition | Evidence | Result |
|---|---|---|
| 1. Android reports "contact exists" | Telecom: `BLOCK_CHECK_FINISHED (not blocked [Allow, logged, notified, contact exists])` at 14:41:00.010; also in `SCREENING_COMPLETED` and `FILTERING_COMPLETED` | **Met** |
| 2. The app is consulted and rejects the call | Telecom: `SCREENING_BOUND` (`com.callscreen.debug/…PreFilterScreeningService`) 14:41:00.028; `SCREENING_COMPLETED ([Reject, logged, contact exists, mCallBlockReason = 1, …])` 14:41:00.031; `REQUEST_REJECT` 14:41:00.035; `SET_DISCONNECTED` `INCOMING_REJECTED` 14:41:00.097, about 0.34 s after the call arrived (14:40:59.758). No `START_RINGER`: the Redmi did not ring | **Met** |
| 3. The Phone app's call history shows "Blocked" | The "M6 test A" call-details page in Google Phone: **"Blocked call, 2:40 pm"**, number ending `39` | **Met** |

**M7 Passed** (the owner's review, 2026-11-07): all three conditions are supported by the evidence below. It is the result for this phone, carrier and build only; it says nothing about other devices, OEMs or carriers, and no other case is changed by it.

**The evidence**, kept by the tester in `%USERPROFILE%\Documents\lily-m1-redacted\`, outside the repository:

| File | Shows | SHA-256 |
|---|---|---|
| `M7-p1-airtel-telecom-TC228-redacted.txt` | Telecom's events for `TC@228` only | `59dabf1cc822af15bc0cc3612d48f30ab96332ab14c397697a36812c0183d3d2` |
| `M7-p1-airtel-calldetails-redacted.png` | The "M6 test A" call-details page: the name, the number's last two digits (`39`), and "Blocked call, 2:40 pm"; every other row covered | `bdbe0b1afbebfe24b97cd01df4f9d96a8a95ee06eb338e2d4e56a4d6aa36f3d1` |

**How the evidence was made:**
- **Telecom:** filtered from `dumpsys telecom` in memory, for `TC@228` only; the full dump was never saved; handles redacted (Android also masks them); no run of 7 or more digits.
- **The screenshot:** taken with `adb exec-out screencap` (nothing written to the phone), after a strict check of the screen in memory: the focused activity was Google Phone's call-details page (`ConversationHistoryCallDetailsWrapperActivity`); "M6 test A" and "Blocked" present; no Recents markers; no other contact name; one phone number, ending `39`. The owner navigated to the page; no tap was sent.
  - The redacted copy is a new, flattened PNG with no metadata: solid boxes cover `+91` and eight digits (placed by measuring the 13 characters), and every other call-history row (the 11:13, 10:15 and 10:05 missed calls, the 9:59 and 9:18 blocked calls, and Fri 12:45 am). Each box was confirmed solid; the copy was viewed whole and at 3× on the number row.
  - **Checked against `TC@228`:** "2:40 pm" today matches 14:40:59; "Blocked" matches `Reject` and `INCOMING_REJECTED`; "M6 test A" matches "contact exists"; the last two digits match X, the only blocklist entry.
- **Kept locally only, never committed or uploaded:** the unredacted original (`UNREDACTED-DO-NOT-SHARE-M7-calldetails-original.png`, SHA-256 `56851bf3659c2c69b107b5198228c2ac599c37a88e5e32b664f8aea64233270b`).
- **Not evidence, recorded for the audit trail:**
  - a first capture caught the home screen (no call data) and was deleted (SHA-256 `2b9a99fe…68cb`);
  - a second caught Google Phone's Recents list, which shows other people's calls. It is kept locally, unused and unshared, at the owner's instruction (`UNREDACTED-DO-NOT-SHARE-M7-calllog-original.png`, SHA-256 `b5ce1eef886bd9b13e6a4df36abdbe4f1125ac40dd306ad9ca87abb39132bf3c`). The check before capturing was tightened after it.

**Contacts access stays granted** until the session's cleanup, which the owner approves separately (§4).

**For a failed case,** also record:
- the time;
- the app's screen at that moment (a screenshot);
- whether it repeats on a second call;
- the log, if taken (checklist §3), and only after reading it for phone numbers.

**Do not change the app or retry until it passes:** a failure is a finding.

## 4. Before committing this record

- [x] Every phone number in this file and in every screenshot shows only its last two digits. (Checked 2026-11-06: both images and this file.)
- [x] No contact names other than the test entries appear. (The caller-ID name the Phone app showed is covered.)
- [x] No call recording exists anywhere. (Confirmed by the tester, Anuj Kumar, 2026-11-06, for the M1 call.)
  - [x] The M2 (Airtel) call of 2026-11-07, 09:19 IST: confirmed by the tester, Anuj Kumar, 2026-11-07: no recording was made.
  - [x] The M6 calls of 2026-11-07 (`TC@218` 09:59, `TC@220` 10:05, `TC@221` 10:15): confirmed by the tester, Anuj Kumar, 2026-11-07: no audio recording was made of any of them.
  - [x] The M6 retest of 2026-11-07 (`TC@222`, 11:13): confirmed by the owner: no recording was made on either phone.
  - [x] The M7 call of 2026-11-07 (`TC@228`, 14:40): the owner confirmed before the call that call recording was disabled on both phones.
- [x] Logs read through; any with a phone number deleted or redacted. (The Telecom excerpt holds one call and no run of 7 or more digits; no full dump was kept.)
- [ ] Cleanup done (checklist §6): test numbers removed, the role returned, contacts access and OEM settings restored, number hiding undone on C, the app uninstalled, USB debugging off. **Partly done, 2026-11-07** (§4a): contacts access revoked, the two test contacts deleted, the unredacted originals deleted. **The rest is deferred: the session continues** (M8, M11, M12 and other cases run on the same phone next).

### 4a. Partial session cleanup, 2026-11-07

After M7 passed (#253, merged as `556e6d5`), the owner approved a partial cleanup. **The session continues**, so only what M6 and M7 alone needed was undone.

**Done:**
- **Contacts access revoked** by the owner, in Android's settings, after M7. Verified read-only: `READ_CONTACTS` `granted=false` for user 0; app-ops `READ_CONTACTS: ignore` (the state before M7). Android restarted the app's process, as it does when a permission is revoked; the app stayed installed.
- **The two test contacts deleted** by the owner: "M6 test A" (phone) and "M6 test B" (Google account). This rests on the owner's confirmation that a search for "M6 test" returns nothing; the contacts were never read programmatically. The Google-account contact sits in Google's Trash for 30 days.
- **The unredacted originals deleted** (owner-approved), each by its exact path and only after its SHA-256 matched:

| File | SHA-256 |
|---|---|
| `UNREDACTED-DO-NOT-SHARE-M1-calllog-original.jpeg` | `b7e10167ef1f1cdbfe416269d9ffee804bfcce1661187e868235f41d03f0f875` |
| `UNREDACTED-DO-NOT-SHARE-M1-blocklist-original.jpeg` | `fc8a25b26dbc6f41798febd80efa849c3d1a49c17e8f47fcad1aa80ce66e6953` |
| `UNREDACTED-DO-NOT-SHARE-M6-blocklist-original.png` | `bc316cbb0a8ef0913081b7e0cef049248955bacda0dcb75ede07fa125a3c6ba3` |
| `UNREDACTED-DO-NOT-SHARE-M6-blocklist-after-call-original.png` | `75f1853427aad3623e651586d34de0cce7b3476eb9ab940dfea06f3a6b19996c` |
| `UNREDACTED-DO-NOT-SHARE-M7-calldetails-original.png` | `56851bf3659c2c69b107b5198228c2ac599c37a88e5e32b664f8aea64233270b` |
| `UNREDACTED-DO-NOT-SHARE-M7-calllog-original.png` (the Recents capture, not evidence) | `b5ce1eef886bd9b13e6a4df36abdbe4f1125ac40dd306ad9ca87abb39132bf3c` |

  One further copy was deleted the same way: a photo of the M7 call-details screen in the tester's Downloads folder, a duplicate of M7 evidence showing the full test number (SHA-256 `f370da894696c47fa664457adaf31f73a0d19632b0202e2a48f0af27362f4b2f`). Where §3c, §3g and §3h say the originals are kept, they are now deleted.
- **The redacted evidence is preserved,** outside the repository: all 11 files (M1, M6, M7) still match the SHA-256 recorded in §3c, §3g and §3h.

**Unchanged:**
- **M6's assessment:** "Accepted with finding — Passed on a later attempt; the initial attempt failed (TC@218); root cause inconclusive."
- **M7:** Passed (§3h).

**Deferred to the end of the session** (each needs the owner's separate approval): returning the call-screening role (the app still holds it); removing X from the blocklist; uninstalling the app; turning off USB debugging and developer options; deleting the tester's working files.

**Left with the owner:** other photos from the session (onboarding screens, the Calls feed, and a photo of the Phone app's Recents list showing unrelated personal calls), and any copies in WhatsApp on the phones or in a WhatsApp backup. None of them is evidence, and none is in the repository.

## 5. Sign-off

| | |
|---|---|
| Tester's statement | "I ran the cases marked Passed or Failed above on the physical phones listed, and the evidence is as captured." |
| Tester | Anuj Kumar |
| Date | 2026-11-06 (M1); 2026-11-07 (M2 on Airtel, as reported by the tester); 2026-11-07 (M6 and M7, reviewed by the owner) |

**After the session,** copy each case's result into the checklist's results table (§5) and into `PRODUCTION-READINESS.md` §3.5 with a link to this record. Claude Code can do that from the committed record, and analyse any failure.
