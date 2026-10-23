# Runbook: access-token signing-key rotation

- **Status:** documented procedure. It is **not yet exercised operationally**: no
  environment provisions these keys today, and no rotation has been performed.
  It is derived from ADR-0029 ("Keys and rotation") and from the code on `main`.
  Where the two differ, or where something is missing, §11 says so.
- **Owner of the operation:** SRE. Every step that touches a Secret, a
  deployment or a production rollout needs SRE authorisation under the normal
  change process. Nothing here grants it.
- **Applies to:** `services/go/identity` (the issuer) and `services/go/edge-api`
  (the verifier).
- **Contains no key material.** Every path and key id below is a placeholder.

## 1. What rotates, and what does not

| Secret | Rotated by this runbook | Why |
|---|---|---|
| Ed25519 access-token **signing key** (identity) and its **public key** (edge-api) | **Yes** | edge-api trusts an active and a previous key, so the change overlaps and no valid token is ever rejected. |
| Identity **HMAC secret** (`CS_IDENTITY_HMAC_SECRET_KEY_FILE`) | **No** | See §11.1. It has no rotation mechanism, and changing it invalidates every session and changes every subscriber id. |
| Database DSN and its CA bundle | No | Separate procedure, not covered here. |

Access tokens are short-lived, and only they are signed with this key. Refresh
tokens are opaque values hashed with the HMAC secret, so a signing-key rotation
never signs anyone out.

## 2. Facts from the implementation

These come from the code; check them again if the code changes.

| Item | Value | Where |
|---|---|---|
| Token | EdDSA (`Ed25519`) `at+jwt`, header exactly `{alg, typ, kid}` | `packages/go/accesstoken` |
| Access-token lifetime | 15 minutes (`accesstoken.Lifetime`) | `accesstoken.go` |
| Clock skew allowed | 60 seconds (`accesstoken.DefaultClockSkew`) | `accesstoken.go` |
| Private key file (identity) | PEM `PRIVATE KEY`, PKCS#8, Ed25519; a regular file of at most 16 KiB | `CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE` |
| Identity key id | `CS_IDENTITY_ACCESS_TOKEN_KID` | written into every token header |
| Public key files (edge-api) | PEM `PUBLIC KEY`, PKIX, Ed25519; a regular file of at most 16 KiB | `CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE`, `CS_EDGE_API_ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE` |
| Edge key ids | active `CS_EDGE_API_ACCESS_TOKEN_KID`; previous `CS_EDGE_API_ACCESS_TOKEN_PREVIOUS_KID` | `services/go/edge-api/internal/auth` |
| Key id rules | 1–128 bytes, printable ASCII (`0x21`–`0x7E`), no `"` and no `\`. No padding whitespace. | `validKeyID`, both services' `Validate` |
| Previous-key rules (edge-api) | both variables or neither; previous kid ≠ active kid; its own file (not the active key's, not the DSN's); the file must not contain the active key | `previousKeyProblems`, `trustedKeys` |
| Issuer and audience | must be byte-identical in identity and edge-api | both services |
| When keys are read | **at startup only**: there is no hot reload, so every change below is a rollout | both services |
| Unknown or untrusted kid at edge-api | 401 `ERR_INVALID_TOKEN` | `auth.Middleware` |
| Misconfiguration | the service refuses to start, and the error names the variable, never the path or key | both services |

**Tested in code:** `TestRotation_OverlapProcedureNeverRejectsAValidToken`
(`services/go/edge-api/internal/auth/rotation_test.go`) runs §5's steps through
the real middleware, with a simulated clock. It is mutation-checked: a verifier
that ignores the previous key fails it. That proves the verifier logic, not the
operational procedure.

## 3. Roles and approvals

| Action | Who | Approval |
|---|---|---|
| Generate a key pair | SRE key custodian | change ticket |
| Create or update the Secrets | SRE | change ticket; the second-person review your secrets policy requires |
| Roll out edge-api or identity | SRE (on-call aware) | normal production change approval |
| Destroy an old private key | SRE key custodian | recorded in the ticket |
| Emergency rotation (§8) | incident commander and SRE | incident process; the ticket follows |

## 4. Key generation and custody

1. Generate on a trusted, access-controlled host, never on a developer laptop or
   in CI logs:

   ```sh
   umask 077
   openssl genpkey -algorithm ed25519 -out signing-<KID>.key
   openssl pkey -in signing-<KID>.key -pubout -out signing-<KID>.pub
   ```

   These two commands produce exactly the formats both services accept (PKCS#8
   `PRIVATE KEY` and PKIX `PUBLIC KEY`). That was verified against
   `accesstoken.ParsePrivateKeyPEM` and `ParsePublicKeyPEM` when this runbook
   was written.
2. **Choose a kid** that names the key and never repeats, for example
   `signing-2026-11`. Never reuse a kid for a different key.
3. **Custody:**
   - the private key goes only into identity's Secret;
   - the public key goes into edge-api's Secret (it is not secret, but keep it
     under the same change control so no one can substitute it);
   - mount both read-only with a restrictive file mode (for example
     `defaultMode: 0400`);
   - never put either in an environment variable, a ConfigMap, a log, a ticket
     body or a chat.
4. **Delete the generation host's copy** of the private key once the Secret is
   created and verified. The Secret store is the system of record.
5. **Never reuse identity's HMAC secret file as the signing key, or the
   reverse.** Identity refuses an HMAC file that contains the signing key.

## 5. Planned rotation

Notation: `OLD` is the current key and kid; `NEW` is the replacement.

**Before you start:**
- §4 is done for `NEW`, and both Secrets exist with `NEW` added **alongside**
  `OLD`, not replacing it;
- issuer and audience are unchanged;
- dashboards from §6 are open;
- clocks on all nodes are NTP-synchronised. The 60-second skew is the only
  tolerance.

**Step 1: edge-api trusts both keys, with NEW active.**
- **Set:**
  - `CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE` to NEW's public key file;
  - `CS_EDGE_API_ACCESS_TOKEN_KID` to `NEW`;
  - `CS_EDGE_API_ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE` to OLD's public key file;
  - `CS_EDGE_API_ACCESS_TOKEN_PREVIOUS_KID` to `OLD`.
- **Roll out edge-api** to every replica.
- **Verify:**
  - the rollout completes;
  - `edge_api_auth_requests_total{outcome="invalid_token"}` shows no step
    change (tokens are still OLD-signed, and OLD is still trusted);
  - no edge-api pod is stuck in startup. A misconfiguration stops the new pods
    starting, so the rollout stalls and the old pods keep serving. Treat a
    stalled rollout as a failed step 1.

**Step 2: identity signs with NEW.**
- **Set:** `CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE` to NEW's private key file,
  and `CS_IDENTITY_ACCESS_TOKEN_KID` to `NEW`.
- **Roll out identity.** During the rollout some replicas sign with OLD and some
  with NEW. edge-api trusts both, so this is safe.
- **Verify:**
  - `invalid_token` at edge-api stays flat;
  - identity's `identity_auth_requests_total{endpoint="token_refresh"}` and
    `{endpoint="otp_validate"}` success rates are unchanged. That metric arrives
    with #106; until it is merged, use the identity request logs for 5xx only.
- **Record the time T** at which the last identity pod running OLD terminated.

**Step 3: wait.** Wait at least 15 minutes (token lifetime) plus 60 seconds
(skew) after T. After that, no unexpired OLD-signed token can exist.

**Step 4: edge-api trusts NEW only.**
- **Unset** both `CS_EDGE_API_ACCESS_TOKEN_PREVIOUS_*` variables, and roll out
  edge-api.
- **Verify:** `invalid_token` shows no step change. A rise means step 3 was too
  short, or an identity replica was still signing with OLD.

**Step 5: retire OLD.** Follow §9.

## 6. Monitoring and validation

| Signal | Source | Healthy during rotation |
|---|---|---|
| `edge_api_auth_requests_total{outcome="invalid_token"}` | edge-api `GET /metrics` (health port) | no step change at any step |
| `edge_api_auth_requests_total{outcome="admitted"}` | edge-api | no drop |
| `identity_auth_requests_total{endpoint="token_refresh",outcome="success"}` | identity `GET /metrics` (#106) | no drop after step 2 |
| `identity_auth_requests_total{endpoint="token_refresh",outcome="invalid_token"}` | identity (#106) | flat. A signing-key change does not touch refresh tokens. |
| Pod startup and readiness | orchestrator | every rollout completes; a stall is a misconfiguration |
| Startup errors | service logs | none. They name the variable at fault, never its value. |

A sudden spike in `invalid_token` at edge-api **after step 2** almost always
means edge-api does not trust `NEW`. Roll back step 2 immediately (§7).

## 7. Rollback

| Failed at | Rollback | Effect |
|---|---|---|
| Step 1 (edge-api does not start or serve) | Revert edge-api to OLD active, no previous key | none: tokens are still OLD-signed |
| Step 2 (`invalid_token` spikes) | Revert identity to OLD key and kid | NEW-signed tokens already issued stay valid, because edge-api still trusts NEW as active. No user action needed. |
| After step 3, before step 4 | Revert identity to OLD | safe: edge-api still trusts both |
| After step 4 | **Re-add OLD as edge-api's previous key first (step 1 again), then** revert identity | Reverting identity first would make every newly issued token untrusted. |

Never remove a key from edge-api while identity may still be signing with it.

## 8. Emergency rotation (suspected compromise of OLD's private key)

The aim is to stop trusting OLD as fast as possible, accepting that clients must
refresh.

1. **Declare an incident,** and record when the key was last known safe.
2. **Generate NEW** (§4).
3. **Do step 1 and step 2 back to back,** without the usual observation pause.
4. **Do step 4 immediately, skipping the step 3 wait.** Every OLD-signed access
   token is then refused (401 `ERR_INVALID_TOKEN`), including any minted by an
   attacker.
   - Legitimate clients recover by refreshing at identity: refresh tokens are
     unaffected, so this costs one refresh, not a sign-in.
   - Expect an `invalid_token` spike that decays over about 15 minutes. That is
     the intended effect, not a failure.
   - That clients refresh automatically on 401 is client behaviour, and it must
     be verified for each client release.
5. **Destroy OLD** (§9) as soon as identity no longer uses it.
6. **Review:**
   - edge-api `admitted` traffic for subjects active between the last-known-safe
     time and step 4;
   - refresh-token reuse at identity
     (`identity_auth_requests_total{endpoint="token_refresh",outcome="invalid_token"}`).

If identity's **HMAC secret** may also be compromised, see §11.1: this runbook
does not cover it.

## 9. Retiring and destroying an old key

1. **Confirm** that no Secret and no deployment still references OLD, and that
   step 4 completed at least 16 minutes ago.
2. **Remove OLD** from identity's and edge-api's Secrets.
3. **Destroy** every copy of OLD's private key: the Secret's previous versions
   where your Secret store keeps them, and any escrow. Record the destruction in
   the ticket.
4. **Keep the kid recorded as retired,** and never issue it again.

## 10. Failure scenarios

| Scenario | Symptom | Action |
|---|---|---|
| New edge-api pods fail to start | rollout stalls; logs name `CS_EDGE_API_…` | fix the variable; old pods keep serving meanwhile |
| Previous kid equals active kid | edge-api refuses to start ("must differ") | use distinct kids |
| Previous file holds the active key | edge-api refuses to start ("holds the active key") | mount the correct OLD public key |
| identity cannot read or parse the key | identity refuses to start; the rollout stalls | check the PEM type (`PRIVATE KEY`, PKCS#8) and the file size |
| Issuer or audience differs between the services | every token refused at edge-api (`invalid_token`) | make both byte-identical; roll back identity first |
| Clock skew over 60 s on a node | sporadic `invalid_token` | fix NTP; unrelated to the key |
| Step 4 done too early | `invalid_token` rises after step 4 | re-add OLD as previous (step 1 config); wait the full §5 step 3 |
| Key leaked in a log or ticket | — | treat as compromise: §8 |

## 11. Mismatches and gaps (flagged, not resolved here)

1. **The HMAC secret has no rotation path.** `CS_IDENTITY_HMAC_SECRET_KEY_FILE`
   is used for:
   - OTP code hashes;
   - **refresh-token hashes** (`HashRefreshToken`);
   - **subscriber ids** (`usr_` + HMAC of the MSISDN).

   Changing it invalidates every session and pending OTP. **It also gives every
   existing user a new subscriber id**, orphaning anything keyed by the old one.
   There is no dual-key support. Rotating it needs a design decision (for
   example a key id stored with each hash, plus a subscriber-id migration)
   before it can be done safely. Until then, treat a suspected HMAC compromise
   as a separate incident requiring an engineering decision.
2. **No manifest provisions these keys.** No manifest on `main` mounts the
   signing key, the public keys or the HMAC secret. Wiring the Secrets and
   volumes is SRE work, gated on infrastructure authorisation.
3. **Startup-only loading:** every rotation step is a rollout, so the procedure's
   duration is bounded below by two edge-api rollouts and one identity rollout,
   plus the 16-minute wait.
4. **Only one previous key.** A second rotation cannot begin until step 4 of the
   first has completed.
5. **ADR-0029 is owner-ratification pending.** The procedure follows its
   decisions as implemented. If the owner amends them, revisit this runbook.

## 12. Evidence

| Claim | Evidence |
|---|---|
| The verifier accepts both keys during the overlap and only NEW after it | `TestRotation_OverlapProcedureNeverRejectsAValidToken`, mutation-checked |
| The previous-key configuration is validated | `TestRotation_PreviousKeyConfigurationIsValidated` |
| The §4 commands produce accepted key files | checked against `accesstoken`'s PEM parsers when this was written |
| The rotation works in a real environment | **not yet shown**: no environment provisions these keys |
