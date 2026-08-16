# Business Portal · 1 · Information Architecture, Navigation and Screen Inventory

Surface 3. External, customer-operated, organisation-scoped.

---

## 1.1 What this surface is

The web application a business uses to run CallScreen as a receptionist: assign
numbers, set routing and hours, manage who can see what, read call insights, and
pay for it.

**The Android app remains the primary product for the individual.** A business
member still screens their personal calls on their phone. This portal is where
the *organisation* is administered, and its user is an administrator at a desk,
not a subscriber in a queue.

### The governing constraint

> **Every call record in this portal belongs to a person as well as to an
> organisation.**

A business line's calls are visible to the organisation. A member's personal
line's calls are not, ever, under any role, by any configuration. That boundary
is stated to the member in the Android app (`A56`), stated to the administrator
here, and enforced server-side.

An administrator who can accidentally read an employee's personal call would be
a product defect of the most serious kind, and the IA is arranged so that the
possibility does not arise: **personal lines are not objects in this surface.**
They do not appear in a list, a filter, an export, or a search.

### The second constraint

The buyer is a small Indian business — a clinic, a shop, a two-person agency, a
property broker. Not an enterprise IT department. The portal must be
self-explanatory on first use, with no implementation call and no onboarding
consultant.

---

## 1.2 Roles

| Role | Can | Cannot |
|---|---|---|
| `owner` | Everything, including billing and deleting the organisation | — |
| `admin` | Numbers, routing, team, CRM, analytics, API keys | Billing, delete the organisation |
| `member` | Read call insights for lines they are assigned to; edit their own contacts | Numbers, team, billing, keys, other lines' calls |
| `billing` | Billing, invoices, plan, usage | Any call content |
| `viewer` | Read-only analytics and insights, no call content | Anything else |

**`billing` cannot read call content.** The finance person in a clinic has no
business reading a patient's screening transcript, and separating those roles is
the difference between a product a clinic can adopt and one it cannot.

A capability the role lacks renders as a **Gated empty state** naming the role
required and the person to ask
([`01 §1.5`](../01-cross-surface-conventions.md)).

---

## 1.3 Information architecture

```
Business Portal  (organisation-scoped)
│
├── AUTH                   MSISDN + OTP · device trust · org selection
│
├── DASHBOARD              what happened, and is anything wrong
│   ├── Today's calls, answered / screened / missed
│   ├── Number health          ◀── the forwarding-health analogue
│   ├── Usage against plan
│   └── Recent flagged calls
│
├── CALLS                  the record
│   ├── Insights list          filter by number, outcome, verdict, member
│   ├── Call detail            transcript · timeline · verdict · outcome
│   ├── Saved views
│   └── Export
│
├── NUMBERS                the operational core
│   ├── List
│   ├── Number detail
│   │   ├── Assignment         which members
│   │   ├── Hours              business hours + holidays
│   │   ├── Routing            transfer targets, fallbacks
│   │   ├── Assistant          greeting, instructions, voice, language
│   │   └── Forwarding health
│   └── Add a number
│
├── TEAM
│   ├── Members and roles
│   ├── Invitations
│   └── Line assignments
│
├── CRM                    lightweight, deliberately
│   ├── Contacts               people who called, enriched
│   ├── Contact detail         every call from them
│   └── Integrations           export to a real CRM
│
├── ANALYTICS
│   ├── Call volume and outcomes
│   ├── Response and handling
│   ├── Spam and fraud
│   └── Usage reports
│
├── DEVELOPERS
│   ├── API keys
│   ├── Webhooks
│   └── Delivery logs
│
├── BILLING
│   ├── Overview and usage
│   ├── Plan and subscription
│   ├── Invoices
│   └── Payment methods
│
└── SETTINGS
    ├── Organisation
    ├── Data and privacy         retention, export, erasure, DPA
    ├── Audit log
    └── Danger zone              transfer ownership, delete organisation
```

---

## 1.4 Navigation

**Persistent left sidebar**, 240 dp, collapsible. Organisation switcher at the
top for users who belong to more than one — rare, but a bookkeeper serving three
clinics is a real user.

Responsive down to tablet. Below 768 dp the portal renders a **reduced,
read-only view**: dashboard, call insights, and number health. Administration is
not attempted on a phone, and the portal says so plainly rather than shipping a
cramped form that produces misconfigured routing.

```
┌──────────┬──────────────────────────────────────────────┐
│ ⬡ Sunrise│  Dashboard                              ⌘K   │
│   Clinic⌄│                                              │
│          ├──────────────────────────────────────────────┤
│ ▦ Dashboard                                             │
│ ☰ Calls  │                                              │
│ # Numbers│              content                         │
│ ⚇ Team   │                                              │
│ ⚉ CRM    │                                              │
│ ▤ Analytics                                             │
│ ⌘ Developers                                            │
│ ₹ Billing│                                              │
│ ⚙ Settings                                              │
│          │                                              │
│ ─────────│                                              │
│ 4 of 5   │  ← plan usage, always visible                │
│ numbers  │                                              │
└──────────┴──────────────────────────────────────────────┘
```

| Element | Behaviour |
|---|---|
| Sidebar | Sections the role cannot access are **absent** |
| Org switcher | Only if the user belongs to > 1. **The current org name is always visible** — acting on the wrong organisation is this surface's version of prod/staging confusion |
| Plan usage | Persistent in the sidebar footer. Approaching a limit is a fact the user should never discover from a failure |
| Command palette | `⌘K`. Jump to a number, a member, a contact, a screen |
| Breadcrumb | On every screen below the top level |

---

## 1.5 Screen inventory

| ID | Screen | Role | Presentation |
|---|---|---|---|
| `B01` | Sign in | any | Full |
| `B02` | Organisation selection | any | Full |
| `B03` | Create organisation | any | Full |
| `B04` | Accept invitation | any | Full |
| `B05` | Onboarding — first number | `owner` `admin` | Full |
| `B10` | Dashboard | any | Full |
| `B20` | Call insights | `owner` `admin` `member` | Full |
| `B21` | Call detail | `owner` `admin` `member` | Full |
| `B22` | Saved views | `owner` `admin` `member` | Sheet |
| `B23` | Export calls | `owner` `admin` | Dialog |
| `B30` | Numbers list | `owner` `admin` | Full |
| `B31` | Number detail | `owner` `admin` | Full |
| `B32` | Add a number | `owner` `admin` | Full |
| `B33` | Number — assistant configuration | `owner` `admin` | Full |
| `B34` | Number — routing and hours | `owner` `admin` | Full |
| `B35` | Number — forwarding health | `owner` `admin` | Full |
| `B40` | Team members | `owner` `admin` | Full |
| `B41` | Member detail and role | `owner` `admin` | Full |
| `B42` | Invite members | `owner` `admin` | Dialog |
| `B50` | CRM contacts | `owner` `admin` `member` | Full |
| `B51` | Contact detail | `owner` `admin` `member` | Full |
| `B52` | CRM integrations | `owner` `admin` | Full |
| `B60` | Analytics | any except `billing` | Full |
| `B61` | Usage reports | any | Full |
| `B70` | API keys | `owner` `admin` | Full |
| `B71` | Create key (one-time reveal) | `owner` `admin` | Dialog |
| `B72` | Webhooks | `owner` `admin` | Full |
| `B73` | Delivery logs | `owner` `admin` | Full |
| `B80` | Billing overview | `owner` `billing` | Full |
| `B81` | Plan and subscription | `owner` `billing` | Full |
| `B82` | Invoices | `owner` `billing` | Full |
| `B83` | Payment methods | `owner` `billing` | Full |
| `B90` | Organisation settings | `owner` `admin` | Full |
| `B91` | Data and privacy | `owner` | Full |
| `B92` | Audit log | `owner` `admin` | Full |
| `B93` | Danger zone | `owner` | Full |
| `B99` | Access denied (gated) | any | Full |

**37 screens.**

---

## 1.6 Conventions specific to this surface

| Convention | Detail |
|---|---|
| **Personal lines are not objects here** | They do not appear in any list, filter, search or export. The boundary is architectural, not a permission check |
| Every list is filterable, sortable, saveable as a view, and exportable | Exports are audited and appear in `B92` |
| **Usage against plan is always visible** | In the sidebar, on the dashboard, and inline where a limit is near. A user should never learn about a limit from a failure |
| Time is absolute and zoned to the organisation's timezone | Set once, at organisation creation, and shown everywhere |
| Destructive actions confirm by typing the object's name | Deleting a number disconnects a working phone line |
| **Configuration changes state their effect before applying** | "Calls to this number will go to voicemail outside 9–6 from tomorrow." Routing is a live telephony configuration and a preview is not a nicety |
| No dark patterns in billing | Cancel is in plain words, one retention offer maximum, no obscured close |

---

## 1.7 What the portal deliberately cannot do

| Cannot | Why |
|---|---|
| See a member's personal-line calls | The boundary. Not a permission — an absence |
| Change a member's personal settings | Their phone, their account |
| Listen to audio not recorded under consent | Recording is per-line and consented; there is nothing to play otherwise |
| Edit a transcript | It is a record. Annotations are appended and attributed |
| Impersonate a member | Same reason as the console |
| Delete audit entries | Not for `owner` |
| Port a number without carrier verification | Number porting is a regulated process; the portal initiates and tracks it, and does not pretend to complete it instantly |
| Configure routing from a phone | Below 768 dp the portal is read-only. A misconfigured transfer target is a business's calls going nowhere |
