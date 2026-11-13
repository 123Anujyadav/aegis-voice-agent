# ADR-0036: Google ID-token verification library (server) and Credential Manager (Android)

- **Status:** **Proposed.** Not accepted, not implemented. It applies only if [ADR-0035](0035-google-sign-in-and-passkeys.md) is accepted.
- **Date:** 2026-11-13
- **Deciders:** the repository owner (see `README.md`, "Approval")
- **Independent human review:** proposed as part of ADR-0035's high-risk decision: the verifier is the trust boundary for Google Sign-In. For M1 the owner may accept on internal assurance (rule 8a); review is required before M2 (rule 8b).
- **Consulted:** none
- **Informed:** —

---

## Context

- **ADR-0035 (Proposed)** has the identity service accept a Google **ID token** (an RS256-signed JWT) from the Android app and verify it server-side: signature against Google's published keys, `iss`, `aud`, `azp`, `exp`, `iat`, `nonce`, `sub`.
- **ADR-0029 (Proposed, decided under delegation)** keeps identity's own token profile on the standard library with no JWT library, and says that widening it, including **remote JWKS fetching**, means adopting "a maintained library through its own ADR". ADR-0032 D32.3 applied that to Google's tokens. This is that ADR.
- **Identity's module today** (`services/go/identity/go.mod`, `go 1.25.0`): pgx v5 and `golang.org/x/oauth2` v0.36.0 (used only by the dormant Play Integrity token source). The workspace (`go.work`) is `go 1.25.0`; CI builds with go1.26.11.
- **Precedent for containment:** ADR-0015 and ADR-0034 contain a third-party dependency to one module or package and verify the transitive set with `go list -deps`. ADR-0034 also showed that a dependency can raise shared module versions through the workspace, so the effect on identity's dependency set must be measured, not assumed.
- **Android:** the app has no Credential Manager, Google ID or Play-services-auth library. It already uses Play Integrity (`com.google.android.play:integrity`), which brings Play services `tasks` and `basement`. CI gates: the release permission list, an APK size within 2% of the baseline, and an OSV audit of the resolved release dependencies.

## Decision Drivers

1. Correct, maintained verification of RS256 JWTs and Google's key set, with key rotation handled.
2. Smallest dependency footprint in identity; no change to unrelated modules' versions.
3. The identity Go version unchanged (`go 1.25.0`) unless separately decided.
4. Fail closed on any key-fetch or parse failure; the HTTP client is ours (TLS verified, no redirects, timeouts, size limit).
5. On Android: the platform-supported API for Google Sign-In.

## Considered Options (server)

1. **`github.com/coreos/go-oidc/v3`** (v3.21.0, 2026-09-01), which uses `github.com/go-jose/go-jose/v4`.
2. **`google.golang.org/api/idtoken`** (Google's client library).
3. **`github.com/lestrrat-go/jwx/v3`** (v3.4.0, 2026-10-23), a general JOSE toolkit.
4. **Hand-written RS256 and JWKS verification** on the standard library.

## Decision Outcome

**Proposed: Option 1, `github.com/coreos/go-oidc/v3`,** used only for signature and claim verification against Google's key set, inside one new identity package (for example `internal/googleauth`) behind a small port. Nothing else in identity, and no other module, imports it.

- **Facts read from the Go module proxy on 2026-11-13:** v3.21.0's `go.mod` declares `go 1.25.0` and requires only `github.com/go-jose/go-jose/v4` v4.1.4 and `golang.org/x/oauth2` v0.36.0. go-jose v4.1.9 (2026-11-05) declares `go 1.24.0` and has no requirements.
  - **So it should not raise identity's Go version,** and `x/oauth2` is already at v0.36.0. **The exact effect on identity's module graph and the workspace is measured in the implementing pull request** (`go list -m all` before and after, `go list -deps` for the identity binary), and the pull request stops if anything outside identity changes.
- **How it is used:**
  - a key set pointed directly at Google's JWKS URL (`https://www.googleapis.com/oauth2/v3/certs`), with **our** HTTP client (TLS verified, no redirects, short timeouts, bounded body), so no discovery document is fetched;
  - signature algorithm restricted to RS256;
  - client-id (audience) check against the configured server client id;
  - issuer: Google uses two spellings (`https://accounts.google.com` and `accounts.google.com`), so the library's single-issuer check is replaced by an explicit check of exactly those two;
  - expiry checked by the library, with identity's clock; `iat`, `azp`, `nonce` and `sub` checked by identity;
  - single use: the token's hash is reserved until its expiry (identity's existing replay pattern).
- **Fail closed:** a key-fetch failure, an unknown `kid`, or any parse error refuses the sign-in with the generic error.

**Android:** `androidx.credentials:credentials` and `androidx.credentials:credentials-play-services-auth` (1.6.0, latest stable on Google's Maven repository on 2026-11-13) and `com.google.android.libraries.identity.googleid:googleid` (1.2.1). The request is a `GetGoogleIdOption` (or the "Sign in with Google" button option) with the server client id and the ADR-0035 nonce. The versions are pinned in the catalog with a reason, re-checked at implementation.

### Consequences

**Positive**
- A widely used, maintained verifier with key rotation; a small transitive set.
- No change expected to identity's Go version or to other modules (to be measured).

**Negative**
- Identity's first JOSE/JWT dependency (go-jose); a supply-chain surface to watch (govulncheck, the OSV audit).
- An external network call from identity to Google's key endpoint: **an egress change needing SRE and owner approval** (staging network policy), and key caching to survive short outages.
- Android: three new libraries, an APK size increase (the 2% gate's baseline must be raised in the same pull request, with the reason recorded), a possible R8 rule if the Play-services provider is loaded reflectively (only on evidence), a check that no new permission is merged (the release permission gate), and data-inventory and privacy-disclosure updates.

### Confidence

**Medium-high** for the server library (stable API, small footprint, facts read from the module proxy). The Android versions and any R8 or permission effects are confirmed in the implementing pull request.

### Revisit Trigger

- A go-oidc or go-jose vulnerability without a fixed release within 14 days.
- go-oidc unmaintained (no release for 12 months).
- Google changing ID-token signing (algorithm or key endpoint).

## Options in Detail

### Option 2: `google.golang.org/api/idtoken`
- **Rejected:** a large transitive set (Google API client, auth, and their dependencies). ADR-0034 measured that this family raises shared module versions through the workspace, including identity's `x/oauth2` and `cloud.google.com/go/compute/metadata`. Too much for one verification.

### Option 3: `lestrrat-go/jwx/v3`
- **Rejected:** a much broader JOSE surface (JWE, JWS, JWK, JWT and more) than one RS256 verification needs.

### Option 4: hand-written verification
- **Rejected:** contrary to ADR-0029's rule for remote JWKS, and key rotation, `kid` selection and JWKS parsing are exactly where hand-written code goes wrong.

## Approval record

| Field | Record |
|---|---|
| Decision | **None.** Proposed only |
| Classification | Proposed as part of ADR-0035's high-risk decision (the trust boundary for Google Sign-In). The owner's classification is final |
| Version | — |
| Owner decision | Pending ([`AUTH-DECISIONS-2026-11-13.md`](../phase19/AUTH-DECISIONS-2026-11-13.md), decision 1) |
| Independent human review | Not available (OD-12); required before M2 |
| Other approvals | SRE and owner: identity egress to Google's key endpoint (a network-policy change) |
| AI involvement | Drafted by Claude Code (AI); library facts read from proxy.golang.org and Google's Maven repository on 2026-11-13 |
| Does not | Add any dependency; authorise implementation, egress or any Google Cloud setup |

## References

- ADR-0015, ADR-0029, ADR-0032 (D32.3, D32.9), ADR-0034, ADR-0035
- OpenID Connect Core 1.0 §3.1.3.7; Google Identity: "Verify the Google ID token on your server side"
- https://proxy.golang.org (go-oidc v3.21.0, go-jose v4.1.9); https://dl.google.com/android/maven2 (androidx.credentials, googleid)
