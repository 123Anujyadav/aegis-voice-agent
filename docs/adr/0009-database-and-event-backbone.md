# ADR-0009: Database and event backbone — Aurora PostgreSQL per bounded context, Redis, MSK, S3

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Lead Staff Engineer
- **Consulted:** —
- **Informed:** All engineering, Compliance
- **Depends on:** ADR-0008, ADR-0012

---

## 1. Context

Fourteen services need to store state and to tell each other things happened. The
Phase 1 monorepo guidelines already fixed two rules that constrain this ADR before
it starts:

> **No shared business logic across service boundaries.** If two services both
> need it, one owns it and the other asks over the wire.
>
> **No shared database access layer.** Each service owns its schema. Sharing a
> data layer is a distributed monolith.

This ADR decides what "each service owns its schema" means physically, and how
services communicate asynchronously without reaching into each other's tables.

The workload has an unusual shape. It is **not** a high-QPS transactional system.
It is a modest volume of durable records (subscribers, call sessions, transcripts,
subscriptions) alongside a large volume of **ephemeral, latency-critical session
state** that lives for seconds and then either becomes a durable record or is
discarded — plus a stream of events that other services react to well after the
call has ended.

## 2. Problem Statement

Three questions, and getting them confused is how a distributed monolith gets
built:

1. **Durable state.** One database or many? Which engine?
2. **Ephemeral state.** Where does live call-session state live, given that it
   changes every few hundred milliseconds and must never be on the critical path
   to disk?
3. **Inter-service communication.** How does `transcript-service` learn a call
   ended, without `session-orchestrator` calling it synchronously and coupling a
   real-time path to a persistence path?

Compliance shapes all three. Personal data must be region-locked, encrypted, and
**deletable on request** (ADR-0012). A design that scatters subscriber data across
six datastores makes erasure a research project.

## 3. Constraints

| # | Constraint | Source |
|---|---|---|
| C1 | Each service owns its schema; no cross-service table access | Phase 1 §20 |
| C2 | All data at rest in India, encrypted with customer-managed keys | ADR-0012, ADR-0008 |
| C3 | Live session state must not require a synchronous disk write | ADR-0011 |
| C4 | Events must survive consumer downtime and be replayable | Reliability |
| C5 | Erasure must be executable per data subject across every store | ADR-0012, DPDP |
| C6 | Managed services — no self-operated database or broker | ADR-0008 C4 |
| C7 | Schema changes must be safe under rolling deploy (expand/contract) | Phase 1 §15 |

## 4. Considered Options

**Durable store:**
1. One shared PostgreSQL, schema-per-service
2. Aurora PostgreSQL cluster per bounded context
3. DynamoDB
4. PostgreSQL + Cassandra for transcripts

**Event backbone:**
5. Amazon MSK (Kafka)
6. Amazon SNS/SQS
7. NATS JetStream
8. Amazon Kinesis

## 5. Decision

**Aurora PostgreSQL per bounded context, Redis for ephemeral session state, MSK
for events, S3 for audio and transcripts.**

**Durable state — four Aurora PostgreSQL clusters**, one per bounded context:

| Cluster | Owns | Primary data classification |
|---|---|---|
| `identity` | Subscribers, devices, consent records, sessions | `PERSONAL`, `SECRET` |
| `telephony` | Call sessions, DIDs, forwarding state, screening outcomes | `PERSONAL` |
| `content` | Transcripts, summaries, fraud verdicts | `SENSITIVE` |
| `billing` | Subscriptions, entitlements, payment records | `PERSONAL`, `LEGAL_HOLD` |

Aurora PostgreSQL Serverless v2, multi-AZ, in `ap-south-1`, encrypted with KMS
CMKs, continuous backup to `ap-south-2` for DR.

**Ephemeral state — ElastiCache for Redis.** Live call-session state, turn-taking
context, rate-limit counters, idempotency keys, and short-lived caches. Nothing in
Redis is a system of record; anything that must survive is written to Postgres via
an event, not synchronously.

**Events — Amazon MSK (Kafka).** Topic naming and consumer-group naming are
already fixed by `packages/go/eventbus` and enforced there. Producers write
through the **transactional outbox** in the same package.

**Objects — S3.** Call audio (where consented) and generated transcript artefacts.
SSE-KMS, region-locked, lifecycle rules implementing the retention schedule in
ADR-0012.

## 6. Why This Option Was Selected

**Separate clusters per bounded context, because C1 needs a boundary that is
enforced by something other than good intentions.** Schema-per-service in one
cluster is a convention; a separate cluster is a wall. The moment a service can
technically `JOIN` across a boundary, someone eventually will — usually during an
incident, under time pressure, with the best intentions — and the coupling is
permanent.

- **PostgreSQL because the data is relational and the queries are joins.** A
  subscriber has devices, consent records, call sessions, and a subscription.
  Every access pattern we have is a join or a range scan on a well-known key.
  This is what relational databases are for.
- **Aurora Serverless v2 for C6 and for the traffic shape.** Diurnal load with a
  high peak-to-mean (ADR-0002 §13) is exactly the profile that autoscaling
  storage-compute separation handles well, and it avoids provisioning for peak
  around the clock.
- **Four clusters, not fourteen.** One per *bounded context*, not one per
  service. `identity`, `contacts-sync` and `edge-api` share a context; splitting
  them further would multiply operational surface for no boundary benefit.
- **Redis for C3 because session state changes faster than durability is worth.**
  A screening call's state — current turn, partial transcript, pending tool call —
  mutates every few hundred milliseconds and is worthless once the call ends. Any
  design that writes it synchronously to Postgres puts a disk write inside the
  latency budget for no product benefit.
- **Kafka for C4** because retention and replay are the properties we actually
  need. When `fraud-engine` ships a new model and wants to re-score last week's
  calls, or `transcript-service` has a bug and drops a day of writes, replay is
  the recovery mechanism. A queue that deletes on acknowledgement cannot do this.
- **The transactional outbox is why this is correct rather than merely
  conventional.** Writing to Postgres and publishing to Kafka are two systems; a
  crash between them loses the event or duplicates the write. The outbox makes
  the event part of the database transaction and publishes it asynchronously.

## 7. Trade-offs

**Accepted.**

- **Four clusters cost more than one** — in money, in operational surface, and in
  the impossibility of a cross-context transaction. We accept eventual consistency
  between contexts, mediated by events. A subscriber cancelling a plan and their
  entitlement changing are not atomic, and the design must tolerate the gap.
- **No cross-context joins.** Reporting that spans contexts must be assembled from
  events into a separate analytical store, not by querying two production
  databases. This is more work and it is the correct amount of work.
- **Redis is a dependency on the hot path.** If Redis is unavailable, live calls
  degrade. Mitigated by treating it as `DEGRADED` rather than `UNHEALTHY` in the
  health model (`packages/go/platform`) — a cache miss falls through, but session
  state loss ends the call.
- **Kafka is operationally heavier than SQS**, even managed. Partitions,
  consumer-group rebalancing and lag monitoring are real ongoing concerns.
- **Erasure spans four clusters plus S3 plus Kafka** (C5). Kafka in particular
  cannot delete a single record from a topic. This is addressed in §10 and is the
  most under-appreciated cost of the design.

## 8. Alternatives Rejected

**Option 1 — one shared PostgreSQL, schema-per-service.** Cheaper and
operationally simpler, and rejected on C1. A shared cluster makes the boundary a
naming convention. It also creates a single blast radius: one runaway query, one
connection exhaustion, one failed migration takes down every service at once.

**Option 3 — DynamoDB.** Rejected on query shape. Our access patterns are joins
and range scans over related entities; modelling those in a single-table design is
possible and would make every subsequent query change a migration. The operational
advantages are real but do not compensate for fighting the data model
indefinitely.

**Option 4 — PostgreSQL + Cassandra for transcripts.** Rejected on volume.
Transcripts are text, retained 90 days (ADR-0012), for a consumer product — this
is comfortably within Postgres range. Introducing a second database technology for
a volume problem we do not have is the definition of premature optimisation.

**Option 6 — SNS/SQS.** Rejected on C4. Simpler and cheaper, with no partition or
rebalancing concerns. But messages are deleted on acknowledgement: no replay, no
retention, no ability for a new consumer to read history. Re-scoring historical
calls with an improved fraud model is a first-class requirement, and SQS cannot do
it.

**Option 7 — NATS JetStream.** Genuinely attractive — lighter than Kafka with
comparable semantics. Rejected on C6: no managed offering in-region, so we would
operate it ourselves. Reconsider if a managed offering appears.

**Option 8 — Kinesis.** Rejected on ecosystem. Comparable capability to Kafka with
a smaller tooling and client ecosystem, and it would tie the event contract to an
AWS-proprietary API rather than the Kafka protocol — undermining the portability
discipline in ADR-0008 §14.

## 9. Operational Impact

- **Migrations are expand/contract, always** (C7). A destructive change and the
  code that stops using the column never ship together. `squawk` in the lint
  chain (Phase 1 §18) blocks locking DDL, because a 30-second table lock during a
  screening call is an outage.
- **Four clusters means four migration pipelines**, four backup verifications,
  four connection-pool configurations. Aurora Serverless v2 connection limits are
  a real ceiling and require PgBouncer-style pooling as service count grows.
- **Consumer lag is a golden signal.** Per-topic, per-consumer-group, alerted.
  Lag is how a broken consumer announces itself.
- **Dead-letter topics must be monitored, not just created.** `packages/go/eventbus`
  defines the DLQ naming; an unwatched DLQ is a silent data-loss channel.
- **Backup restore is rehearsed quarterly**, alongside the DR game day in
  ADR-0008 §9. An unverified backup is not a backup.

## 10. Security Impact

- **Encryption at rest with customer-managed KMS keys** across Aurora, S3 and
  MSK (C2). Key policy is separate from data-plane IAM so a compromised workload
  role cannot alter key policy.
- **Least-privilege database credentials per service**, delivered via IRSA and
  short-lived tokens. No shared application user across contexts — that would
  re-create the shared-database problem at the credential layer.
- **PII classification drives storage**, via the annotations in
  `contracts/proto/callscreen/common/v1/annotations.proto`. A field marked
  `SENSITIVE` is encrypted, retention-bounded, and excluded from analytics export
  by policy rather than by reviewer memory.
- **Erasure is genuinely hard and must be designed, not assumed** (C5):
  - **Aurora** — delete or anonymise by subject key. Straightforward.
  - **S3** — object deletion by prefix, plus lifecycle expiry as the backstop.
  - **Redis** — TTL-bounded, so erasure is automatic within the TTL window.
  - **Kafka — cannot delete an individual record.** Two mitigations, both
    required: (a) event payloads carry **identifiers, not personal data**, so the
    stream holds references rather than content; (b) topics carrying any personal
    field use **compaction with tombstones** and a bounded retention. This
    constraint must shape event schema design from the first topic, because it
    cannot be retrofitted.
- **Backups are in scope for erasure.** A deleted subject who reappears from a
  restore is a compliance failure. Retention windows on backups are bounded and
  documented in ADR-0012.

## 11. Cost Impact

The largest managed-service line items on the platform, after telephony and
inference.

- **Aurora Serverless v2 scales with load**, which suits the diurnal profile — but
  the *floor* ACU across four clusters is a fixed monthly cost that exists whether
  or not a single call is screened. Four clusters means four floors; this is the
  concrete price of the boundary in §6.
- **MSK is provisioned, not serverless, in our sizing.** Broker count and storage
  are a fixed cost driven by retention. Retention length is therefore a direct
  cost lever, traded against replay capability.
- **S3 is the cheapest tier and the one that grows monotonically.** Lifecycle
  policies moving audio to infrequent-access and then expiring it (ADR-0012) are
  both a compliance control and the primary storage cost control.
- **Redis is sized by concurrent sessions**, consistent with every other capacity
  unit in the platform.
- **Cross-AZ replication traffic** is a real line item on Aurora and MSK.

## 12. Performance Impact

- **Nothing in the durable path is on the latency budget.** ADR-0011 allocates no
  hop to Postgres, and that is a deliberate design property, not an oversight:
  live call state is in Redis (C3), and durable writes happen after the turn.
- **Redis is on the hot path** and is budgeted within the orchestrator's own
  processing allowance. It must be in-AZ where possible; a cross-AZ Redis hop is
  measurable at this budget.
- **Kafka is entirely off the hot path.** Events are published post-turn or
  post-call. A producer that blocks a turn on a broker acknowledgement is a bug —
  the outbox exists precisely so the producer never has to.
- **Connection pooling matters more than query optimisation** at our volumes.
  Aurora connection exhaustion under a concurrency spike is a more likely failure
  than a slow query.

## 13. Scalability Impact

- **Reads scale with Aurora replicas; writes scale vertically.** At our projected
  volumes the write ceiling is distant. The bounded-context split also means each
  context scales independently — `content` will grow fastest and can be sized
  without affecting `identity`.
- **Kafka partition count is the parallelism ceiling** for a consumer group and
  must be provisioned ahead of need: increasing partitions on a live topic
  reshuffles key-to-partition mapping and breaks ordering guarantees. Partition
  count is a design decision with long-lived consequences, not a runtime knob.
- **Redis scales with concurrent sessions** and is the component most directly
  coupled to peak concurrency.
- **S3 does not meaningfully constrain.**

## 14. Migration Strategy

1. **Phase 1 (launch).** Four Aurora clusters, Redis, MSK, S3 as decided. Schema
   ownership enforced by separate credentials and separate clusters.
2. **Schema evolution** is always expand/contract (C7), gated by `squawk`.
3. **Event schema evolution** follows the contract rules in ADR-0001 and Phase 1
   §14 — additive within a major version, `v2` topic for anything breaking, dual
   publication until consumer telemetry shows the old version is drained.
4. **Analytics** arrives as a separate concern: events streamed from Kafka into a
   columnar store (ClickHouse or Redshift) when reporting demand justifies it.
   **Production databases are never queried for analytics** — that path is closed
   deliberately.
5. **Portability** per ADR-0008 §14: Postgres wire protocol and Kafka protocol
   rather than proprietary APIs, so a provider move is operational.

## 15. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Cross-context join added under incident pressure | Medium | High | Separate clusters make it impossible, not merely discouraged; separate credentials |
| Personal data written into a Kafka payload | High | Critical | Identifiers-not-content rule; PII annotations on event schemas; contract review by CODEOWNERS |
| Erasure misses a store | Medium | Critical | Data map generated from schema annotations; erasure runbook covering all five stores including backups; tested per release |
| Aurora connection exhaustion under concurrency spike | Medium | High | Connection pooling; per-service limits; connection count as a monitored signal |
| Kafka partition count under-provisioned | Medium | High | Sized ahead of projected peak; increasing partitions treated as a breaking change |
| Redis unavailability ends live calls | Low | High | Multi-AZ; `DEGRADED` vs `UNHEALTHY` distinction; session state loss ends the call gracefully rather than hanging |
| Locking migration takes a table offline mid-call | Medium | Critical | `squawk` blocks it in CI; expand/contract mandatory; migrations run outside peak |
| Unwatched DLQ silently accumulates | High | Medium | DLQ depth alerted per topic; DLQ is a monitored resource |
| Backup restore reintroduces erased subject | Low | Critical | Bounded backup retention; erasure replayed against restores; documented in ADR-0012 |

## 16. Future Review Trigger

Revisit when **any** holds:

- Any single Aurora cluster exceeds **70%** sustained write capacity
- Kafka consumer lag p95 exceeds **30 seconds** under normal load
- Transcript storage growth makes Postgres uneconomic versus a columnar or object
  store for `content`
- Analytics demand requires a dedicated warehouse (§14 step 4)
- A managed NATS JetStream offering appears in-region at MSK parity
- Erasure of a single data subject cannot be completed within the DPDP-mandated
  window using the current runbook

## 17. References

- ADR-0008 (cloud infrastructure), ADR-0011 (latency budget), ADR-0012 (privacy,
  retention and erasure)
- `packages/go/eventbus` — topic naming, consumer groups, outbox, DLQ conventions
- `contracts/proto/callscreen/common/v1/annotations.proto` — data classification
- Phase 1 Repository Foundation §20 (monorepo guidelines), §18 (`squawk`)
