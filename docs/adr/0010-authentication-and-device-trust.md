# ADR-0010: Authentication and device trust — MSISDN identity, hardware-bound tokens, Play Integrity

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Lead Staff Engineer
- **Consulted:** Security
- **Informed:** All engineering
- **Depends on:** ADR-0002, ADR-0008

---

## 1. Context

The subscriber's identity in this product **is their phone number**. It is the
key to their forwarding configuration (ADR-0002), the routing key for their DID,
and the thing an attacker would target to intercept another person's screened
calls. Getting authentication wrong here does not leak an account — it leaks
somebody's phone calls.

Two constraints from earlier phases bound the design tightly:

- **An APK is a public artefact.** Anything inside it — including strings in
  native libraries and values obfuscated by R8 — is extractable. `SECURITY.md`
  states this plainly: there is no such thing as a secret in a mobile client.
- **Clients stay on old versions for months** (Phase 1 §14). Whatever we ship
  must remain safe on a handset that has not updated since launch.

## 2. Problem Statement

How does the platform know that a request genuinely comes from (a) the subscriber
who owns that phone number, on (b) an app we built, running on (c) a device that
has not been tampered with?

These are three separate questions and they need three separate mechanisms.
Conflating them is the standard mobile-authentication mistake: a bearer token
proves the first, proves nothing about the second or third, and is trivially
replayable once extracted from a rooted device.

## 3. Constraints

| # | Constraint | Source |
|---|---|---|
| C1 | No secret of any kind may ship in the APK | `SECURITY.md` |
| C2 | Identity is the MSISDN; account recovery must survive a lost handset | Product |
| C3 | Must be robust to SIM swap — the classic attack against phone-number identity | Threat model |
| C4 | Must work on a stock handset with no hardware assumptions beyond API 26 | ADR-0002 C3 |
| C5 | Auth must not add measurable latency to the screening path | ADR-0011 |
| C6 | Old client versions must remain authenticable for months | Phase 1 §14 |
| C7 | Consent state is bound to identity and gates data flows | ADR-0012 |

## 4. Considered Options

1. **Bearer JWT after SMS OTP** — the conventional mobile pattern
2. **OAuth 2.0 / OIDC against a hosted IdP** (Cognito, Auth0)
3. **Bearer JWT + Play Integrity attestation at token issuance**
4. **Hardware-bound proof-of-possession tokens + Play Integrity** (DPoP-style)
5. **Carrier-based silent number verification** (mobile network operator APIs)

## 5. Decision

**Option 4, with Option 5 as a progressive enhancement.**

Three independent mechanisms, each answering one of the questions in §2:

**(a) Who owns the number — MSISDN verification.** SMS OTP at enrolment and on
re-verification. Rate-limited per number and per device, with exponential
backoff. Silent carrier verification (Option 5) is used opportunistically where
available because it is both faster and phishing-resistant; OTP remains the
universal fallback.

**(b) Which app — Play Integrity.** The client requests a Play Integrity verdict
and presents it at **token issuance and refresh**, not on every request. The
backend verifies the verdict server-side against Google, checking app
recognition, device integrity, and licensing. A failed verdict does not
necessarily reject — it downgrades the session to a restricted tier and raises a
signal.

**(c) Which device — hardware-bound proof of possession.** At enrolment the app
generates a **non-exportable EC P-256 key pair in the Android Keystore**, backed
by StrongBox or the TEE where present. The public key is registered with
`identity`. Every subsequent token refresh, and every mutating request, carries a
signature over the request made with that private key.

Token structure:

| Token | Lifetime | Bound to | Purpose |
|---|---|---|---|
| Access token | **15 minutes** | Device key (thumbprint claim) | API authorisation |
| Refresh token | **90 days**, rotating | Device key + Play Integrity | Obtain access tokens |

Access tokens are **EdDSA-signed JWTs** carrying a `cnf` (confirmation) claim with
the device key thumbprint. A token presented without a matching proof-of-possession
signature is rejected — which is what makes a stolen token useless.

Refresh tokens **rotate on every use**, with reuse detection: presenting a
previously-used refresh token invalidates the entire family and forces
re-enrolment. That is the signal that a token was exfiltrated.

## 6. Why This Option Was Selected

**Because C1 forces it.** If no secret can ship in the APK, then the client's
credential must be *generated on the device* and *never leave it*. Android
Keystore with a non-exportable key is exactly that primitive, and it is available
on every handset we support (C4). The private key cannot be extracted even from a
rooted device when StrongBox is present, and cannot be extracted without
compromising the TEE otherwise.

- **Proof-of-possession defeats the dominant mobile attack.** Bearer tokens are
  stolen from rooted devices, from insecure storage, from logs, from proxies. A
  PoP token that has been stolen is inert without the key that never left the
  Keystore. This is the single largest security improvement available in this
  design and the reason Options 1–3 are rejected.
- **Three mechanisms because there are three questions** (§2). Play Integrity
  attests the *app and device*; it says nothing about who owns the number. OTP
  proves *number control*; it says nothing about the app. The Keystore key proves
  *device continuity*; it says nothing about either. Each covers the others' blind
  spot.
- **Attestation at issuance, not per request** (C5). Play Integrity has real
  latency and a quota. Checking it on every API call would put a Google round trip
  inside the request path; checking it at token issuance and refresh gets the
  security property at a fraction of the cost.
- **15-minute access tokens with 90-day rotating refresh** balances C5 and C6.
  Short access tokens bound the damage of any leak; long rotating refresh tokens
  mean a subscriber who opens the app monthly is not forced through OTP again.
- **Refresh-token reuse detection is the exfiltration alarm.** It is the only
  mechanism here that *detects* compromise rather than merely resisting it.

## 7. Trade-offs

**Accepted.**

- **SMS OTP is the weakest link and we ship it anyway.** SMS is interceptable, and
  OTP is phishable. It is retained because it is the only universally-available
  number-verification mechanism in this market (C4). Mitigated by rate limiting,
  by the SIM-swap controls in §10, and by preferring silent carrier verification
  where available — but the weakness is real and is the top item in §15.
- **Play Integrity is a Google dependency.** It does not work on devices without
  Play Services, which is a small but non-zero share of the Indian Android market.
  Those devices get the restricted tier rather than a hard rejection.
- **Hardware-bound keys complicate device migration.** A subscriber with a new
  handset cannot move the key — by design. They must re-enrol, which means OTP,
  which means the SIM-swap surface is exercised at exactly the moment it is most
  dangerous. This is the sharpest trade-off in the ADR and is why §10's SIM-swap
  controls exist.
- **PoP signing adds a signature per mutating request.** EC P-256 signing on
  modern Android hardware is sub-millisecond; on older handsets it is measurable
  but well within budget (C5).
- **Three mechanisms is more implementation surface** than a bearer token, and
  more places to be subtly wrong. Mitigated by keeping all of it in
  `core/security` and `identity`, and by treating both as critical modules under
  the 90% coverage gate.

## 8. Alternatives Rejected

**Option 1 — bearer JWT after OTP.** The conventional choice, rejected on the
threat model. A bearer token is a password that grants access to somebody's phone
calls, stored on a device we do not control, that an attacker who roots the
handset simply copies. For a product with this data sensitivity the incremental
cost of PoP is small and the difference in outcome is large.

**Option 2 — hosted IdP (Cognito / Auth0).** Genuinely attractive for the
operational saving. Rejected on two grounds: **residency** (ADR-0012 — the
identity store holds `PERSONAL` and `SECRET` data and must be in India, which
constrains IdP choice sharply) and **fit** — our identity primitive is an MSISDN
with a hardware-bound device key and a consent record, not an email/password
account. We would be fighting the IdP's model. Reconsider for the *operator*
console (Phase 4), where a conventional IdP is the right answer.

**Option 3 — bearer JWT + Play Integrity.** A real improvement over Option 1 and
still rejected. Attestation at issuance proves the token was minted to a genuine
app on a genuine device; it does nothing to stop that token being copied off the
device afterwards. Attestation and proof-of-possession solve different problems,
and this option takes only one of them.

**Option 5 alone — carrier silent verification.** Excellent when available:
faster than OTP, no user interaction, immune to SMS interception. Rejected as the
*primary* because coverage across Indian carriers and circles is inconsistent and
cannot be relied on for enrolment. Adopted as an enhancement precisely where it
works.

## 9. Operational Impact

- **`identity` becomes a critical-path dependency for every authenticated
  request.** Token verification must be local (public-key JWT validation in
  `edge-api`) rather than a call to `identity` per request — otherwise `identity`
  becomes a synchronous dependency of the screening path and its availability
  becomes the platform's availability.
- **Key rotation for token signing** must be seamless. Publish a JWKS with
  overlapping keys; rotate signing keys on a schedule; never invalidate
  outstanding tokens by rotating (C6).
- **OTP delivery is a vendor dependency** with its own cost, deliverability
  profile, and failure modes per circle. Delivery rate is a monitored signal, and
  a fallback provider is required.
- **Refresh-token family invalidation is a support event.** A subscriber whose
  family was invalidated is locked out until re-enrolment, and they will contact
  support. The runbook must distinguish genuine compromise from a client bug that
  replayed a refresh token.
- **New alerts:** OTP send/verify failure rate, Play Integrity verdict
  distribution, refresh-token reuse detections, attestation-failure rate by
  device model.

## 10. Security Impact

This ADR *is* a security control, so this section is the substance rather than a
consequence.

- **SIM swap is the primary threat** (C3) and the reason phone-number identity is
  dangerous. Controls:
  - **Re-enrolment on a new device requires OTP *and* triggers a cooling-off
    period** before the forwarding configuration can be changed or transcripts
    can be read. An attacker who swaps a SIM gets an authenticated session but
    not immediate access to the subscriber's call history.
  - **Notification to the previously-enrolled device** on any new enrolment,
    delivered over FCM. The legitimate subscriber learns immediately.
  - **Elevated-risk operations** — changing the forwarding DID, exporting
    transcripts, changing consent — require re-verification regardless of session
    age.
- **Replay protection.** PoP signatures cover a nonce and a timestamp; `edge-api`
  rejects signatures outside a narrow window and tracks recently-seen nonces.
  Without this, a captured signed request is replayable.
- **Certificate pinning** in `core/network` against our own leaf and a backup
  pin. Pinning without a backup pin is an outage waiting for a certificate
  rotation.
- **No secret in the APK** (C1) is satisfied structurally: the only credential is
  generated on-device and is non-exportable.
- **Consent binding** (C7). The consent record lives in `identity` alongside the
  subscriber and is evaluated on every data-flow decision — including the
  cross-border routing decisions in ADR-0005 §10 and ADR-0007 §10.
- **Audit logging** for enrolment, re-enrolment, refresh-token reuse detection,
  attestation failures, and every elevated-risk operation. These records carry
  `LEGAL_HOLD` retention under ADR-0012.
- **Rate limiting** on OTP send, OTP verify, and token refresh, per number, per
  device, and per source address. OTP endpoints are the most-attacked surface on
  any consumer platform.

## 11. Cost Impact

Small relative to telephony and inference, but not zero:

- **SMS OTP is metered per message** and is the dominant auth cost. It scales with
  enrolments and re-enrolments, not with usage — so it is a customer-acquisition
  cost more than an operating cost. Silent carrier verification, where available,
  is generally cheaper as well as better, which is a rare alignment.
- **Play Integrity has a quota**; checking at issuance and refresh rather than per
  request keeps us well inside it and avoids the paid tier for longer.
- **Token verification is local and effectively free** — the design decision in
  §9 that keeps `identity` off the request path also keeps it off the bill.
- **`identity`'s Aurora cluster** is sized in ADR-0009 and is the smallest of the
  four.

## 12. Performance Impact

- **Auth adds nothing to the screening path** (C5). Screening is triggered by an
  inbound call from the carrier, not by an authenticated client request — the
  latency budget in ADR-0011 contains no auth hop, and that is deliberate.
- **Token verification in `edge-api` is local public-key validation**,
  microseconds, no network call.
- **PoP signature generation on-device** is sub-millisecond on modern hardware and
  a few milliseconds on the oldest supported handsets — imperceptible in an app
  interaction and absent from the call path entirely.
- **Play Integrity is the one slow operation** (hundreds of milliseconds, network
  round trip to Google). Confined to issuance and refresh, where it is off the
  interactive path and can be done in the background before the token expires.

## 13. Scalability Impact

- **Stateless verification scales horizontally without limit.** Because
  `edge-api` validates tokens locally against a JWKS, adding capacity requires no
  shared session store and no coordination.
- **Refresh-token state is the one stateful element** — the rotation family and
  reuse-detection record must be consistent, and it lives in `identity`'s Aurora
  cluster. Refresh volume is orders of magnitude below request volume, so this is
  comfortable.
- **OTP is rate-limited by design**, so it cannot become a scaling problem; it can
  become a *cost* problem during an enrolment spike, which is a different concern.
- **Nonce tracking for replay protection** is in Redis (ADR-0009) with a TTL
  matching the acceptance window, so it is bounded regardless of traffic.

## 14. Migration Strategy

1. **Phase 1 (launch).** MSISDN + OTP enrolment, Keystore-bound PoP tokens, Play
   Integrity at issuance and refresh.
2. **Phase 2.** Silent carrier verification (Option 5) added as the preferred
   enrolment path where the carrier supports it, with OTP as automatic fallback.
   This is a server-side routing decision and requires no client change.
3. **Phase 3.** Passkey-style re-enrolment to soften the device-migration
   trade-off in §7, if platform support and market readiness allow.
4. **Version compatibility** (C6): the token format carries a version claim, and
   `edge-api` supports every version still present in client telemetry. New claims
   are additive. This is the same discipline as contract versioning in ADR-0001.
5. **Rollback:** auth mechanism selection is server-side policy, so an enhancement
   that misbehaves is disabled without a client release.

## 15. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| SIM swap grants an attacker a subscriber's call history | Medium | Critical | Cooling-off period on re-enrolment; notification to prior device; re-verification for elevated operations |
| SMS OTP intercepted or phished | Medium | High | Rate limiting; silent carrier verification preferred where available; short OTP validity |
| Play Integrity unavailable on a share of devices | Medium | Medium | Restricted tier rather than hard rejection; monitored verdict distribution |
| Device key lost on handset change locks out subscriber | High | Medium | Documented re-enrolment path; support runbook; Phase 3 passkey work |
| Refresh token exfiltrated from a rooted device | Medium | High | PoP binding makes it inert without the key; reuse detection invalidates the family |
| Replay of a captured signed request | Low | High | Nonce + timestamp in the signature; narrow acceptance window; nonce tracking in Redis |
| `identity` becomes a synchronous dependency of every request | Medium | High | Local JWT verification in `edge-api`; JWKS with overlapping keys |
| Certificate pin rotation bricks old clients | Low | Critical | Backup pin shipped from day one; pin rotation rehearsed before it is needed |
| OTP vendor deliverability collapse in a circle | Medium | High | Secondary OTP provider; delivery rate monitored per circle |

## 16. Future Review Trigger

Revisit when **any** holds:

- Silent carrier verification coverage exceeds **70%** of enrolments, making OTP a
  genuine fallback rather than the primary path
- SIM-swap fraud is observed in production at any rate above zero
- Refresh-token reuse detections exceed **0.1%** of refreshes, indicating
  systematic exfiltration rather than client bugs
- Play Integrity verdict-failure rate exceeds **5%** of issuances
- Android or Play Services materially changes the attestation or Keystore APIs
- Expansion to a market where SMS OTP is not viable or where a different identity
  primitive is conventional

## 17. References

- ADR-0002 (telephony architecture — why MSISDN is the identity),
  ADR-0008 (cloud infrastructure), ADR-0009 (identity data store),
  ADR-0011 (latency budget), ADR-0012 (consent binding)
- `SECURITY.md` — no secrets in the APK; secret handling
- `services/go/identity`, `android/core/security`, `android/core/network`
- RFC 9449 (DPoP), RFC 8037 (EdDSA for JOSE)
