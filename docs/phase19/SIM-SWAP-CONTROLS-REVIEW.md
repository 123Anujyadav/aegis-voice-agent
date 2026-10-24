# SIM-swap controls: review and policy recommendations

- **Reviewed:**
  - ADR-0010 §10, the SIM-swap controls (C3: "the classic attack against
    phone-number identity");
  - the re-enrolment audit from #122;
  - session housekeeping.
- **Base:** checked against the code on `main` at `c6cecf9`.
- **No product policy is chosen here.** Every policy item below is a
  recommendation for the owner. Nothing was implemented, because no item
  passes the test "the policy is already clear".

## 1. What ADR-0010 §10 requires, and where it stands

| Control | State on `main` |
|---|---|
| Re-enrolment on a new device requires an OTP | **Met.** Every enrolment requires an OTP and a device proof. |
| Re-enrolment is audited | **Partly met** (#122). `identity.device.reenrolled` is written to the append-only log and counted. **Detection forgets after about 25 hours: see §2.** |
| A **cooling-off period** after re-enrolment, before the forwarding configuration can change or transcripts can be read | **Not met.** The operations it would gate do not exist yet: edge-api serves only `/v1/me`. |
| **Notification to the previously enrolled device** over FCM | **Not met.** There is no push integration: `packages/go/integrations` has only an `FcmPushDispatcher` interface and a mock. It needs an FCM project and credentials, which the owner must provide. |
| **Re-verification for elevated-risk operations** (changing the forwarding DID, exporting transcripts, changing consent) | **Not met.** None of these operations exist yet. |
| Audit records with `LEGAL_HOLD` retention | The log is append-only. **But the re-enrolment record cannot be traced to a subscriber after about 25 hours: see §3.** |

## 2. Finding: re-enrolment detection forgets after about 25 hours

**How detection works:**
- At enrolment, `auditReenrolment` asks `OtherDeviceKeyEnrolled`: does this
  subscriber have any other session, revoked or expired, bound to a different
  device key?
- That question can only see **session rows that still exist**.

**Why rows disappear:**
- Sessions have a **fixed 24-hour lifetime**, set at creation. Refresh does
  not extend it (`identity.go`, `ExpiresAt: time.Now().Add(24 * time.Hour)`).
- Housekeeping deletes a session one margin (1 hour) after it expires
  (`pgstore/housekeeping.go`, the "sessions" step).

**The consequence:**
- A subscriber's previous device is remembered for at most about **25 hours
  after their last enrolment**.
- An attacker who swaps the SIM of someone who has not enrolled in the last
  day enrols on a new device, and **nothing flags it as a re-enrolment**. It
  looks like a first enrolment.
- That is exactly when the signal matters most: the victim is not actively
  using the app, so they are least likely to notice anything else.

**Why this was not fixed here:** fixing it means remembering which device keys
a subscriber has enrolled, for some period longer than a session. That
period is a data-retention and privacy decision. A device-key thumbprint is a
persistent pseudonymous device identifier, and India's DPDP Act applies. It
is not an engineering default.

**Options for the owner:**

| Option | What it does | Retention cost | Risk it leaves |
|---|---|---|---|
| A. A per-subscriber device history `identity_subscriber_devices(subscriber_id, jkt, first_enrolled, last_enrolled)`, kept for R days after `last_enrolled` | Detection covers R days of dormancy. The history is pruned by housekeeping at R. | One small row per device per subscriber, for R days | A subscriber who is dormant for more than R days is undetected |
| B. Option A, with R = the longest period the product lets a subscriber stay dormant (for example 90 days, ADR-0010's original session lifetime) | As A | As A | As A, but R is tied to a product rule |
| C. Keep the current behaviour | Detects only within about 25 hours | None | §2 as stated |

**Recommendation:** option A or B, with R chosen by the owner after a privacy
review.

**Implementation once R is decided:**
- forward migration `00NN`;
- one upsert at enrolment;
- `OtherDeviceKeyEnrolled` reads the history instead of sessions;
- one housekeeping step;
- real-PostgreSQL tests, including "detected after the sessions have been
  pruned".

It is a focused change.

## 3. Finding: the re-enrolment record cannot be traced to a subscriber

**The gap:**
- The audit row records the event type, the time and the **session id**.
- The CHECK constraints in `identity_security_audit` (migration 0005) allow
  only `req_` and `sess_` ids. That is deliberate: no number and no
  subscriber id.
- The session row is deleted about 25 hours later (§2).
- After that, a retained `identity.device.reenrolled` record cannot answer
  "which subscriber re-enrolled?".
- ADR-0010 wants these records kept under `LEGAL_HOLD` precisely so that such
  questions can be answered later.

**Options for the owner:**
1. **Allow the subscriber id (`usr_…`) in audit rows.** This needs a
   migration that relaxes the CHECK. It links security events to a
   pseudonymous subscriber for the full retention period, which is a privacy
   decision.
2. **Keep only session ids, and retain the session-to-subscriber link for as
   long as the audit record.** Under §2's option A this comes almost for free:
   the device history can record the session id of each enrolment.
3. **Accept** that these records are only attributable for about 25 hours.

**Recommendation:** decide this together with §2. Option 2 together with §2's
option A adds the least new linkage.

## 4. Policies only the owner can set

| Policy | The question | Recommendation, and why |
|---|---|---|
| **Cooling-off period** | How long after a re-enrolment the sensitive operations stay blocked. Does it also apply to a **first** enrolment of a number that has any history? | **24 to 72 hours, starting at 72.** An attacker who swaps a SIM usually acts within hours. A legitimate user on a new phone can still screen calls during cooling-off; only history access and configuration changes wait. |
| **Scope of cooling-off** | Which operations it blocks | ADR-0010's list: changing the forwarding DID, reading or exporting transcripts, changing consent. **Not** live call screening. |
| **Notifying the prior device** | Which channel (FCM, or SMS to the number, which the attacker now controls and is therefore useless), and whether the prior device can **cancel** the new enrolment | **FCM to every device in the §2 history, with a "this wasn't me" action that revokes the new sessions.** This needs FCM credentials and egress. |
| **What happens to the old device's sessions on re-enrolment** | Revoke them, or keep them | **Keep them during cooling-off.** Revoking them would let an attacker lock the real subscriber out, the reverse of the goal. Let the "this wasn't me" action revoke the *new* device instead. |
| **Re-verification for elevated operations** | Fresh OTP, a device-biometric step, or both | **A fresh OTP plus a fresh DPoP proof from the enrolled key.** The OTP alone is not enough after a SIM swap. This is decided when the operations are built. |
| **Retention R** (§2) and attribution (§3) | How long device history and audit linkage last | Decide after a privacy review |

## 5. What can be built once those are decided

These are focused PRs; none is started:
1. The device history (§2), once R is decided.
2. A re-enrolment state on the session (`cooling_off_until`), set when
   re-enrolment is detected and carried as a claim or role in the access
   token, so edge-api can enforce it without calling identity. **This depends
   on ADR-0029 claim changes**, which go through the same two-step rollout as
   the `client_id` finding in
   [ADR-0029-DEVIATION-ANALYSIS.md](ADR-0029-DEVIATION-ANALYSIS.md) §5.
3. Notification to the prior device, once FCM credentials are provided.
4. Re-verification, when the elevated operations themselves are built.

**External dependencies:**
- FCM (item 3);
- a privacy review (items 1 and §3);
- the elevated operations themselves (items 2 and 4).
