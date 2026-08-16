# 2 · Ubiquitous Language Glossary

The vocabulary. One meaning per term, one term per meaning, used identically in
conversation, in this model, in code, and in the interface.

> **A glossary is not documentation of names. It is the agreement that makes
> the model usable.** When a domain expert says "the line lapsed" and an
> engineer hears exactly one thing, the model is working. When two people in the
> same meeting mean different things by "screening", the model has already
> failed and nobody has noticed yet.

---

## 2.1 How to read this

| Column | Means |
|---|---|
| **Term** | The word we use. Capitalised terms are modelled types |
| **Context** | The bounded context that owns the definition. `SK` = shared kernel |
| **Meaning** | What it is, precisely, including what it excludes |
| **Never** | Words that mean the same thing and are prohibited, or near-misses that mean something else |

---

## 2.2 The call

| Term | Context | Meaning | Never |
|---|---|---|---|
| **Call** | Telephony | An inbound attempt to reach a Subscriber's Line. Exists whether or not it was screened | "contact", "connection" |
| **CallSession** | Telephony | The platform's record of one Call, from receipt to outcome. The aggregate root | "call record" (that is a Consumer projection) |
| **Caller** | SK | The party who dialled. **Untrusted by assumption** | "customer", "user" — the user is the subscriber |
| **Screening** | Telephony | The act of an Assistant conversing with a Caller on the Subscriber's behalf | "filtering", "guarding", "vetting", "protecting" |
| **Pre-filter** | Consumer | The on-device Stage 1 evaluation that decides whether a Call needs Screening at all | "local screening" — it does not screen, it decides |
| **Announcement** | AI Orchestration | The deterministic, model-free statement played to the Caller at the start of every Screening. The Caller's lawful basis (I1) | "greeting", "intro", "prompt". It is not a greeting and it is not configurable |
| **Turn** | AI Orchestration | One completed contribution to a conversation, by either party | "message", "utterance" (an Utterance is raw speech; a Turn is a completed contribution) |
| **Takeover** | Telephony | The Subscriber joining a live Screening, bridged to the Caller | "barge in", "intercept", "interrupt" |
| **Transfer** | Business | The Assistant routing a Call to a configured destination per RoutingPolicy | "forward" — forwarding means something else entirely here |
| **Forwarding** | Telephony | The carrier-side conditional diversion of an unanswered Call to our DID | "diversion", "CFNRy", "redirect" |
| **Ring-through** | Telephony | A Call that reached the handset and was not screened | "passed", "allowed" (Allowed is an Outcome of a Screening) |
| **Outcome** | Telephony | How a Call ended: `RANG_THROUGH`, `REJECTED`, `SILENCED`, `TAKEN_OVER`, `TRANSFERRED`, `DECLINED`, `CALLER_ENDED`, `VOICEMAIL`, `REFUSED`, `SHED` | "result", "status" |
| **Leg** | Telephony | One media path within a Call: inbound, takeover, or transfer | "channel", "stream" |
| **Diversion** | Telephony | The SIP header proving a Call reached our DID by forwarding rather than by direct dial | — |
| **Undiverted** | Telephony | An inbound Call to our DID with no valid Diversion. **Hostile by assumption** (I10) | "direct call" |

---

## 2.3 The people and the parties

| Term | Context | Meaning | Never |
|---|---|---|---|
| **Subscriber** | Identity | A person with an account, identified by their MSISDN. The data principal under DPDP | "user" in domain conversation, "customer" (that is a billing relationship) |
| **Line** | Telephony | A phone number under our screening, belonging to a Subscriber or an Organisation | "number" alone — a DID is also a number |
| **MSISDN** | SK | The Subscriber's own phone number in E.164. The identity primitive (ADR-0010) | Used interchangeably with "phone number" — a Caller has a PhoneNumber, a Subscriber has an MSISDN |
| **Device** | Identity | A handset holding a non-exportable credential, enrolled to a Subscriber | "phone" (ambiguous with Line), "client" |
| **Operator** | Administration | An internal person with a role in the Console. **Never a Subscriber concept** | "admin" (Admin is one specific OperatorRole), "agent" (Agent means the AI) |
| **Member** | Business | A Subscriber who belongs to an Organisation | "employee", "user" |
| **Organisation** | Business | A business customer. Owns BusinessLines, has Members | "company", "account", "team", "workspace" |
| **Data principal** | Identity | The DPDP term for the person a piece of personal data is about. Used verbatim where the statute requires it | Substituted with "user" in legal text |

---

## 2.4 The two agents

The most important distinction in the vocabulary. Conflating these two is the
single most consequential language failure available to us.

| Term | Context | Meaning | Never |
|---|---|---|---|
| **Assistant** | AI Orchestration | The product-facing name for either agent, used in the interface. Always capitalised in copy | "AI", "bot", a persona, a product name, a human name |
| **Screening Agent** | AI Orchestration | The agent that converses with a Caller. Counterparty is **hostile by assumption**. Cannot see Subscriber personal data (I4) | Called "the Assistant" in engineering conversation without qualification |
| **Personal Assistant** | AI Orchestration | The agent that converses with the authenticated Subscriber about their own Calls. Trusted counterparty | Conflated with the Screening Agent, ever, in any document or diagram |
| **Tool** | AI Orchestration | A capability the agent may invoke. The Screening Agent's tools are read-mostly and PII-free by construction | "function", "action" (an Action is a Subscriber decision) |
| **DisclosureScope** | SK | The explicit, default-empty set of facts the Screening Agent may reveal to a Caller | "permissions", "settings" |
| **Thinking** | AI Orchestration | The state in which a model request is **genuinely in flight**. Not a visual effect (U8) | Used to describe waiting for a caller, or a queued request |
| **Provenance** | AI Orchestration | Whether a string was written by a model, produced deterministically, or authored editorially. Drives the AiBadge | "source", "origin" |

---

## 2.5 Judgement and risk

| Term | Context | Meaning | Never |
|---|---|---|---|
| **Assessment** | Fraud | The platform's judgement about one Call, with its evidence and confidence. The aggregate | "score", "rating", "detection" |
| **Verdict** | Fraud | The user-facing rendering of an Assessment's RiskLevel and Confidence together | Used to mean the Assessment itself |
| **RiskLevel** | SK | One of `SAFE`, `UNKNOWN`, `SPAM`, `FRAUD`, `EMERGENCY` | "threat level", "severity" |
| **Confidence** | SK | `LOW`, `MEDIUM` or `HIGH`, calibrated. **Always rendered** (U4) | Hidden, defaulted, or inferred from RiskLevel |
| **Unknown** | Fraud | "We did not assess this." A stated fact with a reason | Treated as equivalent to Safe. They are opposite claims |
| **Evidence** | Fraud | The specific Transcript turns that produced an Assessment. **An Assessment without evidence is not published** | "reason", "explanation" (the Pattern explanation is editorial copy) |
| **Pattern** | Fraud | A named scam shape: `OTP_REQUEST`, `BANK_IMPERSONATION`, `ADVANCE_FEE`, `DIGITAL_ARREST`… | "category", "type" |
| **Reputation** | Fraud | An aggregated, k-anonymised signal about a PhoneNumber across subscribers. Contains no subscriber identifiers | "community rating", "crowd score" |
| **Dispute** | Consumer | A Subscriber asserting an Assessment was wrong. A first-class quality signal, not a complaint | "feedback", "correction" |
| **Emergency** | AI Orchestration | Detected intent that terminates Screening and hands control to the Subscriber (U7). A control-flow event, not a risk grade | Treated as a severe kind of fraud |

---

## 2.6 Consent, privacy and the record

| Term | Context | Meaning | Never |
|---|---|---|---|
| **Consent** | Identity | A purpose-bound, withdrawable permission from a Subscriber. Append-only | Used for the Announcement, which is a **lawful basis**, not a consent |
| **ConsentPurpose** | SK | One specific purpose. `CONTACT_SYNC`, `CALL_RECORDING`, `TRANSCRIPT_RETENTION`, `PRODUCT_ANALYTICS`, `CRASH_DIAGNOSTICS`, `BUSINESS_LINE_VISIBILITY` | Bundled into a single "accept" |
| **Lawful basis** | Identity | The legal ground for processing that is not consent. The Announcement is ours for Caller audio | Called a consent, because it cannot be withdrawn |
| **Transcript** | AI Orchestration | The complete textual record of a Screening. `SENSITIVE`. The product's record | "log", "recording" (a Recording is audio) |
| **Recording** | Voice | Persisted call **audio**. Off by default. Requires `CALL_RECORDING` consent | Used to mean the Transcript |
| **Interim** | Voice | Unconfirmed ASR text, pre-endpoint. Never persisted, never announced, never in a notification | Treated as a Turn |
| **Summary** | AI Orchestration | A model-generated one-line description of a Screening. Always carries Provenance | Presented without an AiBadge |
| **Retention** | Identity | How long a class of data is kept, within statutory bounds, subscriber-configurable | Called "storage" or "backup" |
| **Erasure** | Identity | Fulfilment of a DPDP deletion request across every store | "delete account" (which is the user-facing name for the request) |
| **Break-glass** | Administration | A time-boxed, reason-required, audited grant that reveals `SENSITIVE` data to an Operator | "elevated access", "admin mode" |

---

## 2.7 Telephony infrastructure

| Term | Context | Meaning | Never |
|---|---|---|---|
| **DID** | Telephony | A Direct Inward Dial number we control, to which Calls are forwarded. Publicly dialable and therefore an attack surface | "our number" in engineering conversation |
| **ForwardingConfiguration** | Telephony | The carrier-side setting diverting a Line's unanswered Calls to a DID | "the setting", "CFNRy" |
| **Lapsed** | Telephony | A ForwardingConfiguration that was active and is no longer, for any reason | "broken", "failed" — it may have been cleared deliberately |
| **Unverifiable** | Telephony | We could not determine forwarding state because the carrier does not support interrogation. **Distinct from Lapsed** | Collapsed into Lapsed. Claiming a fault we did not observe is as damaging as missing one |
| **MMI** | Telephony | The GSM supplementary-service code that provisions or clears forwarding. Constructed client-side from a validated DID only (I10) | Accepted from a server response |
| **Interrogation** | Telephony | Querying the carrier for current forwarding state (`*#61#`) | "check", "ping" |
| **Circle** | Telephony | The Indian telecom service area. Carrier behaviour varies by circle, so it is part of CarrierIdentity | "region" (Region is an AWS deployment concept) |
| **Admission** | Telephony | The decision to accept a Call into the screening pipeline, subject to capacity and rate limits | "acceptance", "routing" |
| **Shed** | Telephony | Refusing admission under load, causing the Call to ring through unscreened. Never a dropped Call (I11) | "drop", "reject" (Reject is a pre-filter outcome) |

---

## 2.8 Commerce

| Term | Context | Meaning | Never |
|---|---|---|---|
| **Plan** | Billing | A named set of Entitlements at a price. `FREE`, `PREMIUM`, `BUSINESS` | "tier" (Tier means ModelTier), "package" |
| **Entitlement** | Billing | A capability granted by a Plan, with an optional limit. The authority on "may they do X" | "feature", "permission" |
| **Premium** | Billing | The paid consumer Plan | "Pro", "Plus", "Gold" |
| **Quota** | Billing | A metered limit within a Plan, typically screened minutes | "allowance", "credits" |
| **Grace** | Billing | The stated period after a failed payment during which Screening continues | "dunning" in user-facing language |
| **Usage** | Billing | Metered consumption per Line per period. Must reconcile exactly with the Invoice | "activity" |

---

## 2.9 Terms we do not use

Prohibited because each imports a mental model that is wrong for this product.

| Prohibited | Why | Say instead |
|---|---|---|
| "AI" as a noun in the interface | Imports an anthropomorphic frame we deliberately reject | **Assistant** |
| "Bot" | Same, worse | **Assistant** |
| "Blacklist" / "whitelist" | Non-descriptive and unnecessary | **Blocklist** / **Allowlist** |
| "Spam call" as a verdict label | Overclaims certainty. The domain has Confidence | **Likely spam** at the rendered confidence |
| "Wiretap", "monitor", "listen in" | Describes capabilities we do not have and must not appear to have | **Screening**, **live transcript** |
| "User" in domain conversation | Ambiguous across Subscriber, Member, Operator and Caller | The specific term |
| "Record" as a verb for transcription | Conflates text with audio, which have different consents | **Transcribe** for text, **record** only for audio |
| "Session" without qualification | Four different sessions exist: CallSession, VoiceSession, AuthSession, AssistantSession | The specific one |
| "Notify" for anything not delivered to a device | Blurs a domain concept | **Publish** an event, **surface** in the interface |
| "Safe" for an unassessed Call | Opposite claims | **Not assessed** |
| "Protect", "guard", "defend" | Overclaims and sets an expectation we cannot meet on every Call | **Screen**, **flag**, **block** |

---

## 2.10 Terms that differ between contexts, deliberately

Recorded because these are the collisions that produce silent misunderstanding.

| Term | In one context | In another |
|---|---|---|
| **Line** | Telephony: a phone number under screening | Business: a BusinessLine, which *references* a Telephony Line and adds routing and assignment |
| **Session** | Identity: an authenticated AuthSession | Telephony: a CallSession · Voice: a VoiceSession · AI: an AssistantSession |
| **Tier** | Billing: a PlanTier | AI Orchestration: a ModelTier (ADR-0006) |
| **Region** | Platform: an AWS deployment region | Telephony: distinct from Circle, which is the Indian telecom service area |
| **Verified** | Business: a VerifiedBusinessIdentity from a registry | Telephony: a ForwardingConfiguration confirmed by interrogation |
| **Active** | Identity: a Subscriber in good standing | Telephony: a ForwardingConfiguration currently diverting · Billing: a Subscription being paid for |

**Where a term is ambiguous across contexts, the context name is mandatory in
speech and in code.** "Telephony Line", "Business Line". Never the bare word in
a document that spans both.
