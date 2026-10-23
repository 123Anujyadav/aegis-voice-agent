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
   - requests a *standard* integrity token;
   - binds it to a server-chosen request hash, which identity must issue and
     check;
   - sends it at enrolment and refresh.

   The protocol for that hash is not yet designed.
5. **The product decision:** what a **restricted** session may do (ADR-0010:
   a failed verdict downgrades rather than rejects). Until it is made, the
   verifier must not be wired into issuance.

**The code change:**
- **Implement `IntegrityVerifier.Decode`** against `decodeIntegrityToken`:
  - return `ErrIntegrityTokenInvalid` for a token Google rejects;
  - return any other error for an outage;
  - honour `ctx`.
- **Configure** with `SetIntegrityVerifier(v, policy)`. The policy's package name
  and certificate digests come from the release signing configuration.

**Before claiming verification works:** an integration test against Google with
a real device token, both certified and restricted.

## 3. Monitoring once connected

| Signal | Means |
|---|---|
| `identity_otp_deliveries_total{outcome="failed"\|"timeout"}` rising for one provider | That provider is degraded. Failover is carrying traffic: check `secondary` deliveries. |
| all providers failing | OTP requests return `ERR_OTP_DELIVERY_FAILED`, and enrolment is down. |
| `identity_play_integrity_outcomes_total{outcome="unavailable"}` | Google is unreachable or timing out (or no verifier is configured). |
| `identity_play_integrity_restrictions_total{reason=…}` | The verdict distribution ADR-0010 §9 asks to alert on. |
