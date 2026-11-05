# Device test session record: 2026-11-05, Redmi 9 Power (Android 12, Airtel)

**Prepared by Claude Code; filled in from the tester's reports and the evidence in §3c.** Only M1 has been run: **M1 Passed** (§3b). Every other case is not run.

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
| Date and time (IST) | Started 2026-11-05; the M1 call 2026-11-06 00:45:39 |
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
| M2 (Airtel) | | Airtel | | | **What the caller heard:** | Not run | |
| M2 (Vi) | | Vi | | | | Blocked: no Vi SIM | |
| M2 (BSNL) | | BSNL | | | | Blocked: no BSNL SIM | |
| M6 | | | | | | Not run | |
| M7 | | | | | | Not run | |
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

- **M6:** saved contact, no contacts access. Attempt 1 is consistent with it, but is not M6.
- **M7:** saved contact, contacts access granted, expected Blocked.
- **The contact behaviour is observed on this one phone only.** It is not established for other phones or OEMs.

**For a failed case,** also record:
- the time;
- the app's screen at that moment (a screenshot);
- whether it repeats on a second call;
- the log, if taken (checklist §3), and only after reading it for phone numbers.

**Do not change the app or retry until it passes:** a failure is a finding.

## 4. Before committing this record

- [x] Every phone number in this file and in every screenshot shows only its last two digits. (Checked 2026-11-06: both images and this file.)
- [x] No contact names other than the test entries appear. (The caller-ID name the Phone app showed is covered.)
- [x] No call recording exists anywhere. (Confirmed by the tester, Anuj Kumar, 2026-11-06: no recording of the test call exists.)
- [x] Logs read through; any with a phone number deleted or redacted. (The Telecom excerpt holds one call and no run of 7 or more digits; no full dump was kept.)
- [ ] Cleanup done (checklist §6): test numbers removed, the role returned, contacts access and OEM settings restored, number hiding undone on C, the app uninstalled, USB debugging off. **Deferred: session continues** (M6, M7 and other cases run on the same phone next). The unredacted originals are deleted then.

## 5. Sign-off

| | |
|---|---|
| Tester's statement | "I ran the cases marked Passed or Failed above on the physical phones listed, and the evidence is as captured." |
| Tester | Anuj Kumar |
| Date | 2026-11-06 |

**After the session,** copy each case's result into the checklist's results table (§5) and into `PRODUCTION-READINESS.md` §3.5 with a link to this record. Claude Code can do that from the committed record, and analyse any failure.
