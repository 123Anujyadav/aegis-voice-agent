# ADR-0028: Tracing backend: OpenTelemetry Collector to AWS X-Ray in `ap-south-1`

- **Status:** **Proposed, owner-selected and conditional.** The owner selected
  this backend on 2026-10-23 on two conditions, prerequisites P1 and P2 below.
  It becomes Accepted only when SRE records both as met. **Nothing here is
  provisioned, deployed or credentialed.**
- **Date:** 2026-10-23
- **Deciders:** the repository owner; SRE for P1 and P2
- **Consulted:** _none yet_
- **Informed:** SRE (owns `/infra/`); Security (IAM and region-lock policy)
- **Depends on:** ADR-0008, ADR-0012, ADR-0026 (step 4), ADR-0027

---

## Context

ADR-0026 implements tracing without a backend. Its step 4 is "a backend
adapter … with its own ADR", and this is that ADR. Three options were compared
in the Phase 16 decision packet (`docs/phase16-platform/README.md`): Collector
to X-Ray, Collector to self-hosted Tempo, and vendor SaaS. The owner selected
the first, subject to two checks this repository cannot perform.

The constraints that decide it are already recorded:
- **ADR-0008:** "IRSA for every workload. No static cloud credentials
  anywhere"; VPC endpoints for AWS services; region-lock SCPs; `ap-south-1`
  primary and `ap-south-2` DR.
- **ADR-0012:** "Telemetry, traces" are `INTERNAL`, with a fixed 30-day
  retention.
- **ADR-0026:** W3C Trace Context, and spans recorded in process with an
  attribute allowlist and allowlisted error text. Rule 5: exporters never
  block the call path.

## Decision

**Services export OTLP to an in-cluster OpenTelemetry Collector, which forwards
to AWS X-Ray in `ap-south-1`.**

### Prerequisites, both required before this ADR is accepted

| # | Check | Owner | How it is verified | Status |
|---|---|---|---|---|
| **P1** | X-Ray, in the target account and `ap-south-1`, accepts the W3C trace ids this implementation generates: 16 random bytes from `crypto/rand`, not prefixed with an epoch | SRE | The procedure below, in a **non-production** account | **pending** |
| **P2** | The expected ingestion volume and monthly cost are estimated, and the budget is confirmed acceptable | SRE, with owner sign-off on the budget | The procedure below | **pending** |

**If P1 fails**, the fallback is an option in `packages/go/telemetry` that
makes the first 4 bytes of each trace id the Unix time in seconds. Such ids are
still valid W3C ids, so nothing else changes. That option is added only if P1
fails. **If P2 fails**, choose between export-side sampling at the Collector
(below) and option B, self-hosted Tempo.

### Architecture

```
service process ──OTLP/HTTP (ClusterIP)──► OTel Collector (≥2 replicas) ──► X-Ray VPC interface endpoint (ap-south-1)
  telemetry.RecordingTracer                  memory_limiter, batch,             private DNS, no internet path
  + step-4 OTLP exporter                     optional sampling, awsxray
```

- **The service side** (ADR-0026 step 4) is an OTLP exporter behind
  `telemetry.Exporter`. **OTLP/HTTP with JSON encoding** needs only the
  standard library, so the zero-new-dependency property holds. The OpenTelemetry
  Go SDK is the alternative, if its features are later needed.
  - It is non-blocking, per ADR-0026 rule 5: a bounded queue that drops and
    counts spans when full, and flushes within `ShutdownTimeout`.
  - It sends to the Collector's ClusterIP Service. No service holds a
    credential or knows X-Ray exists.
- **The Collector** runs an image **pinned by digest**, either AWS
  Distro for OpenTelemetry or `otel-collector-contrib`, following the
  `infra/README.md` image policy.
  - Its pipeline is: `otlp` receiver (HTTP), then `memory_limiter`, then
    `batch`, then `awsxray` exporter, with `region: ap-south-1`.
  - It has at least 2 replicas, a PodDisruptionBudget (`minAvailable: 1`), and
    an internal-only ClusterIP Service with **no Ingress and no
    LoadBalancer**.
  - A NetworkPolicy admits OTLP only from the `aegis-system` namespace, and
    allows egress only to the X-Ray endpoint and DNS.
- **Manifests** live in the tree ADR-0027 selects (`infra/k8s`).

### Identity and least privilege

- **One IAM role, for the Collector only, through IRSA.**
  - Trust: the cluster's EKS OIDC provider, with conditions on `aud`
    (`sts.amazonaws.com`) and on `sub`
    (`system:serviceaccount:<namespace>:otel-collector`).
  - Permissions: `xray:PutTraceSegments` and `xray:PutTelemetryRecords`.
  - Scope: X-Ray does not scope these actions to individual resources, so the
    role's reach is bounded by its trust policy and its two actions.
  - Add the X-Ray sampling-rule reads only if remote sampling is ever adopted;
    always-on recording (ADR-0026 rule 3) does not need them.
- **The Collector's ServiceAccount** carries the `eks.amazonaws.com/role-arn`
  annotation. **No access key exists anywhere.** No credential is placed in
  the repository, in a service, or in a chat.

### Region, retention and data

- **Region:**
  - X-Ray runs in `ap-south-1` only, with `ap-south-2` used only by the DR
    deployment's own Collector.
  - The Collector's region is pinned in its configuration, and no
    cross-region export is configured.
  - The region-lock SCPs must allow `xray:*` in those two regions only.
- **Retention:** X-Ray retains trace data for **30 days**, which matches
  ADR-0012's fixed 30 days for traces. SRE confirms the current retention as
  part of P2.
- **Data:** spans carry only allowlisted attributes and allowlisted error text
  (ADR-0026 rules 4 and 6). Encryption at rest optionally uses a KMS key that
  SRE selects.

### Sampling, failure behaviour and cost controls

- **Sampling:** recording is always on in process (ADR-0026 rule 3). **The
  only cost knob is export-side**: a `probabilistic_sampler` or
  `tail_sampling` processor in the Collector. It is off until P2 shows it is
  needed, and it is set by configuration, not code.
- **Failure:**
  - **Collector unavailable:** the service exporter drops and counts spans, so
    turns never wait.
  - **X-Ray unavailable:** the Collector uses bounded `retry_on_failure` and a
    bounded `sending_queue`, then drops.
  - **Accepted cost:** both may lose spans; neither delays a call.
  - **Visibility:** drop counts are surfaced through the Collector's own
    metrics, which are internal only.
- **Cost controls:**
  - an AWS Budgets alarm on X-Ray spend;
  - Collector `memory_limiter` bounds;
  - export-side sampling as the lever;
  - the fixed retention.

### Verification procedure, run by SRE in a non-production account

**P1, trace-id compatibility:**
1. In a non-production account, in `ap-south-1`, run the pinned Collector image
   with the IRSA role, or a temporary role in a sandbox.
2. Send one span through OTLP/HTTP whose trace id is **random**, as generated
   by `telemetry.RecordingTracer`. Send a second, control span whose trace id
   starts with the current epoch seconds.
3. Look up both in X-Ray, for example with `aws xray batch-get-traces`, using
   the X-Ray form of each id: `1-<first 8 hex>-<remaining 24 hex>`.
4. P1 passes if the random-id trace is stored and returned intact. Record the
   account type, region, Collector image digest, date and result here.

**P2, volume and cost:**
1. **Estimate volume:**
   - traces per month ≈ 30 × the expected daily call count, at one trace per
     call;
   - spans per trace ≈ turns per call × 12 (ADR-0011's 11 hops, plus the
     turn).
2. Price the estimate at **current** `ap-south-1` X-Ray rates. These rates are
   not recorded in this repository, and the classic segment path and
   CloudWatch Transaction Search price differently, so confirm which one the
   `awsxray` exporter path uses.
3. Set the Budgets alarm. Obtain the owner's sign-off on the monthly figure.
   Record the estimate, the assumptions and the sign-off here.

### SRE-owned infrastructure steps, after P1 and P2, in order

1. Create the Collector's IAM role and its OIDC trust policy (above).
2. Add the X-Ray VPC interface endpoint `com.amazonaws.ap-south-1.xray`, with
   private DNS, and a security group allowing 443 from the node security
   group. Mirror it in `ap-south-2`.
3. Extend the region-lock SCPs to allow X-Ray in `ap-south-1` and
   `ap-south-2`.
4. Commit the Collector manifests under `infra/k8s`: Deployment,
   PodDisruptionBudget, ClusterIP Service, NetworkPolicy and the annotated
   ServiceAccount, with the image pinned by digest.
5. Set the Budgets alarm, and optionally the KMS key.

Each step is reviewed infrastructure change. None is performed by the change
that adds this ADR.

## Consequences

- **Positive:** no service holds a credential; the data path stays in-region
  and off the internet; retention matches ADR-0012; and the service side stays
  dependency-free.
- **Negative:** SRE runs a Collector; X-Ray's query features are less flexible
  than Tempo's; and cost scales with traces.
- **Neutral:** option B (Tempo) remains the documented fallback, and the
  `telemetry.Exporter` seam makes a switch one adapter.

## Revisit trigger

- P1 or P2 fails.
- Trace volume makes X-Ray cost exceed the approved budget.
- A residency or retention requirement changes in ADR-0012.

## References

- ADR-0008, ADR-0012, ADR-0026 (rules 3–6, step 4), ADR-0027
- `docs/phase16-platform/README.md`: "Objective 3 — decision packet and
  integration plans", which holds the backend comparison
- `packages/go/telemetry`: `RecordingTracer`, `Exporter`, `spanErrorText`
