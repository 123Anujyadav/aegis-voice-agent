# ADR-0031: Play Integrity at token issuance: request binding and tier recording

- **Status:** **Proposed: decided under owner delegation (2026-10-24), owner
  ratification pending.**
  - These are Claude's engineering decisions, made under the owner's standing
    delegation, about *how* to carry out what the accepted ADR-0010 §5(b)
    already requires. They are **not** an owner-accepted ADR.
  - **What the restricted tier may do is NOT decided here.** It stays the
    owner's product policy.
- **Date:** 2026-10-24
- **Deciders:** Claude, under the repository owner's standing delegation;
  ratification by the repository owner
- **Informed:** Android; Security; SRE (Google Cloud project, quota)
- **Depends on:** ADR-0010 §5(b), ADR-0029 (DPoP), #108 and #118 (the verifier
  port, the offline policy, the outcome metrics)

## Context

**ADR-0010 §5(b), accepted, requires:**
- "The client requests a Play Integrity verdict and presents it at **token
  issuance and refresh**";
- the backend verifies it server-side;
- "A failed verdict does not necessarily reject: it downgrades the session to
  a restricted tier and raises a signal";
- devices without Play Services "get the restricted tier rather than a hard
  rejection".

**What exists on `main`:**
- `IdentityService.VerifyPlayIntegrity`, a fail-closed port with no Google
  implementation;
- `IntegrityPolicy.Assess`, which returns certified or restricted with
  reasons from a closed list;
- outcome and restriction metrics.

**Nothing calls the verifier on the issuance path,** and the HTTP API has no
way to carry a token. The Phase 19 review recorded this as the main ADR-0010
§5(b) gap.

**Three things are missing before this can be wired in:**
1. what the verdict is bound to;
2. how the token travels;
3. what the session records.

They are engineering mechanics, not product policy, so they are decided here.

## Decisions

### D31.1: the verdict is bound to the DPoP proof of the same request

**Use a standard Play Integrity request, with
`requestHash = base64url(SHA-256(DPoP proof))`.** The hash is taken over the
proof exactly as it appears in the request's `DPoP` header (its ASCII bytes);
the result is 43 characters, well under Google's 500-character limit.

**The client's order of work:**
1. build the DPoP proof;
2. request an integrity token with that hash;
3. send both.

**The server's check:**
1. recompute the hash from the `DPoP` header it received;
2. pass it as `IntegrityBinding.RequestHash`.

`Assess` already restricts a verdict whose hash does not match
(`request_hash_mismatch`).

**What this binding gives:**
- **The device key:** the proof carries the public JWK.
- **This exact request:** `htm`, `htu`, `iat` and `jti`.
- **Single use:** a replayed proof is refused before integrity is looked at.

**There is nothing new to store and no nonce endpoint.**

**Rejected:**
- *A server-issued nonce.* It costs a round trip and needs server state, and
  it binds nothing the proof does not already bind.
- *A hash of the request body.* The body does not identify the device key.

### D31.2: the token travels in an `Integrity-Token` request header

- **Where it goes:** the `Integrity-Token` header, on
  `POST /v1/otp/validate` and `POST /v1/token/refresh`. It is optional; §D31.3
  says what its absence means.
- **Why a header and not a body field:** tokens can be several kilobytes
  (`maxIntegrityTokenSize` is 16 KiB), and the JSON body limit stays at 1 KiB.
  This follows the `DPoP` header's precedent.
- **Repeats and size:** a repeated header is refused, exactly as a repeated
  `DPoP` header is. A header over 16 KiB is invalid.
- **Privacy:** the token is never logged, stored, traced or echoed.
  `DeviceAttestation.IntegrityToken` is not populated.

### D31.3: what each outcome does at issuance

| Outcome | Session tier | Issuance | Signal |
|---|---|---|---|
| A certified verdict | `certified` | Proceeds | Outcome metric |
| A restricted verdict (any `Assess` reason) | `restricted` | **Proceeds** (ADR-0010: "does not necessarily reject") | Outcome and reason metrics, **plus an audit event** `identity.integrity.restricted` |
| No `Integrity-Token` header | `unverified` | Proceeds | Outcome metric `not_presented`, a new value in the closed vocabulary. It is distinct from `invalid_request`, which stays for a malformed token or binding. |
| No verifier configured, a Google outage, or a decode timeout | `unverified` | Proceeds | Outcome metric (`unavailable`) |
| A token Google **rejects** (forged, malformed, another app) | — | **Refused:** 400 `ERR_INVALID_INTEGRITY_TOKEN`. No session or token is issued. | Audit event `identity.integrity.rejected` |

**Why `unverified` is separate from `restricted`:**
- When the restricted tier gets its permissions, `unverified` sessions are to
  be treated *as restricted* (ADR-0010 §5(b): no verdict is not a pass).
- They are recorded separately so that outages and devices without Play
  Services never inflate the "failed verdict" signal. ADR-0010 has a revisit
  trigger at 5% verdict failures, and conflating the two would set it off
  falsely.

**Why a rejected token refuses:**
- This is the boundary #108 already established: a token that does not decode
  "IS an error: there is no verdict to downgrade".
- Presenting a forged token is an active signal, not a missing capability.

**Order inside enrolment:** the integrity check runs **after** the OTP code
and the proof have been verified, and before the session is created.
- **Google quota is only spent on requests that have already proved the
  number and the key.**
- **The cost:** a rejected token after a correct code spends that challenge,
  and the client must request a new code. This is acceptable for a forged or
  broken token.

### D31.4: the session records its tier

**Migration:** a forward migration adds
`identity_sessions.integrity_tier TEXT NOT NULL DEFAULT 'unverified'`, with
`CHECK (integrity_tier IN ('certified','restricted','unverified'))`.

**Existing sessions** become `unverified`, which is the truth: none was ever
assessed.

**Refresh:**
- A refresh that carries a token is assessed by the same table, and the tier
  is updated within the same compare-and-swap that rotates the refresh token.
- A refresh with no token keeps the session's tier.
- A rejected token at refresh is refused with nothing rotated, like any other
  refresh failure.

**Quota.** Google's default is 10,000 integrity requests per app per day.
Refreshing every 15 minutes is about 96 refreshes per subscriber per day, so
sending a token on *every* refresh would exhaust the default quota at about
100 active subscribers.
- **Clients attach a token at enrolment, and on refresh at most once every
  6 hours.**
- That interval is a client constant, not a server rule: the server accepts
  a token whenever one is sent.
- SRE must request a quota increase before launch.

### D31.5: recorded, not enforced, until the owner sets the tier's permissions

**What this ADR does NOT do:**
- put the tier in the access token;
- make edge-api check it;
- change what any session may do.

**Why:**
- What a `restricted` (or `unverified`) session may do is product policy.
  ADR-0010 leaves it open, and the Phase 19 tracker lists it as an owner
  decision.
- Enforcing anything before that decision would be choosing it.

**When the owner decides:**
- the tier is carried to edge-api as a role, or as a claim through ADR-0029's
  two-step claim rollout;
- the decided routes check it.

**Until then:** the tier is a recorded, audited and counted signal, which is
exactly ADR-0010's "raises a signal".

## Consequences

- **Android work:**
  - compute the hash over the DPoP proof;
  - make a standard Play Integrity request;
  - send the `Integrity-Token` header at enrolment, and at refresh at most
    every 6 hours.

  This is not in this repository's scope; it is tracked by the Android owner.
- **Production stays as it is today until a Google verifier exists.** Every
  session is `unverified`, issuance is not blocked, and the outcome metric
  reads `unavailable`. That is the honest state.
- **Tests can cover the whole table offline,** using the existing fake
  verifier: certified, restricted, missing, unavailable, rejected, a hash
  mismatch, and refresh.
- **A new 400 code,** `ERR_INVALID_INTEGRITY_TOKEN`, is added to the closed
  problem vocabulary and counted by `identity_auth_requests_total`.

## Revisit triggers

- **The owner decides the restricted tier's permissions:** carry the tier to
  edge-api (D31.5).
- **Google changes the standard request's `requestHash` semantics or limits.**
- **Quota use above 50% of the granted daily quota.**
- **More than 5% of verdicts are restricted** (ADR-0010's own trigger). Count
  only `restricted`, not `unverified`.
