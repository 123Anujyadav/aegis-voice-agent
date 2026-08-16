# Console · 1 · Information Architecture, Navigation and Screen Inventory

Surface 2. Internal operations. Never ships to subscribers.

---

## 1.1 What this surface is

The tool the people who run the platform use to keep it running: support agents
answering "why didn't my call get screened", fraud analysts reviewing flagged
sessions, SREs handling incidents, AI engineers managing prompts and evals, and
admins managing access.

**It shares the design system and nothing else.** Different users, different
device, different threat model, its own authentication, its own navigation.

### The governing constraint

> **This tool can see subscriber personal data. That capability is the reason it
> exists and the reason it is dangerous.**

Invariant U12: PII is redacted by default, and revealing it is an audited,
reason-required, time-boxed break-glass action. Every design decision on this
surface is downstream of that sentence. An internal tool with ambient PII access
is a breach that has not happened yet.

### The second constraint

Operators use this for eight hours at a time, at speed, on a large screen, with
a keyboard. That argues for density — and density is *not* an excuse to abandon
the design system. Principle 10: an analyst reviewing 200 cases an hour benefits
from the same clarity as a subscriber glancing once a day, and gets it from the
same tokens. What changes is spacing scale and information density, not
vocabulary.

---

## 1.2 Roles

Capability is role-scoped. A capability the role lacks renders as a **Gated
empty state** naming the role required and who grants it — never a disabled
control, never a hidden one
([`01 §1.5`](../01-cross-surface-conventions.md)).

| Role | Can | Cannot |
|---|---|---|
| `support` | Look up a subscriber by MSISDN, see redacted account and forwarding state, see session metadata, run diagnostics | Read transcripts, hear audio, change fraud verdicts, touch flags or prompts |
| `support_lead` | All of `support`, plus approve break-glass requests, issue credits | Read transcripts without their own break-glass |
| `fraud_analyst` | The fraud queue, case detail, transcript access **within a case**, rule tuning proposals | Search transcripts globally, change live rules without review |
| `sre` | Live ops, sessions, incidents, runbooks, flags, carrier health | Any subscriber PII, any transcript |
| `ai_engineer` | Prompts, evals, model routing, degradation controls | Any subscriber PII, any transcript |
| `admin` | User management, role assignment, audit log | Grant themselves a role — a second admin must approve |
| `auditor` | Read-only across the audit log and access records | Anything else |

**No role can read transcripts globally.** Full-text search over subscriber
conversations is a surveillance capability, and it does not exist on this
surface. Transcript access is per-session, from a case or a support ticket, with
a stated reason and an expiry.

---

## 1.3 Information architecture

```
Console
│
├── AUTH               SSO only · hardware key required · no password
│
├── OVERVIEW           the live state of the platform
│   ├── Live sessions, concurrency, admission
│   ├── Latency against the ADR-0011 budget
│   ├── Carrier health matrix
│   └── Open incidents
│
├── SESSIONS           what is happening on calls right now
│   ├── Live list
│   ├── Live session detail        metadata + verdicts; content behind break-glass
│   └── Historical session detail
│
├── SUBSCRIBERS        customer support
│   ├── Lookup                     by MSISDN hash, account ID, session ID
│   ├── Subscriber detail          redacted by default
│   ├── Forwarding diagnostics     the #1 support case
│   ├── Billing lookup
│   └── Break-glass                reason-required, time-boxed, audited
│
├── FRAUD              review and tuning
│   ├── Queue
│   ├── Case detail
│   ├── Rules
│   └── Reported numbers
│
├── ANALYTICS
│   ├── Product
│   ├── Latency                    per-hop, against budget
│   ├── Cost                       per screened minute, per component
│   └── Quality                    verdict agreement, dispute rate
│
├── INCIDENTS
│   ├── List
│   ├── Detail
│   └── Runbooks
│
├── AI
│   ├── Prompt registry
│   ├── Prompt detail and diff
│   ├── Rollout
│   ├── Eval runs
│   └── Eval detail
│
├── FLAGS
│   ├── List
│   └── Detail and rollout
│
└── ADMIN
    ├── Users and roles
    ├── Access requests
    └── Audit log
```

---

## 1.4 Navigation

**Persistent left sidebar**, collapsible to icons, 240 dp expanded. Desktop-first;
below 1024 dp the sidebar becomes an overlay drawer. Below 768 dp the console is
**not supported** and says so plainly rather than degrading into something
unusable during an incident.

```
┌──────────┬───────────────────────────────────────────────┐
│ CONSOLE  │  Breadcrumb · Overview                    ⌘K  │
│          ├───────────────────────────────────────────────┤
│ ◉ Overview│                                              │
│ ▤ Sessions│                                              │
│ ⚇ Subs    │              content                         │
│ ⚠ Fraud   │                                              │
│ ▦ Analytics│                                             │
│ ⚡ Incidents│                                             │
│ ⬡ AI      │                                              │
│ ⚑ Flags   │                                              │
│ ⚙ Admin   │                                              │
│          │                                               │
│ ─────────│                                               │
│ ● prod   │  ← environment indicator, always visible      │
│ a.yadav  │                                               │
└──────────┴───────────────────────────────────────────────┘
```

| Element | Behaviour |
|---|---|
| Sidebar | Persistent. Sections the role cannot access are **absent**, not disabled — a support agent does not need to see that a prompt registry exists |
| Breadcrumb | Always present. Deep views are three levels down and a user must always know where they are |
| **Command palette** (`⌘K` / `Ctrl-K`) | The primary navigation for experienced operators. Jump to any screen, any subscriber by ID, any session, any incident, any flag |
| Environment indicator | **Always visible, colour-coded.** `prod` is unmistakable. Acting on production believing it is staging is the classic internal-tool incident |
| Break-glass indicator | When an active PII grant exists, a persistent bar shows what is unlocked and the time remaining |
| Notifications | In-app toasts plus an incident banner. No web push |

### Keyboard

Keyboard-first, because the users are at a desk all day and speed is the point.

| Key | Action |
|---|---|
| `⌘K` | Command palette |
| `g` then `o`/`s`/`u`/`f`/`i` | Go to Overview / Sessions / Subscribers / Fraud / Incidents |
| `/` | Focus search in the current list |
| `j` / `k` | Next / previous row |
| `Enter` | Open row |
| `Esc` | Close overlay, clear selection |
| `?` | Keyboard reference |

**Every action is reachable without the keyboard**, and every action is
reachable without the mouse. Flow AF7 is keyboard-only fraud triage
([`01 §1.10`](../01-cross-surface-conventions.md)).

---

## 1.5 Screen inventory

| ID | Screen | Role | Presentation |
|---|---|---|---|
| `C01` | Sign in (SSO + hardware key) | any | Full |
| `C02` | Overview — live operations | any | Full |
| `C10` | Sessions — live list | `sre` `fraud_analyst` | Full |
| `C11` | Session detail — live | `sre` `fraud_analyst` | Full |
| `C12` | Session detail — historical | `sre` `fraud_analyst` `support` | Full |
| `C20` | Subscriber lookup | `support` `support_lead` | Full |
| `C21` | Subscriber detail (redacted) | `support` `support_lead` | Full |
| `C22` | **Break-glass request** | `support` `fraud_analyst` | Dialog |
| `C23` | Forwarding diagnostics | `support` `support_lead` | Full |
| `C24` | Billing lookup | `support_lead` | Full |
| `C25` | Support actions (credit, resend, reset) | `support_lead` | Sheet |
| `C30` | Fraud queue | `fraud_analyst` | Full |
| `C31` | Fraud case detail | `fraud_analyst` | Full |
| `C32` | Fraud rules | `fraud_analyst` | Full |
| `C33` | Reported numbers | `fraud_analyst` | Full |
| `C40` | Analytics — product | any | Full |
| `C41` | Analytics — latency | `sre` `ai_engineer` | Full |
| `C42` | Analytics — cost | `sre` `ai_engineer` `admin` | Full |
| `C43` | Carrier health matrix | `sre` `support_lead` | Full |
| `C44` | Analytics — quality | `ai_engineer` `fraud_analyst` | Full |
| `C50` | Incidents list | `sre` | Full |
| `C51` | Incident detail | `sre` | Full |
| `C52` | Runbook viewer | `sre` | Full |
| `C60` | Prompt registry | `ai_engineer` | Full |
| `C61` | Prompt detail and diff | `ai_engineer` | Full |
| `C62` | Prompt rollout | `ai_engineer` | Full |
| `C70` | Eval runs | `ai_engineer` | Full |
| `C71` | Eval run detail | `ai_engineer` | Full |
| `C80` | Feature flags | `sre` `ai_engineer` | Full |
| `C81` | Flag detail and rollout | `sre` `ai_engineer` | Full |
| `C90` | Users and roles | `admin` | Full |
| `C91` | Access requests | `admin` `support_lead` | Full |
| `C92` | **Audit log** | `admin` `auditor` | Full |
| `C99` | Access denied (gated) | any | Full |

**34 screens.**

---

## 1.6 Cross-cutting conventions specific to this surface

| Convention | Detail |
|---|---|
| **Redaction is the default rendering** | An MSISDN renders as `+91 98••• ••210`. A transcript renders as turn counts and timings with content withheld. Revealing requires `C22` |
| **Every list is filterable, sortable and exportable** | Export is audited and rate-limited. A bulk export of subscriber data is the highest-risk action on this surface and is treated as one |
| **Time is always absolute and always zoned** | IST by default, UTC on hover. Relative time alone is unusable in an incident |
| **IDs are copyable everywhere** | One click. An operator's job is largely moving identifiers between systems |
| **Destructive and production-affecting actions confirm by typing the target's name** | Not a checkbox. Not "are you sure" |
| **Nothing auto-refreshes under a cursor** | Live lists buffer updates and show "12 new" rather than reordering under a click |
| **No action is available that is not in a runbook** | If an operator can do it, it is documented. Undocumented capability is how internal tools cause outages |

---

## 1.7 What the console deliberately cannot do

Recorded as design decisions, not omissions.

| Cannot | Why |
|---|---|
| Search transcripts globally | Surveillance capability. Access is per-session, from a case, with a reason |
| Listen to live call audio | `media-relay` never writes audio to disk (Invariant I9) and there is no operator tap. Adding one would be a wiretap |
| Impersonate a subscriber ("log in as") | The single most abused feature in every admin tool ever built |
| Edit a transcript | It is a record. Corrections are annotations, appended and attributed |
| Change a verdict retroactively on the subscriber's surface | An analyst can annotate a case; they cannot rewrite what the subscriber was told |
| Disable a subscriber's forwarding | It is their carrier configuration. We can diagnose it; only they can change it |
| Delete audit entries | Not for any role, including `admin` |
| Grant oneself a role | A second `admin` approves |
