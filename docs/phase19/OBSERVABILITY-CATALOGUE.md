# Observability catalogue: what the running services expose today (2026-11-10)

- **As of:** `main` `5fb7b33`. Every name below is taken from the code (file and line given). This document describes existing behaviour only.
- **What it does not decide:**
  - the tracing backend (ADR-0028 is Proposed and conditional on P1 and P2);
  - where alert or SLO rules live (ADR-0013 makes them operator artefacts);
  - any threshold.
  - **E5 (SRE) is still open.**
- **Scope:** the three Go services with a running binary and instruments: **identity**, **edge-api** and **telephony-gateway**. The other services are scaffolds, with the platform health endpoints only.
- **Keeping it current:** a change to a metric, a label vocabulary, a span or a health check updates this file in the same PR.

## 1. Endpoints every Go service serves (`packages/go/platform`)

| Endpoint | Listener | Behaviour | Source |
|---|---|---|---|
| `GET /healthz` | health port (8081 by default) | Liveness | `platform/server.go:273` |
| `GET /readyz` | health port | Readiness: 200 when every **critical** check passes ("degraded" if a non-critical one fails), 503 otherwise. Each check reports its name, status, duration and a fixed message, never error detail | `platform/server.go:274` |
| `GET /metrics` | health port, **never** the application port | Prometheus text format (ADR-0013), served when the service sets a metrics handler | `platform` `SetMetricsHandler`; `metricsexport` |

## 2. Metrics

Every name is subsystem-prefixed (ADR-0013), and **every label has a closed vocabulary**: nothing taken from a request (no path, number, session or subscriber id) ever becomes a label value.

### identity (`services/go/identity/internal/httpapi/metrics.go`)

| Metric | Type | Labels | Values | Meaning |
|---|---|---|---|---|
| `identity_auth_requests_total` | counter | `endpoint`, `outcome` | endpoint: `otp_request`, `otp_validate`, `token_refresh`, `account_delete`, `account_deletion_code`, `account_deletion_confirm` (an unknown path counts as `unknown` and is not labelled with the path). Outcome: `success`, or the response's problem code lower-cased without `ERR_` (e.g. `invalid_otp`, `rate_limited`) | Every response the API decides (line 26) |
| `identity_security_events_total` | counter | `type` | `identity.otp.verification_failed`, `identity.otp.lockout`, `identity.refresh.token_reused`, `identity.device.enrolled`, `identity.device.reenrolled`, `identity.integrity.restricted`, `identity.integrity.rejected`, `identity.account.deleted`; anything else is `other` | The security audit events, counted once each (line 33) |
| `identity_security_audit_failures_total` | counter | `type` | As above | Audit events that were **not** recorded. The code says any non-zero rate "should page" (line 40) |
| `identity_play_integrity_outcomes_total` | counter | `outcome` | Verification outcomes | No verifier is configured, so every call would be `unavailable` (line 51) |
| `identity_play_integrity_restrictions_total` | counter | `reason` | Restriction reasons | Why a verdict was restricted (line 52) |
| `identity_otp_deliveries_total` | counter | `provider`, `outcome` | provider: a validated short name, else `other`. Outcome: `delivered`, `failed`, `timeout`, `abandoned` (`otpdelivery.Outcome`) | OTP send attempts per provider (line 59). **Not hooked to the development mock today** (`OTP-PROVIDERS.md` §1) |
| `identity_deletion_code_deliveries_total` | counter | `provider`, `outcome` | As above | Account-deletion code sends, apart from sign-in (line 65). No provider is configured |

### edge-api (`services/go/edge-api/internal/auth/metrics.go`)

| Metric | Type | Labels | Values | Meaning |
|---|---|---|---|---|
| `edge_api_auth_requests_total` | counter | `outcome` | `admitted`, `unauthenticated`, `invalid_token`, `invalid_dpop_proof`, `unavailable` (an unknown refusal code counts as `unavailable`, the fail-closed reading) | Every request the authenticator decided (line 14) |

### telephony-gateway (`services/go/telephony-gateway/internal/app/metrics.go`; the owner-held fence)

| Metric | Type | Labels | Values | Meaning |
|---|---|---|---|---|
| `telephony_gateway_fence_readiness_checks_total` | counter | `result` | `owned`, `not_owner`, `error` | Each `/readyz` evaluation of the fence (line 16) |
| `telephony_gateway_fence_owned` | gauge | — | 1 or 0 | Whether the last evaluation found this process the fence owner (line 19) |

## 3. Readiness checks (`/readyz`)

| Service | Check | Critical | What it says | Source |
|---|---|---|---|---|
| identity, inactive | `identity` | no | "identity inactive: no CS_IDENTITY_* security configuration supplied" (development only; staging and production refuse to start, OD-15) | `cmd/server/identity.go:84` |
| identity | `identity-postgres` | **yes** | The database answers a ping (fixed message "identity database unreachable") | `cmd/server/identity.go:98` |
| identity | `identity-housekeeping` | no | Bounded cleanup is running | `cmd/server/identity.go:104` |
| edge-api | `edge-replay-store` | **yes** | The DPoP replay store (PostgreSQL) answers | `services/go/edge-api/cmd/server/auth.go:108` |
| telephony-gateway | the fence check | yes | This process owns the fence (counted by the metrics above) | `telephony-gateway` `internal/app` |

## 4. Tracing

- **One span per public-edge request:** `edge.request` (`packages/go/middleware/trace.go:14`). Its attributes:
  - `correlation_id`;
  - `http_method` (a fixed vocabulary);
  - `http_status_code`.
  - A status of 5xx records a fixed error, "http status N": never a message or a request detail.
- **Exporter:** the **log exporter only** (`telemetry.NewLogExporter`, through the service's redacting logger). This applies to edge-api and identity. There is no OTLP exporter and no backend (ADR-0026 step 4 waits on ADR-0028).
- **Trace context:** W3C `traceparent`, parsed by `packages/go/telemetry`, which is fuzzed in `hardening`.

## 5. What an operator does not have yet

- **No alert rules, SLO rules or dashboards.** They are operator artefacts under ADR-0013, and need E5.
- **No tracing backend:** ADR-0028 is Proposed and conditional.
- **The development OTP mock is not counted** in `identity_otp_deliveries_total`. Hooking it in is an identity change (OD-12).
- **Signals the code calls for alerting are named here only as inputs** to that future work. Examples are `identity_security_audit_failures_total` and the `refresh.token_reused` event. **No threshold is set by this document.**
