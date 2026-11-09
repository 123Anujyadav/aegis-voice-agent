# Device test session record: template

**Copy this file once per session**, for example to `DEVICE-TEST-SESSION-<date>-<tester initials>.md`, and fill it in **during** the session. The cases and their expected results are in [`PHYSICAL-DEVICE-TEST-CHECKLIST.md`](PHYSICAL-DEVICE-TEST-CHECKLIST.md).

**When a case counts as passed:**
- someone ran it on a physical phone;
- the observed result matches "Expected";
- the redacted evidence is attached.

**Rules for filling it in:**
- **Fill in only what was actually done.** Leave a case "Not run" rather than estimate it.
- **Commit only after the redaction check (§4) is complete.** Unredacted evidence never goes into the repository.

## 1. Session

| Field | Value |
|---|---|
| Date and time (IST) | |
| Tester | |
| Location (city only) | |
| Repository commit the APK was built from (`git rev-parse --short HEAD`) | |
| APK | `app-debug.apk` |
| APK SHA-256 (`sha256sum app/build/outputs/apk/debug/app-debug.apk`) | |
| App version (`adb shell dumpsys package com.callscreen.debug \| grep versionName`) | |

## 2. Devices and SIMs

Record one row per phone used. **Write SIM numbers as "+91 ••••••••NN"**, with the last two digits only.

| Role | Maker | Model | Android (`getprop ro.build.version.release`) | Dual-SIM? | Carrier(s) and SIM (redacted) |
|---|---|---|---|---|---|
| P1, test phone | | | | | |
| P2, Android 8/9 | | | | | |
| P3, dual-SIM | | | | | |
| P4, OEM | | | | | |
| C, caller (X) | | | | | |

## 3. Cases run

**Result** is one of:
- **Passed:** observed matches expected, with evidence attached;
- **Failed:** observed differs from expected; record exactly how;
- **Blocked:** it could not be run, and why;
- **Not run.**

**"Accepted with finding" is not a Passed result** (OD-10, owner, 2026-11-09: [record](OD-10-DECISION-2026-11-09.md) §7). The owner may accept a finding, but only explicitly, naming the criterion it applies to. Record the finding, its impact, evidence, risk, the owner's acceptance (date and exact wording) and any required remediation or follow-up.

| Case | Phone | Carrier | Time | Steps followed as written? (yes, or what differed) | Observed | Result | Evidence files (redacted) |
|---|---|---|---|---|---|---|---|
| M1 | | | | | | Not run | |
| M2 (Jio) | | Jio | | | **What the caller heard:** | Not run | |
| M2 (Airtel) | | Airtel | | | **What the caller heard:** | Not run | |
| M2 (Vi) | | Vi | | | **What the caller heard:** | Not run | |
| M2 (BSNL) | | BSNL | | | **What the caller heard:** | Not run | |
| M6 | | | | | | Not run | |
| M7 | | | | | | Not run | |
| M8 | | | | | | Not run | |
| M9 | | | | | | Not run | |
| M10 (SIM 1) | | | | | | Not run | |
| M10 (SIM 2) | | | | | | Not run | |
| M11 | | | | | | Not run | |
| M12 | | | | | **OEM settings changed:** | Not run | |

**For a failed case,** also record:
- the time;
- the app's screen at that moment (a screenshot);
- whether it repeats on a second call;
- the log, if taken (checklist §3), and only after reading it for phone numbers.

**Do not change the app or retry until it passes:** a failure is a finding.

## 4. Before committing this record

- [ ] Every phone number in this file and in every screenshot shows only its last two digits.
- [ ] No contact names other than the test entries appear.
- [ ] No call recording exists anywhere.
- [ ] Logs read through; any with a phone number deleted or redacted.
- [ ] Cleanup done (checklist §6): test numbers removed, the role returned, contacts access and OEM settings restored, number hiding undone on C, the app uninstalled, USB debugging off.

## 5. Sign-off

| | |
|---|---|
| Tester's statement | "I ran the cases marked Passed or Failed above on the physical phones listed, and the evidence is as captured." |
| Tester | |
| Date | |

**After the session,** copy each case's result into the checklist's results table (§5) and into `PRODUCTION-READINESS.md` §3.5 with a link to this record. Claude Code can do that from the committed record, and analyse any failure.
