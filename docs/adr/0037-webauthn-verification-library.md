# ADR-0037: WebAuthn (passkey) verification library (server)

- **Status:** **Proposed.** Not accepted, not implemented. It applies only if [ADR-0035](0035-google-sign-in-and-passkeys.md) is accepted, and is needed only for the passkey slice (after Google Sign-In).
- **Date:** 2026-11-13
- **Deciders:** the repository owner (see `README.md`, "Approval")
- **Independent human review:** proposed as part of ADR-0035's high-risk decision: the verifier is the trust boundary for passkey sign-in. For M1 the owner may accept on internal assurance (rule 8a); review is required before M2 (rule 8b).
- **Consulted:** none
- **Informed:** —

---

## Context

- **ADR-0035 (Proposed)** has identity register and verify passkeys: WebAuthn registration (attestation `none`) and authentication ceremonies, with server-generated single-use challenges, RP ID and origin checks (including Android's `android:apk-key-hash:` origin), user-verification and user-presence flags, COSE public keys, and signature counters.
- **Verifying WebAuthn needs CBOR and COSE parsing and several signature formats.** The Go standard library has the signature primitives but no CBOR or COSE, and the ceremony rules (W3C Web Authentication Level 3 §7) are long and security-critical.
- **Identity's module today:** `go 1.25.0`, pgx v5, `x/oauth2` v0.36.0 (and, if ADR-0036 is accepted, go-oidc and go-jose). The workspace is `go 1.25.0`; CI builds with go1.26.11, locally go1.26.x.
- **Android needs no extra library for passkeys** beyond ADR-0036's `androidx.credentials` (creation and get-credential requests use the same Credential Manager).

## Decision Drivers

1. Correct, maintained implementation of the registration and authentication ceremonies, tested against the specification.
2. Configurable RP ID and origins (Android origins included), user-verification requirement, and attestation `none`.
3. Smallest footprint; no change to unrelated modules; identity's Go version unchanged unless separately decided.
4. Challenge storage, single use and DPoP binding stay **ours** (identity's PostgreSQL), not the library's session object alone.

## Considered Options

1. **`github.com/go-webauthn/webauthn` v0.18.0** (2026-08-27; `go 1.25.0`).
2. **`github.com/go-webauthn/webauthn` v0.18.2** (2026-09-19; the latest; `go 1.26.0`).
3. **Hand-written verification** on the standard library plus a CBOR library.
4. **A smaller or less-used library** (for example `github.com/fxamacker/webauthn`).

## Decision Outcome

**Proposed: `github.com/go-webauthn/webauthn`, at the latest release that keeps identity on `go 1.25.0` (today v0.18.0),** contained to one new identity package (for example `internal/passkey`) behind a small port. Moving to a release that needs Go 1.26 (v0.18.1 or later) is a separate, recorded step that raises identity's and the workspace's `go` line together, and is taken when a security fix requires it.

- **Facts read from the Go module proxy on 2026-11-13:**
  - v0.18.0 declares `go 1.25.0`. v0.18.1 and v0.18.2 declare `go 1.26.0`.
  - v0.18.0 requires:
    - `github.com/fxamacker/cbor/v2` v2.9.3;
    - `github.com/go-viper/mapstructure/v2` v2.5.0;
    - `github.com/go-webauthn/x` v0.3.0;
    - `github.com/golang-jwt/jwt/v5` v5.3.1 (metadata statements);
    - `github.com/google/go-tpm` v0.9.8 (TPM attestation);
    - `github.com/google/uuid` v1.6.0;
    - `github.com/tinylib/msgp` v1.6.4;
    - indirectly `golang.org/x/crypto` v0.55.0 and `golang.org/x/sys` v0.47.0.
  - Test-only modules (testify, mock) are not linked into the binary.
- **The real footprint is measured in the implementing pull request:** `go list -m all` before and after; `go list -deps` of the identity binary; govulncheck. **The pull request stops** if any module outside identity changes version, or if `x/crypto`/`x/sys` changes affect other identity code paths without review.
- **How it is used:**
  - RP ID and the allowed origins from configuration: the Android `apk-key-hash` origins for the Play App Signing and upload certificates; never a debug origin in production configuration;
  - `attestation: none`, `residentKey: required`, `userVerification: required`;
  - **challenges generated, stored, expired, bound to the DPoP key and consumed once by identity**, then handed to the library's verification;
  - the stored credential (id, public key, counter, flags) is identity's own record; the library's user/session types are adapters only;
  - counter rule (ADR-0035 D35.5): 0 is accepted (synced passkeys); a non-increasing non-zero counter is refused and audited.
- **Tests** use a small software authenticator written in identity's test code (P-256 keys, CBOR via the same library), not a separate test-only module (`github.com/descope/virtualwebauthn` would pull an older go-webauthn into the graph).

### Consequences

**Positive**
- The de facto Go WebAuthn library, maintained, implementing the full ceremony checks.
- Identity's Go version stays at 1.25.

**Negative**
- The largest dependency addition to identity so far (CBOR, TPM attestation code, JWT for metadata, msgp, uuid, mapstructure), most of it unused with attestation `none` but still compiled in: a larger supply-chain surface to watch (govulncheck, OSV).
- Pinning to the last Go-1.25 release can lag fixes; a fix that needs v0.18.1+ forces the Go-version step.
- `x/crypto` and `x/sys` may move up for identity (measured; reviewed).

### Confidence

**Medium.** The library choice is clear; the footprint and the `x/crypto`/`x/sys` effect are measured only in the implementing pull request.

### Revisit Trigger

- A go-webauthn vulnerability fixed only in a release that needs a newer Go: take the Go-version step.
- The library unmaintained (no release for 12 months).
- A need for attestation verification (enterprise authenticators), which changes the attestation policy.

## Options in Detail

### Option 2: v0.18.2 now
- **Not chosen now:** it raises identity's and the workspace's `go` line to 1.26.0, a toolchain-wide change better taken on its own. Both are fine with CI's go1.26.11.

### Option 3: hand-written verification
- **Rejected:** CBOR/COSE parsing and the ceremony rules are security-critical and easy to get subtly wrong; no reason to own them.

### Option 4: a less-used library
- **Rejected:** smaller user base and review history for a trust boundary.

## Approval record

| Field | Record |
|---|---|
| Decision | **None.** Proposed only |
| Classification | Proposed as part of ADR-0035's high-risk decision (the trust boundary for passkeys). The owner's classification is final |
| Version | — |
| Owner decision | Pending ([`AUTH-DECISIONS-2026-11-13.md`](../phase19/AUTH-DECISIONS-2026-11-13.md), decision 1); needed before the passkey slice only |
| Independent human review | Not available (OD-12); required before M2 |
| AI involvement | Drafted by Claude Code (AI); library facts read from proxy.golang.org on 2026-11-13 |
| Does not | Add any dependency; authorise implementation or any domain or Digital Asset Links setup |

## References

- ADR-0015, ADR-0029, ADR-0034, ADR-0035, ADR-0036
- W3C Web Authentication Level 3; FIDO Alliance and Android Credential Manager documentation on passkeys and Digital Asset Links
- https://proxy.golang.org (go-webauthn v0.18.0, v0.18.2)
