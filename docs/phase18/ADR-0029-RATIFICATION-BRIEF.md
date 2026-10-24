# ADR-0029 ratification brief

**For:** the repository owner. **Asks for:** ratify, amend, or reject the
decisions in [ADR-0029](../adr/0029-access-token-and-proof-of-possession.md),
which Claude made under the owner's standing delegation on 2026-10-23.

**Status today:**
- ADR-0029 is **Proposed: decided under owner delegation, owner ratification
  pending.**
- Its decisions bind the implementation until the owner amends or rejects them.
  They are **not** an owner-accepted ADR, and this brief does not make them one.

**Read with:** the per-deviation analysis in
[../phase19/ADR-0029-DEVIATION-ANALYSIS.md](../phase19/ADR-0029-DEVIATION-ANALYSIS.md).
It covers the threat model, RFCs, tests, operations and rollback for each of
the four deviations. It also records two findings that are not in this brief:
- the `at+jwt` access token has no RFC 9068 `client_id` (§5 there);
- edge-api's availability depends on PostgreSQL (§3 there).

Neither finding is a defect today. **No external review has been done;**
passing tests are not proof that the design is secure.

**What ratification does NOT do:** it does not authorise a production
deployment, key or secret provisioning, infrastructure or network changes, or
spending. Those stay gated as before.

## 1. The decisions, with evidence and consequences

| # | Decision | Alternative rejected | Evidence on `main` | Material consequence | Reversible? |
|---|---|---|---|---|---|
| D11.2 | EdDSA `at+jwt` access tokens (15 min), verified locally at edge-api. Opaque, rotating refresh tokens stored only as an HMAC; reuse revokes the family. | Opaque access tokens checked against identity per request | #100. `edge-api/internal/auth`, identity `token_issuance_test.go`, the E2E test `TestE2E_IdentityTokenAdmittedByEdgeThroughTheRealLifecycle` (real PostgreSQL, in CI) | **Revocation is not immediate for access tokens.** A revoked session's access token stays valid until it expires: up to 15 min plus 60 s skew. This is ADR-0010's accepted design, restated. | Yes. A shorter lifetime is a constant. |
| D11.3 | A standard-library **fixed-profile** JWS (no JWT library), under conditions: strict JSON, RFC vectors, fuzzing | A maintained JWT library | #99. RFC 8037 A.4 and RFC 9449 vectors, `FuzzVerify` in both packages, strict-JSON parser-differential tests | The repository owns the parser's correctness. That is mitigated by the narrow profile, conditions (a)–(c), and a revisit trigger if the profile must widen. | Yes, behind the same API. |
| D11.3 | A **mounted key set** (active plus one previous), not a remote JWKS | ADR-0010 §9's "publish a JWKS" | #100. `TestRotation_OverlapProcedureNeverRejectsAValidToken` (mutation-checked); the runbook (#107) | **Amends ADR-0010 §9.** Rotation is a rollout, with no network fetch on the trust path. Only one previous key, so rotations cannot overlap. | Yes. A JWKS could be added later. |
| D11.3 | Production issuance is gated on the key-set verifier and the rotation test | Allowing single-key issuance | #100 and #103. The gate's condition is met. | None beyond D11.3. Production stays blocked on §2. | — |
| D11.3 | Database TLS: `verify-full` with an explicit CA, unless `ENVIRONMENT` is explicitly `development` | `sslmode=require`, or trusting the network | #99 and #100 (refusal); #104 (a real TLS server: verified connection, untrusted CA and host mismatch refused; mutation-checked) | Every deployment needs a CA bundle and a DSN with `verify-full`. A plaintext DSN outside development stops startup. | Yes. |
| D11.4 | RFC 9449 DPoP on **every** protected request, plus enrolment and refresh. `ath` required. No server nonces. `jti` restricted to printable ASCII. | ADR-0010 §5(c)'s narrower "refresh and every mutating request" | #99 and #100. RFC 9449 example proofs verify; the replay store is in PostgreSQL | **Amends ADR-0010 §5(c).** The client signs every request, and each costs one database round trip for replay. Without nonces, a proof pre-generated within the 2-minute freshness window is accepted once. | Partly. Widening is easy; narrowing would reopen ADR-0010. |
| D11.5 | Issuer and verifier land together, with the unsigned token removed | Staged landing | #100 | No published state ever had a verifier that rejects all issued tokens. | — |

## 2. Known deviations from ADR-0010 the owner should see

| Deviation | Where it stands | Owner action |
|---|---|---|
| **Session lifetime is 24 h, not 90 days** | Stricter than ADR-0010. Subscribers re-verify daily. | A product decision: keep or extend. |
| **Play Integrity is not verified against Google** | The fabricating stub is replaced by a fail-closed boundary and an offline-tested verdict policy (#108). Since #131 (ADR-0031), it is assessed at enrolment and refresh, and the tier is recorded on the session, but not enforced. There is no Google verifier, so every session is `unverified`. | Provide a Google Cloud project, credentials and egress. Decide what the restricted tier may do. |
| **No OTP delivery provider** | A failover sender boundary exists (#109, merged). No provider; requests are refused truthfully. | Choose a provider (and a secondary). In India this needs DLT registration. |
| **A mounted key set instead of a JWKS** | §1, D11.3 | Ratify or reject. |
| **DPoP on every request** | §1, D11.4 | Ratify or reject. |
| **No exponential backoff, no per-device OTP limit** (ADR-0010 §5(a)) | Fixed 15-minute lockout after 5 wrong codes in 15 minutes. A per-device limit cannot exist before enrolment: there is no device identity yet. | Decide the trade-off in §3a. |

## 3. A security finding outside ADR-0029's decisions

**Identity's HMAC secret has no rotation path.** It hashes OTP codes and
refresh tokens, and it derives subscriber ids. Rotating it would sign everyone
out **and give every subscriber a new id**. This is recorded in the rotation
runbook (#107) and in ADR-0029's open items.
- **No compromise is known.**
- **Fixing it needs a design decision:** for example, a key id stored with each
  hash, and a subscriber-id migration.
- **Recommendation:** decide before production. Until then, a suspected HMAC
  compromise has no clean recovery.

## 3a. An OTP backoff trade-off for the owner

ADR-0010 §5(a) asks for exponential backoff on OTP verification. It was **not
implemented**, because it trades one risk for another.

- **For:** escalating lockouts (15 minutes, doubling up to a cap) cut targeted
  code guessing several-fold. With a 4-hour cap, guesses fall from about 240 a
  day to about 30 per number.
- **Against:** anyone who knows a phone number can request a code for it and
  submit wrong guesses, **locking the real subscriber out**. Today that takes
  a burst every 15 minutes. With escalation, one burst every few hours keeps
  the victim locked out.
- **Context:** at 240 guesses a day, finding a 6-digit code already takes years
  per number.

**Options:**
1. Implement backoff with a low cap (about 1–4 hours) and accept the cheaper
   lockout denial of service.
2. Keep the fixed lockout, and record this as a deviation from ADR-0010.
3. Escalate only on the device side once a device identity exists.

**Recommendation:** option 2 until abuse data says otherwise. It is a choice
about subscriber availability, so it is the owner's.

## 4. Observability now in place (supports the ADR-0010 §9 alerts)

- `edge_api_auth_requests_total{outcome}` (#105, merged): every edge
  authentication decision.
- `identity_auth_requests_total{endpoint,outcome}` (#106, merged): every
  identity response, including OTP failures and refused refreshes.
- `identity_security_events_total{type}` (#113, merged): wrong codes,
  lockouts, and **refresh-token reuse detections**, which ADR-0010 calls the
  exfiltration alarm. Reuse was previously recorded nowhere.
- `identity_security_audit_failures_total{type}`: audit events that were
  **not** recorded, because the durable audit log refused them or the
  re-enrolment lookup failed. **Any non-zero rate means audit records are
  being lost, and it should page.** Before this counter, the loss was visible
  only inside the process.
- All are counters under a closed label vocabulary. No identifier is a
  label, and all are served only on the health port.

## 5. Options

1. **Ratify as written.** Change the Status line to Accepted, owner-ratified.
   Nothing in code changes.
2. **Ratify with amendments.** The likeliest candidates:
   - restore the 90-day session lifetime;
   - require server nonces for DPoP;
   - add a JWKS alongside the mounted key set;
   - narrow DPoP to ADR-0010 §5(c)'s original scope;
   - record the token profile as "RFC 9068-derived", or add `client_id`
     through the two-step rollout (deviation analysis §5).

   Each is a focused follow-up, and none is needed for correctness.
3. **Reject a specific decision.** Name it. The implementation then follows the
   owner's direction, and the affected PRs above show the scope of the change.
