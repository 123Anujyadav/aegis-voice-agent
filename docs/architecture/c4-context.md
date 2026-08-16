# C4 Level 1 — System Context

Who uses CallScreen, and what does it depend on.

**Source ADRs:** 0002 (telephony), 0003 (carrier), 0005–0007 (AI vendors),
0010 (identity), 0012 (privacy).

---

## Diagram

```mermaid
flowchart TB
    subgraph people["People"]
        SUB["<b>Subscriber</b><br/>Our customer.<br/>Consented. Owns the number."]
        CALLER["<b>Caller</b><br/><b>NOT our customer.</b><br/>Never consented.<br/>Data Principal under DPDP."]
        OPS["<b>Operator</b><br/>Support and on-call<br/>engineering staff"]
    end

    subgraph platform["🇮🇳 CallScreen Platform"]
        SYS["<b>CallScreen</b><br/>Answers unknown inbound calls with an AI agent,<br/>screens intent, detects fraud, and reports<br/>to the subscriber.<br/><br/>Android app + India-resident backend."]
    end

    subgraph telco["Telephony"]
        CARRIER["<b>Mobile Carrier</b><br/>Jio · Airtel · Vi · BSNL<br/>Holds the conditional-forwarding<br/>configuration"]
        CPAAS["<b>CPaaS</b> 🇮🇳<br/>Exotel primary · Plivo secondary<br/>Licensed operator.<br/>Owns our DIDs."]
    end

    subgraph ai["AI vendors"]
        ASR["<b>Speech-to-Text</b><br/>Google STT v2 🇮🇳<br/>Deepgram 🌐 · Sarvam 🇮🇳"]
        LLM["<b>Claude API</b> 🌐<br/>Haiku 4.5 · Sonnet 5 · Opus 5"]
        TTS["<b>Text-to-Speech</b> 🌐<br/>ElevenLabs · Cartesia · Sarvam 🇮🇳"]
    end

    subgraph other["Other external"]
        PLAY["<b>Google Play</b><br/>Integrity attestation<br/>Distribution · Billing"]
        FCM["<b>FCM</b><br/>Push notification"]
        SMS["<b>SMS gateway</b> 🇮🇳<br/>OTP delivery"]
        PAY["<b>Payments</b> 🇮🇳<br/>UPI · cards · subscriptions"]
    end

    CALLER -->|"dials the subscriber's<br/>existing number"| CARRIER
    CARRIER -->|"conditional forward<br/>on no-reply · CFNRy"| CPAAS
    CPAAS ==>|"inbound leg +<br/>bidirectional media"| SYS

    SUB -->|"installs · enrols ·<br/>reviews screened calls"| SYS
    SUB -.->|"sets forwarding via<br/>MMI code, with consent"| CARRIER
    SYS -.->|"live-screening alert"| FCM
    FCM -.-> SUB

    SYS ==>|"caller audio 🔒"| ASR
    SYS -->|"conversation turn"| LLM
    SYS ==>|"agent speech"| TTS

    SYS -->|"verify device<br/>and app integrity"| PLAY
    SYS -->|"send OTP"| SMS
    SYS -->|"charge subscription"| PAY
    OPS -->|"support · incident<br/>response"| SYS

    classDef person fill:#0B3D91,stroke:#062a66,color:#fff
    classDef system fill:#1168BD,stroke:#0b4c8c,color:#fff
    classDef ext fill:#6E7781,stroke:#4a5058,color:#fff
    classDef risk fill:#8B2635,stroke:#5e1923,color:#fff

    class SUB,OPS person
    class CALLER risk
    class SYS system
    class CARRIER,CPAAS,ASR,LLM,TTS,PLAY,FCM,SMS,PAY ext
```

---

## The asymmetry that defines this system

The two humans in this diagram have completely different relationships with us,
and almost every non-obvious design decision follows from the gap between them.

| | Subscriber | Caller |
|---|---|---|
| Relationship | Customer | **None** |
| Consent | Explicit, layered, revocable | **Announcement + continuing the call** |
| Knows the platform exists | Yes | Only from the announcement |
| Data we hold | Account, devices, call history | **Voice, number, an AI's fraud judgement** |
| Rights under DPDP | Full | **Full — identical** |
| Can complain to the Board | Yes | **Yes** |

The caller is shown in red above because they are the party we are most likely to
harm and least likely to hear from. ADR-0012 exists primarily for them.

---

## Why the call path looks like a detour

The caller dials the subscriber's number. The call reaches the **carrier**, not
us. Only when the handset does not answer within the ring window does the
carrier forward it to a DID we control at the CPaaS — and only then do we see it
at all.

This is not a design preference. Android does not expose call audio to
third-party applications under any API level, permission, or role (ADR-0002 §2).
Carrier-side forwarding is the only mechanism by which an AI can converse with a
caller on a stock handset.

Two consequences visible in the diagram:

- **The subscriber configures their own carrier**, via an MMI code dialled with
  their consent. We never touch their carrier account.
- **Our failure mode is "the phone rings normally."** If the platform is
  unreachable, forwarding fails and the call completes as it always did.

---

## Residency at a glance

| Marker | Meaning |
|---|---|
| 🇮🇳 | Inside the India residency boundary. Default for all personal data. |
| 🌐 | Outside it. Permitted **only** under the consent-gated, audited exception in ADR-0012 §5.4. |

Claude and the default TTS providers sit outside the boundary today. Eliminating
that — by promoting in-country providers once they reach quality parity — is a
tracked Phase-2 goal (ADR-0012 §14), not a permanent accommodation.
