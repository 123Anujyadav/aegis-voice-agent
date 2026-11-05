# Forwarding test preparation: M3, M4 and M5 (do not run)

- **Status, 2026-11-05: PREPARED ONLY. Do not run.** Do not set call forwarding, and dial no forwarding code. These cases are written down now so they can run as soon as their prerequisites exist and are approved.
- **Nothing here has been run, and nothing has been activated.**
- **The app never dials a forwarding code:** its forwarding screen says "Not available yet" (`HonestScreensDeviceTest`).
- **Companion:** [`PHYSICAL-DEVICE-TEST-CHECKLIST.md`](PHYSICAL-DEVICE-TEST-CHECKLIST.md), the 9 cases that need no forwarding.

## 1. Why these three need more

They test how each carrier forwards a call that rings unanswered (CFNRy: call forwarding on no reply) to the product's number. They need:

| Prerequisite | What it is | Status |
|---|---|---|
| **A DID pool** (X4) | The product's inbound numbers, where forwarded calls land | **Not available.** A CPaaS under ADR-0003 (Exotel primary, Plivo secondary); a paid service needing the owner's approval for that category (`PRODUCTION-READINESS-PLAN.md` §4) |
| **The carrier forwarding matrix** (X4) | For each carrier: whether CFNRy works, its codes, the ring delay, and what happens to a **rejected** call | Not known; M3 measures part of it |
| **The deployed telephony gateway** | Answers the forwarded call (M5) | **Staging is NO-GO** (telephony programme gate P1-38; A1–A12 unapproved) |
| **The voice pipeline** (speech, LLM) | The assistant that answers (M5) | **Deferred:** OD-17 (speech); ADR-0006 (LLM) is a paid service needing approval |
| **Forwarding provisioning** (C8) | How a subscriber's forwarding is set and checked | Blocked on X4 |

## 2. The cases

**Expected results are from `ANDROID-SCREENING-DEVICE-VALIDATION.md` §5, unchanged.**

| # | Setup | Steps | Expected | Needs |
|---|---|---|---|---|
| **M3** | As M2 (X blocked; app holds the role), **with CFNRy set to a test DID** | C calls from X | **Rejected on the handset.** Then: does the carrier forward a rejected call (to voicemail or the DID)? **If it does, ADR-0002's "no forwarding for blocked" fails on that carrier.** Record it per carrier | DID pool; CFNRy |
| **M4** | Number Y on the **allowlist**; CFNRy on | C calls from Y; answer once, then leave it unanswered once | **Rings locally both times.** Unanswered, the carrier forwards it to the DID: the known gap for allowed numbers (`RELEASE-READINESS-ASSESSMENT.md` §2b, expected today) | DID pool; CFNRy |
| **M5** | Unknown number Z; CFNRy on | C calls from Z; do not answer | **Rings, then is forwarded after the ring delay; the AI answers** | DID pool; CFNRy; the deployed gateway; the voice pipeline |

**Run order when allowed:** M3 and M4 first, since they need only the DID and forwarding. M5 waits for the gateway and the voice pipeline.

## 3. Forwarding, when it is approved: set it, check it, remove it

Standard GSM codes, for reference only. **Carrier support varies, especially on VoLTE (Jio), so confirm each carrier's own method first.** Dial nothing until the DID pool and the test are approved.

| Action | Code |
|---|---|
| Set forwarding on no reply to the DID | `**61*<DID>#`, or with a ring delay in seconds, `**61*<DID>**<seconds>#` |
| Check it | `*#61#` |
| **Remove it (always, after the test)** | `##61#` |

**Safety:**
- **Forward only to a DID the product owns and controls.** Never to a personal or third-party number.
- **Remove forwarding immediately after each session,** and check it with `*#61#`. A forgotten forward sends the tester's unanswered calls elsewhere.
- **Forwarded calls may be charged** by the carrier or the CPaaS. Each session needs the owner's approval for that spend.
- **Callers in M5 hear the assistant.** Use only the tester's own numbers. **The caller announcement and the consent rules (ADR-0012) apply** before any real caller is involved.
- **Record no audio.** Note what is heard, as in M2.

## 4. Results (when allowed)

| # | Carrier | Date | Result | Evidence (redacted) | Notes |
|---|---|---|---|---|---|
| M3 | | | **Not run: prerequisites missing** | | |
| M4 | | | **Not run: prerequisites missing** | | |
| M5 | | | **Not run: prerequisites missing** | | |
