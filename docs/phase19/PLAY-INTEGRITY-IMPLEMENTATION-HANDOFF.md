# Play Integrity: SRE handoff, permission verification and wiring checklist

- **As of:** 2026-10-25, `main` at `cd6bd50`.
- **What this is:** the operational follow-on to
  [PLAY-INTEGRITY-DECISION-MEMO.md](PLAY-INTEGRITY-DECISION-MEMO.md). It does
  not repeat the memo's analysis.
- **It approves nothing.**
  - Every policy value and the authentication method stay **proposed** (memo
    §11–§12).
  - Nothing here may be acted on until the owner approves memo §12 groups A
    and B1.

## 1. SRE handoff for B2: the deployment identity Google must trust

**What the repository has today (verified):**
- no EKS cluster definition;
- no identity Deployment, Kubernetes ServiceAccount or IRSA annotation.

`infra/k8s/base` holds only a generic `aegis-service-template` Deployment in
namespace `aegis-system`. ADR-0008 (accepted) fixes the platform: EKS in
`ap-south-1`, and no static cloud credentials.

**The outputs SRE must provide** (values, not secrets):

| # | Output | Why it is needed |
|---|---|---|
| S1 | The EKS cluster's **OIDC issuer URL** (`aws eks describe-cluster --name <cluster> --query cluster.identity.oidc.issuer`) | It is the `--issuer-uri` of Google's OIDC provider. Google's EKS guide states that no EKS-side change is needed for it. |
| S2 | identity's **namespace** | The attribute condition limits federation to it |
| S3 | identity's **Kubernetes ServiceAccount** name, and confirmation that identity's pods run as it (`serviceAccountName`) | The federated subject is `system:serviceaccount:<S2>:<S3>`. Only that subject gets `roles/iam.workloadIdentityUser` on the Google service account (memo §7). |
| S4 | A **projected service-account token** volume in identity's pod spec: audience = the provider's resource path, `expirationSeconds: 3600` | This is the credential source. The kubelet rotates it, and nothing long-lived exists. |
| S5 | A **ConfigMap** holding Google's credential configuration file, mounted read-only into identity | Google states the file has no private key and need not be confidential, so it is a ConfigMap, **not a Secret** |
| S6 | **Egress** from identity to `sts.googleapis.com`, `iamcredentials.googleapis.com` and `playintegrity.googleapis.com` | Token exchange, impersonation and decoding. An egress block is handled safely, as `unavailable`, but would silently leave every session `unverified`. |

S4 and S5 are pod-spec changes for SRE, made **after** S1–S3 exist and
the owner has approved B1. They are not made here.

### 1a. Order, classification and the evidence to return

**None of S1–S6 is a secret.** Each is public configuration (an identifier, a
manifest or a network rule). If a step seems to need a secret, stop: under the
recommended design none does (Google states that the credential configuration
holds no private key).

| # | Depends on | Evidence SRE returns (reviewable, no secrets) |
|---|---|---|
| S1 | An EKS cluster, which the repository does not define | The issuer URL string, as `aws eks describe-cluster` prints it |
| S2 | The cluster | The namespace manifest (in SRE's infrastructure change) |
| S3 | S2 | The ServiceAccount manifest, and identity's Deployment showing `serviceAccountName: <S3>` |
| — | **C3, Google side (owner or SRE):** the pool and the OIDC provider for S1, with an attribute condition on S2 and S3 | The provider's resource path (public). **S4 and S5 cannot be written without it.** |
| S4 | C3 (the provider path is the token audience), and **B1 approved** | The pod spec, showing the projected token volume: audience = the provider path, `expirationSeconds: 3600` |
| S5 | C3, **C4** (the service account's email appears in the impersonation URL), and **B1 approved** | The ConfigMap manifest. The reviewer confirms it has **no `private_key` field** |
| S6 | identity's Deployment (S3) | The egress rule change, and a TLS handshake from an identity pod to the three hosts (it needs no credentials) |

**Order:** S1 → S2 → S3 → C3 → C4 → S4, S5. S6 can run in parallel once S3
exists. Staging (§3, items 9–11) waits for all of them, plus C1, C2, C5 and
C7.

## 2. Verification plan for B3: the minimum Google permissions

Google's documentation names a service account "within the Google Cloud
project that's linked to your app", with the `playintegrity` scope. **It states
no IAM role.** Grant nothing broad on the assumption that one is needed.

**Who and where:** owner or SRE, in **staging**, with a Play-installed build
(C7).
1. Create the dedicated service account with **no project roles**.
2. Grant only `roles/iam.workloadIdentityUser` on that service account, to the
   S3 subject.
3. With the verifier configured in staging only (§3, after approval), make
   one enrolment from the device.
4. **Read the outcome without exposing secrets.** Use
   `identity_play_integrity_outcomes_total{outcome}` and identity's error log,
   which never contains tokens or Google's response text:
   - **`certified` or `restricted` recorded:** the permissions are sufficient.
     Record "no IAM role beyond the Play link" in memo §12 B3.
   - **`unavailable`, and Google answers 403:**
     - read the Google Cloud audit or error detail for the **specific missing
       permission**;
     - grant the **narrowest role that contains only it**;
     - repeat.
     - A 403 for an API that is *disabled* is C2, not a role.
   - **`unavailable` for a token-exchange failure:** the problem is the
     federation configuration (S1, S3, S4 or S5), not IAM on the Play
     Integrity side.
5. Record the final binding set in memo §12 B3, then **remove anything not
   proven necessary**.

## 3. Wiring checklist, in dependency order (after approval)

**Preconditions:** memo §12 A and B1 approved; C1–C5 and S1–S5 done.

**Code (one identity PR):**
1. **Configuration contract.** Read these through `platform.LoadConfig`, as
   `SecurityConfig` does:
   - the package name;
   - the certificate digests (43-character base64url);
   - the approved `MaxAge`, `FutureSkew` and `DecodeTimeout`;
   - the credential-configuration file path.
   - All the names are **proposed**: the credentials register's names are
     proposals.
2. **All or nothing.**
   - Nothing set: no verifier (`SetIntegrityVerifier(nil, …)`), which is
     today's behaviour.
   - Everything set and valid: the verifier.
   - **Partly set or invalid: identity refuses to start**, naming the
     variables and never a value.
   - A silently half-configured verifier must be impossible.
   - The existing `IntegrityPolicy.Validate` and `playintegrity.New` checks
     run at startup.
3. **`TokenSource`** from the WIF credential configuration
   (`golang.org/x/oauth2/google`, external account, scope `playintegrity`).
   This is a new dependency.
   - It is built only when configured.
   - Its errors reach the adapter as plain errors, so they are `unavailable`,
     never a refusal.
4. **Wiring:** in `cmd/server`, call `playintegrity.New` and then
   `SetIntegrityVerifier`, only in the fully configured case.

**Tests (local fake only, no Google):**
5. None set → no verifier, and every session `unverified` (today's
   behaviour, now asserted).
6. Partial or invalid configuration (each variable missing, a hex digest, an
   out-of-bounds duration) → startup fails, and the message names variables
   only.
7. Fully configured against `httptest`'s fake `decodeIntegrityToken`:
   - a certified verdict → `certified`;
   - each failed check → `restricted`, with its reason;
   - fake 400 → refused, 400 `ERR_INVALID_INTEGRITY_TOKEN`, no session;
   - fake 5xx, 429, 401 or 403, a timeout, or an oversized response →
     `unverified`, never refused.
   - Several of these already exist at the adapter and service level
     (`playintegrity_test.go`); the new tests cover the **wiring and
     configuration**.
8. A `TokenSource` failure → `unavailable` → `unverified`.

**Staging acceptance (real device, Play-installed build):**
9. Enrolment from a Play-installed build → `certified`. **This is the first
   real-device evidence**, and B3 (§2) is verified in the same run.
10. A sideloaded build of the same app → `restricted` (unlicensed or
    unrecognised), **not refused**.
11. Unset the configuration (the kill switch, memo §5) → `unverified`, nothing
    refused.

**Not part of this PR:**
- O8 enforcement (memo §10c);
- the refresh-time downgrade.

Each waits for its own approval.
