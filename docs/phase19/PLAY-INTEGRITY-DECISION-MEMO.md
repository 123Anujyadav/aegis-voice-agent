# Play Integrity: integration and Google authentication, decision memo

- **As of:** 2026-10-25, `main` at `b4d929d`. §3b, §4, §10 and §11 were revised after a second review.
- **What this is:** an engineering recommendation for the owner. **It decides
  nothing.**
  - Every policy value below is marked **PROPOSED, awaiting owner
    ratification**.
  - ADR-0031 is unchanged and stays *Proposed, owner ratification pending*.
- **What it does not do:**
  - wire the Google verifier;
  - refuse any additional request;
  - change session policy;
  - create a credential or touch any Google Cloud project.

## 1. Recommendation in brief

1. **Ratify ADR-0031 as written**, and integrate the **existing** adapter
   (`services/go/identity/internal/playintegrity`, #134) through the existing
   port (`IdentityService.SetIntegrityVerifier`, which nothing calls today).
   - The integration is configuration and wiring only.
   - The verdict policy, the binding and the failure semantics are already
     implemented and tested.
2. **Authenticate to Google with Workload Identity Federation from EKS,
   impersonating a dedicated service account in the Play-linked project.**
   **Never use a service-account key.** Accepted ADR-0008 ("IRSA for every
   workload. No static cloud credentials anywhere") and `SECURITY.md` ("no
   static cloud credentials") already rule a key out.
3. **Roll out in staging first, with configuration as the kill switch.**
   Removing the verifier configuration returns identity to today's behaviour:
   every session `unverified`, nothing refused.
4. **Fix the client's quota defect first.** This is the one safe code task
   available now, and it is done in #158, merged as `97ee159` (§6).

## 2. What exists, what is approved, what is proposed

| Item | Status | Evidence |
|---|---|---|
| ADR-0010 §5(b): verdict at issuance and refresh, verified server-side; a failed verdict downgrades to a restricted tier and raises a signal | **Accepted** (ADR-0010) | `docs/adr/0010-authentication-and-device-trust.md` |
| D31.1–D31.5: proof-bound `requestHash`, `Integrity-Token` header, the outcome table, tier recording, not enforced | **Implemented**; ADR **pending ratification** | ADR-0031 "Implementation status"; migration `0006_session_integrity_tier` |
| Verdict checks (`IntegrityPolicy.Assess`) | **Implemented and tested** offline | `services/go/identity/internal/app/integrity.go` |
| Google adapter (`decodeIntegrityToken`) | **Implemented**, tested only against a local fake, **never called Google, not wired** | `internal/playintegrity/playintegrity.go`, `playintegrity_test.go` |
| Android client: standard token bound to the proof | **Implemented, not configured** | #143, #146 (`callscreen.playIntegrityCloudProjectNumber` unset) |
| Policy values (max age, skew, decode timeout, digests) | **Unset, owner decision**; proposed in §3 | `IntegrityPolicy.Validate` bounds only |
| What a `restricted` or `unverified` session may do | **Owner decision (O8)**, not enforced | ADR-0031 D31.5 |

## 3. Verdict policy

### 3a. Already implemented: the checks

A verdict is `certified` only if **all** of the following hold. Otherwise it
is `restricted`, and every failed check is listed as a reason.

| Check | Fails as |
|---|---|
| Request and app package names equal the configured package | `package_mismatch` |
| `requestHash` equals `base64url(SHA-256(DPoP proof))`, compared in constant time | `request_hash_mismatch` |
| The timestamp is no older than `MaxAge`, and no further ahead than `FutureSkew` | `stale_verdict` / `future_verdict` |
| App recognition is `PLAY_RECOGNIZED` | `app_not_recognized` |
| A signing-certificate digest matches a configured one | `certificate_mismatch` |
| Device recognition includes `MEETS_DEVICE_INTEGRITY` | `device_integrity_missing` |
| App licensing is `LICENSED` | `unlicensed` |

These are the checks Google's documentation describes: the request hash, the
app and device recognition, and licensing
([standard requests](https://developer.android.com/google/play/integrity/standard),
updated 2026-10-23).

### 3b. PROPOSED values, awaiting owner ratification

| Setting | Allowed by code | **Proposed** | Rationale and trade-off |
|---|---|---|---|
| `MaxAge` | (0, 10 min] | **2 min** | A standard token is requested immediately before the call it accompanies, so a fresh verdict is seconds old. 2 minutes tolerates slow networks. Longer widens the replay window for a captured token, although the DPoP binding already confines it to one request. |
| `FutureSkew` | [0, 1 min] | **30 s** | Tolerates clock drift between Google and identity without accepting obviously future-dated verdicts. |
| `DecodeTimeout` | (0, 5 s] | **3 s** | It must leave room within identity's 10-second handler budget. A timeout is `unavailable`: the session is `unverified` and **not refused**. |
| `CertificateDigests` | ≥ 1, each **43-character base64url** | **The Play App Signing certificate's SHA-256** (Play Console, *App signing key certificate*), plus the upload key's only if test builds are distributed outside Play | With Play App Signing, users receive APKs signed by Google's app-signing key. Google does not say which certificate `certificateSha256Digest` reports, so **confirm it against a real verdict in staging**. **Format trap:** Play Console shows SHA-256 as colon-separated hex, but the API returns base64url. Convert it with `printf %s HEX \| tr -d : \| xxd -r -p \| base64 \| tr '+/' '-_' \| tr -d =`. `IntegrityPolicy.Validate` rejects a hex value, so a pasted fingerprint fails at startup, not silently. A wrong digest makes verdicts `restricted`, never refused. |
| Require `LICENSED` | (implemented) | **Keep** | `LICENSED` means installed or updated from Google Play. `UNLICENSED` means sideloaded. It is **`UNEVALUATED` when the user is not signed in to Google Play**, or when the device is not trustworthy enough ([verdicts](https://developer.android.com/google/play/integrity/verdicts), updated 2026-10-23). Those users become `restricted`, **not refused**, so this is a signal, not a lockout. |
| Device bar | `MEETS_DEVICE_INTEGRITY` (implemented) | **Keep**; do not require `MEETS_STRONG_INTEGRITY` | On Android 13 and higher, device integrity means hardware-backed proof of a locked bootloader and a certified image. Strong integrity additionally needs security updates within the last year on all partitions, which excludes many genuine, older-patched phones. Basic-only devices become `restricted`. |

**How strict this can be without harm:** a `restricted` result changes no
permission until the owner decides O8. The only user-refusing outcome is
D31.3's refusal of a token **Google itself rejects**, and that refusal does not
depend on these values.

## 4. Failure handling: implemented behaviour

| Condition | What happens | Session |
|---|---|---|
| No `Integrity-Token` header | Proceeds; metric `not_presented` | `unverified` |
| No verifier configured (today) | Proceeds; `unavailable` | `unverified` |
| Google timeout, 5xx, 429 (quota), 401 or 403 (**our** credentials refused), oversized (> 64 KiB) or malformed response | A plain error, treated as `unavailable`; **never refused because Google could not answer** | `unverified` |
| Google answers HTTP 400 (forged, malformed or another app's token) | `ErrIntegrityTokenInvalid` → **refused**, 400 `ERR_INVALID_INTEGRITY_TOKEN`; no session or token issued; audit event (D31.3, **pending ratification**) | — |
| A replayed DPoP proof | Refused **before** integrity is evaluated (shared replay store) | — |
| A token decoded a second time | Google clears its verdicts (the documented replay protection) → checks fail | `restricted` |
| A verdict bound to another request | `request_hash_mismatch` | `restricted` |
| Any other failed check | Reasons recorded, metric and audit event | `restricted` |

**Granting nothing on failure:** the integrity check runs **after** the OTP
code and the proof are verified, and **before** the session is created (D31.3).
- A refused token creates nothing.
- A rejected token at refresh rotates nothing (D31.4). **The session continues with its existing tier.**
  - The app maps the refusal to `AuthState.Unavailable` (`IdentityAuthenticator.kt`), keeping the session.
  - Its next refresh within 6 hours in the same app process carries no token (#158), and succeeds.
  - So a Google-rejected token at refresh **does not downgrade or end** the session. §10 proposes a change; it is an owner decision.
- No error carries the integrity token, the access token or Google's response
  text (`playintegrity.go`).

**One rollout risk to watch:** if the deployment were misconfigured (for
example a wrong package name), Google might answer 400 for genuine tokens. D31.3
would then refuse them. §5 mitigates this.

## 5. Rollout and rollback (PROPOSED)

1. **Staging first,** with a Play-distributed internal-testing build and the
   real project. Confirm on a real device that tokens are acquired and that
   `certified` verdicts are recorded. This is the first real-device evidence;
   none exists today.
2. **Watch** `identity_play_integrity_outcomes_total` by outcome and reason, and the
   `identity.integrity.rejected` audit events. ADR-0010's revisit trigger is
   5% verdict failures.
3. **Kill switch:** unset the verifier configuration. identity then behaves
   exactly as today (every session `unverified`, nothing refused). No data
   migration is involved.
4. **Quota:** request an increase from Google before launch (D31.4). The client
   side of the quota rule is #158.

## 6. The quota defect fixed while reviewing (#158)

D31.4 says *"Clients attach a token at enrolment, and on refresh at most once
every 6 hours."*
- **The defect:** the Android client attached one on **every** refresh, every
  15 minutes. With the default 10,000 per day, that would have exhausted the
  quota at about 100 subscribers once the project number was configured.
- **The fix (#158):** client-side only. The server keeps a session's tier on a
  refresh without a token, so no security behaviour changes.

## 7. Google authentication: Workload Identity Federation (recommended)

**The hosting** (ADR-0008, accepted): AWS EKS in `ap-south-1`, IRSA for every
workload, and no static cloud credentials.

**The mechanism** ([Workload Identity Federation with Kubernetes](https://docs.cloud.google.com/iam/docs/workload-identity-federation-with-kubernetes),
updated 2026-10-22, which covers EKS explicitly):
- **In Google Cloud:**
  - a workload identity pool, with an **OIDC provider** whose issuer is the EKS
    cluster's OIDC issuer URL;
  - the attribute mapping `google.subject=assertion.sub`;
  - an **attribute condition limited to identity's namespace and Kubernetes
    service account**.
- **In the pod:** a projected service-account token whose audience is the
  provider, with a 1-hour expiry that the kubelet rotates.
- **The credential configuration file** (`credential_source` points at the
  projected token). Google states it "doesn't contain a private key and doesn't
  need to be kept confidential", so it is a ConfigMap, not a Secret.
- **Access model: impersonation** of a dedicated service account in the
  Play-linked project.
  - The federated subject gets `roles/iam.workloadIdentityUser` on that service
    account **only**.
  - Google's Play Integrity documentation specifies "a service account within
    the Google Cloud project that's linked to your app", with the
    `playintegrity` scope, so direct federated access is not relied on.
  - Google notes that direct access is "recommended", but that "certain API
    methods might have limitations".

| | WIF + impersonation (**recommended**) | Service-account key |
|---|---|---|
| Allowed by accepted policy | Yes | **No.** ADR-0008 and `SECURITY.md` forbid static cloud credentials |
| Secret to leak | None. Short-lived tokens only (1-hour projected token, then STS and impersonation tokens) | A long-lived private key, exportable and replayable anywhere |
| Rotation | Automatic | Manual, and a leaked key works until it is revoked |
| Revocation and recovery | Remove the IAM binding or disable the pool provider | Delete the key, then provision and deploy a new one |
| Least privilege | Bound to one namespace and Kubernetes service account by an attribute condition | Usable by whoever holds the file |
| Operational complexity | Pool, provider, binding and ConfigMap, set up once | Key provisioning, a Secret, and rotation |
| Code | `golang.org/x/oauth2/google` external-account support, behind the adapter's existing `TokenSource` interface. **A new dependency for identity.** | The same library |

**Costs and quota:**
- **IAM:** Google's IAM pricing page says "All use of Identity and Access
  Management API is free of charge". **That page does not name Workload
  Identity Federation**, so its cost is **not explicitly verified**.
- **Play Integrity:** the default quota is **10,000 requests per app per day
  (verified)**. Its price is **not stated** on Google's setup page
  (**unverified**).

**External prerequisites (owner or SRE):**
1. A Google Cloud project, **linked to the app in Play Console**.
2. The Play Integrity API enabled in that project.
3. A workload identity pool, plus an OIDC provider for the EKS cluster's issuer
   with the namespace and service-account condition.
4. A dedicated service account, and the `workloadIdentityUser` binding for
   identity's subject.
5. The Play App Signing certificate digest (§3b).
6. A quota increase request before launch.

**Whether the dedicated service account needs any IAM role beyond the Play
Console link is not stated** in Google's documentation. Verify it in staging
without granting broad roles.

**Development without Google:** fully supported today.
- The adapter is tested against a local fake.
- `TokenSource` is injected, so no credential code exists in the adapter.
- The Android client is contract-tested against the real identity and
  edge-api services in CI (#155, #156).

## 8. Owner decisions and external setup still required

| # | Item | Kind |
|---|---|---|
| O7 | Ratify ADR-0031 as written, **including D31.3's refusal of tokens Google rejects** | Owner decision |
| — | Ratify the §3b values, or choose others within the code's bounds | Owner decision |
| O8 | What `restricted` and `unverified` sessions may do (today: recorded, never enforced) | Owner decision |
| — | Approve WIF + impersonation (§7) as the authentication method | Owner and SRE decision |
| E1 | Prerequisites 1–6 (§7) | External setup |
| — | A Play-distributed build for staging verification | Owner (Play Console) |

## 9. What can be done safely now, and what cannot

- **Done:** #158, the client's quota rule, merged as `97ee159` with post-merge CI 10/10 green.
- **Cannot be done yet:**
  - wiring the verifier into `cmd/server`;
  - adding the OAuth dependency;
  - defining the deployment configuration names.

  That work would make D31.3's refusal live once it is configured, so it waits
  for O7 and the authentication decision.
- **After the decisions:**
  1. one PR: configuration, plus WIF `TokenSource` wiring, with
     `SetIntegrityVerifier` called **only** when fully configured;
  2. tests against the local fake;
  3. staging (§5).

## 10. O8: what each tier may do (PROPOSED, owner decision)

### 10a. What exists today (verified)

- **Recorded:** the tier, in `identity_sessions.integrity_tier` (`certified`,
  `restricted` or `unverified`; migration `0006`).
- **Not carried:** the tier is **not** in the access token, and **no service
  enforces it** (D31.5).
- **Protected API:** edge-api serves exactly **one** protected route,
  `GET /v1/me` (`internal/httpapi`).
- **Roles:** the access token already has a `roles` claim. identity issues
  `["subscriber"]`, the `accesstoken` package "defines no role", and edge-api
  passes roles to handlers, **but no route checks them**.
- **Elevated-risk operations** (ADR-0010: changing the forwarding DID,
  exporting transcripts, changing consent) **do not exist as routes yet**.
  Their re-verification requirement is also unimplemented.

So today **no tier changes what a session can do**. Any policy below needs new
enforcement, which is listed in §10c.

### 10b. Proposed least-privilege policy

| Tier | Meaning | Proposed policy |
|---|---|---|
| `certified` | Genuine Play-distributed app on a certified device (§3a) | All standard features. Elevated-risk operations **still** require re-verification (ADR-0010), whatever the tier. |
| `restricted` | A verdict was obtained and failed a check: sideloaded, modified or unrecognised app, uncertified device, unlicensed, or not signed in to Play | Sign in, `GET /v1/me`, and features that expose no stored sensitive data. **Denied:** elevated-risk operations and reading stored transcripts or call history. Re-assessed at the next refresh that carries a token. |
| `unverified` | No verdict: no token sent, no Play services, Google unavailable or timed out, or no verifier configured | **The same as `restricted`** (ADR-0031 D31.3 already states "no verdict is not a pass"). It is kept distinct in metrics so outages do not look like attacks. |
| Rejected at **issuance** (Google 400) | A forged, malformed or other-app token | **No session** (implemented, D31.3). |
| Rejected at **refresh** | The same, on an existing session | Today: refused once, then the session continues with its old tier (§4). **Proposed:** identity sets the tier to `restricted`. That fails safe without locking anyone out on a Google-side fault. **Revoking is not recommended.** |

**The trade-off:** treating `unverified` as `restricted` denies sensitive
features to phones without Play services and during Google outages. Today that
costs nothing, because no sensitive route exists. Revisit it with ADR-0010's
5% verdict-failure trigger and the outcome metrics.

### 10c. Enforcement this would need (none exists)

1. **identity** adds a tier role to `roles` at issuance and refresh (for
   example `integrity:certified`).
   - This needs no claim-schema change: `roles` is already a string array that
     edge-api accepts.
   - A tier change reaches edge-api within one access-token lifetime
     (15 minutes).
2. **edge-api** gets a per-route requirement (a middleware that checks roles)
   for each sensitive route as that route is built.
3. **identity** implements the §10b rejected-at-refresh downgrade, if it is
   approved.
4. The ADR-0010 re-verification for elevated-risk operations, which is
   separate and also unimplemented.

None of this is started. Items 1 and 2 *are* the enforcement of O8, so they
wait for the decision.

## 11. Decisions requiring explicit owner approval

| # | Decision | Recommendation |
|---|---|---|
| O7 | Ratify ADR-0031 as written, including refusing tokens Google rejects (400) at issuance | **Ratify.** It refuses only tokens Google itself declares invalid. Every Google-side failure, and every failed check, degrades to a tier instead. |
| — | `MaxAge` 2 min, `FutureSkew` 30 s, `DecodeTimeout` 3 s | **Accept.** They are within the code's bounds (≤ 10 min, ≤ 1 min, ≤ 5 s), and a timeout never refuses. |
| — | Certificate digest: the Play App Signing certificate, converted to base64url, confirmed in staging | **Accept.** |
| — | Keep `LICENSED` and `MEETS_DEVICE_INTEGRITY`; do not require `MEETS_STRONG_INTEGRITY` | **Accept.** |
| O8 | §10b tier policy, including `unverified` treated as `restricted` | **Accept, as policy.** Enforcement (§10c) is built per route. |
| O8 | A Google-rejected token at refresh downgrades the tier to `restricted` | **Accept** (a small identity change, after approval). |
| — | Workload Identity Federation + service-account impersonation (§7); no key | **Accept.** It is the only option consistent with accepted ADR-0008 and `SECURITY.md`. |
