# Prototype screens: honesty and readiness audit

- **As of:** 2026-10-26, from `main` `47954e6`. Follows #179, which covered onboarding, Protection and the calls feed.
- **Method:** every screen's displayed values were traced to their source in code. "Real" means read from an API, stored data or the device. A value is not treated as real because it is on screen.
- **Classes:**
  - **REAL:** backed by an implemented API, persistent local data, or device state.
  - **STATIC:** a genuine label or explanation.
  - **SAMPLE:** invented data.
  - **NOT BUILT:** a feature that does not exist.
  - **POLICY:** needs an owner decision, consent or a backend first.

## 1. Screens a release build can reach

The bottom tabs lead to: Calls, Protection, Assistant and Settings.

| Screen | Was | Source | Now (#182) |
|---|---|---|---|
| Assistant home (`ASSISTANT_HOME`) | "Autonomous Screening: AI will answer unknown calls on your behalf", with a switch defaulting to **on** | SAMPLE / NOT BUILT. The switch was `remember { mutableStateOf(true) }`: never saved and wired to nothing. Assistant screening needs carrier forwarding (ADR-0002 Stage 2), which is not available. | "Assistant screening: **Not available yet** … needs call forwarding". There is **no switch**: a toggle that saves and changes nothing would mislead. |
| Voice setup (`VOICE_SETUP`) | Three voices, an "Active" chip on the selected one, and "Audition & Save Voice" | STATIC (the vendors are ADR-0007's) / NOT BUILT. The selection lived only in memory, the button only went back, and no tts-gateway exists. | The voices are kept as **planned** voices. "Choosing one isn't available yet, and nothing is saved." There is no "Active" chip, and the button is disabled: "Saving a voice isn't available yet". |
| Voice conversation (`VOICE_CONVERSATION`) | "AI Listening…", a pulsing orb, "Say something to test Aegis response" | NOT BUILT. **The app declares no `RECORD_AUDIO` and records nothing**, so "listening" was false and privacy-alarming. | "Voice sessions aren't available yet. The app doesn't use your microphone. Voice input will ask for microphone access first, and only when you turn it on" (UX §1.5 #5). The orb no longer pulses. |
| Settings tab (`TASK_CENTER`) | "Update Fraud Database: spam telemetry signatures" (Done), "Verify Family Contacts: Confirm **David's** Senior Mode emergency bypass number", "Microphone Calibration" | SAMPLE (a fictional person, an invented database, an invented calibration) | A **real checklist** computed on each return from the device (`SetupTasks`). **On-device blocking** reflects the call screening role: Done, To do, or "Not available on this Android version". **Block saved contacts** reflects contacts access. **Call forwarding** is Not available. |

## 2. Prototype screens: debug-only design previews

**Nothing in a release build navigates to these.** Previously they were registered in the release graph, and debug builds could open them with the start-screen extra (#180). Every value on them is invented: none reads an API, stored data or the device.

#182 registers them **only in debug builds** (`AegisNavGraph(includeDesignPreviews = BuildConfig.DEBUG)`), each under a fixed label: "Design preview: sample data, not a working feature. Not in release builds." `DesignPreviewGatingTest` proves a release graph contains none of them, and a debug graph shows the label. Their design structure is kept for when real data exists.

| Screen (route) | Sample content | What real data would need | Class |
|---|---|---|---|
| Incoming call | "Unknown Caller +1 (555) 234-5678" | Android cannot show the AI on an incoming call (ADR-0002 C1); the live screen comes from the gateway via FCM | NOT BUILT |
| Live screening | A scripted bank-scam transcript; it named a real person ("screening this call for Abhik"), now replaced | The gateway and STT/LLM pipeline (Stage 2), FCM, consent | NOT BUILT, POLICY |
| Post-call summary | "Call Duration: 42 seconds", "Low Risk", an extracted summary | Call records and summaries from the backend; retention under ADR-0012 | NOT BUILT, POLICY |
| Fraud warning | "HIGH RISK FRAUD DETECTED … matched suspicious audio vectors" | A fraud verdict source (fraud domain) and a scoring policy | NOT BUILT, POLICY |
| Emergency detection | "Dr. Smith (City Hospital)", "authentic emergency intent … Silent mode bypassed" | Emergency classification (AI orchestration) | NOT BUILT, POLICY |
| Fraud intelligence | Numbers labelled "Customs / Courier Arrest Scam", "High Risk" | A reputation feed (k-anonymised, fraud domain) and the band-to-decision policy | NOT BUILT, POLICY |
| Family shield | "Sarah / Wife / Protected" and other family members | Family sharing: a domain model, consent from each member, a backend | NOT BUILT, POLICY |
| Conversation memory | "Mark (Courier) prefers leaving packages…", "Dr. Smith's Office" | Assistant memory: storage, retention and consent | NOT BUILT, POLICY |
| Consent vault (`CONSENT_VAULT`) | "**DPDP Act 2023 Compliant**", "Data pinned to AWS India (ap-south-1)", "Voice Recording … Retained 7 days for deepfake analysis" | ADR-0012 §5.1 consents recorded and versioned in identity; **no consent API or on-device record exists**. The "7 days" contradicts ADR-0012 (audio default 30 days, opt-in, off). The compliance claim is unverified (no DPDP review yet). | POLICY. It must never imply consent is captured or enforced until it is. |
| Business: AI receptionist config, call-routing builder, AI ops studio, analytics dashboard, conversation trace replay, ops dashboard, routing rules (7 screens) | "12,450 calls handled", "1.2M AI requests +12%", "Environment: Production", prompt versions, a trace with invented data, a receptionist called "Alex" | The business domain (docs/domain/16-business.md) backend; nothing exists | NOT BUILT |

## 3. Owner-reserved decisions found (recommendation and consequence)

| Decision | Recommendation | Consequence |
|---|---|---|
| **Consent UI** (A10/A11, ADR-0012 §5.1) | Build it only with identity's consent records: versioned, timestamped, with the notice text. Until then, show no consent toggles. | *Toggles before records exist* would imply consent is captured and enforced when it is not: a DPDP risk. *Waiting:* Assistant screening and recording stay unavailable, which they are anyway without forwarding. |
| **Voice choice in the app** | Do not expose vendor names to users. ADR-0007 routes voices by language and latency automatically. Offer "voice style" if anything. | *Vendor names in the UI:* ties the product to vendors, and contradicts routing and failover (a user's "Cartesia" choice would silently change on failover). |
| **Family sharing** | Needs its own ADR: what is shared, per-member consent, children (DPDP §9). | *Without one:* the Family screen must stay a preview. |
| **Fraud warnings and intelligence** | Same as the reputation decision (`ANDROID-SCREENING-DEVICE-VALIDATION.md` §6): show bands only from a real k-anonymised source, and never invent a verdict. | *Showing verdicts without a source:* false accusations about callers. |
| **Business analytics** | Defer until the business backend exists. | *None:* preview only. |

## 4. What remains

- **Physical-device and carrier testing:** `ANDROID-SCREENING-DEVICE-VALIDATION.md` §5.
- **Each preview screen:** becomes real only with its backend, consent and policy (§2–§3).
- **Retired claims:** `ReleaseCopyGuardTest` keeps the removed claims from returning to production source.
