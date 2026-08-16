# 17 · Billing

**Subdomain:** Generic · **Prefix:** `BI` · **Topic domain:** `billing`

---

## 17.1 Purpose

Decide what a Subscriber or Organisation is entitled to, meter what they use,
and take money for it — without ever holding a payment instrument and without
ever gating a risk verdict.

## 17.2 Responsibilities

**Owns**

- `Subscription`, `Plan` and the `Entitlement` set — **the single authority on
  "may they do X"**
- `UsageRecord` — metered consumption, reconcilable against the invoice
- `Invoice`, `PaymentAttempt`, `Credit` — the financial record
- Grace periods and the degradation ladder
- `PaymentMethodReference` — a provider token, never an instrument

**Does not own**

| Not owned | Owned by |
|---|---|
| Who the Subscriber is | Identity |
| What they configured | Consumer, Business |
| Whether a call was screened | Telephony |
| Payment instrument data | **The payment provider. It never enters our domain** |
| The organisation's tax identity | Business holds it; Billing references it |

> **Entitlements are derived from the `Subscription`, never stored on the
> `Subscriber`** (INV-BI-5). Two sources of truth for "what can this person do"
> is how a cancelled plan keeps working for months.

---

## 17.3 Domain Entities

### `Subscription` — aggregate root

**Attributes**

```
id                : SubscriptionId              INTERNAL · STANDARD
holderRef         : SubscriberId | OrganisationId  INTERNAL · STANDARD
planId            : PlanId <ref>                INTERNAL · STANDARD
entitlements      : Entitlement[] <owned>
state             : SubscriptionState           PUBLIC   · STANDARD
periodStart       : Instant                     INTERNAL · LEGAL_HOLD
periodEnd         : Instant                     INTERNAL · LEGAL_HOLD
paymentMethodRef  : PaymentMethodReference?     INTERNAL · STANDARD
graceEndsAt       : Instant?                    PUBLIC   · STANDARD
cancelAt          : Instant?                    PUBLIC   · STANDARD
cancelledAt       : Instant?                    INTERNAL · LEGAL_HOLD
seats             : Int                         INTERNAL · STANDARD
```

**Relationships** — References a `Subscriber` or an `Organisation` by identity,
and a `Plan`. Owns its `Entitlement` set by containment, so a plan change and its
entitlement consequences are one transaction.

**Lifecycle** — Created on plan selection or organisation creation (free tier is
a real `Subscription`, not the absence of one — modelling "no subscription" as a
state creates a second code path that inevitably diverges). Moves through
`PAST_DUE` and `GRACE` on payment failure. Cancellation takes effect **at period
end**, never immediately — they paid for the period.

**Validation Rules** — Exactly one active `Plan` (INV-BI-2). `seats` ≥ active
`Membership` count for a business subscription; a downgrade below current usage
must state what stops before confirming (P-BI-5). `entitlements` must always
contain `RiskVerdictVisible` (INV-BI-6) — a subscription that could omit it
cannot be constructed.

**Privacy Classification** — `INTERNAL`. Period boundaries are `LEGAL_HOLD` —
they are the basis of an invoice and survive erasure of other personal data.

**Audit Requirements** — **Change** level on every state transition, plan change
and entitlement change.

---

### `Plan` — reference entity

```
id            : PlanId              PUBLIC · STANDARD
tier          : PlanTier            PUBLIC · STANDARD
displayName   : String              PUBLIC · STANDARD
price         : Money               PUBLIC · STANDARD
period        : BillingPeriod       PUBLIC · STANDARD
entitlements  : Entitlement[]       PUBLIC · STANDARD
quotas        : UsageQuota[]        PUBLIC · STANDARD
status        : PlanStatus          PUBLIC · STANDARD
```

**Lifecycle** — Published, grandfathered, retired. **A retired plan continues to
serve its existing subscribers** — retiring a plan someone is on and silently
migrating them is how a price change becomes a scandal.

**Validation Rules** — Every plan, including `FREE`, includes
`RiskVerdictVisible`. A plan definition omitting it is rejected at publication.

**Privacy Classification** — `PUBLIC`. Catalogue data.

---

### `UsageRecord`

```
id            : UsageRecordId          INTERNAL · LEGAL_HOLD
subscriptionId: SubscriptionId <ref>   INTERNAL · LEGAL_HOLD
lineId        : LineId <ref>           INTERNAL · LEGAL_HOLD
metric        : UsageMetric            PUBLIC   · LEGAL_HOLD
quantity      : Quantity               INTERNAL · LEGAL_HOLD
period        : BillingPeriodRef       INTERNAL · LEGAL_HOLD
sourceEventId : EventId                INTERNAL · LEGAL_HOLD
recordedAt    : Instant                INTERNAL · LEGAL_HOLD
```

**Relationships** — References the `Subscription` and the `Line` that generated
it. `sourceEventId` points at the `telephony.call.ended.v1` that produced it,
which is what makes the invoice reconcilable line by line.

**Lifecycle** — Recorded per call, aggregated per period, frozen when the period
closes. **Never edited after freeze** — a correction is a `Credit`.

**Validation Rules** — Idempotent on `sourceEventId`: a replayed event must not
double-bill. This matters because Kafka replay is a first-class recovery
mechanism (ADR-0009 §6), and a billing consumer that is not idempotent turns a
recovery into a charge.

**Privacy Classification** — `LEGAL_HOLD`. Usage is the basis of a financial
record and survives erasure.

**Audit Requirements** — **Change** level. Every adjustment is attributed.

---

### `Invoice` — aggregate root

```
id            : InvoiceId              INTERNAL · LEGAL_HOLD
subscriptionId: SubscriptionId <ref>   INTERNAL · LEGAL_HOLD
holderRef     : SubscriberId | OrganisationId  INTERNAL · LEGAL_HOLD
lineItems     : InvoiceLineItem[] <owned>
subtotal      : Money                  INTERNAL · LEGAL_HOLD
tax           : Money                  INTERNAL · LEGAL_HOLD
total         : Money                  INTERNAL · LEGAL_HOLD
taxIdentity   : TaxIdentity?           PERSONAL · LEGAL_HOLD
state         : InvoiceState           PUBLIC   · LEGAL_HOLD
issuedAt      : Instant                INTERNAL · LEGAL_HOLD
dueAt         : Instant                INTERNAL · LEGAL_HOLD
```

**Lifecycle** — Issued at period close. **Immutable once issued** (INV-BI-4) —
a correction is a credit note referencing it, never an edit. Retained under
`LEGAL_HOLD` beyond any erasure request, and the erasure flow states this as a
named exception rather than silently keeping it.

**Validation Rules** — `total = subtotal + tax`, computed in exact minor units.
Line items must reconcile exactly with `UsageRecord`s for the period — an
invoice a customer cannot reconcile is a support ticket waiting to happen.

**Privacy Classification** — `LEGAL_HOLD`. `taxIdentity` is `PERSONAL`
(GSTIN plus legal name and address).

**Audit Requirements** — **Change** on issue and on every credit note.

---

### `PaymentAttempt`

```
id            : PaymentAttemptId       INTERNAL · LEGAL_HOLD
invoiceId     : InvoiceId <ref>        INTERNAL · LEGAL_HOLD
providerRef   : ProviderTransactionRef INTERNAL · LEGAL_HOLD
amount        : Money                  INTERNAL · LEGAL_HOLD
outcome       : PaymentOutcome         PUBLIC   · LEGAL_HOLD
failureReason : PaymentFailureReason?  PUBLIC   · LEGAL_HOLD
attemptedAt   : Instant                INTERNAL · LEGAL_HOLD
```

**Validation Rules** — `failureReason` is a **domain enumeration**, not a
provider string. Provider messages are translated by the ACL, because "an issuer
declined this card" and `ERR_5023` are different products.

**Privacy Classification** — `LEGAL_HOLD`. **No instrument data at any
classification** — there is no field here that could hold one.

---

### `PaymentMethodReference`

```
id           : PaymentMethodId       INTERNAL · STANDARD
providerToken: ProviderToken         SECRET   · STANDARD
brandHint    : String?               PUBLIC   · STANDARD    ── "•••• 4242"
expiryHint   : YearMonth?            PUBLIC   · STANDARD
addedAt      : Instant               INTERNAL · STANDARD
```

**The entity exists to make the absence explicit.** There is no card number, no
CVV, no bank account, no UPI handle — only an opaque provider token and the two
hints needed to let a human recognise which method they are looking at.

**Privacy Classification** — `providerToken` is `SECRET`. Hints are `PUBLIC`
because they are already designed to be non-identifying.

**Audit Requirements** — **Change** on addition and removal.

---

### `Credit`

```
id            : CreditId               INTERNAL · LEGAL_HOLD
holderRef     : SubscriberId | OrganisationId  INTERNAL · LEGAL_HOLD
amount        : Money                  INTERNAL · LEGAL_HOLD
reason        : CreditReason           PUBLIC   · LEGAL_HOLD
issuedBy      : OperatorId? <ref>      INTERNAL · LEGAL_HOLD
invoiceRef    : InvoiceId? <ref>       INTERNAL · LEGAL_HOLD
issuedAt      : Instant                INTERNAL · LEGAL_HOLD
```

**Lifecycle** — Issued by an operator with `support_lead`, or automatically for
a service failure. Applied to the next invoice. Never reversed — a mistaken
credit is corrected by a debit adjustment, keeping the record additive.

---

## 17.4 Value Objects

| Value object | Definition | Notes |
|---|---|---|
| `Money` | currency + `amount_minor` as an integer | Shared kernel, frozen in `types.proto`. **Never a float** — rounding drift is both a correctness bug and an audit finding |
| `PlanTier` | `FREE` · `PREMIUM` · `BUSINESS` | |
| **`Entitlement`** | capability + optional limit | The published language every context consumes |
| `EntitlementCapability` | `RiskVerdictVisible` · `FraudEvidenceDetail` · `CallRecording` · `ExtendedRetention` · `MultiLanguage` · `PremiumVoice` · `CustomInstructions` · `AvailabilitySchedule` · `UnlimitedScreening` · `ApiAccess` · `TeamSeats` | |
| `UsageQuota` | metric + limit + overageRate? | |
| `UsageMetric` | `SCREENED_MINUTES` · `CALLS_HANDLED` · `RECORDING_MINUTES` · `API_CALLS` | |
| `BillingPeriod` | `MONTHLY` · `ANNUAL` | |
| `GracePeriod` | duration + degradation ladder | Stated in **days**, never vaguely |
| `PaymentFailureReason` | `INSUFFICIENT_FUNDS` · `CARD_EXPIRED` · `ISSUER_DECLINED` · `AUTHENTICATION_REQUIRED` · `PROVIDER_ERROR` | Domain enum, translated by the ACL |
| `ProrationAmount` | Money + basis + period fraction | Shown as a number before confirming |
| `SubscriptionState` | See [§17.13](#1713-state-machines) | |

### `RiskVerdictVisible` is an entitlement that cannot be removed

It is modelled as an entitlement rather than as an unconditional behaviour so
that its universality is **visible in the domain** — every plan's entitlement
list shows it, and a plan definition omitting it is rejected at publication
(INV-BI-6).

Modelling it as "not an entitlement at all" would have been simpler and would
have hidden the commitment. This way, anyone reading a plan can see that safety
is not for sale.

---

## 17.5 Aggregates

| Aggregate | Root | Contains | Consistency boundary |
|---|---|---|---|
| **Subscription** | `Subscription` | `Entitlement[]` | Plan change and entitlement change must be atomic |
| **Invoice** | `Invoice` | `InvoiceLineItem[]`, `PaymentAttempt[]` | An invoice and its line items must be consistent |
| **UsageRecord** | `UsageRecord` | — | Idempotent on `sourceEventId` |
| **Plan** | `Plan` | `Entitlement[]`, `UsageQuota[]` | Reference data |
| **Credit** | `Credit` | — | |

```
┌────────────────────────────────────────────────┐
│  Subscription  «aggregate root»                │
│   holderRef · planId · state · graceEndsAt     │
│   ┌──────────────────────────────────────────┐ │
│   │ Entitlement[]                            │ │
│   │  RiskVerdictVisible ◀── ALWAYS PRESENT   │ │
│   │                        on EVERY plan,    │ │
│   │                        including FREE.   │ │
│   │                        INV-BI-6          │ │
│   └──────────────────────────────────────────┘ │
└─────────────────┬──────────────────────────────┘
                  │ published as OPEN HOST SERVICE
                  ▼        to every other context
   ┌──────────────────────────────────────────┐
   │ billing.entitlement.changed.v1           │
   └──────────────────────────────────────────┘

┌──────────────────┐   ┌────────────────────────────────┐
│ UsageRecord «root│──▶│  Invoice  «root»   IMMUTABLE   │
│ idempotent on    │   │   lineItems reconcile EXACTLY  │
│ sourceEventId    │   │   with UsageRecords            │
│ (Kafka replay!)  │   │   ┌──────────────────────────┐ │
└──────────────────┘   │   │ PaymentAttempt[]         │ │
                       │   │  domain failure reasons, │ │
                       │   │  never provider strings  │ │
                       │   └──────────────────────────┘ │
                       └────────────────────────────────┘
                                    ▲
                            correction = Credit,
                            never an edit
   ┌─────────────────────────────────────────────────┐
   │ PaymentMethodReference                          │
   │  providerToken (SECRET) + "•••• 4242"           │
   │  NO INSTRUMENT DATA EXISTS IN THIS DOMAIN       │
   └─────────────────────────────────────────────────┘
```

---

## 17.6 Domain Services

| Service | Responsibility | Notes |
|---|---|---|
| **`EntitlementResolutionService`** | Answer "may this holder do X, right now" | **The single authority.** No other context re-derives entitlement logic |
| `UsageMeteringService` | Convert call events into `UsageRecord`s | Idempotent on `sourceEventId` |
| `ProrationService` | Compute the exact amount for a mid-period change | Shown before confirming, never after |
| `DunningService` | Drive the grace period and the degradation ladder | Screening continues during grace |
| `QuotaProjectionService` | Project when a quota will be exhausted | "You'll reach your limit around the 24th" — a limit should never be discovered from a failure |
| `InvoiceReconciliationService` | Prove the invoice equals the usage | An unreconcilable invoice is a churn event |

---

## 17.7 Repositories

`SubscriptionRepository` · `InvoiceRepository` · `UsageRecordRepository` ·
`PlanRepository` · `CreditRepository`

---

## 17.8 Domain Events

| Event | Payload | Consumers |
|---|---|---|
| `billing.subscription.activated.v1` | subscriptionId, holderRef, planId, tier | Every context |
| `billing.subscription.plan_changed.v1` | subscriptionId, from, to, direction, effectiveAt | Every context |
| `billing.subscription.cancelled.v1` | subscriptionId, effectiveAt, tenureDays | Consumer, Telephony, Notifications |
| `billing.subscription.lapsed.v1` | subscriptionId, holderRef | Telephony, Consumer, Notifications |
| **`billing.entitlement.changed.v1`** | holderRef, added[], removed[] | **Every context.** The published language |
| `billing.usage.recorded.v1` | subscriptionId, lineId, metric, quantity, periodRef | Analytics |
| `billing.quota.threshold_crossed.v1` | subscriptionId, metric, pct, projectedExhaustion | **Notifications**, Business |
| `billing.quota.exceeded.v1` | subscriptionId, metric | **Telephony**, Notifications, Consumer |
| `billing.payment.succeeded.v1` | subscriptionId, invoiceId | Notifications |
| `billing.payment.failed.v1` | subscriptionId, reason, graceEndsAt | **Notifications**, Consumer |
| `billing.invoice.issued.v1` | invoiceId, holderRef, total | Notifications, Business |
| `billing.credit.issued.v1` | creditId, holderRef, reason | Administration, Notifications |

**No event carries a `Money` amount for a person's balance beyond the invoice
total, and none carries a payment token.** `billing.entitlement.changed.v1`
carries capability names, which are `PUBLIC` — that is what makes it safe to be
the most widely-consumed event in the platform.

---

## 17.9 Commands

| Command | Refused when |
|---|---|
| `StartSubscription(holderRef, planId, paymentMethodRef?)` | Plan retired; holder already subscribed |
| `ChangePlan(subscriptionId, planId)` | Downgrade below current usage without the consequence being stated and accepted |
| `CancelSubscription(subscriptionId)` | Already cancelling. **Takes effect at period end** |
| `ReactivateSubscription(subscriptionId)` | Past the reactivation window |
| `RecordUsage(sourceEventId, subscriptionId, lineId, metric, quantity)` | **Duplicate `sourceEventId` — silently idempotent, never double-billed** |
| `IssueInvoice(subscriptionId, period)` | Period not closed; usage not reconciled |
| `RetryPayment(invoiceId)` | Outside the retry schedule |
| `ApplyCredit(holderRef, amount, reason)` | Operator lacks `support_lead` |
| `AddPaymentMethod(holderRef, providerToken)` | — (token only; no instrument fields exist to supply) |
| `EnterGracePeriod(subscriptionId)` | Already in grace |

---

## 17.10 Queries

| Query | Scope |
|---|---|
| **`GetEntitlements(holderRef)`** | The hot-path authority. Cached by consumers; **fails closed on premium, open on `RiskVerdictVisible`** |
| `GetSubscription(holderRef)` | Holder; `OWNER`/`BILLING` for an organisation |
| `GetUsage(subscriptionId, period)` | Holder. Must reconcile with the invoice |
| `ProjectQuotaExhaustion(subscriptionId, metric)` | Holder |
| `ListInvoices(holderRef)` | Holder, `BILLING` |
| `GetPlanComparison(currentPlanId)` | Public |

---

## 17.11 Policies

| # | Policy |
|---|---|
| **P-BI-1** | **No entitlement gates a risk verdict.** `RiskVerdictVisible` is held by every plan, permanently. We charge for depth, never for safety |
| **P-BI-2** | A failed payment enters a grace period stated in days. **Screening continues during grace** — cutting a business's call screening over a failed card is disproportionate |
| **P-BI-3** | Quota exhaustion degrades to ring-through, **never to a dropped call** |
| **P-BI-4** | Cancellation takes effect at period end, not immediately |
| **P-BI-5** | A downgrade below current usage states exactly what stops, before confirming |
| **P-BI-6** | Invoices and usage are `LEGAL_HOLD` and survive erasure, and the erasure flow names them as exceptions |
| **P-BI-7** | Usage recording is idempotent on `sourceEventId`. A Kafka replay must never produce a charge |
| **P-BI-8** | A retired plan continues to serve its existing subscribers |
| **P-BI-9** | At 80% of a quota, project the exhaustion date. A limit is never discovered from a failure |
| **P-BI-10** | One retention offer on cancellation. Exactly one |

---

## 17.12 Invariants

| # | Invariant | Source |
|---|---|---|
| **INV-BI-1** | `Money` is exact minor units in an integer. No floating point exists in this context | `types.proto` |
| **INV-BI-2** | A `Subscription` has exactly one active `Plan` | |
| **INV-BI-3** | No payment instrument data exists in this domain. Only a provider token and non-identifying hints | PCI posture |
| **INV-BI-4** | An `Invoice` is immutable once issued. Corrections are credit notes | |
| **INV-BI-5** | Entitlements derive from the `Subscription` and are never stored on the `Subscriber` | |
| **INV-BI-6** | **`RiskVerdictVisible` is an entitlement of every plan including `FREE`, and cannot be removed.** A plan definition omitting it is rejected at publication | Product commitment |
| **INV-BI-7** | `UsageRecord` is idempotent on `sourceEventId` | ADR-0009 §6 |
| **INV-BI-8** | Invoice line items reconcile exactly with the period's `UsageRecord`s | |
| **INV-BI-9** | The `BILLING` organisation role can never reach call content through this context | |
| **INV-BI-10** | No entitlement can disable fraud scoring or the safety layer | **I11** |

---

## 17.13 State Machines

### `Subscription`

```
   (none) ──start──▶ TRIALING ──▶ ACTIVE ◀────────── payment succeeds ──┐
                         │           │                                  │
                    trial ends       │ payment fails                    │
                    no method        ▼                                  │
                         └────▶  PAST_DUE ──retry window elapses──▶ GRACE
                                                                        │
                                    SCREENING CONTINUES THROUGHOUT ─────┘
                                         PAST_DUE and GRACE
                                                     │
                                            grace elapses
                                                     ▼
                                                  LAPSED
                                                     │
                                        reverts to FREE entitlements —
                                        NOT to no entitlements.
                                        RiskVerdictVisible persists.
                                                     │
   ACTIVE ──cancel──▶ PENDING_CANCELLATION ──period end──▶ CANCELLED «terminal»
                              │
                        reactivate within window
                              ▼
                           ACTIVE
```

**`LAPSED` reverts to the free tier, not to nothing.** A lapsed subscriber still
gets screening, transcripts, the blocklist, and — always —
`RiskVerdictVisible`.

### `Invoice`

```
  DRAFT ──period closes, usage reconciled──▶ ISSUED ──payment──▶ PAID «terminal»
                                                │
                                     retries exhausted
                                                ▼
                                            OVERDUE ──write off──▶ VOID «terminal»
                                                │
                                          credit note
                                                ▼
                                            CREDITED
                                     (the invoice itself is
                                      never edited — INV-BI-4)
```

### `UsageRecord`

```
  RECORDED ──period closes──▶ FROZEN «terminal»
      │
   duplicate sourceEventId ──▶ IGNORED (idempotent, no charge)
```

---

## 17.14 Ownership

| Aspect | Value |
|---|---|
| Team | `callscreen/billing` |
| Service | `billing` (Go) — a **critical module**, ≥ 90% coverage gate |
| Durable store | `billing` Aurora, schema `billing` |
| CODEOWNERS | `docs/domain/17-billing.md`, `services/go/billing/**` |

---

## 17.15 External Dependencies

| Dependency | Purpose | Guard |
|---|---|---|
| **Payment provider** | Charging, tokenisation, hosted fields | **ACL.** Provider-hosted fields throughout. **No instrument data ever enters our domain.** Provider error strings are translated to domain `PaymentFailureReason`s |
| Telephony context | `telephony.call.ended.v1` for metering | Idempotent consumption on `sourceEventId` |
| Business context | Organisation tax identity, seat counts | Conformist |
| Tax and invoicing rules (India, GST) | Invoice composition | Reference data, versioned |

---

## 17.16 Security Constraints

| # | Constraint |
|---|---|
| 1 | **No payment instrument data exists in this domain.** There is no field capable of holding one (INV-BI-3) |
| 2 | **`providerToken` is `SECRET`** — never logged, never in an error, never in a crash report |
| 3 | **Invoices and usage are `LEGAL_HOLD`** and survive erasure. The erasure flow names them explicitly rather than silently retaining them |
| 4 | **Usage metering is idempotent**, so Kafka replay — a first-class recovery mechanism — can never produce a charge |
| 5 | **The `BILLING` organisation role reaches no call content** through any query in this context |
| 6 | **`RiskVerdictVisible` cannot be removed by any command, flag, plan definition or operator action** |
| 7 | **No entitlement can disable fraud scoring or the safety layer** (I11) |
| 8 | **Credits are attributed to an operator** and appear in the access review |
| 9 | **Money is exact.** No floating point exists in this context |
| 10 | **Provider webhooks are signature-verified** at the ACL before any domain state changes |
