# External dependencies and owner decisions: tracker

- **As of:** 2026-10-24, `main` at `c6cecf9`.
- **What it lists:** everything authentication and identity work is waiting
  on that the repository cannot supply itself. None of it blocks the other
  engineering work, which continues around these items.
- **Ground rules:**
  - Nothing here is requested in chat.
  - No secret, account or infrastructure is created from this repository.
  - Nothing that depends on a missing item is reported as working.

## 1. External services and infrastructure

| # | Dependency | Owner | Blocks | What exists today | Next step for the owner or SRE |
|---|---|---|---|---|---|
| E1 | **Google Play Integrity:** a Google Cloud project, credentials, and egress to Google | Owner | ADR-0010 §5(b): integrity at issuance and refresh; the restricted tier | A fail-closed verifier port, an offline-tested verdict policy, a decode timeout and outcome metrics (#108, #118). **Wired into issuance since #131** ([ADR-0031](../adr/0031-play-integrity-at-issuance.md)): the verdict is bound to the DPoP proof, sent in an `Integrity-Token` header, and the tier recorded but not enforced. No Google verifier is configured, so every session is `unverified`. Nothing produces a real verdict without E1. | Create the project and link the Play Console. Provision credentials as a Kubernetes Secret (never in the repository). Decide the restricted tier. |
| E2 | **OTP delivery provider, plus India DLT** (entity, sender header, templates) and a secondary provider | Owner | Any real enrolment | A failover sender, health checks and delivery metrics (#109, #118); `docs/runbooks/identity-provider-onboarding.md` | Choose the providers, register with DLT, then provision credentials as a Secret |
| E3 | **Signing-key operations:** generating Ed25519 keys and holding them in Kubernetes Secrets for identity (private) and edge-api (public set) | SRE | Production issuance | The key-set verifier, rotation tests, fingerprint commands (#119) and the runbook (#107, #114) | Generate and provision keys in staging. **Run the rotation runbook once in staging** (ADR-0029 analysis §2). |
| E4 | **Database:** `verify-full` CA bundle, multi-AZ, point-in-time recovery | SRE | Running edge-api and identity outside development; ADR-0030 stage 2 → 3 | TLS policy enforced and tested (#99, #104) | Provide the CA bundle and DSNs. Confirm PITR before D30.3 stage 3 (migration plan §4). Alert on the edge-api 503 rate (ADR-0029 analysis §3). |
| E5 | **Tracing backend** (ADR-0028: X-Ray via OTel) | SRE | Exporting production spans | Edge and identity spans exist with the log exporter (#97, #101, #121) | P1: confirm X-Ray accepts random W3C trace ids. P2: cost and budget. Then the infrastructure steps. |
| E6 | **Trusted-proxy model:** how the ALB sets `X-Forwarded-For`, and which hops are trusted | SRE | Rate limiting per source address (ADR-0010 §10) | Nothing. **Forwarding headers are not trusted anywhere.** | Document the ALB topology and the number of trusted hops. Only then can per-source limits be designed. |
| E7 | **FCM project and credentials** | Owner | Notifying the prior device on re-enrolment (SIM-swap review §4) | `FcmPushDispatcher` interface and mock only | Create the project, then provision credentials as a Secret |
| E8 | **External cryptographic review / penetration test** of `accesstoken`, `dpop` and the refresh path | Owner (spend) | Treating production issuance as reviewed | One in-process independent review (ADR-0029 §6). Tests are not proof of security. | Commission it |
| E9 | **Privacy review** (DPDP Act) | Owner | Device-history retention R and audit attribution (SIM-swap review §2–§3) | — | Set R, and decide how audit records are attributed |

## 2. Owner decisions

| # | Decision | Brief | Recommendation |
|---|---|---|---|
| O1 | Ratify ADR-0029, including the reversal of D11.3 and the amendments to ADR-0010 §5(c), §9 and §10 | `docs/phase18/ADR-0029-RATIFICATION-BRIEF.md`; `docs/phase19/ADR-0029-DEVIATION-ANALYSIS.md` | Ratify, and record the token profile as "RFC 9068-derived" (no `client_id`) |
| O2 | ADR-0030 D30.3 (stored subscriber ids), including the `id_origin` column and the subscriber keyring rule | `docs/phase19/ADR-0030-D30.3-MIGRATION-PLAN.md` | Accept with both amendments. Stage 4 (retirement) is a separate, later decision. |
| O3 | OTP backoff (fixed lockout or exponential) | Ratification brief §3a | Keep the fixed lockout until abuse data says otherwise |
| O4 | Session lifetime (24 h, or ADR-0010's 90 days) | Ratification brief §2 | A product decision. Note that it changes the SIM-swap memory window (SIM-swap review §2). |
| O5 | SIM-swap policies: cooling-off length and scope; prior-device notification; old-session handling; re-verification | `docs/phase19/SIM-SWAP-CONTROLS-REVIEW.md` §4 | 72 h cooling-off on history and configuration changes; keep old sessions; FCM "this wasn't me" |
| O6 | Device-history retention R | SIM-swap review §2 | Choose after the privacy review (E9) |
| O7 | Ratify ADR-0031 (Play Integrity at issuance: request binding, header, tier recording) | `docs/adr/0031-play-integrity-at-issuance.md` | Ratify. It carries out the accepted ADR-0010 §5(b) and changes no permission. |
| O8 | What a `restricted` (and `unverified`) session may do | ADR-0010 §5(b); ADR-0031 D31.5 | A product decision. Until it is made, the tier is recorded and audited, never enforced. |

## 3. Engineering work that is ready once each item lands

| When this lands | Work that becomes possible |
|---|---|
| E1 | A Google `IntegrityVerifier` (`decodeIntegrityToken`) behind the existing port. With ADR-0031 implemented, this is the only missing piece for real verdicts. |
| O2 | The `pgstore` subscriber directory, real-PostgreSQL tests, and wiring at stage 0 → 1 (migration plan §7) |
| O6 / E9 | The device-history table, and re-enrolment detection that survives housekeeping |
| E6 | The design for per-source rate limiting |
| D30.2 keyrings | Per-session issued-hash history for refresh reuse (refresh analysis §3) |
| O1 (if `client_id` is chosen) | The two-step `client_id` rollout (ADR-0029 analysis §5) |
