# ADR-0029 evidence review and recommendation

- **Reviewed:** ADR-0029 in full, and the ratification brief
  ([ADR-0029-RATIFICATION-BRIEF.md](ADR-0029-RATIFICATION-BRIEF.md)).
  Each decision was compared against the accepted ADRs and against the code and
  tests on `main` at `94f1b9b`.
- **By:** Claude, under the owner's delegation.
- **This is a recommendation, not a ratification.** ADR-0029 remains
  owner-ratification pending: it records itself as decisions made under
  delegation, and only the owner can accept it.

## Recommendation

**Ratify ADR-0029, and in doing so explicitly accept three amendments to
ADR-0010 and one reversal of the owner's own D11 requirement.**

None of the four is a correctness or security defect in ADR-0029. Each changes
something the owner previously wrote, which is why it should be accepted
explicitly rather than by implication:

1. **Reversal of the owner's D11.3 requirement.** The D11 record asked for "an
   ADR authorising a JWT library, since hand-rolling JWT verification is not
   acceptable for a security boundary". ADR-0029 decides the opposite: a
   standard-library fixed profile.
   - **The evidence supports it:** see §1.
   - **The owner must still choose it knowingly.**
   - **If the owner prefers a library,** the change is contained: the
     `accesstoken` and `dpop` APIs would stay, behind which the parser is
     replaced.
2. **ADR-0010 §9, JWKS replaced by a mounted key set.** No network fetch on the
   trust path; rotation is a rollout.
3. **ADR-0010 §5(c), proof of possession widened** from "refresh and mutating
   requests" to every protected request.
4. **ADR-0010 §10, replay tracking.**
   - The client-chosen `jti`, inside a 2-minute `iat` window, plays the role of
     the nonce.
   - Seen values are tracked in PostgreSQL rather than Redis.
   - There are no server-issued nonces.

**Not part of ratification:** the product decisions the brief lists, which
ADR-0029 does not settle and should not be read as settling:
- session lifetime;
- the restricted tier;
- Play Integrity going live;
- the OTP provider;
- OTP backoff;
- ADR-0030 D30.3.

## 1. Decision-by-decision evidence

| Decision | Consistent with | Verified on `main` | Finding |
|---|---|---|---|
| D11.2: EdDSA `at+jwt` access tokens, verified locally | ADR-0010 §5 (EdDSA JWT, 15 min, `cnf`), §9 (local verification) | `accesstoken` tests. The E2E test admits an identity-issued token at edge-api against real PostgreSQL in CI. | Consistent. |
| D11.2: an unenrolled session gets no access token | ADR-0010 §5(c) | The HTTP enrolment and refresh paths call only the device-proof variants. An unbound session refreshing is refused (`ErrSessionNotDeviceBound`, 401). | Consistent: **an unenrolled session cannot obtain an access token over HTTP.** |
| D11.2: opaque, rotating refresh tokens; reuse revokes the family | ADR-0010 §5 | Reuse tests in `app`. Reuse is audited and counted (#113). | Consistent. The 24 h lifetime is a deviation, listed in the brief. |
| D11.3: standard-library fixed profile | **Reverses the owner's D11.3 requirement**; see the recommendation | `alg:none`, HS256-with-public-key, ES256 swap, `alg`/`typ` variants, `jku`/`x5u`/`jwk` injection and padded segments are all refused by tests. RFC 8037 and RFC 9449 vectors. `FuzzVerify` in both packages. Strict-JSON parser-differential tests. | The conditions are met with evidence. The independent review found no critical or high issues. |
| D11.3: key set (active plus previous) | Amends ADR-0010 §9 (JWKS) | Rotation test, mutation-checked. The runbook (#107, #114). | Verifier logic proven; **the operation has never been exercised.** |
| D11.3: DB TLS `verify-full` | ADR-0008 (no weak transport) | Refusal tests (#99, #100). A real TLS server: connection verified, untrusted CA and host mismatch refused (#104, mutation-checked). | Consistent. |
| D11.4: DPoP on every protected request; `ath`; no nonces | Widens ADR-0010 §5(c). §10 replay intent met with `jti` plus window. | RFC 9449 example proofs verify. Replay is reserved atomically in PostgreSQL. | Consistent with §10's intent. Redis is replaced by PostgreSQL. |
| D11.5: issuer and verifier land together | — | #100 | Done. |

## 2. ADR-0010 requirements that are NOT met (outside ADR-0029's scope)

These belong to ADR-0010, not ADR-0029, so they do not block ratification.
**They are listed so ratification is not mistaken for "authentication is
complete".**

| ADR-0010 requirement | State on `main` |
|---|---|
| §5(b) Play Integrity at issuance and refresh | Wired since #131 (ADR-0031): assessed at enrolment and refresh, and the tier recorded but **not enforced**. There is no Google verifier (needs Google setup), so every session is `unverified`, and a non-genuine app holding a valid OTP can still enrol. |
| §5(a) OTP exponential backoff, per-device limit | Fixed lockout. Backoff deferred as an owner trade-off (brief §3a). There is no device identity before enrolment. |
| §10 rate limiting on **token refresh**, and **per source address** | **Not implemented.** OTP send and verify are limited per number only. Refresh has no rate limit. |
| §10 SIM-swap controls: cooling-off on re-enrolment, notification to the prior device, re-verification for elevated operations | **Not implemented.** None of the elevated operations exist yet. |
| §10 audit logging with `LEGAL_HOLD` retention (enrolment, re-enrolment, refresh-token reuse, attestation failures) | **Partly met since this review (#120):** an append-only `identity_security_audit` table records wrong codes, lockouts, refresh-token reuse and device enrolment. **Still missing:** re-enrolment as its own event, and attestation failures (no verifier exists yet). |
| §9 OTP delivery with a fallback provider | Failover sender (#109); no provider. |

## 3. What ratification would and would not change

- **Would:**
  - change ADR-0029's Status to Accepted, owner-ratified;
  - turn the four items above into recorded amendments of ADR-0010 and of the
    D11 record.
- **Would not:**
  - change any code;
  - authorise production, key provisioning, infrastructure changes or spending;
  - close any item in §2.
