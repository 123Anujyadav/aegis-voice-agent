# ADR-0008: Cloud infrastructure — AWS, ap-south-1 primary, ap-south-2 DR, EKS

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Lead Staff Engineer
- **Consulted:** Legal (residency)
- **Informed:** All engineering, Finance
- **Depends on:** ADR-0003, ADR-0012

---

## 1. Context

Everything in ADRs 0002–0007 runs somewhere. This ADR decides where, and it is
constrained more tightly than a typical cloud choice by two facts:

- **Personal data must stay in India** (ADR-0012). That eliminates any provider
  or region combination that cannot keep the full data path — compute, storage,
  backups, logs, and disaster recovery — in-country.
- **The workload is concurrency-bound and latency-critical**, not
  request-throughput-bound (ADR-0002 §13). Long-lived stateful media and
  inference sessions with a sub-second end-to-end budget behave differently from
  a stateless web tier, and the infrastructure has to accommodate draining rather
  than terminating.

## 2. Problem Statement

Which cloud provider and which regions, and what is the compute substrate?

The residency constraint means the real question is narrower than it looks:
**which provider offers two production-grade regions inside India**, so that
disaster recovery does not require leaving the jurisdiction? A provider with one
Indian region forces a choice between no DR and non-compliant DR, and neither is
acceptable.

## 3. Constraints

| # | Constraint | Source |
|---|---|---|
| C1 | All personal data — compute, storage, backup, logs, DR — stays in India | ADR-0012, DPDP Act 2023 |
| C2 | Two in-country regions, so DR does not cross the jurisdiction boundary | C1 + availability |
| C3 | Low-latency path to the telephony provider's Indian PoPs | ADR-0003 C5, ADR-0011 |
| C4 | Managed Kubernetes, managed Postgres, managed Kafka | Team size, Phase 1 A4 |
| C5 | Workloads must drain, not terminate — killing a pod drops live calls | ADR-0004 §9 |
| C6 | Infrastructure defined as code, reproducible per environment | Phase 1 §14 |
| C7 | Cost must scale sub-linearly with subscriber count | Business |

## 4. Considered Options

1. **AWS** — `ap-south-1` (Mumbai), `ap-south-2` (Hyderabad)
2. **Google Cloud** — `asia-south1` (Mumbai), `asia-south2` (Delhi)
3. **Microsoft Azure** — Central India, South India, West India
4. **Indian sovereign providers** (Yotta, CtrlS, ESDS)
5. **Hybrid** — AWS for the platform, GCP for AI-adjacent services

## 5. Decision

**AWS. `ap-south-1` (Mumbai) primary, `ap-south-2` (Hyderabad) for disaster
recovery. EKS as the compute substrate.**

Concretely:

- **Compute:** EKS with managed node groups. **Graviton (ARM64) by default**;
  x86 only where a dependency forces it. Multi-AZ within `ap-south-1`.
- **Data:** Aurora PostgreSQL and MSK per ADR-0009, both in-region.
- **Storage:** S3 with SSE-KMS, region-locked, lifecycle policies per ADR-0012.
- **Networking:** private subnets for all workloads; NAT egress; ALB/NLB ingress
  only at the edge; VPC endpoints for AWS services to keep traffic off the
  public internet.
- **DR posture:** **warm standby** in `ap-south-2` — infrastructure defined and
  deployed, data replicated continuously, application capacity scaled to zero
  until invoked.
- **Identity:** IRSA for every workload. No static cloud credentials anywhere
  (`SECURITY.md`).
- **IaC:** Terraform, per-environment state, with `prod-in` as a distinct
  residency boundary (Phase 1 tree).

## 6. Why This Option Was Selected

**Because C2 is the constraint that decides it, and AWS satisfies it with the
most operational maturity.**

- **Two mature Indian regions** (C1, C2). `ap-south-1` is one of AWS's
  longest-established regions outside its core markets; `ap-south-2` gives a
  genuine in-country DR target. We can lose a region and stay both available and
  compliant.
- **Managed Kafka (MSK) is the differentiator against GCP** (C4). ADR-0009 makes
  Kafka the event backbone. On AWS that is a managed service in-region; on GCP it
  is either self-operated Kafka or a Pub/Sub migration that changes the semantics
  we are relying on. For a team of this size, not operating a Kafka cluster is
  worth a great deal.
- **Graviton price/performance** (C7). Our services are Go and Python — both
  build and run cleanly on ARM64 — and Graviton delivers materially better
  price/performance for the connection-bound, moderately CPU-bound profile
  `media-relay` and the gateways have.
- **Telephony proximity** (C3). Both Exotel and Plivo operate Indian
  infrastructure with good connectivity into Mumbai. This is verified by
  measurement as a launch acceptance test (ADR-0003 §12), not assumed.
- **EKS handles C5 correctly.** Termination grace periods, PodDisruptionBudgets
  and readiness gates give us the drain-before-close behaviour that long-lived
  media sessions require.

**GCP's advantage is AI tooling, and it does not apply to us.** Every model in
ADRs 0005–0007 is a third-party API call. We are not training, not serving our
own models, and not using Vertex. The single strongest argument for GCP is
therefore neutralised by our own architecture, which leaves MSK and regional
maturity to decide it.

## 7. Trade-offs

**Accepted.**

- **`ap-south-2` is a smaller region** with a narrower service catalogue and
  fewer availability zones than `ap-south-1`. The DR plan must be validated
  against what actually exists there, not against what exists in Mumbai. This is
  a real constraint and the reason DR is warm standby rather than active-active.
- **Warm standby has a real RTO.** Failover is minutes, not seconds. Active-active
  across two Indian regions would be faster and roughly doubles the infrastructure
  bill for a product that has not yet proven demand. We accept minutes.
- **ARM64 by default occasionally bites.** A dependency without an ARM build
  forces either an x86 node pool or a source build. Rare with Go and Python, not
  never — and the CI matrix must cover it.
- **AWS-specific services create switching cost.** MSK, Aurora, IRSA and KMS are
  not portable. Mitigated by keeping the *interfaces* generic (Kafka protocol,
  Postgres wire protocol) so a migration is operational rather than a rewrite —
  but the migration would still be substantial.
- **Egress pricing is a structural cost.** Media and inference traffic both
  egress; this is an ongoing line item that grows with usage.

## 8. Alternatives Rejected

**Google Cloud.** The closest call. Two Indian regions, excellent Kubernetes
(GKE Autopilot is arguably better than EKS for a small team), and superior AI
tooling. Rejected primarily on **managed Kafka**: ADR-0009 depends on Kafka
semantics, and GCP has no managed Kafka offering equivalent to MSK, which would
either put cluster operations on us or force a Pub/Sub redesign. Secondarily, the
AI-tooling advantage does not materialise for an architecture built on
third-party model APIs. Reconsider if we ever move to self-hosted models
(ADR-0006 §16).

**Azure.** Three Indian regions — the best regional footprint of the three — and
a credible managed platform. Rejected on ecosystem fit and team familiarity:
nothing in our stack is Microsoft-shaped, AKS and Azure networking would be
learned from scratch, and the extra Indian region does not buy anything the
two-region AWS posture lacks.

**Indian sovereign providers.** Rejected on managed-service depth. They offer the
strongest possible residency story — genuinely Indian-owned infrastructure — and
that is a real advantage if regulation tightens. But they do not offer managed
Kubernetes, managed Postgres and managed Kafka at the maturity C4 requires, which
would transfer substantial operational load to a team that cannot absorb it.
**Revisit if data-localisation rules move from "in India" to "Indian-owned"**,
which is the specific regulatory change that would invert this decision.

**Hybrid AWS + GCP.** Rejected outright. Two clouds means two IAM models, two
networking models, two sets of runbooks, cross-cloud egress on the latency-critical
path, and a residency story that must be argued twice. The premise — that GCP's
AI tooling is worth it — is false for us (§6).

## 9. Operational Impact

- **Two production Terraform environments** (`prod-in`, `prod-global`) exist in
  the Phase 1 tree. Only `prod-in` is real at launch; `prod-global` is the
  scaffold for expansion and must stay empty rather than half-configured.
- **DR is a rehearsed procedure, not a document.** Warm standby that has never
  been failed over does not work. Quarterly game-day exercising a full
  `ap-south-2` promotion, with the result recorded in `docs/runbooks/`.
- **Node draining is a first-class concern** (C5). Termination grace periods must
  exceed the longest expected screening session; PDBs must be sized so that a
  rolling node replacement cannot take out media capacity.
- **Cost visibility from day one.** Tagging policy enforced by Terraform and by
  OPA/Conftest in CI, so per-service and per-environment cost is attributable
  before the bill becomes a surprise.
- **Region-lock guardrails.** Service control policies preventing resource
  creation outside approved Indian regions — the technical enforcement of C1,
  which must not depend on engineers remembering.

## 10. Security Impact

- **IRSA everywhere; zero static cloud credentials** (`SECURITY.md`). This is the
  single highest-value security property of the platform's infrastructure.
- **Private subnets for all workloads.** Only the edge load balancers are
  internet-reachable. `media-relay`'s inbound provider WebSocket (ADR-0004) is the
  one carefully-controlled exception and is authenticated.
- **KMS with customer-managed keys** for S3, Aurora and MSK. Key policy separate
  from data-plane IAM, so a compromised workload role cannot rewrite key policy.
- **Region-lock SCPs** are a security control as much as a compliance one: they
  prevent an accidental or malicious deployment that would move personal data out
  of jurisdiction.
- **VPC endpoints** keep AWS service traffic off the public internet, reducing
  both exposure and egress cost.
- **CloudTrail and VPC flow logs** retained per ADR-0012, in-region, immutable.

## 11. Cost Impact

Infrastructure is a smaller line than telephony (ADR-0003 §11) or inference
(ADR-0006 §11), but it is the one that scales with *idle* capacity rather than
usage, which makes it dangerous in a different way.

Principal drivers and levers:

- **Compute is concurrency-provisioned, not request-provisioned.** Media and
  gateway capacity must be sized for peak concurrent calls with headroom, and
  peak-to-mean is high (ADR-0002 §13). Graviton and aggressive right-sizing are
  the main levers; Savings Plans once the baseline is known.
- **Warm-standby DR costs the data-replication and control-plane footprint**
  continuously, with application capacity at zero. This is the deliberate
  cheap-DR posture; active-active would roughly double it.
- **Egress** is structural and grows with usage.
- **MSK and Aurora are the largest managed-service line items** and are sized in
  ADR-0009.

## 12. Performance Impact

- **Region choice is a latency decision.** `ap-south-1` is chosen partly because
  it is where the telephony providers are; the ingress hop budget in ADR-0011
  assumes a domestic Mumbai path and is invalidated if compute moves.
- **Multi-AZ within the region** costs a small amount of inter-AZ latency on the
  data path and buys availability. For a sub-second budget this is measurable and
  accepted; services on the hot path are AZ-affinitised where it is safe to do so.
- **Graviton is not a latency compromise.** For our profile it is neutral-to-better
  on latency as well as cheaper.
- **Cross-region is off the hot path entirely.** `ap-south-2` carries replication
  only; no request traverses regions in normal operation.

## 13. Scalability Impact

- **Scale on concurrent sessions**, consistently with every other component ADR.
  HPA metrics must be connection- and session-based, not CPU-based — a media
  relay at 30% CPU can still be at its connection limit.
- **Cluster autoscaler headroom must anticipate spikes.** A screening call cannot
  wait for a node to boot; capacity must exist before the call arrives, which
  means over-provisioning relative to a stateless web tier.
- **Managed services have their own scaling limits** — Aurora connection counts,
  MSK partition and throughput limits — that are covered in ADR-0009 and are not
  solved by the cluster autoscaler.
- **`prod-global` is the expansion mechanism**, not a bigger `prod-in`. When a
  second country arrives it gets its own residency boundary, not a shared one.

## 14. Migration Strategy

There is no incumbent to migrate from. The strategy that matters is portability
and the exit path:

1. **Phase 1 (launch).** `ap-south-1` primary, `ap-south-2` warm standby, all
   Terraform-defined.
2. **Portability discipline.** Kubernetes as the workload abstraction; Kafka and
   Postgres *protocols* rather than proprietary APIs; no application code
   importing an AWS SDK outside a narrow adapter. This keeps a future move
   operational rather than a rewrite.
3. **International expansion** provisions `prod-global` in a new region using the
   same Terraform modules. Modules are region-parameterised from the start
   specifically so this is a variable change.
4. **Exit trigger** is the sovereign-provider scenario in §8 — a regulatory move
   to Indian-owned infrastructure. The mitigation is the portability discipline
   above; the migration would still be a multi-quarter programme and should be
   costed as one.

## 15. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| `ap-south-1` regional outage | Low | Critical | Warm standby in `ap-south-2`; quarterly rehearsed failover |
| `ap-south-2` lacks a service the DR plan assumes | Medium | High | DR plan validated against actual `ap-south-2` catalogue, not Mumbai's; re-validated each game day |
| Resource created outside India | Medium | Critical | Region-lock SCPs; Conftest policy in CI; audit alerting |
| Node replacement drops live calls | High if unmitigated | High | Grace periods > max session length; PDBs sized for concurrency; drain-before-close lifecycle |
| Cost grows faster than subscribers | Medium | High | Tagging enforced in IaC; per-service dashboards; Graviton default; Savings Plans once baseline is known |
| Vendor lock-in blocks a required migration | Medium | High | Protocol-level portability; AWS SDKs confined to adapters; exit costed rather than assumed cheap |
| ARM64 dependency gap | Low | Medium | CI builds ARM64; x86 node pool available as fallback |
| Localisation rules tighten to Indian-owned infrastructure | Low | Critical | Portability discipline; sovereign-provider option kept live in §8 |

## 16. Future Review Trigger

Revisit when **any** holds:

- Indian data-localisation rules move from "in India" to "Indian-owned
  infrastructure"
- Monthly infrastructure spend exceeds **₹20 lakh**, warranting a full multi-cloud
  or committed-use comparison
- Self-hosted model serving is adopted (ADR-0006 §16), which re-opens the GCP
  comparison on genuine merit
- A DR game day fails to meet the documented RTO
- International expansion begins — re-run this decision for that jurisdiction
  rather than extending `prod-in`
- GCP announces managed Kafka in an Indian region at MSK parity

## 17. References

- ADR-0003 (carrier selection), ADR-0009 (database and event backbone),
  ADR-0011 (latency budget), ADR-0012 (privacy and residency)
- `infra/terraform/envs/prod-in`, `infra/terraform/envs/prod-global`
- `infra/policy` — region-lock and tagging enforcement
- `docs/runbooks/dr-failover.md` (to be written)
