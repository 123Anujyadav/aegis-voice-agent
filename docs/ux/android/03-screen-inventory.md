# Android · 3 · Screen Inventory

Every screen on Surface 1, with its identifier, section, presentation, gating
and where its full contract lives.

**Identifiers are stable.** They appear in analytics as `screen_id`, in the
navigation graph, in test names and in bug reports. A screen may be renamed;
its identifier may not.

---

## 3.1 Legend

| Column | Values |
|---|---|
| **Presentation** | `Full` screen · `Sheet` bottom sheet · `Dialog` · `Takeover` full-screen, nav hidden · `System` OS-owned |
| **Nav** | Whether bottom navigation is visible |
| **Gate** | `—` none · `Free` · `Premium` · `Business` · `Role` requires a permission/role |
| **Contract** | Document holding the 13-attribute specification |

---

## 3.2 Onboarding and authentication — A0x

| ID | Screen | Presentation | Nav | Gate | Contract |
|---|---|---|---|---|---|
| `A01` | Welcome | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A02` | Phone number | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A03` | OTP verification | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A04` | Device trust failed | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A05` | SIM selection | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A06` | How screening works | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A07` | Forwarding activation | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A08` | Forwarding verification | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A08e` | Forwarding — manual instructions | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A09a` | Rationale — notifications | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A09b` | Rationale — screening role | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A09c` | Rationale — contacts | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A10` | Contacts sync consent | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A11` | Privacy and consent | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A12` | Assistant setup | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A13` | Test call | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A14` | Ready | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A15` | Re-authentication | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A16` | New device enrolment | Full | — | — | [06](06-screens-onboarding-auth-permissions.md) |

---

## 3.3 Calls — A2x

| ID | Screen | Presentation | Nav | Gate | Contract |
|---|---|---|---|---|---|
| `A20` | Calls feed *(app home)* | Full | Yes | — | [05](05-screens-home-history-search.md) |
| `A21` | **Live Screening** | Takeover | No | — | [04](04-screens-screening-and-calls.md) |
| `A22` | Call Detail | Full | Yes | — | [04](04-screens-screening-and-calls.md) |
| `A22s` | Transcript search | Full | No | — | [04](04-screens-screening-and-calls.md) |
| `A22a` | Audio playback | Inline | Yes | Premium + consent | [04](04-screens-screening-and-calls.md) |
| `A23` | Takeover — connecting | Inline in A21 | No | — | [04](04-screens-screening-and-calls.md) |
| `A24` | Search | Full | No | — | [05](05-screens-home-history-search.md) |
| `A25` | Filters | Sheet | Yes | — | [05](05-screens-home-history-search.md) |
| `A26` | Caller profile | Full | Yes | — | [05](05-screens-home-history-search.md) |
| `A27` | Post-call summary | Sheet | Yes | — | [04](04-screens-screening-and-calls.md) |
| `A28` | Call actions | Sheet | Yes | — | [05](05-screens-home-history-search.md) |
| `A29` | Share / export transcript | Sheet → System | Yes | — | [04](04-screens-screening-and-calls.md) |

---

## 3.4 Protection — A3x

| ID | Screen | Presentation | Nav | Gate | Contract |
|---|---|---|---|---|---|
| `A30` | Protection overview | Full | Yes | — | [05](05-screens-home-history-search.md) |
| `A31` | Fraud list | Full | Yes | — | [05](05-screens-home-history-search.md) |
| `A31d` | Fraud detail — evidence | Full | Yes | Premium | [05](05-screens-home-history-search.md) |
| `A32` | Blocklist | Full | Yes | — | [05](05-screens-home-history-search.md) |
| `A32d` | Blocked number detail | Sheet | Yes | — | [05](05-screens-home-history-search.md) |
| `A33` | Allowlist | Full | Yes | — | [05](05-screens-home-history-search.md) |
| `A34` | Report a number | Sheet | Yes | — | [05](05-screens-home-history-search.md) |
| `A35` | Add number to list | Sheet | Yes | — | [05](05-screens-home-history-search.md) |
| `A36` | Spam list | Full | Yes | — | [05](05-screens-home-history-search.md) |

---

## 3.5 Assistant — A4x

| ID | Screen | Presentation | Nav | Gate | Contract |
|---|---|---|---|---|---|
| `A40` | Assistant — Ask | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A40b` | Assistant — Behaviour | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A41` | Voice | Full | Yes | Premium *(beyond 2 voices)* | [07](07-screens-settings-premium-business.md) |
| `A42` | Instructions | Full | Yes | Premium *(beyond preset)* | [07](07-screens-settings-premium-business.md) |
| `A43` | Language and script | Full | Yes | Premium *(beyond 2)* | [07](07-screens-settings-premium-business.md) |
| `A44` | Bulk action review | Sheet | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A45` | What it may share | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A46` | When to screen | Full | Yes | Premium *(beyond default)* | [07](07-screens-settings-premium-business.md) |
| `A47` | Microphone rationale | Full | No | — | [06](06-screens-onboarding-auth-permissions.md) |

---

## 3.6 Settings — A5x

| ID | Screen | Presentation | Nav | Gate | Contract |
|---|---|---|---|---|---|
| `A50` | Settings root | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A51` | Account | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A51n` | Change number | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A52` | **Forwarding health** | Full | Yes | — | [04](04-screens-screening-and-calls.md) |
| `A53` | Privacy and data | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A53e` | Export my data | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A53d` | Erase my data | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A54` | Notifications | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A55` | Premium and plan | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A56` | Business | Full | Yes | Business | [07](07-screens-settings-premium-business.md) |
| `A57` | Accessibility | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A58` | Appearance | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A59` | Devices and sessions | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A65` | About and legal | Full | Yes | — | [07](07-screens-settings-premium-business.md) |

---

## 3.7 Premium — A6x

| ID | Screen | Presentation | Nav | Gate | Contract |
|---|---|---|---|---|---|
| `A60` | Paywall | Sheet | Overlaid | — | [07](07-screens-settings-premium-business.md) |
| `A61` | Plan comparison | Full | Yes | — | [07](07-screens-settings-premium-business.md) |
| `A62` | Checkout | Full → System | No | — | [07](07-screens-settings-premium-business.md) |
| `A63` | Purchase success | Full | No | — | [07](07-screens-settings-premium-business.md) |
| `A64` | Manage subscription | Full | Yes | Premium | [07](07-screens-settings-premium-business.md) |

---

## 3.8 System and recovery — A7x

| ID | Screen | Presentation | Nav | Gate | Contract |
|---|---|---|---|---|---|
| `A70` | Offline | Persistent chip | Yes | — | [04](04-screens-screening-and-calls.md) |
| `A71` | Forwarding broken | Persistent banner | Yes | — | [04](04-screens-screening-and-calls.md) |
| `A72` | **Emergency handoff** | Takeover, non-dismissible | No | — | [04](04-screens-screening-and-calls.md) |
| `A73` | Session revoked | Full, clears stack | No | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A74` | Permission recovery | Contextual banner | Yes | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A75` | Screening role lost | Blocking banner | Yes | — | [06](06-screens-onboarding-auth-permissions.md) |
| `A76` | App update required | Full, blocking | No | — | [04](04-screens-screening-and-calls.md) |
| `A77` | Service degraded | Contextual banner | Yes | — | [04](04-screens-screening-and-calls.md) |

---

## 3.9 Counts

| Section | Screens |
|---|---:|
| Onboarding and authentication | 19 |
| Calls | 12 |
| Protection | 9 |
| Assistant | 9 |
| Settings | 14 |
| Premium | 5 |
| System and recovery | 8 |
| **Total** | **76** |

Of these, **9 are on the screening hot path** — `A20`, `A21`, `A22`, `A23`,
`A27`, `A52`, `A70`, `A71`, `A72` — and are held to the stricter bar in
[`04`](04-screens-screening-and-calls.md): one-handed, two-second
comprehension, no blocking dialog, and a defined behaviour under every
degradation in [`01 §1.3`](../01-cross-surface-conventions.md).

---

## 3.10 Screens deliberately not built

Recorded so the decision is not re-made by omission.

| Not built | Instead |
|---|---|
| Dialer | The system dialer |
| Contact list / editor | The system contacts app; we read, we do not own |
| Voicemail inbox | Carrier voicemail; the timeline records when a call went there |
| Home dashboard | The Calls feed is the home |
| Notification centre | Notifications are notifications |
| Onboarding checklist (persistent) | Contextual prompts at the point of loss |
| Referral / invite | Not at launch. It is an engagement surface (Principle 6) |
| Rating prompt | Not at launch, and never during or after a screening. Asking for a review while a user is dealing with a fraud call is grotesque |
| Tutorial carousel | The test call (`A13`) demonstrates the product instead of describing it |
| Achievements / stats hero | Anti-pattern ([`00 §0.4`](../00-principles.md)) |
