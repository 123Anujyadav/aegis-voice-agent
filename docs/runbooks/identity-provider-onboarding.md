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
     variable;
   - a **second template for account-deletion codes**
     (`docs/phase19/ACCOUNT-DELETION.md` §3.1), registered the same way, if
     web deletion will be enabled (O13). A deletion message is a different
     template, and an unregistered one is blocked.

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
- **Web deletion (#193), separately, with the same provider adapter:**
  - implement `otpdelivery.DeletionCodeProvider` as well, with
    `SendDeletionCode` using the registered **deletion** template, under the
    same rules as `SendOtp`;
  - a provider without a registered deletion template simply does not
    implement it, and is never asked to send a deletion code;
  - the composed `otpdelivery.Sender` gives deletion codes the same failover,
    attempt bound and error discipline, with their own health
    (`DeletionHealth`) and observations (`ObserveDeletion`, counted as
    `identity_deletion_code_deliveries_total`).
- **Wiring, in `cmd/server`:**
  - `SetOtpSender(sender)`;
  - only if `sender.DeletionCodes()` returns a sender, **and** web deletion is
    approved (O13): `SetDeletionCodeSender(it)` and an hourly budget
    (`SetDeletionCodeBudget`; the value is the owner's or SRE's).
  - Without both, `/v1/account/deletion/*` answers 503 for every number.
- **The owner's ordered setup checklist** for internal testing is
  `docs/phase19/INTERNAL-TESTING-SETUP.md` (step A).

**Before claiming delivery works:** a live test from a staging environment to a
real handset on each provider, including the failover path (§1b).

### 1a. The contract a production adapter must meet

Verified against the code on `main`. **No adapter exists**, and none is
complete before a provider is chosen and its documented API is implemented.

- **Primary and fallback.**
  - `otpdelivery.Config.Providers` is an ordered list of 1 to 3 providers,
    with distinct short lower-case names (the `provider` metric label).
  - `New` accepts one provider, but ADR-0010 §9 requires a fallback, so
    production wiring builds **at least two**.
  - Deletion codes use the same order, over only the providers that
    implement `DeletionCodeProvider`.
- **Two templates, never crossed.**
  - `SendOtp` uses the registered **sign-in** template; `SendDeletionCode`
    uses the registered **deletion** template.
  - A provider without a deletion template does not implement
    `DeletionCodeProvider`.
  - Indian providers usually require the DLT entity id, the header and the
    template id with each message. These are identifiers, not secrets.
    Confirm the exact fields with the chosen provider.
- **Time.**
  - Each attempt is bounded by `AttemptTimeout`, 0.5–4 s.
  - **A sign-in code is sent inside the request,** whose handler deadline is
    10 s (`httpapi.HandlerTimeout`). The whole failover and the store calls
    must fit: keep **providers × `AttemptTimeout` ≤ 8 s** (2 providers:
    ≤ 4 s; 3 providers: ≤ 2.5 s). Otherwise the request deadline cuts the
    last provider short, and the request fails.
  - A deletion code is sent after the answer, detached from the request and
    bounded at 30 s.
- **Cancellation.**
  - An adapter honours `ctx`. One that ignores it is still bounded: the
    attempt stops waiting, and the late answer is discarded.
  - Once the request ends (its deadline, or the client going away), no
    further provider is tried, and the undelivered challenge is **withdrawn**.
    The withdrawal is detached from the ended request and bounded at 2 s;
    it is tested against real PostgreSQL.
- **Duplicates.**
  - An adapter never retries, and any automatic retry the provider offers
    stays off: `Sender` owns failover.
  - After a timeout the next provider is sent **the same code**, so a slow
    first provider can deliver it twice. It never sends two different codes.
  - A user's "resend" is a new request with a new code. Each number gets
    3 requests per 15 minutes, and 5 wrong codes lock it for 15 minutes
    (`defaultOtpRatePolicy`; initial values, not production-tuned).
- **Credentials.**
  - Each provider's credential is **one mounted Secret file**, read once at
    startup the way identity reads its other secrets (`readSecretFile`: a
    regular file, size-bounded; errors name the variable, never the path or
    the content).
  - Proposed name: `CS_IDENTITY_OTP_<NAME>_CREDENTIALS_FILE`, one per
    provider. **identity reads none yet;** the final names come with the
    adapter.
  - Never an environment value, a ConfigMap, a log line or an error.
  - Non-secret settings, such as the header, the template ids and the attempt
    timeout, go in configuration.
- **Errors and logs.**
  - An adapter maps the provider's responses to plain errors, with **no
    response text**: it may echo the number or the code.
  - `Sender` returns `ErrAllProvidersFailed` or the request's context
    error. identity answers `ERR_OTP_DELIVERY_FAILED` (503).
  - An adapter logs neither the number nor the code. identity's own logs and
    error bodies are checked for both by
    `TestHTTP_NothingSensitiveLeaksIntoErrorsOrLogs`.
- **Health and metrics.**
  - `Sender.Health()` and `DeletionHealth()` give each provider's
    consecutive failures and last outcome.
  - Wire `Observe: metrics.ObserveOtpDelivery` and
    `ObserveDeletion: metrics.ObserveDeletionCodeDelivery`, which count
    `identity_otp_deliveries_total` and
    `identity_deletion_code_deliveries_total` by provider and outcome.
- **Tests for the adapter itself:** against a local fake of the provider's
  **documented** API, in CI, never against the live provider. The failover,
  timeouts, health, metrics and both message types are already tested with
  fakes (`internal/otpdelivery`).

### 1b. Staging delivery test plan (after the adapter and A1–A5)

**Preconditions:**
- identity is deployed to the authorised staging environment, with the
  adapter and both providers' credentials.
- Test handsets have Indian SIMs, ideally one per major carrier. The numbers
  belong to the owner or the testers.

**Codes and numbers never leave the handset:** not into a ticket, a chat or a
log. Record only "received: yes/no", the time and the template seen.

| # | Test | Expected |
|---|---|---|
| S1 | Request a sign-in code from the app to each handset | The SMS arrives, from the registered header, matching the sign-in template. Enrolment with it succeeds. `identity_otp_deliveries_total{provider="<primary>",outcome="delivered"}` rises. |
| S2 | Make the **primary** fail, in staging only: block its egress or mount an invalid credential | The fallback delivers. The metrics show the primary `failed` or `timeout`, and the fallback `delivered`. |
| S3 | Make **both** fail | The app shows that the code could not be sent (`ERR_OTP_DELIVERY_FAILED`). No SMS arrives, and the count of rows in `identity_otp_challenges` does not grow. |
| S4 | Four requests for one number within 15 minutes | The fourth is refused (429). |
| S5 | **Only if web deletion is approved (O13) and its budget set:** request a deletion code for a test account | The SMS uses the **deletion** template. Confirming erases the test account. |
| S6 | **Log audit:** search every staging log stream for each test number (in `+91…`, `91…` and 10-digit forms) and for each code received | **No match.** |

Restore the primary's egress and credential after S2 and S3.

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
    from `playintegrity.WorkloadIdentityTokens` (#164), built from Google's
    Workload Identity Federation configuration.

**What a live verifier needs:**
1. **Accounts:** the Android app published in Play Console, and **linked to a
   Google Cloud project**, with the Play Integrity API enabled on that project.
2. **A server credential** permitted to call `decodeIntegrityToken` for the app.
   - Workload Identity Federation from EKS (recommended, awaiting owner
     approval: decision memo §7).
   - **A service-account key is not an option:** accepted ADR-0008 allows no
     static cloud credentials, and `bootstrap.NewPlayIntegrityVerifier`
     refuses one.
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

**The configuration contract, dormant (#164).**
`bootstrap.LoadPlayIntegrityConfig` reads six variables: all set, or none.
Nothing in `cmd/server` calls it yet, and a test keeps it that way until
ADR-0031 and the policy values are ratified.

| Name | Holds |
|---|---|
| `CS_IDENTITY_PLAY_INTEGRITY_PACKAGE_NAME` | The Android application id |
| `CS_IDENTITY_PLAY_INTEGRITY_CERT_DIGESTS` | The release signing certificates' SHA-256 digests, base64url, comma-separated |
| `CS_IDENTITY_PLAY_INTEGRITY_MAX_AGE` | How old a verdict may be, in (0, 10m] |
| `CS_IDENTITY_PLAY_INTEGRITY_FUTURE_SKEW` | How far ahead a verdict timestamp may be, in [0, 1m] |
| `CS_IDENTITY_PLAY_INTEGRITY_DECODE_TIMEOUT` | One decode call, in (0, 5s] |
| `CS_IDENTITY_PLAY_INTEGRITY_CREDENTIAL_CONFIG_FILE` | Google's Workload Identity Federation configuration. It holds no private key, so it is a ConfigMap, not a Secret (handoff S5). |

The values are owner policy, and none has a default.

**The remaining code change:** after approval, wire `LoadPlayIntegrityConfig`,
`NewPlayIntegrityVerifier` and `SetIntegrityVerifier` in `cmd/server`
(`docs/phase19/PLAY-INTEGRITY-IMPLEMENTATION-HANDOFF.md` §3).

**Secure setup:** the handoff, §1 (S1–S6). Nothing goes into the repository,
a ticket, a log or chat. With none of the variables set, identity keeps Play
Integrity unavailable, and sessions stay `unverified`.

**Before claiming verification works:** an integration test against Google with
a real device token, both certified and restricted.

## 3. Monitoring once connected

| Signal | Means |
|---|---|
| `identity_otp_deliveries_total{outcome="failed"\|"timeout"}` rising for one provider | That provider is degraded. Failover is carrying traffic: check `secondary` deliveries. |
| all providers failing | OTP requests return `ERR_OTP_DELIVERY_FAILED`, and enrolment is down. |
| `identity_deletion_code_deliveries_total{outcome="failed"\|"timeout"}` rising for one provider | Its deletion template or delivery is failing. Sign-in delivery is counted apart and may be unaffected. |
| `identity_play_integrity_outcomes_total{outcome="unavailable"}` | Google is unreachable or timing out (or no verifier is configured). |
| `identity_play_integrity_restrictions_total{reason=…}` | The verdict distribution ADR-0010 §9 asks to alert on. |
