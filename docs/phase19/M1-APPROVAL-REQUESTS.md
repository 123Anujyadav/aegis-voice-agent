# Release milestone M1: external approval requests

- **As of:** 2026-11-09, `main` `4078c07`.
- **What this is:** the three external approvals that stand between `main` and M1's exit evidence (OD-10: a release run, the `.aab` on Play's internal track, and at least one tester signing in on a physical phone against staging). Each is set out in the same six points:
  1. why now;
  2. the provider and the alternatives;
  3. the one-time and recurring cost;
  4. the data shared, and privacy;
  5. the accounts, credentials and setup;
  6. the free or local alternative, and the consequence of deferring.
- **What this is not:**
  - **Nothing is requested in chat, and nothing is approved by this document.** Each request needs the owner's explicit approval for its category (spending policy, `PRODUCTION-READINESS-PLAN.md` §4). Approving one does not approve another.
  - **No provider is recommended over another.** The choice is the owner's. The technical notes say only how each option fits what is already built.
  - **No owner decision is closed or changed.** In particular, T4 (Play Integrity) stays unresolved, the telephony staging gate (P1-38: A5–A9) stays unapproved, and the nine OD-10 acceptance items stay OPEN.
- **Prices:** every figure is a **public list price or a vendor's published guide, read on 2026-11-09.** None was quoted to this project, none was verified on the official portals, and none was committed to. Get written quotes before approving spend.
- **Steps:** the step numbers (A1, B1, D1 and so on) are those of [`INTERNAL-TESTING-SETUP.md`](INTERNAL-TESTING-SETUP.md), which holds the full checklist.

| # | Request | Category (§4) | Blocks | Can start |
|---|---|---|---|---|
| R1 | An SMS/OTP provider, a fallback, and India DLT registration | SMS and DLT | Any sign-in (critical path) | Now. DLT is the slowest step |
| R2 | A staging environment for identity and edge-api | Staging | The hosts, pins and release variables; so the release run | Now, in parallel with R1 |
| R3 | A Google Play developer account | Play Console registration | Distribution to testers | Now, in parallel. The upload key follows at no cost |

**Before R1's templates and R3's package name:** the naming review (F26). The product is **Lily AI** (owner, 2026-11-05). SMS text must match its registered DLT template exactly, and Play fixes the package name at the first upload. The deletion template suggested in `ACCOUNT-DELETION.md` §3.1 still says "Aegis", so its wording must be chosen again. Trademark counsel is a separate owner approval.

## R1. SMS/OTP provider and India DLT (E2; steps A1–A5)

**1. Why now.**
- Sign-in is by phone number (ADR-0010), and nobody can sign in without a delivered code. That makes R1 M1's critical path.
- India requires **DLT registration** (TRAI) of the sender as a Principal Entity, its sender header and each message template before carriers deliver. Providers' guides give a few business days for the entity alone, and templates take longer. Starting it first shortens M1 the most.

**2. Provider and alternatives.** No provider is named by any ADR; ADR-0010 §9 requires failover, and `otpdelivery.Sender` already chains up to three providers with timeouts, health and metrics (#109, #118). The broad options:

| Option | Examples (not a shortlist) | Fit with what is built |
|---|---|---|
| An Indian SMS aggregator | MSG91, Exotel, Gupshup, Tata Communications (Kaleyra) | Direct India routes; DLT help is usually part of onboarding. An API key goes in a mounted Secret file |
| A global messaging platform with India routes | Twilio, Infobip, Plivo, Vonage | One account across markets; India still needs the same DLT registration. API credentials as a Secret file |
| The cloud already chosen (ADR-0008) | AWS End User Messaging SMS in `ap-south-1` | Credentials can come from the pod's IAM role (IRSA), so no static key exists; DLT registration is still the sender's. Usable only once R2 exists |
| A non-SMS channel | WhatsApp authentication messages | **Not a substitute:** ADR-0010 verifies the number by SMS, and a change would be an identity-policy decision (owner-only) |

The adapter code (C2) is written **after** the choice, against the chosen provider's documented API, and tested with fakes until its credentials exist.

**3. Cost.**
- **DLT entity registration:** about **₹5,900 including GST** (providers' guides: [Exotel](https://developer.exotel.com/docs/sms-support/dlt-registration), [SMSGatewayHub](https://crm.smsgatewayhub.com/knowledge-base/article/dlt-registration-charges-cost-breakdown-in-india), [Infobip](https://www.infobip.com/docs/essentials/dlt-registration)). The guides **disagree** on whether it is charged once or per operator portal, and once or yearly. Confirm on the portal.
- **Per message:** vendors' 2026 guides give roughly **₹0.10 to ₹0.30 per transactional or OTP SMS**, depending on the route and volume (for example [Message Central](https://www.messagecentral.com/blog/sms-otp-pricing-india), [Bonvoice](https://bonvoice.com/insights/best-otp-sms-service-india-2026/)). These are vendors' own figures. **Recurring**, per message sent, for each sign-in and each deletion code; a failover attempt costs a second message.
- **Internal testing's scale:** a handful of testers sending a few codes each. The per-message cost is small; the fixed DLT fee dominates.

**4. Data shared, and privacy.**
- **To the provider:** each recipient's phone number and the message text with its code. The provider is a **processor under DPDP**, so its terms belong in the privacy review (E9, L1).
- **On the DLT portal:** the entity's documents and the template wording, which carriers can read.
- **Not shared:** identity stores **no phone numbers**. OTP and rate-limit state is keyed by an HMAC pseudonym (migration `0004`), and sessions carry no number.
- **The code is a credential in transit:** short-lived (5 minutes) and rate-limited.

**5. Accounts, credentials and setup.**
- The owner: the provider account(s) and contract; the DLT entity, sender header and **two** templates (sign-in and account deletion, each with the code as its variable).
- SRE: each credential as a **Kubernetes Secret mounted as a file** (never an environment value, a ConfigMap, the repository or chat); **egress** from identity to each provider's API (a network-policy change, needing approval: step A5).
- Engineering: adapter C2, then production wiring of the deletion-code budget once its value is chosen (O13).

**6. Free or local alternative, and deferring.**
- **Local:** the tests' fake senders and the end-to-end harness's stdout sender. **Neither delivers a message;** they prove the code path, not delivery.
- **Email sign-in** (ADR-0032) is Proposed and high-risk (H1, needing OD-12), and still needs an email sender. It is not an alternative today.
- **If deferred:** nobody can sign in, so M1 cannot be met. Everything else continues: the staging work (R2), Play (R3), the release pipeline, and the 9 on-device carrier cases, which need no sign-in (`PHYSICAL-DEVICE-TEST-CHECKLIST.md`).

## R2. Staging for identity and edge-api (X5, E3, E4; steps B1–B6, C1–C5)

**1. Why now.**
- Testers' phones need reachable identity and edge-api hosts.
- The release build refuses to build without both origins and two pins per host (#194), and the pins come from the deployed hosts' certificates.
- So the release run (M1's first evidence) waits on R2.

**2. Provider and alternatives.**
- **Decided by ADR-0008 (Accepted):** AWS, `ap-south-1`, EKS, Aurora PostgreSQL. This request is for **authorization and spend**, not for choosing a cloud.
- **Alternatives within ADR-0008:** a namespace in a shared cluster instead of a dedicated staging cluster; Aurora Serverless v2 or a small provisioned instance. These are SRE sizing choices after authorization.
- **Outside ADR-0008:** another host would be a deviation that needs its own ADR.
- **Separate from the telephony programme's staging gate:** P1-38's A5–A9 stay unapproved. Approving R2 does not approve them.

**3. Cost.**
- **The EKS control plane:** **US$0.10 per cluster-hour** under standard Kubernetes support, about US$73 a month; **US$0.60** under extended support ([AWS EKS pricing](https://aws.amazon.com/eks/pricing/)). **Recurring** while the cluster exists.
- **Not priced here:** the worker nodes, Aurora, the load balancer, data transfer, the domain and certificates. The `ap-south-1` rates were not found in this review. Use the [AWS Pricing Calculator](https://calculator.aws/) for `ap-south-1`. **All recurring while running.**
- **Self-generated, no cost:** the Ed25519 signing key and the HMAC key.

**4. Data shared, and privacy.**
- **Held in staging:** testers' sessions and their device-binding data, pseudonymised OTP state and the security audit log. No phone number is stored.
- **India residency** (ADR-0012): `ap-south-1`.
- **Database TLS:** `sslmode=verify-full` with an explicit CA bundle. Outside development, both services refuse anything else (ADR-0029).
- **Keys:** handled per the runbooks (`access-token-signing-key-rotation.md`); the private signing key goes only to identity, as a mounted Secret file.

**5. Accounts, credentials and setup.**
- The owner: authorize the AWS account, the cluster or namespace, and IAM (B1); choose the domain for two HTTPS hosts (B3).
- **The owner decides who writes or commits the identity and edge-api manifests (C1).** The staging overlay path is `infra/k8s/overlays/staging/` (ADR-0027). How it is built remains gate A5's to approve, so **no staging manifest has been written.**
- SRE: PostgreSQL with `verify-full` (B2); TLS certificates and a backup key pair per host (B4); the keys as Secret files (B5, B6); the schema migrated to the latest migration with `identity migrate` (C2).
- **Configuration:** the `CS_IDENTITY_*` and `CS_EDGE_API_*` names in `INTERNAL-TESTING-SETUP.md` C3–C4. **`CS_IDENTITY_ENVIRONMENT` must be set in the pod specification:** under OD-15, an unconfigured identity in staging or production exits 1.

**6. Free or local alternative, and deferring.**
- **Local:** CI already runs the real binaries on a disposable PostgreSQL, behind a local TLS proxy (`android-identity-contract.yml`, `identity-postgres-tests.sh`). **Testers' phones cannot reach it,** so it is not a staging environment.
- **If deferred:** no hosts, so no pins, no release run and no tester sign-in: M1 cannot be met. The 9 on-device carrier cases and all CI continue.

## R3. Google Play developer account (X1; steps D1–D7)

**1. Why now.**
- The `.aab` must be on Play's internal-testing track for M1.
- The account is independent of R1 and R2. Identity verification takes time.
- On a **personal** account, production access later needs a 14-day closed test (below), so the account type matters now.

**2. Provider and alternatives.**
- **Google Play** is the distribution channel the plan assumes. Other stores (for example Samsung Galaxy Store) are not in the plan.
- **The choice to make: a personal or an organisation account.**
  - Personal accounts created after 2023-11-13 must run a closed test with opted-in testers for 14 days before applying for production. Google's help states 20 testers ([Google Play help](https://support.google.com/googleplay/android-developer/answer/14151465)); check the current figure.
  - Organisation accounts are exempt, but need organisation verification.

**3. Cost.**
- **US$25, one-time** ([Google Play help](https://support.google.com/googleplay/android-developer/answer/6112435)); not refunded if verification fails.
- **The upload key:** none, generated offline with `keytool`.
- **No recurring fee.**

**4. Data shared, and privacy.**
- **With Google:** the developer's identity verification; testers' Google accounts (the tester list); the app bundle. Under Play App Signing, Google holds the app-signing key.
- **Later, before production, not for internal testing:** the Data safety form and the privacy policy (L4, L5). Whether the internal track already requires them is **unverified**.
- **The upload key is the release's root of trust:** never in Git, CI logs or chat.

**5. Accounts, credentials and setup.**
- The owner: register, verify, create the app, keep Play App Signing.
- **The owner confirms the permanent package name (D2).** The release `applicationId` is `com.callscreen`; whether it is free on Play is unverified. It cannot change after the first upload, and the naming review (F26) is pending.
- The owner (repository admin): protect the `play-release` Environment with required reviewers **before** adding the four upload-key secrets (D5, D6).
- Then the tester list (D7).

**6. Free or local alternative, and deferring.**
- **Local:** installing the debug or staging APK with `adb`. Fine for device evidence, but it is **not distribution**, and gives no Play Integrity verdicts. (T4 stays unresolved either way.)
- **If deferred:** no internal track, so M1 cannot be met. Device testing by `adb` continues.

## Not requested here

| Category | Why not now |
|---|---|
| Forwarding (X4: DID pool, CPaaS) | M3, not M1. Device cases M3–M5 moved to M3 under OD-10 |
| LLM | M3 (the voice assistant) |
| Speech | **Deferred** (OD-17) |
| Play Integrity (E1) | T4 is **unresolved**: neither activated nor removed |
| FCM (E7) | Notifies a prior device on re-enrolment (SIM-swap review); not an M1 exit item |
| Observability backend (E5) | Production spans; not an M1 exit item |
| External security review (E8), privacy review (E9), web deletion page (E10) | M2 |

## After an approval

1. The owner records it, with its date and the approved category.
2. Engineering does only the work that the approval covers: for R1, the adapter (C2) with fakes first; for R2, the manifests if the owner assigns them (C1), under gate A5's terms. Nothing else follows automatically.
3. Every credential reaches the system only as a Secret file or a protected Environment secret, set by the owner or SRE.
4. Nothing is reported as working until it is tested against the real service. Each such test is recorded with its evidence.
