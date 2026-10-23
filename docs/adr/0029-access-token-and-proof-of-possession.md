# ADR-0029: Access-token and proof-of-possession profile (resolves D11.2–D11.5)

- **Status:** **Proposed: decided under owner delegation (2026-10-23), owner
  ratification pending.** The owner delegated in-scope engineering and policy
  decisions. The decisions below are Claude's, made under that delegation, and
  are binding on implementation until the owner amends or rejects them. They
  are **not** an owner-accepted ADR.
- **Date:** 2026-10-23
- **Deciders:** Claude, under the repository owner's standing delegation;
  ratification by the repository owner
- **Consulted:** an independent read-only security review of the
  implementation (2026-10-23), summarised in §6
- **Informed:** Security; SRE (key material, database TLS)
- **Amends:** ADR-0010 §5(c), the proof-of-possession mechanism; see D11.4
- **Depends on:** ADR-0008, ADR-0010, ADR-0012, ADR-0015

---

## Context

The Phase 18 D11 record (owner working tree, 2026-08-23) left four
authentication decisions open, and blocked real authentication on them:
- **D11.2:** the access-token model;
- **D11.3:** the key source, rotation, issuer, audience, `cnf` contract, clock
  skew, and "an ADR authorising a JWT library, since hand-rolling JWT
  verification is not acceptable for a security boundary";
- **D11.4:** the proof-of-possession protocol;
- **D11.5:** `identity`'s issuer, which must change together with the
  verifier.

Seven weeks later (2026-10-12) the owner's working tree implemented concrete
answers:
- `packages/go/accesstoken`: EdDSA access tokens;
- `packages/go/dpop`: RFC 9449 DPoP proofs;
- `packages/go/pgshared`: the PostgreSQL replay store;
- a signed issuer in `identity`, and a verifier in `edge-api`.

No record decided them. This ADR does.

## Decisions

### D11.2: token model

**Decided: signed, self-contained access tokens, verified locally. Refresh
tokens are opaque and server-side.** This is ADR-0010 §5 as accepted.

- **Access token:** a JWS with header exactly
  `{"alg":"EdDSA","typ":"at+jwt","kid":…}` and Ed25519 signatures.
  - Claims are exactly iss, aud (a single string), sub, iat, nbf, exp, jti,
    `cnf.jkt` and roles.
  - Lifetime: 15 minutes (`exp = iat + 15m`, `nbf = iat`), at most 4 KiB.
  - It is verified in `edge-api` with no call back to `identity`, as ADR-0010
    §9 requires.
- **Refresh token:** opaque and random. It is stored only as
  HMAC-SHA-256(identity key, token), rotates on every use, and reuse revokes
  the session family (ADR-0010 §5).
- **Device binding:**
  - An unenrolled session, one with no device key, receives **no access
    token**.
  - A device-bound session refreshes only with a DPoP proof made by the bound
    key.
- **The legacy unsigned `Bearer_JWT.<hex json>` token** on `main` is removed in
  the same change that introduces the verifier (D11.5). No release ever
  accepts it.

### D11.3: implementation, keys and parameters

**Decided: the standard-library implementation of a fixed, narrow profile. No
JWT library.** This is the case D11.3 asked an ADR to settle, and the
rationale is a security one, not a preference for avoiding dependencies:

1. **The cryptography is not hand-rolled.**
   - Signatures use `crypto/ed25519` and `crypto/ecdsa` (P-256), and digests
     use `crypto/sha256`, all Go standard library.
   - The repository's own code does encoding, size and shape checks, and
     policy.
2. **The classic JWT-library vulnerabilities are excluded by construction,**
   not by configuration:
   - **Algorithm confusion and `alg: none`:** the header must be byte-equal in
     members, and exactly one algorithm is accepted per token type.
   - **Key injection** (`jwk`, `jku` or `x5u` in an access token): no
     key-bearing header member is accepted.
   - **`kid` path traversal or lookup injection:** `kid` is only a key into an
     in-memory map, built at start-up.
   - **`crit` and extension members:** refused.
   - **Non-canonical encodings:** strict, unpadded base64url only.

   A general-purpose library has to support each of these features, and secure
   use depends on configuring every one of them away. A fixed profile cannot
   accept what it does not parse.
3. **The protocol surface is small and fully specified here,** so a reviewer
   can check it against the RFCs. That check was made (§6).

**Conditions.** These are part of the decision, and the implementation PR must
satisfy them:
- **(a) Strict JSON.** Duplicate member names, and member names that match a
  known member only case-insensitively, are rejected. Go's `encoding/json`
  accepts both, which is a parser differential against other JOSE
  implementations.
- **(b) RFC conformance vectors** in tests:
  - the RFC 8037 Ed25519 JWS example;
  - the RFC 7638 thumbprint computation;
  - RFC 9449 proof structure, including `ath = base64url(SHA-256(token))`.
- **(c) Fuzz tests** for both token parsers: they never panic, and anything
  they accept round-trips.
- **(d) Revisit trigger:** if the profile ever has to widen (more algorithms,
  JWE, remote JWKS fetching, nested tokens), adopt a maintained library
  through its own ADR, rather than extending the hand-written parser.

**Keys and rotation. Decided:**
- **Signer:** `identity` signs with exactly one active Ed25519 key, read from a
  file (a Kubernetes Secret volume). Its `kid` goes in every token header.
- **Verifier:** `edge-api` accepts a **set** of public keys, keyed by `kid`:
  - the active key, required;
  - at most one previous key, optional.

  An unknown `kid` is refused.
- **Rotation procedure** (overlap without an outage):
  1. Deploy `edge-api` with the new public key as the active key and the old
     key as the previous key.
  2. Switch `identity` to the new private key and `kid`.
  3. Wait at least the 15-minute token lifetime plus 60 seconds of skew.
  4. Remove the previous key from `edge-api`.
  5. Destroy the old private key.
- **Production gate:** **issuance is not production-ready with a single-key
  verifier.** The key-set verifier, and a test of that exact rotation
  sequence, are required before any production issuance.
- **No remote JWKS endpoint.** ADR-0010's "publish a JWKS" becomes a
  **mounted key set**. Both services are first-party and deployed together, so
  a network-fetched JWKS would add an availability dependency and a fetch path
  to the trust root, with no benefit.

**Parameters. Decided:**
- **Issuer and audience:** one opaque string each, supplied as operator
  configuration (`ACCESS_TOKEN_ISSUER` and `ACCESS_TOKEN_AUDIENCE`). They are
  identical for `identity` and `edge-api`, and an exact match is required.
- **Clock skew:** 60 seconds, applied to nbf, exp and a future iat.
- **`cnf`:** `{"jkt": <RFC 7638 SHA-256 thumbprint of the device's EC P-256
  key>}`.
- **Roles:** the string list `identity` assigns. This ADR defines no new role
  vocabulary; authorisation semantics remain product policy.

**Database TLS. Decided:**
- **Required TLS:** every connection `identity` and `edge-api` make to the
  PostgreSQL that holds sessions and the replay store must use
  `sslmode=verify-full` with an explicit, absolute `sslrootcert` CA bundle.
  `sslrootcert=system` is not accepted, and no plaintext fallback is allowed.
- **The only exception** is when `ENVIRONMENT` is explicitly set to
  `development`. This mirrors the telephony gateway's existing rule (P1-37 O-5
  and R2).
- **Why:** without TLS, someone on the network path can forge replay answers
  and switch off replay protection.

### D11.4: proof of possession

**Decided: RFC 9449 DPoP, in the narrow profile `packages/go/dpop`
implements.** This **amends ADR-0010 §5(c)**, which said "a signature over the
request made with that private key" on refresh and on every mutating request:

- **Coverage is widened:** a proof is required on **every** request to a
  protected route, not only mutating ones, and on enrolment and refresh at
  `identity`.
- **Header and key:**
  - the header is exactly `{"typ":"dpop+jwt","alg":"ES256","jwk":…}`;
  - `jwk` is exactly `{kty:EC, crv:P-256, x, y}` with on-curve coordinates,
    and a private `d` or any other member is refused;
  - signatures are the raw 64-byte R‖S form, and DER is refused.
- **Claims at a resource server:** exactly jti, htm, htu, iat and ath.
  - `ath = base64url(SHA-256(access token))`, compared in constant time;
  - the proof key's thumbprint must equal the token's `cnf.jkt`, checked
    before any replay reservation.
- **Claims at enrolment and refresh:** exactly jti, htm, htu and iat. `ath` is
  refused.
- **`htm` and `htu`:** byte-exact against the method and the configured
  canonical `https` origin plus the escaped path. There is no URI
  normalisation, so this fails closed. Clients send the canonical form.
- **`iat` freshness:** within [now − 2 minutes, now + 60 seconds].
- **`jti`:** 1–128 bytes, restricted to printable ASCII.
- **Replay:**
  - `(jkt, jti)` is reserved atomically in PostgreSQL (`INSERT … ON CONFLICT`)
    until the proof can no longer be accepted;
  - a second presentation is refused;
  - a store that cannot answer fails closed (503);
  - the reservation has a bounded deadline;
  - expired rows are pruned by `identity`'s housekeeping runner, with a
    margin.
- **Server nonces** (RFC 9449 §8) are **not used**. The freshness window plus
  the replay store covers replay. Revisit if a pre-generated-proof attack is
  shown to be practical for this client.

### D11.5: issuer and verifier together

**Decided:**
- **One change:** the signed issuer in `identity` and the verifier in
  `edge-api` land in a single change, with the removal of the unsigned token.
  No published state has a verifier that rejects all issued tokens, or an
  issuer whose tokens nothing verifies.
- **Order of changes:**
  1. this ADR;
  2. `accesstoken`, `dpop` and `pgshared`, hardened;
  3. `identity` and `edge-api` together;
  4. edge tracing (ADR-0026), on top.

## Consequences

- **Positive:**
  - The authentication boundary has a written, reviewable profile.
  - A stolen access token is useless without the device key (`ath` plus
    `cnf.jkt`), and a stolen proof is useless twice.
  - No third-party code sits on the trust path.
  - Key rotation is defined, and gated in tests.
- **Negative:**
  - The repository owns the correctness of encoding and policy code. That is
    mitigated by conditions (a)–(c) and by the narrow profile.
  - Clients must produce byte-exact canonical `htu` values.
  - Every protected request costs one database round trip for replay.
- **Known deviations from ADR-0010, recorded and not hidden:**
  - **Session lifetime:** the session (refresh family) lifetime is currently
    **24 hours**, not ADR-0010's 90 days. That is stricter. Extending it is a
    product decision.
  - **Play Integrity (ADR-0010 §5(b)) is not verified against Google.** The
    verdict policy and a fail-closed verifier boundary exist, but no verifier
    is implemented, so `VerifyPlayIntegrity` returns "unavailable" and
    certifies nothing (see "Still open"). **This blocks production on its
    own**, independently of everything decided here.
  - **No OTP delivery provider exists.** No real user can enrol, per the
    owner's runbook.

## §6 Security review summary (2026-10-23)

An independent read-only review found **no critical or high issues**.

**Verified:**
- the algorithm is pinned, and all claims are checked;
- comparisons are constant-time;
- the replay reservation is atomic, with no check-then-insert race;
- checks run cheapest first, so the replay check comes last;
- inputs are size-capped;
- every failure fails closed.

**Findings the implementation must close before publication:**
- **Medium:** database TLS was not enforced. See "Database TLS" above.
- **Medium:** only a single trusted `kid`, so rotation could not overlap. See
  "Keys and rotation".
- **Low:** the replay reservation had no deadline.
- **Low:** a NUL byte in `jti` produced a 503. Restricted under D11.4.
- **Low:** a refused request (401) was not recorded anywhere; only 503s were
  logged. This finding was omitted from the first version of this summary and
  is added here.
- **Info:** a JSON parser differential. Condition (a).

## Implementation status (2026-10-23)

This records what has landed against the decisions above. It does **not**
change the Status line: the decisions remain owner-ratification pending.

**D11.5 order, all four steps merged:**
1. this ADR (#98);
2. `accesstoken`, `dpop` and `pgshared`, hardened (#99);
3. `identity` and `edge-api` together, with the unsigned token removed (#100);
4. edge tracing on top (#101, ADR-0026 step 2).

The real-PostgreSQL suites of both services run under `-race` in CI
(`identity-postgres`, #102). An unexpected skip fails that job.

**D11.3 conditions:**
- **(a) Strict JSON:** `accesstoken.DecodeStrictJSON` rejects duplicate member
  names and any member name outside `[a-z]+`, at any depth. `dpop` uses it.
- **(b) RFC vectors:**
  - the RFC 8037 A.4 Ed25519 JWS example (`accesstoken/conformance_test.go`);
  - RFC 9449's example proofs, `jkt` and `ath` (`dpop/conformance_test.go`).
    The RFC 7638 thumbprint is checked against RFC 9449's published `jkt` for
    its example EC key, because RFC 7638's own example is an RSA key.
- **(c) Fuzzing:** `FuzzVerify` exists in both packages.

**Keys and rotation:**
- **Implemented:** `edge-api` verifies with the active key plus an optional
  previous key (`ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE` and
  `ACCESS_TOKEN_PREVIOUS_KID`). Both are set or neither, the kid must be
  distinct, and the file must be its own and must not hold the active key.
- **Tested:** `TestRotation_OverlapProcedureNeverRejectsAValidToken` runs the
  five-step procedure above through the real middleware. It is
  mutation-checked: a verifier that ignores the previous key fails it.
- **The production gate's own condition is met.** Production issuance remains
  blocked for the reasons listed under "Still open" below.

**Database TLS:**
- **Implemented:** `pgshared.PolicyForEnvironment` is enforced by `identity`
  (service start and `identity migrate`) and by `edge-api`. A DSN that is not
  `verify-full` with an explicit CA bundle is refused before any connection,
  without echoing the DSN, unless `ENVIRONMENT` is explicitly `development`.
- **Tested:** refusal when `ENVIRONMENT` is unset, staging or production, in
  both services.
- **Tested against a real TLS-serving PostgreSQL** (#104, in the
  `identity-postgres` job):
  - `verify-full` with the right CA connects, and the server's `pg_stat_ssl`
    shows the connection encrypted with TLS 1.2+;
  - an unrelated CA is refused at the handshake;
  - a host name the certificate does not carry is refused at the handshake.

  A mutation that skips certificate verification fails both refusal tests.

**§6 findings:**

| Finding | Closed by |
|---|---|
| Medium: database TLS not enforced | #99 (`pgshared` policy) and #100 (both services) |
| Medium: single trusted `kid` | #100 (key-set verifier and rotation test) |
| Low: replay reservation without a deadline | #99 (`pgshared.ReserveTimeout`, 2 s) |
| Low: NUL in `jti` gave a 503 | #99 (`jti` restricted to printable ASCII, 1–128 bytes) |
| Low: 401 refusals unrecorded | #105: `edge_api_auth_requests_total{outcome}` at `GET /metrics` on the health listener. Every decision is counted under a closed outcome vocabulary. It is a counter, not a per-request log, so request volume cannot become log volume. |
| Info: JSON parser differential | #99 (condition (a)) |

**Still open:**
- **Owner ratification** of this ADR.
- **Play Integrity is not verified against Google**, which blocks production on
  its own.
  - **Fixed:** the fabricating stub is gone. Without a configured
    `IntegrityVerifier`, `VerifyPlayIntegrity` fails closed and certifies
    nothing.
  - **Implemented and tested offline:** the ADR-0010 §5(b) verdict policy (app
    recognition, signing certificate, device integrity, licensing, request
    binding, freshness), with the restricted tier on failure.
  - **Live verification needs:**
    - a Google Cloud project linked to the app in Play Console, with the Play
      Integrity API enabled;
    - a credential for `decodeIntegrityToken`, provisioned by SRE;
    - egress to Google;
    - the client binding its standard request to a server-chosen hash.
  - **Wiring the tier into issuance and refresh** is a product decision. No
    restricted-tier model exists yet.
- **No OTP delivery provider exists.**
- **Key provisioning and rotation as an operation** (Secret volumes, the order
  of rollout). This is SRE's, and needs authorisation for infrastructure
  changes. The procedure is documented in
  [`docs/runbooks/access-token-signing-key-rotation.md`](../runbooks/access-token-signing-key-rotation.md)
  but has not been exercised: no environment provisions these keys yet. That
  runbook also records a gap this ADR does not address: identity's HMAC secret,
  which hashes refresh tokens and derives subscriber ids, has no rotation path.

## Revisit triggers

- The token or proof profile must widen (D11.3(d)).
- A third-party verifier must accept these tokens: interoperability may then
  require normalised `htu` or a JWKS endpoint.
- Server-nonce support becomes necessary (D11.4).
- The owner ratifies, amends or rejects any decision above.
