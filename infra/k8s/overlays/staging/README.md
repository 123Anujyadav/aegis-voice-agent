# Staging overlay: identity and edge-api

**Status: DRAFT for gate A5 approval (ADR-0027). Not applied. Nothing is
deployed.** No cluster, registry, secret store or DNS name exists for it yet.
These files are a proposal for the owner's review. They are not a record that
staging is authorized: staging authorization (`docs/phase19/INTERNAL-TESTING-SETUP.md`
B1) and gate A5 are both still open.

- **Path.** ADR-0027 (Accepted) sets one manifest tree, `infra/k8s`, with the
  staging overlay at `infra/k8s/overlays/staging/`.
- **Self-contained.** This overlay does **not** reference `../../base`.
  `infra/k8s/base/deployment.yaml` is a generic template (`aegis-service-template`,
  probe port 9090, `latest` tag) that does not match the services' real ports
  or security needs, and P1-37 / ADR-0027 leave the base as it is. Nothing under
  `infra/k8s/base/` was changed, and `infra/k8s/base/services/` is untouched.
- **Scope.** identity and edge-api only (INTERNAL-TESTING-SETUP C1). It is
  separate from the telephony programme's staging (P1-38 A5–A9, gateways A and
  B), which this overlay does not contain.

## Files

| File | What it is |
|---|---|
| `kustomization.yaml` | Namespace `lily-staging`, common metadata labels, the resource list, the generated `access-token-contract` ConfigMap, and the image digests |
| `namespace.yaml` | The namespace, with Pod Security Admission `restricted` (enforce, audit, warn) |
| `identity-serviceaccount.yaml` | identity's ServiceAccount: IRSA annotation (placeholder role), no API token mounted |
| `identity-externalsecret.yaml` | ESO `ExternalSecret` → Secret `identity-secrets`: signing key, HMAC key, DSN |
| `identity-deployment.yaml` | identity: 2 replicas, restricted security context, probes, env, read-only Secret files |
| `identity-service.yaml` | ClusterIP Service on 8080 |
| `identity-pdb.yaml` | PodDisruptionBudget, `minAvailable: 1` |
| `edge-api-serviceaccount.yaml` | edge-api's ServiceAccount: IRSA annotation (placeholder role), no API token mounted |
| `edge-api-externalsecret.yaml` | ESO `ExternalSecret` → Secret `edge-api-secrets`: public key, DSN |
| `edge-api-deployment.yaml` | edge-api: same pattern as identity |
| `edge-api-service.yaml` | ClusterIP Service on 8080 |
| `edge-api-pdb.yaml` | PodDisruptionBudget, `minAvailable: 1` |
| `db-ca-externalsecret.yaml` | ESO `ExternalSecret` → Secret `db-ca-bundle`: the database CA bundle (ADR-0029) |
| `networkpolicy-default-deny.yaml` | Deny all ingress and egress in the namespace |
| `networkpolicy-dns-egress.yaml` | All pods → kube-dns, port 53 |
| `networkpolicy-database-egress.yaml` | identity, edge-api, migrate → database CIDR, TCP 5432 |
| `networkpolicy-identity-ingress.yaml` | Ingress controller → identity:8080; node subnet → identity:8081. Also holds the **disabled** SMS-egress stanza, as a comment |
| `networkpolicy-edge-api-ingress.yaml` | Ingress controller → edge-api:8080; node subnet → edge-api:8081 |
| `migrate/kustomization.yaml` | A separate kustomization holding only the migrate Job |
| `migrate/identity-migrate-job.yaml` | `identity migrate`, run once by an operator (C2) |

## Order of operations (for whoever is eventually authorized; not done)

1. Fill every placeholder (below). Before applying, check that none is left:
   `grep -rnE '<[A-Z_]+>' infra/k8s/overlays/staging --include=*.yaml | grep -v '#'`
   must print nothing. Most unfilled placeholders also fail on their own: the
   API server rejects a label value or a CIDR containing `<`, an image
   reference containing `<` cannot be pulled, and a canonical URL containing
   `<DOMAIN>` makes identity refuse to start.
2. `kubectl apply -k infra/k8s/overlays/staging`. The namespace, policies,
   ServiceAccounts and ExternalSecrets are created. The Deployments do **not**
   become ready yet: there is no schema, and edge-api refuses to start without
   the replay table that identity's migrations create.
3. Run the migrate Job (C2). The procedure is in `migrate/identity-migrate-job.yaml`.
4. `kubectl -n lily-staging rollout restart deployment/identity deployment/edge-api`,
   then confirm `/readyz` is 200 on each health port (C5) before the hosts are
   given to the app.

## Placeholders

| Placeholder | Where | Filled by (per INTERNAL-TESTING-SETUP) |
|---|---|---|
| `<ACCOUNT_ID>`, `<ROLE>` in `arn:aws:iam::<ACCOUNT_ID>:role/<ROLE>` | both ServiceAccounts (two **different** roles) | Owner authorizes the account (B1); SRE creates the roles, with **no** permissions attached (neither service calls AWS) |
| `<REGISTRY>` | `kustomization.yaml`, `migrate/kustomization.yaml` | SRE (image registry; publishing images is itself a deployment decision, see `.github/workflows/container-images.yml`) |
| `<IDENTITY_IMAGE_DIGEST>`, `<EDGE_API_IMAGE_DIGEST>` | the same two files. The identity digest must be identical in both | SRE, from the built and recorded images (`docker/go/Dockerfile`, `--build-arg SERVICE=identity` / `edge-api`) |
| `<DOMAIN>` in `https://identity.<DOMAIN>/…` and `https://api.<DOMAIN>` | `identity-deployment.yaml`, `edge-api-deployment.yaml` | Owner (B3). Each must be **exactly** the origin the app is built with |
| `<ACCESS_TOKEN_ISSUER>`, `<ACCESS_TOKEN_AUDIENCE>`, `<ACCESS_TOKEN_KID>` | `kustomization.yaml` `configMapGenerator` | SRE (B5). The kid follows the key-id rules in `docs/runbooks/access-token-signing-key-rotation.md` §2 |
| `<PLACEHOLDER>/staging/...` remote keys | the three `*-externalsecret.yaml` | SRE (B2, B5, B6), in AWS Secrets Manager, `ap-south-1` |
| `aws-secrets-manager-ap-south-1` (`ClusterSecretStore`) | the three `*-externalsecret.yaml` | SRE: install ESO and create this store (authenticated by ESO's own IRSA role) |
| `<DATABASE_SUBNET_CIDR>` | `networkpolicy-database-egress.yaml` | SRE (B2) |
| `<INGRESS_CONTROLLER_NAMESPACE>` | both ingress policies | SRE, when the ingress is chosen. With the AWS Load Balancer Controller in IP mode, replace this peer with an `ipBlock` of the ALB subnets |
| `<NODE_SUBNET_CIDR>` | both ingress policies (kubelet probes to 8081) | SRE; remove the rule if the CNI already admits node-to-pod probe traffic |
| Resource requests and limits | both Deployments and the Job | SRE, from observed use (the values are starting points) |
| PSA `*-version: latest` | `namespace.yaml` | SRE may pin it to the cluster's minor version |

The secret **values** are never in this repository. What each file must contain:

- `access-token-private-key.pem` (identity): an Ed25519 PKCS#8 PEM `PRIVATE KEY`, at most 16 KiB (B5).
- `hmac-secret-key` (identity): at least 32 random bytes, used byte for byte and never trimmed. It must not be a single repeated byte and must not be the signing key (B6).
- `database-dsn` (both services): one line, with `sslmode=verify-full` and `sslrootcert=/etc/lily/db-ca/ca-bundle.pem` (B2, ADR-0029). Both point at **the same database** (C4).
- `access-token-public-key.pem` (edge-api): the public half of the identity key, as a PEM `PUBLIC KEY` (PKIX, Ed25519).
- `ca-bundle.pem`: the database's CA bundle. `sslrootcert=system` is refused.

## Verified environment variables

Each name was read from the Go code at `290cdd9`. Names are formed as
`<prefix>_<tag>`: the prefix is `CS_IDENTITY` (`services/go/identity/cmd/server/main.go:28`,
`services/go/identity/internal/bootstrap/bootstrap.go:61`) or `CS_EDGE_API`
(`services/go/edge-api/cmd/server/main.go:27`, `services/go/edge-api/internal/auth/auth.go:66`),
joined by `joinEnvName` (`packages/go/platform/config.go:174`).

### identity

| Variable | Set here to | Code | Notes |
|---|---|---|---|
| `CS_IDENTITY_ENVIRONMENT` | `staging`, **in the pod spec** | `packages/go/platform/config.go:308` (default `development`); `services/go/identity/internal/bootstrap/runtime.go:159`; `services/go/identity/cmd/server/identity.go:39,82` | OD-15: set apart from the Secret. Not explicit `development`, so DB TLS must be verified (`packages/go/pgshared/tls.go:34-39`) |
| `CS_IDENTITY_HTTP_PORT` | `8080` | `packages/go/platform/config.go:321` (default 8080) | |
| `CS_IDENTITY_HEALTH_PORT` | `8081` | `packages/go/platform/config.go:328` (default 8081) | Serves `GET /healthz` and `GET /readyz` (`packages/go/platform/server.go:273-274`) |
| `CS_IDENTITY_ACCESS_TOKEN_PRIVATE_KEY_FILE` | `/etc/lily/secrets/access-token-private-key.pem` | `services/go/identity/internal/bootstrap/bootstrap.go:85` | Secret file |
| `CS_IDENTITY_ACCESS_TOKEN_ISSUER` | ConfigMap `issuer` | `services/go/identity/internal/bootstrap/bootstrap.go:86` | Equal to edge-api's |
| `CS_IDENTITY_ACCESS_TOKEN_AUDIENCE` | ConfigMap `audience` | `services/go/identity/internal/bootstrap/bootstrap.go:87` | Equal to edge-api's |
| `CS_IDENTITY_ACCESS_TOKEN_KID` | ConfigMap `kid` | `services/go/identity/internal/bootstrap/bootstrap.go:88` | Equal to edge-api's |
| `CS_IDENTITY_ENROLLMENT_CANONICAL_URL` | `https://identity.<DOMAIN>/v1/otp/validate` | `services/go/identity/internal/bootstrap/bootstrap.go:89`; path `services/go/identity/internal/routes/routes.go:20` | https, same origin as the refresh URL |
| `CS_IDENTITY_REFRESH_CANONICAL_URL` | `https://identity.<DOMAIN>/v1/token/refresh` | `services/go/identity/internal/bootstrap/bootstrap.go:90`; path `services/go/identity/internal/routes/routes.go:22` | The deletion URL is derived from it |
| `CS_IDENTITY_HMAC_SECRET_KEY_FILE` | `/etc/lily/secrets/hmac-secret-key` | `services/go/identity/internal/bootstrap/runtime.go:88` | Secret file |
| `CS_IDENTITY_DATABASE_DSN_FILE` | `/etc/lily/secrets/database-dsn` | `services/go/identity/internal/bootstrap/runtime.go:89`; migrate: `services/go/identity/cmd/server/identity.go:154` | Secret file; also the only identity variable the migrate Job needs |
| `CS_IDENTITY_OTP_PROVIDER` | `none` | `services/go/identity/internal/bootstrap/otp.go:38` | Accepts only `mock` (explicit development only) or `none`. Explicit `none` means no SMS delivery, and a lost tier can never select the mock |

The eight activation variables are listed at `services/go/identity/internal/bootstrap/runtime.go:64-73`.
The migrate command is dispatched at `services/go/identity/cmd/server/main.go:53`.

### edge-api

| Variable | Set here to | Code | Notes |
|---|---|---|---|
| `CS_EDGE_API_ENVIRONMENT` | `staging`, in the pod spec | `packages/go/platform/config.go:308`; `services/go/edge-api/internal/auth/auth.go:166` | Verified DB TLS |
| `CS_EDGE_API_HTTP_PORT` | `8080` | `packages/go/platform/config.go:321` | |
| `CS_EDGE_API_HEALTH_PORT` | `8081` | `packages/go/platform/config.go:328` | `/healthz`, `/readyz` |
| `CS_EDGE_API_ACCESS_TOKEN_PUBLIC_KEY_FILE` | `/etc/lily/secrets/access-token-public-key.pem` | `services/go/edge-api/internal/auth/auth.go:84` | Secret file |
| `CS_EDGE_API_ACCESS_TOKEN_KID` | ConfigMap `kid` | `services/go/edge-api/internal/auth/auth.go:85` | |
| `CS_EDGE_API_ACCESS_TOKEN_ISSUER` | ConfigMap `issuer` | `services/go/edge-api/internal/auth/auth.go:86` | |
| `CS_EDGE_API_ACCESS_TOKEN_AUDIENCE` | ConfigMap `audience` | `services/go/edge-api/internal/auth/auth.go:87` | |
| `CS_EDGE_API_PUBLIC_ORIGIN` | `https://api.<DOMAIN>` | `services/go/edge-api/internal/auth/auth.go:91` | Scheme and host only |
| `CS_EDGE_API_DATABASE_DSN_FILE` | `/etc/lily/secrets/database-dsn` | `services/go/edge-api/internal/auth/auth.go:95` | Secret file; identity's database |

The six activation variables are listed at `services/go/edge-api/internal/auth/auth.go:136-143`.

### Verified, deliberately left unset

| Variable(s) | Code | Why unset |
|---|---|---|
| `CS_IDENTITY_PLAY_INTEGRITY_{PACKAGE_NAME,CERT_DIGESTS,MAX_AGE,FUTURE_SKEW,DECODE_TIMEOUT,CREDENTIAL_CONFIG_FILE}` | `services/go/identity/internal/bootstrap/playintegrity.go:48-66` | O7 and E1 undecided; sessions are recorded `unverified` (C3) |
| `CS_IDENTITY_OTP_MOCK_OUTBOX_FILE` | `services/go/identity/internal/bootstrap/otp.go:39` | Development-only mock |
| `CS_EDGE_API_ACCESS_TOKEN_PREVIOUS_PUBLIC_KEY_FILE`, `CS_EDGE_API_ACCESS_TOKEN_PREVIOUS_KID` | `services/go/edge-api/internal/auth/auth.go:103-104` | Only during a key rotation (runbook) |
| `CS_*_LOG_LEVEL`, `CS_*_LOG_FORMAT`, `CS_*_SHUTDOWN_TIMEOUT`, `CS_*_READ_HEADER_TIMEOUT`, `CS_*_REGION` | `packages/go/platform/config.go:312,331,335,341,346` | Defaults suffice (`info`, `json`, `25s`, `5s`, `in-south-1`). `terminationGracePeriodSeconds: 30` exceeds the 25s shutdown timeout |
| `CS_*_VERSION` | `packages/go/platform/config.go:318` | Ignored: the binary's build metadata wins |

No variable in these manifests is unverified. INTERNAL-TESTING-SETUP C3/C4's
`_AUDIENCE`, `_KID` and `_ISSUER` shorthand expands to the names above.

## Image facts used (`docker/go/Dockerfile`)

One image per service, from `--build-arg SERVICE=<name>`. The runtime is
`scratch` with `ENTRYPOINT ["/service"]` (line 150), so the Job passes only
`args: ["migrate"]`. It runs as `USER 65534:65534` (line 139), which these pods
set as `runAsUser`/`runAsGroup`/`fsGroup`, and exposes 8080 and 8081 (line 144).
There is no shell, so `readOnlyRootFilesystem: true` costs nothing.

## Decisions made in this draft

- **ExternalSecret (ESO), not plain Secret manifests.** `SECURITY.md` already
  fixes the delivery mechanism ("External Secrets Operator → mounted files")
  and the source (the cloud secret manager, per environment), so the
  `ExternalSecret` records what is decided. A data-less `Secret` would either
  be applied empty, and fail at mount time anyway, or invite someone to fill it
  by hand, which is not the delivery path `SECURITY.md` sets. The operator itself, its version and the
  `ClusterSecretStore` are SRE's (B1). The API version is `external-secrets.io/v1beta1`
  as requested. Newer ESO releases serve `external-secrets.io/v1` and deprecate
  `v1beta1`, so SRE matches it to the installed operator.
- **`automountServiceAccountToken: false`** on both ServiceAccounts and all pod
  specs. Neither service calls the Kubernetes API. IRSA does not depend on this
  token: the EKS pod identity webhook injects its own projected token (audience
  `sts.amazonaws.com`) whatever this setting says. Neither service calls AWS
  either (no AWS SDK in either `go.mod`). The IRSA roles are kept because
  ADR-0008 requires IRSA for every workload, but they should have no
  permissions. The owner may instead drop the annotations until a feature needs
  AWS.
- **Secret files.** Mounted read-only at `/etc/lily/secrets/` with
  `defaultMode` 0400 (written as decimal `256`) and `fsGroup: 65534`. The
  kubelet then makes them `0440` with group 65534, readable by the service user
  only. Each key is listed under `items`, so a missing key fails the mount. The
  CA bundle is mounted at `/etc/lily/db-ca/` with mode 0444, since it is public.
  It still comes through ESO, so nobody can swap the CA by editing a ConfigMap.
- **The access-token contract is one generated ConfigMap.** Issuer, audience
  and kid must be byte-identical in both services (runbook §2). Both services
  read them from one source, so they cannot drift. The name carries a content
  hash, so a change rolls both Deployments. A missing ConfigMap stops the
  container from starting (fail closed).
- **`labels` (with `includeSelectors: false`), not `commonLabels`.** Checked
  locally: `commonLabels` adds its labels to NetworkPolicy *peer* selectors. It
  rewrote the kube-dns selector to `{app.kubernetes.io/part-of: lily, k8s-app: kube-dns}`,
  which would silently block DNS.
- **The migrate Job lives in its own kustomization.** A Job in the main overlay
  would run on every `apply` and fail the next apply, because a Job's template
  is immutable. Kept apart, it runs only when an operator applies it.
- **`CS_IDENTITY_OTP_PROVIDER=none`, explicitly.** This is the same behaviour
  as unset in staging, but it stays safe even if the tier is lost.
- **No architecture pin.** ADR-0008 makes Graviton (arm64) the default, but the
  image workflow's platforms are not established here. SRE adds a
  `kubernetes.io/arch` node selector once multi-arch images exist.

## Deliberately absent

- **Ingress** and any public exposure. The domain (B3), the TLS certificates
  and backup key pairs for the pins (B4) are the owner's and SRE's, and none
  exists. Both Services are ClusterIP only.
- **SMS-provider egress.** Not approved: INTERNAL-TESTING-SETUP A1–A5, with A5
  being the network change itself. It appears only as a disabled, commented
  stanza in `networkpolicy-identity-ingress.yaml`.
- **Play Integrity variables.** Unset (O7, E1).
- **AWS API egress** (STS, Secrets Manager). Neither service calls AWS; ESO
  fetches the secrets from its own namespace.
- **Metrics scraping.** `/metrics` on the health port is not opened to any
  scraper; no monitoring stack is chosen.
- **Telephony workloads** (P1-39 O-1 gateways A and B, the O-4 lock-key
  ConfigMap). Not in scope.
- Any change to `infra/k8s/base/`.

## How this draft was checked (locally, offline)

- `kubectl kustomize infra/k8s/overlays/staging` and `… /migrate` render with
  kustomize v5.8.1 (client only; no cluster contacted).
- kubeconform ran on the rendered output against an empty local schema
  directory, with `-ignore-missing-schemas -strict`. It checked YAML parsing
  and `apiVersion`/`kind` detection only. **No schema validation ran**: no
  Kubernetes or ESO schemas are available offline, and none was downloaded.
  ADR-0027's kubeconform policy, with pinned schemas and CRD handling, is still
  an open decision.
- A local script parsed every file and checked the rendered pods: the
  restricted security fields, digests, probes, ports, read-only mounts, every
  `*_FILE` pointing at a projected key, and every env name present in the Go
  code. It also confirmed that no 12-digit number, IP address or non-Kubernetes
  domain appears.
