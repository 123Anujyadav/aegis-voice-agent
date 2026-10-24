# Runbook: onboarding identity's external providers (OTP delivery, Play Integrity)

- **Status:** **no provider is connected.**
  - identity refuses OTP requests truthfully (`ERR_OTP_DELIVERY_UNAVAILABLE`).
  - Play Integrity verification fails closed (`unavailable`).
- **What this runbook is:** what onboarding a real provider requires, and how
  the code receives it.
- **What it is not:** a record of any onboarding. None has been done.
- **Owner:** onboarding needs the repository owner (commercial and product
  choices) and SRE (credentials, egress).
- **No credentials:** this document contains none. Credentials never go in the
  repository, a ticket body or chat.

## 1. OTP delivery (ADR-0010 §5(a), §9)

**What exists in code:**
- `internal/otpdelivery.Sender`, an `app.OtpSender` that:
  - fails over across up to 3 named providers;
  - bounds each attempt (`AttemptTimeout`, 0.5–4 s);
  - stops when the request gives up;
  - never returns provider text.
- `Sender.Health()`: per-provider consecutive failures and the last outcome.
- `identity_otp_deliveries_total{provider,outcome}`, when the sender is built
  with `Observe: metrics.ObserveOtpDelivery` (`internal/httpapi`).

**What a real provider needs, before any code:**
1. **A commercial account** with an SMS provider that delivers in India, and a
   second one for the fallback ADR-0010 §9 requires. Cost and contracts are the
   owner's decision.
2. **India DLT registration (TRAI):**
   - the business registered as a Principal Entity on a DLT platform;
   - its **sender id (header)** registered;
   - the **OTP message template** registered as content, with the code as its
     variable.

   Messages that do not match a registered template are blocked by carriers.
   The exact registration steps belong to the chosen DLT operator and provider;
   confirm them with them.
3. **Credentials** for each provider, provisioned by SRE as Secrets (a mounted
   file, as the other identity secrets are). Never as an environment variable
   value or in a ConfigMap.
4. **Egress** from identity to each provider's API. This is a network-policy
   change and needs SRE authorisation.

**The code change:**
- **Implement `otpdelivery.Provider`** for each provider:
  - `Name()` is a fixed short label such as `primary-sms`;
  - `SendOtp` honours `ctx`, does not retry, and never logs the number or the
    code.
- **Map the provider's errors** to a plain error: their text may echo the number.
- **Build the sender in `cmd/server`** with `Observe` wired to the metrics, and
  set it with `SetOtpSender`. Until this is done, nothing changes.

**Before claiming delivery works:** a live test from a staging environment to a
real handset on each provider, including the failover path.

## 2. Play Integrity (ADR-0010 §5(b))

**What exists in code:**
- `app.IntegrityVerifier`, the port.
- `IntegrityPolicy`, the verdict rules:
  - package name and signing-certificate digests;
  - request-hash binding;
  - freshness;
  - `PLAY_RECOGNIZED`, `MEETS_DEVICE_INTEGRITY` and `LICENSED`.

  Every decode is bounded by `DecodeTimeout` (at most 5 s), and a timeout is
  "unavailable".
- `identity_play_integrity_outcomes_total{outcome}` and
  `identity_play_integrity_restrictions_total{reason}`, via
  `metrics.Instrument`.
- **Issuance wiring (ADR-0031):** the `Integrity-Token` header at enrolment
  and refresh, the verdict bound to the request's DPoP proof, and the tier
  recorded on the session. The tier is not enforced.
- **The Google adapter, `internal/playintegrity`:** a `Decoder` that calls
  `POST {endpoint}/v1/{package}:decodeIntegrityToken` and maps
  `tokenPayloadExternal` into a verdict.
  - Only HTTP 400 is a rejected token. Our own 401/403, 429, 5xx, timeouts
    and malformed or oversized responses count as unavailable.
  - It never puts a token or response text in an error.
  - It is tested against a local fake only. **It has never called Google,
    and identity is not configured with it.** The OAuth access token comes
    from an injected `TokenSource`, which this repository does not implement.

**What a live verifier needs:**
1. **Accounts:** the Android app published in Play Console, and **linked to a
   Google Cloud project**, with the Play Integrity API enabled on that project.
2. **A server credential** permitted to call `decodeIntegrityToken` for the app.
   - Prefer workload identity federation, so no long-lived key is used.
   - Otherwise, a service-account key provisioned by SRE as a Secret.
   - ADR-0008 forbids static cloud credentials on AWS. The cross-cloud choice is
     SRE's.
3. **Egress** from identity to Google's API: a network-policy change.
4. **The client:**
   - requests a *standard* integrity token with
     `requestHash = base64url(SHA-256(DPoP proof))` (ADR-0031 D31.1);
   - sends it in `Integrity-Token` at enrolment, and at refresh at most once
     every 6 hours (quota).
5. **Quota:** Google's default is 10,000 requests per day. Request an
   increase before launch.
6. **The product decision:** what a **restricted** or **unverified** session
   may do (ADR-0031 D31.5). Until it is made, the tier is recorded and
   audited, never enforced.

**The remaining code change:**
- **A `TokenSource`** for the chosen credential (workload identity, or a
  service-account key from a Secret), with the Play Integrity OAuth scope.
  Confirm the scope against Google's documentation.
- **Configuration and wiring:** `playintegrity.New(...)` plus
  `SetIntegrityVerifier(decoder, policy)` in bootstrap. The policy's package
  name and certificate digests come from the release signing configuration.

**Proposed configuration** for that wiring PR. **identity reads none of these
yet**; they follow the existing `CS_IDENTITY_*` naming:

| Name | Holds | Secret? |
|---|---|---|
| `CS_IDENTITY_PLAY_INTEGRITY_PACKAGE_NAME` | The Android application id | No |
| `CS_IDENTITY_PLAY_INTEGRITY_CERT_DIGESTS` | Comma-separated, unpadded base64url SHA-256 digests of the release signing certificate(s) | No |
| `CS_IDENTITY_PLAY_INTEGRITY_CREDENTIALS_FILE` | The path of a **mounted Kubernetes Secret** holding the Google credential: a workload-identity-federation configuration (preferred) or a service-account key | **Yes** |

**Secure setup:**
1. SRE creates the credential in the Google Cloud project linked in Play
   Console, granting only the Play Integrity API.
2. SRE stores it as a Kubernetes Secret and mounts it read-only into identity.
3. Only the file path goes into configuration.

The credential never goes into the repository, a ticket, a log or chat. With
none of these set, identity keeps Play Integrity unavailable, and sessions
stay `unverified`.

**Before claiming verification works:** an integration test against Google with
a real device token, both certified and restricted.

## 3. Monitoring once connected

| Signal | Means |
|---|---|
| `identity_otp_deliveries_total{outcome="failed"\|"timeout"}` rising for one provider | That provider is degraded. Failover is carrying traffic: check `secondary` deliveries. |
| all providers failing | OTP requests return `ERR_OTP_DELIVERY_FAILED`, and enrolment is down. |
| `identity_play_integrity_outcomes_total{outcome="unavailable"}` | Google is unreachable or timing out (or no verifier is configured). |
| `identity_play_integrity_restrictions_total{reason=…}` | The verdict distribution ADR-0010 §9 asks to alert on. |
