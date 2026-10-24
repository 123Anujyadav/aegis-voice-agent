# Refresh-token controls: threat analysis

- **Question (Phase 19, task 5):** would further refresh-token controls
  (rate limiting, changes to reuse handling) reduce real risk?
- **Base:** checked against the code on `main` at `c6cecf9`.
- **Outcome:**
  - rate limiting on refresh is **not** recommended (§2);
  - the reuse-handling improvement in §3, first recorded for later, is
    **implemented** (2026-10-24, after `18d0112`): only a token the session
    actually held is reuse.

## 1. What a refresh needs today

`POST /v1/token/refresh` (`RefreshTokenWithDeviceProof`) succeeds only when
**all** of the following hold:
1. The session exists, is not revoked and has not expired. The lifetime is a
   fixed 24 h.
2. The refresh token is the **current** one: 32 random bytes, stored only as an
   HMAC and compared in constant time.
3. The session is bound to a device key.
4. A DPoP proof is fresh and not replayed, and is signed by **that** enrolled
   key. The key is generated on the device and cannot be exported.
5. The rotation's compare-and-swap commits.

Every refused path issues nothing, and a proof failure does not rotate the
token. Tests cover this: `TestHTTP_RefreshProofFailuresRotateNothing`,
`TestHTTP_UnenrolledSessionsCannotRefresh` and #124.

## 2. Rate limiting refresh: not recommended

ADR-0010 §10 lists refresh among the rate-limited endpoints. Against this
design, a rate limit would not protect anything:

| Threat | Why a rate limit adds nothing |
|---|---|
| Guessing a refresh token | 256 bits. Guessing is infeasible at any rate. |
| Guessing a session id | `sess_` plus 256 bits. Infeasible. |
| A stolen refresh token, without the device key | Refused at step 4 however often it is tried |
| Load caused by an attacker | Each attempt costs one session read, plus one replay write if it gets as far as the proof. This is the same cost as every protected edge-api request, which has no per-request limit either. Overload is a capacity and per-source question (task 6), not a per-session one. |
| A broken client in a retry loop | It harms only its own session. A per-session limit would make that client's recovery slower, not safer. |

Adding one would mean a new state table and more tuning, for no reduction in
risk. **This follows the Phase 19 directive: do not add low-value rate
limits.**

**Limiting per source address** is a different control. It stays blocked
until the trusted-proxy model is verified: how the ALB sets `X-Forwarded-For`
must be confirmed by SRE. Until then, forwarding headers are not trusted.

## 3. Reuse handling: the garbage-token revocation (implemented)

**Current behaviour, deliberate and documented in step 3 of
`RefreshTokenWithDeviceProof`:**
- **Rule:** a refresh token that is not the current one counts as **reuse**.
  The session is revoked before the proof is checked, so a stale token
  revokes the session whatever proof comes with it.
- **Source:** this follows RFC 9700 §4.14.2. When a refresh token is
  presented by both an attacker and the legitimate client, one of them holds
  a superseded token, and revoking the session cuts off both.

**The limitation:**
- The service stores only the *current* hash, so it cannot tell a token it
  really issued (and has since rotated) from **garbage**.
- Anyone who knows a session id can therefore revoke that session by sending
  any token, with no proof and no stolen token.
- The legitimate subscriber then has to enrol again with an OTP.

**How real is it:**
- The session id is a 256-bit value that only the client holds.
- It is returned once at enrolment and sent in each refresh body.
- It is not in the access token, edge-api never sees it, and identity does
  not log it.
- Exploiting the limitation needs the session id to leak without the refresh
  token: for example from client-side crash reports or analytics, or a
  support transcript.
- **The impact is denial of service against one subscriber, who recovers by
  re-enrolling.** No data is exposed and no access is gained.

**The improvement, for later:**
- **Record:** per session, the hashes of the refresh tokens it has issued (at
  most about 100 a day at one refresh every 15 minutes), pruned with the
  session.
- **New rule:** a token that matches a *previously issued* hash is reuse and
  revokes the session, as now. A token that matches no issued hash is refused
  with 401 and **revokes nothing.**
- **Effect:** the RFC 9700 signal is kept exactly, and the garbage-token
  revocation goes away.

**Implemented (2026-10-24).** This was first deferred until after ADR-0030
D30.2, so that history rows would carry key ids. On review that dependency
does not hold: a superseded-token row never outlives its session, the session
lifetime is 24 h, and the rows are deleted with the session. So no row ever
needs re-keying, and there was no reason to wait.
- **Storage:** migration `0007_superseded_refresh_tokens`. The rotation's
  compare-and-swap records the hash it replaces, in the same transaction.
- **Rule:** `refreshMismatch` revokes only for a recorded hash. A hash never
  held is 401 and revokes nothing.
- **When the store cannot answer:** the mismatch is treated as reuse
  (revoking is the safe failure).
- **During rollout:** a token superseded *before* the migration was never
  recorded, so for at most one session lifetime it is refused without
  revoking. That is the old garbage case, not a security regression.

**Revisit trigger:** session ids are found in any client log or telemetry, or
reuse-revocation counts (`identity_security_events_total{type="identity.refresh.token_reused"}`)
rise with no matching compromise.

## 4. What this analysis does not claim

**Passing tests do not prove that the refresh path is secure.** Tests show
that the specific refused cases are refused. The external review recommended
in [ADR-0029-DEVIATION-ANALYSIS.md](ADR-0029-DEVIATION-ANALYSIS.md) should
cover this path too.
