# F1: what a refused refresh proof should do to the session

- **Status: Proposed. High-risk: not implemented, and not to be implemented until the owner decides and an independent human review takes place.** The decision concerns when a session ends, so it is high-risk under `ARCHITECTURE_FREEZE.md` §6.1 (rules 8 and 10) and waits on OD-12.
- **Origin:** finding F1 in [`PRODUCTION-READINESS.md`](PRODUCTION-READINESS.md).
- **Review:** this is an AI-performed, read-only investigation (§6.1 rule 9). It is not an independent human review.
- **As of:** `main` `604e437`, 2026-11-01.

## 1. The question

When identity refuses the device proof on `POST /v1/token/refresh`, the Android app ends the session and the user has to sign in again. Is that right for every reason identity can refuse a proof? And if not, what is the safest backward-compatible change?

## 2. What happens today

### 2.1 Identity (the server)

Refresh checks run in this order (`services/go/identity/internal/app/identity.go`, the refresh path):

1. **The session and the refresh token.** An unknown, revoked or expired session, a token the session never held, or a session with no device key gives `401 ERR_INVALID_TOKEN`. A token the session held earlier is reuse: the session is revoked and `identity.refresh.token_reused` is audited.
2. **The proof** (`dpop.Verifier.Verify`). Any refusal gives `400 ERR_INVALID_DPOP_PROOF`. A replay store that cannot answer gives 503. Inside `Verify` the order is:
   1. size and shape;
   2. header and key;
   3. **signature**;
   4. **claims:** `htm`, **`htu`**, **`iat`**;
   5. the replay reservation (`packages/go/dpop/dpop.go`).
3. **The key** (`acceptRefreshProof`). A proof by a key other than the session's enrolled key gives `400 ERR_INVALID_DPOP_PROOF`.
4. **Only then** is the refresh token rotated and an access token issued.

So **a refused proof changes nothing on the server.** The session stays live, the refresh token stays current, and nothing is revoked or audited. Identity ends a session only through step 1: expiry after 24 hours, reuse, or erasure.

A proof's `iat` must lie within [server time − 2 minutes, server time + 60 seconds] (ADR-0029 D11.4; `bootstrap.ProofMaxAge` and `ProofFutureSkew`). `htu` must equal the configured canonical URL byte for byte, with no normalisation.

### 2.2 The app (the client)

| Identity's answer to refresh | `IdentityError` | `IdentityAuthenticator.refresh` | Stored session |
|---|---|---|---|
| `401 ERR_INVALID_TOKEN` | `SessionEnded` | `signedOut(SESSION_ENDED)` | Cleared |
| **`400 ERR_INVALID_DPOP_PROOF`** | **`InvalidDeviceProof`** | **`signedOut(SESSION_ENDED)`** | **Cleared** |
| `400 ERR_INVALID_INPUT`, `ERR_MALFORMED_BODY`, `ERR_UNSUPPORTED_MEDIA_TYPE`, `ERR_BODY_TOO_LARGE` | `InvalidInput` | `signedOut(STORED_SESSION_UNRECOVERABLE)` | Cleared |
| `400 ERR_INVALID_INTEGRITY_TOKEN`, `429 ERR_RATE_LIMITED`, `503 ERR_TEMPORARILY_UNAVAILABLE`, any other or unknown code | `InvalidIntegrityToken`, `RateLimited`, `TemporarilyUnavailable`, `UnexpectedResponse` | `Unavailable` | Kept |
| No answer (network, timeout) | `Network` | `Unavailable` | Kept |
| The phone could not sign (C18, #204) | `DeviceProofUnavailable` | `Unavailable` | Kept; nothing sent |

The mapping is in `core/network/.../identity/IdentityContract.kt` (`fromProblem`). **An unknown code maps to `UnexpectedResponse`, and the session is kept.**

edge-api's `401 ERR_INVALID_DPOP_PROOF` on `GET /v1/me` already keeps the session (`EdgeError.ProofRejected`, outcome `KEPT` in C17). Only identity's refresh answer ends it.

## 3. The causes of `400 ERR_INVALID_DPOP_PROOF` on refresh

| Cause | Can the app's own proof hit it? | Recoverable? | Effect today |
|---|---|---|---|
| **Phone clock** slow by more than 2 minutes or fast by more than 60 seconds | **Yes.** `DpopProofs` signs `iat` from `System.currentTimeMillis()`, the phone's wall clock | **Yes**, once the clock is right | Signed out at the next refresh, and **cannot sign in again** either: the enrolment proof is refused the same way |
| **`htu` mismatch from server configuration:** the canonical refresh URL differs from the app's identity origin, for example after a domain change or a proxy | **Yes**, for every user at once | **Yes**, once the configuration is fixed | **Every user signed out** within about 15 minutes (one access-token lifetime) |
| Proof missing, repeated or oversized; bad shape or signature | Only through a client bug, or an intermediary that alters headers | Depends | Signed out |
| Replayed `jti` | No. The app makes a fresh proof per attempt and never resends one (`retryOnConnectionFailure(false)`, ACCOUNT-DELETION F1) | — | Signed out |
| **Another key:** this phone's Keystore key is gone or was regenerated (`KeystoreProofKey.loadOrCreate` creates a new one), so it is not the enrolled key | Yes, after a Keystore loss | **No.** This device can never prove possession again | Signed out, which is correct |
| A stolen refresh token used without the key | Not the app's case. The attacker's client does not follow the app's rules | — | Refused by identity; **not audited** (§5, E2) |

**Clock skew, URL mismatch and an invalid key do share one error code.** Worse, a stale proof made with another key is reported as stale, because `Verify` checks claims before identity compares the key.

### 3.1 Recoverable versus security-critical

The security decision is the server's. A refused proof is refused whatever the app does next, and the session's state is unchanged. **So ending the session locally after a `400` protects nothing.** It only removes the user's own valid session, which an attacker could not use anyway without the key.

- The local sign-out is justified only when the session can never work again on this phone. That means identity said the session ended (`ERR_INVALID_TOKEN`), or this phone no longer holds the enrolled key.
- Clock skew and URL mismatch are recoverable failures that currently cause an unrecoverable outcome.
- The security-relevant signal is on the server: a refresh with the **current** refresh token and a valid proof from **another** key. Identity refuses it, but neither counts nor audits it.

## 4. Governance check

| Question | Answer |
|---|---|
| Changes an Accepted ADR? | **No**, for options A, B and E. ADR-0010 (Accepted) requires a matching proof at refresh, and identity's checks are unchanged. **Option C** changes identity's error contract, and **option D** reverses ADR-0029 D11.4 ("server nonces are not used"). ADR-0029 is Proposed (decided under delegation), so either would be a proposed amendment, not an edit |
| Changes a frozen invariant (§3, I1–I12)? | **No.** Only I5 touches the device credential (generated on the device, non-exportable), and no option changes how the key is made or stored |
| Moves a trust boundary? | A, C, D and E: no. **B** brings in a new input: identity's `Date` response header, over the same TLS connection that already delivers the tokens. It is bounded, and it can only make the app's own proofs fresher or staler, never acceptable to a server that would refuse them |
| High-risk? | **Yes**, for A, B, C and D: they change when a session ends, or how a proof is made. E adds to identity's audit trail and metrics without changing behaviour. The owner decides its classification (§6.1 rule 7); this document treats it as high-risk until then |

## 5. Options

| | Change | Where | Fixes | Contract change | Cost and risk |
|---|---|---|---|---|---|
| **A** | **Keep the session on a refresh `ERR_INVALID_DPOP_PROOF`** (`Unavailable`). End it on that code only when this phone's key thumbprint is not the enrolled one. The session record gains the thumbprint at enrolment | App (`IdentityAuthenticator`, `EncryptedSessionStore`) | No more sign-out from clock skew or URL mismatch. Key loss is still detected, locally and exactly | None. Older records without a thumbprint keep today's behaviour | Small. A session that keeps being refused for another reason stays "signed in" until identity's 24-hour expiry answers `ERR_INVALID_TOKEN`, which ends it as usual. That bounds the stuck state |
| **B** | **Correct the proof time.** Take the offset between the phone clock and identity's `Date` header, bounded to ±24 h, and sign `iat` with it. After a refused refresh whose `Date` shows the phone outside the window, retry once with a fresh proof | App (`DpopProofs`, `IdentityClient`) | A phone with the wrong time can refresh, **and enrol** | None | Small. A forged `Date` needs control of identity's TLS. The retry is bounded to one, with a new `jti` |
| **C** | **A distinct code**, for example `400 ERR_DPOP_PROOF_NOT_ACCEPTED`, for a proof that is correctly signed by the enrolled key but has the wrong `iat` or `htu`. Identity compares the key before it classifies, so a caller with another key learns nothing new | identity, and `dpop.Verify` returns the key with a claim refusal | Lets the app (or any client) tell recoverable from final | **Yes**, a new code. Backward-compatible: today's app maps an unknown code to `UnexpectedResponse` and **keeps** the session | Medium. A proposed amendment to ADR-0029's error contract |
| **D** | **Server nonces** (RFC 9449 §8) instead of `iat` freshness | identity, app | Clock skew, entirely | Yes: reverses ADR-0029 D11.4 | Large: a round trip, nonce state, and a new contract |
| **E** | **Observability:** a reason label on the refresh `invalid_dpop_proof` outcome metric (`shape`, `signature`, `stale`, `target`, `key`, `replayed`), and an audit event for **a current refresh token with a proof by another key** | identity (`httpapi/metrics.go`, `app` audit) | Makes a URL misconfiguration visible as a spike of `target` refusals before users notice it. Records a theft signal | None | Small, additive |

**Not recommended:** keeping today's behaviour unchanged. With C17 the app refreshes in the background, so one wrong server setting signs every user out, and one wrong phone clock locks that user out of signing in.

## 6. Proposed decision

**A + B + E now; C later, if a third-party or older client needs it; not D.**

- **A** removes the harm: no refused proof signs a user out unless the phone has lost its key.
- **B** removes the most likely cause: phone clocks. It also fixes enrolment, which A cannot.
- **E** makes the server-side cause visible, and records the one signal that does concern security.
- None of the three changes identity's answers, an Accepted ADR or an invariant. They are backward-compatible with every released client and server, because A and B are client-only and E is additive.

**For the owner to decide:**
1. Accept, amend or reject this proposal.
2. Classify E (§6.1 rule 7).
3. Arrange the independent human review that A and B need (OD-12).

## 7. Test plan

Each item names the test to add and the regression it must catch. Every test is checked by breaking the code and watching it fail.

| Option | Test | Must catch |
|---|---|---|
| A | `IdentityAuthenticatorTest`: refresh answered `ERR_INVALID_DPOP_PROOF` with the enrolled key present keeps the stored session, and `sessionState` stays `SignedIn` | A return to clearing the session |
| A | Same, with a different key: the session is cleared, `SESSION_ENDED` | Key loss going undetected (a stuck session) |
| A | Same, for a stored record without a thumbprint (older format): today's behaviour | A crash or misread on old records |
| A | A refusal on every attempt, then `ERR_INVALID_TOKEN` at expiry: the session ends then | An unbounded stuck state |
| A | `SessionCheckWiringTest` (C17), against the real authenticator and MockWebServer: a refresh refused for its proof gives outcome `KEPT`, and no A73 is shown | The background check signing users out |
| B | `DpopProofsTest`: `iat` = phone time + offset, and the offset is clamped to ±24 h | An unbounded or inverted offset |
| B | `IdentityClientTest`: a `400 ERR_INVALID_DPOP_PROOF` whose `Date` is 10 minutes ahead is retried once, with a new `jti` and a corrected `iat`. A second refusal is final | An endless retry, or a reused `jti` |
| B | Live contract (`android-identity-contract`): identity started with a fixed clock 10 minutes ahead; enrolment and refresh succeed | Real-server disagreement |
| E | identity `metrics_test.go`: one refusal of each reason increments its label exactly once | A mislabelled reason |
| E | identity `app` test: a current refresh token with another key's proof emits exactly one audit event, and the session stays live | A missing theft signal, or a silent revocation |
| All | Real PostgreSQL (`identity-postgres.yml`) and the identity-to-edge lifecycle test, unchanged and green | Server regressions |
| All | On an emulator (`android-device-tests.yml`): sign in, set the clock 5 minutes back, force a refresh; the user stays signed in | Device-level regressions |

## 8. Until then

Nothing here is implemented. Meanwhile:
- staging and production checks should confirm that identity's canonical URLs equal the app's configured origin (`INTERNAL-TESTING-SETUP.md`) before any user signs in;
- a support script should tell a user who is signed out unexpectedly to check that the phone sets its date and time automatically.
