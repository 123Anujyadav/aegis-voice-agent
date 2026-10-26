# Internal-testing setup: owner checklist

- **As of:** 2026-10-26, `main` `0bde265`.
- **Purpose:** the exact steps between the repository and a Play **internal-testing** release that testers can actually use (sign in, then set up call screening), in dependency order. Each step names who must act, whether it needs your explicit approval, its cost (all **unverified** unless stated), and the local or mock alternative.
- **Nothing here has been done.** No account, provider, DLT registration, domain, server, key, secret, Environment variable or Play Console setting has been created. Nothing has connected to a live server, and no Play-installed build has been tested.
- **Not claimed:** Google Play readiness. Phase 19's completion criteria are still only **proposed** (`ROADMAP-INVENTORY.md` §2, OD-10).
- **Secrets never go into the repository, a ticket or chat.** Where a step needs one, it says where it lives: a GitHub Environment secret, or a Kubernetes Secret mounted as a file.

## 0. What depends on what

```
 A. SMS/OTP provider + India DLT ──────────────┐
                                                ├─► E. adapter code (engineering) ─┐
 B. Staging cloud, database, keys, domain ──┬──┘                                   │
                                            ├─► C. deploy identity + edge-api ─────┼─► F. pins + Environment variables ─► G. first release run ─► H. Play internal test
 D. Play Console app + Play App Signing ────┼──────────────────────────────────────┘                                          ▲
    + upload key + play-release Environment ┴──────────────────────────────────────────────────────────────────────────────────┘
```

- **Can start now, independently and in parallel:** A (provider and DLT, which is the slowest), B (cloud account and domain decision), and D (Play Console and upload key).
- **Depends on A:** E. The adapter can be written against a provider's documented API **only after you choose the provider**, and tested with the fake sender until credentials exist.
- **Depends on B and E:** C. The deployment can be tested before A finishes, but nobody can sign in until the adapter and its credentials exist.
- **Depends on C:** F. Pins come from the deployed hosts' certificates.
- **Depends on D and F:** G. The release build refuses to run without the endpoint variables (#194).

## A. SMS/OTP provider and India DLT (E2): **the critical path**

| # | Step | Who | Your approval | Cost |
|---|---|---|---|---|
| A1 | **Choose a primary SMS provider that delivers in India, and a fallback** (ADR-0010 §9 requires failover; `otpdelivery.Sender` supports up to 3 providers) | Owner | **Yes:** provider and contract | Unverified |
| A2 | **Register on an India DLT platform (TRAI) as a Principal Entity**, and register the **sender id (header)** | Owner, with the provider | **Yes** | Unverified |
| A3 | **Register two content templates, each with the code as its variable:** (1) the sign-in code; (2) the **account-deletion** code (`ACCOUNT-DELETION.md` §3.1 suggests "Your Aegis account deletion code is {#var#}. If you did not ask to delete your account, ignore this message."). Carriers block messages that match no registered template. | Owner | **Yes:** the wording | Unverified |
| A4 | **Provider credentials:** SRE provisions them as Kubernetes Secrets mounted as **files** (never an environment value or a ConfigMap) | SRE | — | — |
| A5 | **Egress** from identity to each provider's API (a network-policy change) | SRE | **Yes:** network change | — |

- **Local alternative:** the fake senders in the unit tests, and the end-to-end harness's stdout sender (`E2E-OTP`, `E2E-DELETION-CODE`). **Neither is a production integration.**
- **Runbook:** `docs/runbooks/identity-provider-onboarding.md` §1.

## B. Staging cloud, database, keys and domain (X5, E3, E4)

The cloud is decided by **ADR-0008 (Accepted):** AWS, `ap-south-1` primary, EKS for compute, and Aurora PostgreSQL. What is not decided or provisioned is listed below.

| # | Step | Who | Your approval | Cost |
|---|---|---|---|---|
| B1 | **Authorize a staging environment for identity and edge-api:** the AWS account, an EKS cluster or namespace, and its IAM. This is separate from the telephony programme's staging gate (P1-38: A5–A9 unapproved, staging NO-GO). | Owner, then SRE | **Yes** | Unverified |
| B2 | **PostgreSQL** reachable from both services, with **`sslmode=verify-full` and an explicit CA bundle**. Outside `development`, both services refuse anything else (`pgshared`, ADR-0029). identity and edge-api use **the same database**: edge-api's DPoP replay table is created by identity's migrations. Point-in-time recovery comes before production (E4). | SRE | — | Unverified |
| B3 | **The public domain** for two HTTPS hosts: identity and edge-api, for example `identity.<domain>` and `api.<domain>`, each a DNS name. The release build refuses IP addresses. | Owner | **Yes:** the domain | Unverified |
| B4 | **TLS certificates** for both hosts, plus a **backup key pair generated in advance** for each (its public key becomes the backup pin; ADR-0010 requires two pins) | SRE | — | Unverified |
| B5 | **The access-token signing key:** generate the Ed25519 key pair offline (runbook `access-token-signing-key-rotation.md` §4). The private half goes to identity as a mounted Secret file; the public half goes to edge-api. | SRE | — | None (self-generated) |
| B6 | **identity's HMAC key:** a random secret (≥ 32 bytes) as a mounted Secret file (`CREDENTIALS-AND-ACCOUNTS.md` §1) | SRE | — | None |

**Local alternative:** the CI jobs already run the real identity and edge-api binaries on a disposable PostgreSQL, with generated keys behind a local TLS proxy (`android-identity-contract.yml`, `identity-postgres-tests.sh`).

## C. Deploy identity and edge-api (depends on B; on A and E for sign-in)

| # | Step | Detail |
|---|---|---|
| C1 | **Deployment manifests** | On `main`, `infra/k8s` holds only a generic template. The per-service manifests are in the owner's uncommitted work, and the staging overlay path is `infra/k8s/overlays/staging/` (ADR-0027). **You decide** whether they are committed, or new ones are written for identity and edge-api. Images: `docker/go/Dockerfile`. |
| C2 | **Migrate the schema to `0009`** | Run the identity binary with the single argument `migrate` (the binary's commands are `migrate`, `version` and `signing-key-fingerprint`) and `CS_IDENTITY_DATABASE_DSN_FILE`. It is the **only** path that applies DDL; the service never migrates at startup. It prints the migration names and the resulting version, never the DSN. |
| C3 | **identity's configuration** (names; the values are yours) | **Secret files:** `CS_IDENTITY_DATABASE_DSN_FILE`, `CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE`, `CS_IDENTITY_HMAC_SECRET_KEY_FILE`. **Configuration:** `CS_IDENTITY_ACCESS_TOKEN_ISSUER`, `_AUDIENCE`, `_KID`; `CS_IDENTITY_ENROLLMENT_CANONICAL_URL` = identity origin + `/v1/otp/validate`; `CS_IDENTITY_REFRESH_CANONICAL_URL` = identity origin + `/v1/token/refresh` (the deletion URL is derived from it); `CS_IDENTITY_ENVIRONMENT` (not `development`); ports `CS_IDENTITY_HTTP_PORT` and `CS_IDENTITY_HEALTH_PORT`. **Play Integrity variables stay unset** (O7 and E1 undecided: sessions are recorded `unverified`). |
| C4 | **edge-api's configuration** | **Secret files:** `CS_EDGE_API_DATABASE_DSN_FILE` (the same database), `CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE`. **Configuration:** `CS_EDGE_API_ACCESS_TOKEN_KID`, `_ISSUER`, `_AUDIENCE` (equal to identity's); `CS_EDGE_API_PUBLIC_ORIGIN` = the edge-api origin, **exactly** the value the app is built with; `CS_EDGE_API_ENVIRONMENT`. The previous-key variables are used only during a rotation. |
| C5 | **Health** | Readiness on each health port must be 200 before the hosts are given to the app. |

## D. Play Console, Play App Signing and the upload key (X1, X2): independent of A–C

| # | Step | Who | Your approval | Cost |
|---|---|---|---|---|
| D1 | **Play Console developer account** | Owner | **Yes** | Registration fee **not verified here** |
| D2 | **Confirm the permanent package name.** The release `applicationId` is **`com.callscreen`** (build-logic convention plugin). A Play package name can never be changed after the first upload, and must be unused on Play. Whether it is available is **not verified**. | Owner | **Yes** | — |
| D3 | **Create the app;** keep **Play App Signing** (Google holds the app signing key; new apps are enrolled by default) | Owner | — | — |
| D4 | **Generate the upload key offline** with `keytool`, on the owner's machine. It never enters the repository, CI logs or chat. | Owner | — | None |
| D5 | **The `play-release` GitHub Environment: add its protection (required reviewers) BEFORE any secret** | Owner (repository admin) | — | — |
| D6 | **Environment secrets:** `ANDROID_UPLOAD_KEYSTORE_BASE64`, `ANDROID_UPLOAD_KEYSTORE_PASSWORD`, `ANDROID_UPLOAD_KEY_ALIAS`, `ANDROID_UPLOAD_KEY_PASSWORD` | Owner | — | — |
| D7 | **The internal-testing track:** the tester list (their Google accounts) | Owner | — | — |

## E. Engineering after A is chosen

These are code tasks, each tested with fakes before any credential exists:
- **The sign-in adapter:** an `otpdelivery.Provider` for each chosen provider (runbook §1).
- **The deletion-code adapter:** a `DeletionCodeSender`, needed only when web deletion is enabled (D5 / O13).
- **Production wiring of the hourly deletion-code budget.** Today `SetDeletionCodeBudget` is called only by tests and the end-to-end harness; the value is an owner or SRE choice.

## F. Pins and the release variables (depends on C)

1. **Compute each host's pin** from its served certificate's public key (SPKI SHA-256, base64), for example:
   `openssl s_client -connect <host>:443 -servername <host> </dev/null | openssl x509 -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | openssl base64`.
   Do the same for each **backup** key from B4 (`openssl pkey -in <backup key> -pubout -outform der | …`).
2. **Set the `play-release` Environment variables** (#194). They are public values, not secrets:
   - `CALLSCREEN_IDENTITY_BASE_URL` and `CALLSCREEN_EDGE_API_BASE_URL`: https origins, no path;
   - `CALLSCREEN_IDENTITY_CERT_PINS` and `CALLSCREEN_EDGE_API_CERT_PINS`: `sha256/<current>,sha256/<backup>`;
   - `CALLSCREEN_PLAY_INTEGRITY_CLOUD_PROJECT_NUMBER`: leave **unset** until E1 and O7 are decided.
3. **Check:** the edge-api base URL must equal `CS_EDGE_API_PUBLIC_ORIGIN` exactly. A mismatch makes every proof's `htu` wrong, and edge-api refuses every request.

**Verified locally (2026-10-26):** with `callscreen.requireReleaseEndpoints=true`, the build fails, naming the property, for:
- no values;
- an `http://` origin;
- an IP-address host;
- a single pin;
- a repeated pin.

It passes with a complete configuration. A normal debug build with no endpoints is unaffected. `pr-android` proves the passing and failing paths on every Android PR.

## G. The first release run (depends on D and F)

1. Run **Release (Android)** (`release-android.yml`, manual), with:
   - `version_code`: 1 or more, and greater than anything uploaded before;
   - `version_name`.
2. It builds `bundleRelease` (R8) and checks that the signer is the upload key's certificate, whose SHA-256 it prints so you can compare it in Play Console. It keeps the `.aab` and its R8 mapping as artifacts. **It does not upload to Play.**

## H. Internal testing (depends on G)

1. **Upload the `.aab`** to the internal-testing track yourself, in Play Console.
2. **Testers sign in with their own numbers.** This needs A, E and C working.
3. **Verify at submission (not verified here):** whether the internal track requires the Data safety form, a privacy policy URL, the account-deletion URL, or content-rating answers before it can be published.
4. **Then run the device tests that need this setup:** T2 (the physical phone, SIM and carrier sheet, 12 cases) and T3 (sign-in, refresh and deletion on a device against the live hosts). Neither can run before.

## Decisions that need your explicit approval

| Decision | Blocks |
|---|---|
| The SMS provider(s) and contract; DLT registration; the two template texts (A1–A3) | Any sign-in |
| Staging authorization: account, cluster and IAM (B1); the domain (B3) | C, F, G |
| The permanent package name `com.callscreen` (D2) | The first upload |
| Who writes or commits the identity and edge-api manifests (C1) | C |
| The Phase 19 completion criteria (OD-10) | Calling Phase 19 done |
| The deletion-code budget value, and web deletion itself (O13 / D5) | Only web deletion; **not** internal testing |

## Costs that remain unknown

- SMS per message, for each provider;
- DLT registration fees;
- the AWS staging footprint (EKS, Aurora, load balancer, data transfer);
- the domain and the TLS certificates;
- the Play Console registration fee.

None was looked up or committed to.
