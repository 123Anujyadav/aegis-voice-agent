# Deployment

Where the containers physically run.

**Source ADRs:** 0008 (cloud, regions, DR), 0009 (data stores), 0012 (residency).

---

## Diagram

```mermaid
flowchart TB
    subgraph india["🇮🇳 INDIA RESIDENCY BOUNDARY — enforced by AWS SCPs"]
        direction TB

        subgraph primary["ap-south-1 · Mumbai · PRIMARY"]
            direction TB
            ALB["<b>ALB / NLB</b><br/>public ingress<br/>TLS termination · WAF"]

            subgraph eks["EKS · Graviton ARM64 · multi-AZ"]
                direction LR
                subgraph az1["AZ-a"]
                    NG1["node group<br/><i>telephony</i><br/>media-relay · gateway"]
                    NG2["node group<br/><i>platform</i><br/>edge-api · identity"]
                end
                subgraph az2["AZ-b"]
                    NG3["node group<br/><i>telephony</i>"]
                    NG4["node group<br/><i>ai</i><br/>asr · llm · tts gateways"]
                end
                subgraph az3["AZ-c"]
                    NG5["node group<br/><i>platform</i>"]
                    NG6["node group<br/><i>ai</i>"]
                end
            end

            subgraph datap["Data plane · multi-AZ"]
                AUR[("<b>Aurora PostgreSQL</b><br/>Serverless v2 · 4 clusters<br/>identity · telephony<br/>content · billing")]
                RDS[("<b>ElastiCache Redis</b><br/>session state · nonces")]
                MSK[["<b>MSK</b><br/>Kafka · 3 brokers"]]
                S3B[("<b>S3</b><br/>SSE-KMS · lifecycle")]
            end

            KMS["<b>KMS</b><br/>customer-managed keys"]
            SM["<b>Secrets Manager</b><br/>+ External Secrets Operator"]
        end

        subgraph dr["ap-south-2 · Hyderabad · WARM STANDBY"]
            direction TB
            EKSDR["EKS control plane<br/><b>capacity scaled to 0</b>"]
            AURDR[("Aurora<br/>cross-region replica")]
            S3DR[("S3<br/>replicated")]
        end

        primary -.->|"continuous replication"| dr
    end

    CPAAS["<b>CPaaS</b> 🇮🇳<br/>Exotel · Plivo"]
    CLIENT(["Android clients"])
    VENDOR["<b>AI vendors</b><br/>STT 🇮🇳 · Claude 🌐 · TTS 🌐"]

    CPAAS ==>|"WSS media"| ALB
    CLIENT -->|"HTTPS"| ALB
    ALB --> eks
    eks --> datap
    eks -->|"IRSA — no static creds"| SM
    datap --> KMS
    eks -->|"consent-gated egress<br/>via NAT"| VENDOR

    classDef region fill:#0B3D91,stroke:#062a66,color:#fff
    classDef store fill:#4A5058,stroke:#31363c,color:#fff
    classDef ext fill:#6E7781,stroke:#4a5058,color:#fff
    class AUR,RDS,MSK,S3B,AURDR,S3DR store
    class CPAAS,CLIENT,VENDOR ext
```

---

## Region posture

| | `ap-south-1` Mumbai | `ap-south-2` Hyderabad |
|---|---|---|
| Role | Primary — all production traffic | Warm standby |
| EKS | Full capacity, multi-AZ | Control plane up, **workloads at zero** |
| Aurora | Writer + readers | Cross-region replica |
| S3 | Primary | Replicated |
| MSK | 3 brokers | Rebuilt on failover |
| Cost | Full | Replication + control plane only |

**RTO is minutes, not seconds, and that is the deliberate choice.** Active-active
across two Indian regions would be faster and roughly doubles the bill for a
product that has not yet proven demand (ADR-0008 §7).

`ap-south-2` is a smaller region with a narrower service catalogue. The DR plan is
validated against **what actually exists there**, not against Mumbai's catalogue —
and re-validated at every quarterly game day.

---

## Node group separation

Three workload classes, deliberately not co-scheduled:

| Group | Runs | Why separate |
|---|---|---|
| `telephony` | `media-relay`, `telephony-gateway`, `session-orchestrator` | **Long-lived stateful sessions.** Needs long termination grace and PDBs sized so a rolling replacement cannot take out media capacity. A noisy neighbour here drops live calls. |
| `ai` | `asr-gateway`, `ai-orchestrator`, `tts-gateway`, `fraud-engine` | Network-bound streaming with vendor-shaped burstiness. Scales on a different signal. |
| `platform` | `edge-api`, `identity`, `billing`, `contacts-sync`, `notification-fanout` | Ordinary request/response. Cheapest to scale, safe to evict. |

**Graviton ARM64 by default** across all three; x86 only where a dependency
forces it, which is rare with Go and Python.

---

## The deployment property that matters most

**Draining, not terminating.**

A pod running `media-relay` holds live phone calls. Killing it drops them. The
whole deployment configuration for the `telephony` group exists to prevent that:

- **Termination grace period exceeds the longest expected screening session.**
- **Readiness goes false before the listener closes** — the graceful-shutdown
  sequence implemented in `packages/go/platform` and `packages/python/platform`.
- **PodDisruptionBudgets sized against peak concurrency**, not against replica
  count.
- **Cluster autoscaler headroom is pre-provisioned.** A call cannot wait for a
  node to boot, so capacity exists before the call arrives — which means running
  the hot path at deliberately low utilisation (ADR-0011 §13).

Autoscaling on CPU is wrong here. A media relay at 30% CPU can be at its
connection limit; HPA metrics are connection- and session-based.

---

## Residency enforcement

Three independent layers, because a single control that depends on people
remembering is not a control:

1. **Service Control Policies** deny resource creation outside approved Indian
   regions. Technical, account-wide, not bypassable by a deploy.
2. **Conftest / OPA policy in CI** (`infra/policy`) fails a Terraform plan that
   references a non-approved region.
3. **Schema-level classification** — the `residency_bound` annotation drives
   egress policy in the platform HTTP clients, so a cross-border call is refused
   at the library rather than discovered in an audit.

The single 🌐 egress path — Claude and the default TTS providers — is
consent-gated per subscriber, minimised in payload, and written to the audit
trail with the vendor and basis (ADR-0012 §5.4).

---

## Environments

| Environment | Region | Data | Purpose |
|---|---|---|---|
| `dev` | `ap-south-1` | Synthetic only | Integration |
| `staging` | `ap-south-1` | Synthetic only | Pre-production; same R8/minification as prod |
| `prod-in` | `ap-south-1` + `ap-south-2` | **Real personal data** | Production |
| `prod-global` | — | — | **Scaffold only.** Empty until international expansion. |

`prod-global` exists in the Terraform tree and stays empty. A half-configured
second residency boundary is worse than none — expansion gets its own analysis
(ADR-0008 §14, ADR-0012 §14), not an extension of `prod-in`.
