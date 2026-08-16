# Android · 2 · Navigation Graph

Every route, every edge, every back behaviour.

---

## 2.1 Navigation model

**Bottom navigation, four destinations, labels always visible.** Icon-only
navigation fails Voice Access and is ambiguous at a glance
([`09 §9.6`](../../design/09-components.md)).

Each tab owns an independent back stack. Switching tabs preserves the other
tabs' stacks. Re-selecting the active tab pops its stack to root; re-selecting
at root scrolls to top; at top it is a no-op — never a bounce.

```
┌──────────────────────────────────────────────────────────┐
│                                                          │
│                    content surface                       │
│                                                          │
├──────────────────────────────────────────────────────────┤
│    ☰            ⛨             ⬡             ⚙            │
│  Calls     Protection    Assistant     Settings          │
└──────────────────────────────────────────────────────────┘
```

Bottom nav is **hidden** on: Live Screening, Search, Onboarding, Emergency
handoff, Paywall (a sheet, so it overlays it), and any full-screen blocking
error. It is **present** everywhere else, including all detail screens — a
detail screen that hides the nav strands the user in a place with only one exit.

---

## 2.2 The graph

```
                            ┌─────────────┐
                            │ COLD START  │
                            └──────┬──────┘
                                   │
                    ┌──────────────┴──────────────┐
                    │                             │
             no account                     account + device trusted
                    │                             │
                    ▼                             ▼
          ┌──────────────────┐          ┌──────────────────┐
          │   ONBOARDING     │          │   CALLS (root)   │◀──── default
          │   (linear)       │─────────▶│                  │
          └──────────────────┘          └──────────────────┘
                    ▲                             │
                    │                             │
            device trust failed ──────────────────┘
```

### Onboarding — linear, one-way, exits permanently

```
 A01 Welcome
   │
   ▼
 A02 Phone number ──▶ A03 OTP ──▶ [device trust, invisible] ──▶ A04 Trust failed ⟲
   │                    │                                            │
   │                    └─ change number ──┘                         └─ support
   ▼
 A05 SIM selection        (dual-SIM only; skipped otherwise)
   │
   ▼
 A06 How screening works  (carrier explainer + billing disclosure)
   │
   ▼
 A09a Notifications rationale ──▶ [system dialog] ──▶ (denied: continue)
   │
   ▼
 A09b Screening role rationale ──▶ [system role request] ──▶ (denied: continue, degraded)
   │
   ▼
 A07 Forwarding activation ──▶ [system dialer, MMI] ──▶ A08 Verification
   │                                                        │
   │                                          ┌─────────────┴─────────────┐
   │                                     verified                    not verified
   │                                          │                           │
   │                                          │                           ▼
   │                                          │                  A08e Manual instructions
   │                                          │                     (carrier-specific)
   │                                          │                           │
   │                                          ◀───────────────────────────┘
   ▼                                          ▼
 A09c Contacts rationale ──▶ [system dialog] ──▶ A10 Contacts sync consent
   │
   ▼
 A11 Privacy & consent    (announcement · recording · retention · analytics — separate controls)
   │
   ▼
 A12 Assistant setup      (what to call you · language · voice · greeting style)
   │
   ▼
 A13 Test call            (skippable)
   │
   ▼
 A14 Ready ──────────────▶ CALLS   [onboarding graph destroyed; unreachable by back]
```

**Back within onboarding** goes to the previous step, except after A08
verification succeeds — forwarding is now live on the carrier and stepping back
into the activation flow would re-dial. From A09c onward, back is disabled to
the pre-A08 steps; the top bar offers **Exit setup**, which warns that screening
is active but setup is incomplete, and drops the user into Calls.

**Back on A01/A02** exits the app.

### Calls tab

```
 CALLS (root)
   │
   ├──▶ A24 Search ──────────────▶ A22 Call Detail
   │      (full-screen, no nav)
   │
   ├──▶ A25 Filters (sheet)
   │
   ├──▶ A22 Call Detail
   │      ├──▶ A26 Caller Profile ──▶ A22 (other call from same number)
   │      ├──▶ A22s Transcript search (in-screen)
   │      ├──▶ share sheet (system)
   │      └──▶ A34 Report (sheet)
   │
   ├──▶ A26 Caller Profile
   │
   └──▶ A21 LIVE SCREENING  ◀── also from notification, also from any tab's chip
          │
          ├──▶ takeover ──▶ [system in-call UI] ──▶ A22 Call Detail
          ├──▶ decline   ──▶ A27 Post-call summary (sheet) ──▶ CALLS
          └──▶ ends      ──▶ A27 Post-call summary (sheet) ──▶ CALLS
```

### Protection tab

```
 A30 Protection (root)
   ├──▶ A31 Fraud list ──▶ A31d Fraud detail ──▶ A22 Call Detail (deep-linked turn)
   ├──▶ A36 Spam list  ──▶ A22 Call Detail
   ├──▶ A32 Blocklist  ──▶ A32d Blocked number detail
   ├──▶ A33 Allowlist
   ├──▶ A34 Report a number (sheet)
   └──▶ A24 Search (scoped to protection)
```

### Assistant tab

```
 A40 Assistant (root)  — segmented: [ Ask ] [ Behaviour ]
   │
   ├── Ask (default)
   │     ├──▶ A22 Call Detail       (from a citation)
   │     └──▶ A44 Bulk action review (sheet)
   │
   └── Behaviour
         ├──▶ A41 Voice ──▶ preview playback
         ├──▶ A42 Instructions
         ├──▶ A43 Language & script
         ├──▶ A45 What it may share   (allow-list)
         ├──▶ A46 When to screen      (availability rules)
         └──▶ A13 Test call
```

### Settings tab

```
 A50 Settings (root)
   ├──▶ A51 Account ──▶ A59 Devices ──▶ revoke (dialog)
   │                 └──▶ A51n Change number (flow)
   ├──▶ A52 Forwarding ──▶ A07 Re-activation flow
   │                    └──▶ A05 Change SIM
   ├──▶ A53 Privacy & data ──▶ A53e Export
   │                        └──▶ A53d Erase my data (flow, high friction)
   ├──▶ A54 Notifications ──▶ [system channel settings]
   ├──▶ A55 Premium ──▶ A61 Plans ──▶ A62 Checkout ──▶ A63 Success
   │                 └──▶ A64 Manage subscription
   ├──▶ A56 Business        (only if organisation member)
   ├──▶ A57 Accessibility
   ├──▶ A58 Appearance
   └──▶ A65 About ──▶ legal · grievance officer · support · open-source notices
```

---

## 2.3 Interrupt routes

Routes that can fire from anywhere and are not part of any tab's stack.

| Route | Trigger | Presentation | On exit |
|---|---|---|---|
| **A21 Live Screening** | Screening begins; notification tap; persistent chip | Full-screen takeover, shared-Z, `long`/`emphasized`. Bottom nav hidden | Returns to the exact prior surface and scroll position |
| **A72 Emergency handoff** | Emergency intent detected during a screening | Full-screen, **overrides Live Screening**, cannot be dismissed by back | Explicit action only |
| **A71 Forwarding broken** | Health check fails | Persistent banner on every root; tapping opens A52 | Banner persists until resolved |
| **A70 Offline** | Connectivity lost | Persistent chip below the top app bar | Chip clears on reconnect |
| **A73 Session revoked** | Auth revoked or integrity failure | Full-screen blocking, clears the entire back stack | Re-auth only |
| **A60 Paywall** | Gated feature tapped | Modal sheet over the current surface | Dismiss returns, unchanged |

### Precedence

When more than one interrupt is eligible, exactly one shows:

```
A72 Emergency  >  A73 Session revoked  >  A21 Live Screening
   >  A71 Forwarding broken  >  A70 Offline  >  A60 Paywall
```

**Emergency outranks everything, including a revoked session.** If we detect an
emergency we hand over the dialer even to a user we are in the middle of signing
out, because the alternative is worse in a way no product concern outweighs.

---

## 2.4 Back behaviour

Every route has one defined back behaviour. Ambiguity here is how users get
lost.

| From | Back goes to |
|---|---|
| Any tab root | **Exits the app.** Never cycles to Calls first — a back button that changes tabs is disorienting |
| Any detail screen | Its parent in the graph, preserving the parent's scroll position |
| Call Detail entered from Search | **Search**, with the query and results intact |
| Call Detail entered from a notification | **Calls root** — there is no stack behind a notification, and inventing one is worse than a shallow exit |
| Search | The surface that opened it, scroll preserved |
| Live Screening | The surface the user was on. Screening continues; the persistent chip appears |
| Live Screening entered from a notification, app was closed | Calls root. The chip is present |
| Emergency handoff | **Back is disabled.** Exit is by explicit action |
| Onboarding, step N | Step N−1, subject to [§2.2](#22-the-graph) |
| Onboarding, first step | Exits the app |
| Any sheet | Dismisses the sheet |
| Blocking error | Retry or an explicit escape, both labelled. Back exits the app only where there is genuinely nothing behind |
| Checkout, mid-payment | **Confirmation dialog.** The one place a back press is confirmed, because an abandoned payment can leave a real charge in an ambiguous state |

**Predictive back is supported on API 33+** for every non-blocking route. Routes
where back is disabled or confirmed opt out explicitly rather than by omission.

---

## 2.5 Deep links

| Link | Target | Auth required | Notes |
|---|---|---|---|
| `callscreen://calls` | Calls root | Yes | |
| `callscreen://call/{opaqueId}` | Call Detail | Yes | Opaque ID only — never a phone number in a link ([`01 §1.11`](../01-cross-surface-conventions.md)) |
| `callscreen://screening/live` | Live Screening | Yes | No-ops to Calls if no screening is active |
| `callscreen://protection` | Protection root | Yes | |
| `callscreen://forwarding` | A52 Forwarding | Yes | The support link. Most-used deep link in the product |
| `callscreen://settings/privacy` | A53 | Yes | Required by policy pages |
| `callscreen://premium` | A55, or Paywall if free | Yes | |
| `callscreen://support` | A65 About → support | Yes | |
| App links from the web | Their equivalents | Yes | Unauthenticated deep links land on Welcome and **do not** preserve the target — a preserved target is a phishing lever |

**No deep link carries user content**, and no deep link performs an action.
A link navigates; the user acts. A `callscreen://block/{number}` link would be a
one-tap denial-of-service on someone's contacts.

---

## 2.6 Navigation state preservation

| State | Preserved across | Restored |
|---|---|---|
| Tab back stacks | Tab switches, backgrounding | Yes |
| Scroll position, per list | Navigation away and back | Yes |
| Search query and results | Back from a result | Yes |
| Filter selection | Session; cleared on cold start | Session only — a filter silently persisting across days makes a full feed look empty |
| Transcript scroll | Backgrounding during a live screening | Yes, including the "not at bottom" state |
| Onboarding progress | Process death | Yes, up to the last completed step. Never mid-step |
| Assistant conversation | Backgrounding | Yes, for the session. Not across cold start unless pinned |
| Checkout | Process death | **No.** Restart the flow — a resumed payment state is a source of double charges |

**Process death is treated as backgrounding**, not as a fresh start, everywhere
except checkout. Aggressive OEM process killing is normal on the target devices
(ADR-0002 §15), and a user who loses their place every time they check a message
will conclude the app is broken.
