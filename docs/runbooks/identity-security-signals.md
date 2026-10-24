# Runbook: identity and edge security signals

- **Status:**
  - **Nothing here has been exercised.** No alert is deployed, and no
    threshold has been tuned against real traffic. A runbook is not proof that
    an operation works.
  - The thresholds are **starting points** for SRE to tune once there is a
    baseline.
- **Where the metrics come from:** `GET /metrics` on each service's health
  port (ADR-0013), never the application port. Every label comes from a
  closed vocabulary; no label carries an identifier, number, code or token.
- **Play Integrity verifies nothing yet.** No Google verifier is
  configured, so no genuine-app claim is checked. Every session is
  `unverified`, and an attacker holding a valid OTP can still enrol (as
  `unverified`). The integrity signals below describe a missing dependency
  until a verifier is configured.
- **Since #131:** the `identity.integrity.restricted` and
  `identity.integrity.rejected` events and the `not_presented` outcome exist
  (ADR-0031's implementation).
- **Owner:** SRE runs the alerts. Security owns the response to the
  security-classed signals.
- **Hard rules during any response:**
  - Never drop the audit table's append-only trigger.
  - Never disable DPoP or replay checks.
  - Never widen any acceptance window to stop an alert.

  These controls are the reason the signals exist.

## 1. Page: audit records are being lost

| Signal | Alert (starting point) |
|---|---|
| `identity_security_audit_failures_total{type}` | `increase(...[5m]) > 0`. **Page.** |

**What it means:**
- A security event that ADR-0010 §10 requires to be recorded was **not**
  recorded in `identity_security_audit`.
- The `type` label names the event. A failed re-enrolment lookup counts as
  `identity.device.reenrolled`.
- The request itself was not affected, because audit is best-effort by
  design, so **only this metric shows the loss.**

**First response:**
1. **Check PostgreSQL** health and the identity pool. The audit insert shares
   the database with sessions.
2. **Check for SQLSTATE `CSA01`** in the database logs. Something attempted
   an UPDATE, DELETE or TRUNCATE on the audit table. That is a security
   incident, not an outage.
3. **Check for CHECK violations** (`23514`) on `identity_security_audit`.
   They mean a new event type or id shape reached the table without a
   migration, which is a deployment defect.

**Do not:**
- relax the CHECK constraints;
- try to "replay" lost events from logs. The logs deliberately do not carry
  the identifiers.

## 2. Security signals (ticket by default; page on a step change)

| Signal | Starting threshold | What it means | First response |
|---|---|---|---|
| `identity_security_events_total{type="identity.refresh.token_reused"}` | `increase(...[1h]) > 0`, investigate. A step change: **page.** | A superseded refresh token was presented: theft, or a client bug. The session was revoked. | **Check for client bugs first:** a retry that resends an old token right after a rotation. A sustained rise with no client release is the ADR-0010 "exfiltration alarm". Only a token the session actually held counts. A guessed token is refused and is not counted here (`docs/phase19/REFRESH-TOKEN-THREAT-ANALYSIS.md` §3). |
| `…{type="identity.device.reenrolled"}` | Rate over 3× the 7-day baseline | Enrolments on a new device for a subscriber with a recent session: **the SIM-swap signal.** It covers only about 25 h of history (SIM-swap review §2). | Look for a carrier-side SIM-swap wave. There is no automated response yet: cooling-off is an owner decision (O5). |
| `…{type="identity.otp.lockout"}` | Rate over 3× baseline | Repeated wrong codes: brute-force attempts against numbers | Correlate with `identity_auth_requests_total{endpoint="otp_validate",outcome="invalid_otp"}`. Per-source limiting is blocked on the trusted-proxy model (E6): **do not** rate-limit on `X-Forwarded-For`. |
| `…{type="identity.integrity.rejected"}` | `increase(...[1h]) > 0`, investigate | Google rejected a presented integrity token: forged, or another app's (ADR-0031 D31.3). Enrolment was refused. | A sustained rate is an active attack on enrolment. A single burst right after an app release is more likely a client defect. |
| `identity_play_integrity_outcomes_total{outcome="restricted"}` as a share of all verdicts | **> 5% of verdicts** (ADR-0010's own revisit trigger). Count `restricted` only. | Devices failing integrity checks; the reasons are in `identity_play_integrity_restrictions_total{reason}` | Break down by `reason`. **Do not count `unverified`.** That covers outages and devices without Play Services, and ADR-0031 keeps them apart for exactly this reason. |
| `identity_play_integrity_outcomes_total{outcome="not_presented"}` as a share of issuances | Above the expected share of clients without integrity support | Clients are not sending `Integrity-Token` | An Android client defect or an old app version. This is not a security event by itself. |
| `identity_play_integrity_outcomes_total{outcome="unavailable"}` | Rate over 0 once a verifier is configured | Google unreachable, a timeout, or no verifier | **Until a Google verifier exists, every call is `unavailable` (expected).** Afterwards: egress, quota (the default is 10,000 a day), credentials. |

## 3. Availability signals

| Signal | Starting threshold | What it means | First response |
|---|---|---|---|
| `edge_api_auth_requests_total{outcome="unavailable"}` | > 1% of requests over 5 min. **Page.** | edge-api cannot reach the replay store, so every protected request fails closed with 503 (ADR-0029 analysis §3) | Check PostgreSQL. **Do not bypass replay protection** to restore service. |
| `identity_auth_requests_total{outcome="temporarily_unavailable"}` | > 1% over 5 min | identity dependencies are failing (the database or the replay store) | As above |
| `identity_otp_deliveries_total{outcome=~"failed\|timeout"}` | Per provider, over 5% of attempts | The provider is degraded. Failover is carrying traffic. | See the provider onboarding runbook, §3 |

## 4. What these signals cannot tell you

- **Who:** by design, no metric names a subscriber, a session or a number.
  The audit table holds `req_` and `sess_` ids only, and a session id cannot
  be traced back to a subscriber once housekeeping prunes the session (about
  25 h; SIM-swap review §3).
- **Whether a verdict is real:** there is no Google verifier in this
  repository yet. Until one is configured, integrity signals reflect a
  missing dependency, not device health.
- **Whether a refresh-token reuse was theft:** the counter cannot distinguish
  theft from a client bug.
