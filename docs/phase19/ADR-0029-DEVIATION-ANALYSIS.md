# ADR-0029: analysis of the four deviations

- **For:** the repository owner, before ratifying
  [ADR-0029](../adr/0029-access-token-and-proof-of-possession.md).
- **Compares** ADR-0029 against what the owner required earlier: the D11.3
  record, [ADR-0010](../adr/0010-authentication-and-device-trust.md) §5(c),
  §9 and §10, and [ADR-0015](../adr/0015-postgresql-driver-pgx.md).
- **Checked against** the code on `main` at `d764117`.
- **Does not ratify anything.** ADR-0029 stays *Proposed: decided under owner
  delegation, owner ratification pending.* Only the owner can accept it.
- **Brief and earlier review:**
  [ADR-0029-RATIFICATION-BRIEF.md](../phase18/ADR-0029-RATIFICATION-BRIEF.md)
  and [ADR-0029-REVIEW.md](../phase18/ADR-0029-REVIEW.md).

> **Tests are not proof of security.** The tests named below show that the
> implementation refuses the specific inputs they try, and the mutation checks
> show those tests would notice the named regressions. They do not show that
> the design has no flaw nobody has thought to test. The only review so far is
> one independent, read-only review done inside this same process (ADR-0029
> §6). **No external cryptographic review or penetration test has been done**,
> and production issuance should not be treated as reviewed until one has.

## Summary

| # | Deviation | Recommendation | Residual risk the owner accepts by ratifying |
|---|---|---|---|
| 1 | A standard-library fixed-profile JWS instead of the JWT library D11.3 asked for | **Keep it.** Ratify it explicitly, as a reversal of the owner's own D11.3 requirement. Get an external review before production. | The repository owns the parser's correctness. Today only in-house review and tests stand behind it. |
| 2 | A mounted key set instead of a published JWKS | **Keep it.** | Key rotation is a coordinated rollout, and the runbook for it has never been run. Only one previous key, so rotations cannot overlap. |
| 3 | DPoP on every protected request, not just refresh and mutating requests | **Keep it.** Record that edge-api now depends on PostgreSQL being available. | A PostgreSQL outage makes every protected request return 503. Each request costs one database write. |
| 4 | `jti` replay tracking in PostgreSQL instead of Redis nonces | **Keep PostgreSQL. Do not add server nonces yet.** | A proof generated in advance can be used once within about 3 minutes. Throughput is limited by PostgreSQL's write rate. |

**A new finding outside the four deviations:** the access token is typed
`at+jwt` but does not include RFC 9068's required `client_id` claim. See §5.

---

## 1. Standard-library fixed profile vs. a JWT library

**What was required:**
- The owner's D11.3 record said: "an ADR authorising a JWT library, since
  hand-rolling JWT verification is not acceptable for a security boundary".
- ADR-0029 D11.3 decides the opposite.

**What the threat is.** The JWT attacks that have actually happened mostly
come from *flexibility in the parser*, not from bad cryptography:

| Attack class | Example | How the fixed profile handles it | Evidence |
|---|---|---|---|
| `alg: none` / algorithm confusion | Signing with HS256, using the public key as the HMAC secret | The header must have exactly the expected members. One algorithm per token type: EdDSA for access tokens, ES256 for proofs. | `accesstoken` and `dpop` refusal tests |
| Key injection | `jwk`, `jku` or `x5u` in an access-token header | Any member that carries a key is refused. Keys come only from the configured set. | Refusal tests. `kid` is only used to look up an in-memory map. |
| Treating one token type as another (RFC 8725 §3.12) | A DPoP proof presented as an access token, or the reverse | Each type has a different `typ`, algorithm and key source. Each verifier refuses the other's token. | `dpop/crosstype_test.go` (#123), mutation-checked: removing the `typ` check fails a test |
| Parser differentials | Duplicate member names, or names that differ only in case (Go's `encoding/json` accepts both) | A strict JSON decoder refuses both. | Strict-JSON tests (condition (a)) |
| Non-canonical encodings | Padded base64url, or unusual alphabets | Only strict, unpadded base64url is accepted. | Refusal tests |
| Memory exhaustion while parsing | CVE-2025-27144 (go-jose: a token with many `.` characters) and CVE-2025-30204 (golang-jwt: excessive memory while parsing the header) | The size limit is checked **before** splitting: `MaxTokenBytes` and `MaxProofBytes`, both 4096 bytes (`accesstoken.go:554`, `dpop.go:295`). | `FuzzVerify` in both packages |
| Signature-check bugs | Accepting a signature with r = s = 0 (the CVE-2022-21449 class, in Java) | Standard-library `crypto/ed25519` and `crypto/ecdsa` do the verification. Only the raw 64-byte R‖S form is accepted; DER is refused. | RFC 8037 A.4 and RFC 9449 test vectors |

**RFC references:**
- RFC 7515 (JWS) and RFC 8037 (EdDSA in JOSE);
- RFC 8725 (JWT best practice): §3.1 (check the algorithm), §3.11 (explicit
  typing) and §3.12 (mutually exclusive validation rules);
- RFC 7638 (key thumbprints).

**Arguments for a library:**
1. Many people have reviewed it.
2. Someone else maintains it and publishes security fixes.
3. This repository's parser has had only in-house review.

**Arguments against a library:**
1. A library carries a larger attack surface: JWE, compression, many
   algorithms, remote key fetching.
2. Using it safely depends on every unwanted feature being turned off.
3. Its own CVEs become ours: the two advisories above are recent examples from
   the two main Go libraries. Both are already excluded by construction here.

**What it would cost to operate:**
- **Fixed profile:** none at runtime. We have to keep the parser correct
  ourselves.
- **Library:** dependency upgrades, and tracking its advisories in
  govulncheck.

**Migration and rollback:**
- Both choices sit behind the same API: `accesstoken.Signer` and `Verifier`,
  and `dpop.Verifier`.
- Moving to a library would only change the parser. The tokens on the wire
  would be byte-identical, so the rollout carries no risk to tokens already
  issued.
- Rolling back is the same change in reverse.

**Recommendation:**
- **Keep the fixed profile.** For the attack classes that have actually
  occurred, a parser that accepts nothing it does not need is the safer
  design.
- The owner should ratify this *knowingly*, as a reversal of their own
  requirement.
- **Commission an external review** of `packages/go/accesstoken` and
  `packages/go/dpop` before production issuance. This is the single most
  valuable remaining assurance step, and it is the owner's to commission (it
  costs money).
- Keep revisit trigger (d): if the profile ever has to support more, adopt a
  library through its own ADR.

## 2. Mounted key set vs. JWKS

**What was required.** ADR-0010 §9 says: "Publish a JWKS with overlapping
keys".

**What was decided.** edge-api reads its public keys from a mounted file:
- the active key, which is required;
- at most one previous key;
- an unknown `kid` is refused.

**What the threat is:**

| Threat | Remote JWKS | Mounted key set |
|---|---|---|
| An attacker controls where keys are fetched from (DNS, TLS, SSRF, a compromised endpoint) | A real path to the trust root: a key served by an attacker becomes trusted | **No such path.** Nothing is fetched over the network. |
| The key endpoint is unavailable | Needs caching and a decision about stale keys. Fails open or fails closed depending on configuration. | Not applicable. |
| Revoking a compromised key | Fast: publish a new set, and caches pick it up after their TTL | **A rollout:** redeploy edge-api without the key. It takes as long as a deployment. |
| An extra key is added by mistake | Anyone who can write to the endpoint | Anyone who can write to the Kubernetes Secret. Narrower, and already covered by RBAC. |

**Operational consequences:**
- Rotation means two coordinated rollouts. They are documented in
  [the rotation runbook](../runbooks/access-token-signing-key-rotation.md),
  and #119 added key fingerprints so an operator can check what each side
  holds.
- **The runbook has never been run in a real environment.** The verifier
  logic is tested (`TestRotation_OverlapProcedureNeverRejectsAValidToken`,
  mutation-checked). The operation itself is not.
- With only one previous key, a second rotation cannot start until the first
  has finished (step 4 of the runbook).

**Migration and rollback:**
- A JWKS can be added later without changing any token. It would be a new key
  source behind the same `kid` lookup.
- Nothing needs migrating.

**Recommendation:**
- **Keep the mounted key set.** Both services are first-party and deployed
  together, and removing the network fetch removes a real attack path.
- **Before production, the owner should require a staging run of the rotation
  runbook.** That needs a staging environment and a Kubernetes Secret, which
  only the owner or SRE can provision.

## 3. DPoP on every request vs. the narrower requirement

**What was required.** ADR-0010 §5(c): "every subsequent token refresh, and
every mutating request" must carry a signature.

**What was decided.** A proof is required on **every** protected request,
including reads, and on enrolment and refresh.

**What the threat is:**
- If proofs were required only on mutating requests, a stolen access token
  would still be a bearer token for every read.
- In this product, reads are the sensitive data: call screening transcripts,
  contacts and call history.
- An access token can leak through logs, a proxy, a crash report or a
  compromised TLS endpoint. Requiring a proof everywhere makes a leaked token
  useless without the device key, which stays on the device.
- RFC 9449 §7 binds a proof to each request, with no read/write distinction.
  The narrower scope was an ADR-0010 choice, not an RFC one.

**Implementation:**
- `services/go/edge-api/internal/auth`: the proof's key must match the token's
  `cnf.jkt` before any replay reservation, and `ath` must match the token.
- `htm` and `htu` must match exactly.

**Tests:**
- RFC 9449 example proofs;
- `TestE2E_IdentityTokenAdmittedByEdgeThroughTheRealLifecycle` (real
  PostgreSQL, in CI);
- the type-confusion tests (#123);
- metrics by outcome (`edge_api_auth_requests_total`).

**Operational consequences. These are the material ones:**
1. **edge-api depends on PostgreSQL being available.**
   - Every protected request writes one replay row to `identity_dpop_replay`,
     with a 2-second timeout (`pgshared.ReserveTimeout`).
   - If the database cannot answer, the request fails closed with **503
     `ERR_TEMPORARILY_UNAVAILABLE`**. This is the correct security behaviour,
     but it means a PostgreSQL outage takes down the whole authenticated API,
     reads included.
   - Under ADR-0010's narrower scope, reads would have survived an outage.
2. **The client signs every request.** That is one ES256 signature on the
   device. It is cheap in software. With a hardware-backed key (TEE or
   StrongBox) it can take tens of milliseconds, which the Android client
   should measure.
3. **One database write per request.** A reservation is held until `iat` +
   2 minutes + 1 second (`dpop.go:419`). With up to 60 seconds of future skew,
   that leaves at most about RPS × 3 minutes of live rows, plus whatever
   housekeeping has not pruned yet.

**Migration and rollback:**
- **Narrowing later** (exempting reads) would be a configuration or route
  change at edge-api. Clients that sign everything would keep working.
- **Widening later**, from narrow to everywhere, would break clients that do
  not sign reads.
- So the current choice is the one that is easy to change in either direction.

**Recommendation:**
- **Keep DPoP on every request.**
- **The owner should explicitly accept the PostgreSQL dependency.** Its
  mitigation is ordinary database availability engineering: multi-AZ and
  connection-pool limits.
- Track, as a separate SRE item, an alert on edge-api's 503 rate. The counter
  already exists.

## 4. `jti` replay in PostgreSQL vs. Redis nonces

**What was required. ADR-0010 §10:**
- "PoP signatures cover a nonce and a timestamp; `edge-api` rejects signatures
  outside a narrow window and tracks recently-seen nonces";
- "Nonce tracking for replay protection is in Redis (ADR-0009)".

**What was decided:**
- **The nonce is the client-chosen `jti`.** Its `iat` must fall within
  [now − 2 min, now + 60 s].
- **`(jkt, jti)` is reserved atomically:** a single
  `INSERT … ON CONFLICT … WHERE expires_at <= now` statement
  (`pgshared/pgshared.go:177`).
- **There are no server-issued nonces** (RFC 9449 §8).

Two separate questions are bundled here.

### 4a. Where seen values are stored: PostgreSQL vs. Redis

| Property | Redis | PostgreSQL (as built) |
|---|---|---|
| Durability of a reservation | Depends on configuration. With asynchronous replication, a failover can lose recent keys, **so a replay window reopens.** With no persistence, a restart forgets everything. | Committed before the request is admitted. It survives a failover when replication is synchronous. |
| Atomic check-and-set | `SET NX PX` | `INSERT … ON CONFLICT` on the primary key |
| New infrastructure | A Redis deployment for edge-api, which also needs TLS and auth | None. It uses the database and the `verify-full` TLS that ADR-0029 already requires. |
| Latency and throughput | Lower | Higher. One write per request; it scales with PostgreSQL write capacity. |
| Housekeeping | TTL expiry, built in | Identity's housekeeping prunes old rows. **Correct without pruning:** an expired row is taken over on conflict. Only storage grows if identity is down. |

ADR-0015 already selects pgx as the PostgreSQL driver. Using PostgreSQL adds
no new component, and its consistency is easier to reason about than Redis'
default replication.

### 4b. Client `jti` vs. server nonces

| Threat | Without server nonces (as built) | With server nonces |
|---|---|---|
| Replaying a captured proof | Refused: the `(jkt, jti)` is already reserved | Refused |
| An attacker who **can make the device sign** generates proofs to use later | A proof is valid for up to about 3 minutes after its `iat` | Limited to the nonce's lifetime, which the server controls |
| An attacker who has **extracted the device key** | Not mitigated: they can create fresh proofs | Not mitigated either |
| Device clock skew | Requests fail outside the window (fail closed) | Same, unless the nonce is used for freshness instead of `iat` |

**Cost of server nonces:**
- an extra round trip whenever a nonce expires (the `DPoP-Nonce` challenge);
- server-side nonce state or a keyed-MAC nonce scheme;
- client retry logic.

The gain is narrow. It only helps against an attacker who already has
signing access on the device. That attacker can usually act directly anyway,
and nonces would shorten their window from about 3 minutes to the nonce's
lifetime.

**Migration and rollback:**
- Moving the replay store to Redis means changing the replay-store
  implementation behind the `dpop.ReplayStore` interface. That is safe at
  runtime as long as both stores are checked during the switch-over window
  (about 3 minutes).
- Adding nonces is additive under RFC 9449 §8: the server starts sending
  `DPoP-Nonce`. It needs a client release first.

**Recommendation:**
- **Keep PostgreSQL. Do not add server nonces now.**
- Revisit if either:
  - load measurements show the replay write limits throughput (then consider
    Redis with persistence plus synchronous replication); or
  - a pre-generated-proof attack becomes practical for this client (ADR-0029
    revisit trigger).

## 5. New finding: the access token does not conform to RFC 9068

**The gap:**
- RFC 9068 §2.2 lists `client_id` as a **required** claim in a JWT access
  token typed `at+jwt`.
- The tokens identity issues carry exactly iss, aud, sub, iat, nbf, exp, jti,
  cnf and roles (`accesstoken.go:91-99`). They have **no `client_id`**.
- The edge-api verifier accepts exactly those claims, so a token that
  contained `client_id` would be **refused**.

**Risk:**
- **Security risk: none today.** There is one first-party client, and nothing
  makes a decision based on the client.
- **Interoperability risk:** the token claims to be RFC 9068 (`typ: at+jwt`)
  but is not. A third-party resource server or gateway that enforces RFC 9068
  would refuse it.

**Options:**
1. **Record the profile as "RFC 9068-derived".** This is a documentation-only
   amendment to ADR-0029.
2. **Add `client_id`.** This must be a two-step rollout, because the verifier
   is strict:
   1. edge-api accepts an optional `client_id` whose value is exactly a
      configured one;
   2. once every edge-api replica runs that version, identity starts emitting
      the claim;
   3. optionally, edge-api then requires it.

   Rolling back is the same steps in reverse.

**Recommendation:**
- **Option 1 now, as part of ratification.**
- Option 2 only if a third party ever has to consume these tokens.

This is **not implemented under delegation**, because either option changes
the exact claim set that ADR-0029 D11.3 fixes. That is an amendment for the
owner to accept.

## 6. Other material risks the owner should see

1. **No external review.** See the box at the top. This is the largest
   residual risk.
2. **Revoking an access token is not immediate.** A revoked session's access
   token stays valid for up to 15 minutes plus 60 seconds. This was ADR-0010's
   accepted design; ADR-0029 restates it.
3. **Play Integrity verifies nothing yet** (ADR-0010 §5(b)).
   - Since #131 it is assessed at issuance (ADR-0031).
   - There is no Google verifier, and the tier is not enforced.
   - So an app that is not genuine, holding a valid OTP, can still enrol, as
     `unverified`.
   - Fixing this needs Google setup that only the owner can provide, and the
     owner's decision on what the restricted tier may do.
4. **The rotation runbook has never been exercised** (§2).
5. **edge-api's availability now depends on PostgreSQL** (§3).

## 7. What ratification would mean

**If the owner ratifies ADR-0029 with this analysis:**
- **Accepted, explicitly:**
  - deviation 1, as a reversal of D11.3;
  - deviations 2 and 3, as amendments to ADR-0010 §9 and §5(c);
  - deviation 4, as an amendment to ADR-0010 §10;
  - §5 option 1.
- **Unchanged:** the code.
- **Not authorised:** production, keys, infrastructure or spending.
- **Still open:** an external review (§1), a staging run of the rotation
  runbook (§2), and the ADR-0010 gaps listed in
  [ADR-0029-REVIEW.md](../phase18/ADR-0029-REVIEW.md) §2.
