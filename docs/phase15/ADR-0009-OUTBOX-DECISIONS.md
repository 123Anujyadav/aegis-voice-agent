# ADR-0009 follow-up: outbox entries that fail to publish (decision request)

**Status:** proposed; **owner decision required.** Nothing here is implemented.
**Related:** PR #25 (only reports `MarkFailed` failures; it does not change the
retry design), `docs/adr/0009-database-and-event-backbone.md`,
`READINESS_AUDIT.md` §10.9.7.

## Facts on `main` (`3e38aa5`) [V]

| Item | Fact |
|---|---|
| Table | `outbox_events`: `status VARCHAR(20) DEFAULT 'PENDING'`, `retry_count INT DEFAULT 0`, `error_message TEXT`, `created_at`, `processed_at`. The partial index `idx_outbox_pending (status, created_at) WHERE status = 'PENDING'` (`packages/go/persistence/schema.sql:61-73`). |
| Fetch | `FetchPending`: `WHERE status = 'PENDING' ORDER BY created_at LIMIT $1`, with **no row lock** (no `FOR UPDATE SKIP LOCKED`) (`persistence.go:69`). |
| Failure | `MarkFailed`: `status='FAILED', retry_count = retry_count + 1, error_message = $1` (`persistence.go:97`). |
| Retry | Nothing moves `FAILED` back to `PENDING`. Nothing reads `retry_count`. |
| Callers | `OutboxProcessor.ProcessBatch` has **0 callers** on `main`. No relay runs in any service today. |
| DLQ | `eventbus.Topic.DeadLetterTopic()` returns `<topic>.dlq` (`eventbus/topic.go:183`). The outbox does not use it. |
| ADR-0009 | "Dead-letter topics must be monitored, not just created … an unwatched DLQ is a silent data-loss channel" (§206). The risk table: "DLQ depth alerted per topic" (§311). |
| Consumers | `inbox_messages` provides idempotent de-duplication on the consumer side, so at-least-once delivery is already assumed. |

**Today's behaviour:** one failed publish makes an entry `FAILED` forever, with no alert. That is the silent data-loss channel ADR-0009 warns about. It only matters once a relay is wired up.

## Decisions

### 1. Retry, or permanent failure

| Option | Behaviour | Trade-off |
|---|---|---|
| **1a: permanent** | `FAILED` is terminal; an operator replays it by hand. | Simplest. Every transient broker blip becomes a manual task, and it is data loss unless monitored. |
| **1b: bounded automatic retry** (**recommended**) | A failed publish stays retryable until a limit, then goes to the DLQ (decision 4). | It needs decisions 2–6. Duplicates are bounded by the attempt limit and absorbed by the consumer inbox. |

### 2. Maximum attempts and backoff

| Option | Detail |
|---|---|
| **Recommended** | At most **8 attempts**. Exponential backoff `min(2^(n-1) × 5 s, 15 min)` between them, with full jitter. Without jitter, the 7 waits are 5 s to 320 s, about 10.6 minutes in total before the DLQ, which covers a broker restart. With jitter, each wait is uniform between 0 and that value. For longer outages, use 10 attempts: the waits then reach the 15-minute cap, about 36 minutes in total. |
| Alternative | A fixed interval (for example, 30 s × 20). Simpler, but it hammers a broker that is down. |
| Alternative | A per-topic configuration. Flexible, but more surface. Defer it until a topic needs it. |

### 3. Next-attempt schema

| Option | Schema change | Consequence |
|---|---|---|
| **3a** (**recommended**) | Add `next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now()`, keep `status='PENDING'` while retrying, and replace the partial index with `(next_attempt_at) WHERE status = 'PENDING'`. | A forward migration with a default, so existing rows stay eligible. `FetchPending` gains `AND next_attempt_at <= now()`. One query. |
| 3b | No new column; compute eligibility from a `last_attempt_at` and `retry_count`. | It still needs a column (`last_attempt_at`). The filter is not index-friendly. |
| 3c | A `RETRYING` status. | Another state for every query and dashboard to handle; no benefit over 3a. |

**Recommended with 3a:** change `FetchPending` to claim rows with `FOR UPDATE SKIP LOCKED`, inside a transaction. Without it, two relays can publish the same entry; the inbox de-duplicates, but it is wasted work. This is the same migration and PR, and it needs a decision on how many relays run.

### 4. Dead-letter routing

| Option | Detail |
|---|---|
| **4a** (**recommended**) | After the last attempt, publish the original payload to `topic.DeadLetterTopic()`. Headers: the original topic, `correlation_id`, outbox `id`, the attempt count and the last error. Then mark the row `DEAD` (terminal). If the DLQ publish itself fails, the row stays `PENDING` and is retried on the same schedule. |
| 4b | No DLQ topic; terminal `FAILED` in the table only. | Keeps the evidence in Postgres, but ADR-0009 names DLQ topics as the convention, and replay tooling would read from Kafka. |

`DEAD` is a new terminal status value; `VARCHAR(20)` already fits it. Existing `FAILED` rows (none expected, since no relay runs) are migrated to `PENDING` with `next_attempt_at = now()`, or left as historical; that is the owner's choice.

### 5. Monitoring

| Signal | Recommended alert |
|---|---|
| Retrying entries (`status='PENDING' AND retry_count > 0`) | warn if more than 0 for 15 min |
| The oldest pending entry's age (`now() - min(created_at)`) | page if more than 15 min. This is the "the relay is down or stuck" signal. |
| `DEAD` count per topic | page if any increase |
| DLQ depth and lag per topic (Kafka) | page if more than 0. This is ADR-0009 §311. |
| Publish attempts and failures (a relay metric, by topic and outcome) | dashboard |

The SQL gauges need a periodic exporter, either in the relay or in a metrics job. Thresholds are starting points for the owner to tune.

### 6. Meaning of `retry_count`

| Option | Meaning |
|---|---|
| **Recommended** | **The number of publish attempts that failed.** It is incremented exactly once per failed attempt, in the same `UPDATE` that sets `next_attempt_at` and `error_message`, and never reset. It is 0 for an entry never attempted or published on the first try. It drives decisions 2 and 4: DLQ when `retry_count >= max_attempts`. |
| Alternative | Attempts made, including the successful one. It needs an extra update on success, for no gain. |
| Alternative | Drop the column (with option 1a). |

The rename (`failed_attempts`) is optional. It costs a migration, so the recommendation is to keep the name and document it.

## Consequences of the recommended set (1b, 2, 3a with SKIP LOCKED, 4a, 5, 6)

- **Schema:**
  - a forward migration adding `next_attempt_at`, the new partial index and the `DEAD` value;
  - a rolling deploy is safe if the column has a default and the old code ignores it.
- **Code:**
  - `MarkFailed` becomes "record a failure and schedule the next attempt, or dead-letter";
  - `FetchPending` claims rows;
  - a DLQ producer;
  - tests for the backoff schedule, the transition at the attempt limit, a failed DLQ publish, and two concurrent relays.
- **Semantics:**
  - delivery stays at-least-once;
  - duplicates are bounded by `max_attempts`, plus any re-publish after a `MarkPublished` failure (that path is already documented in #25);
  - ordering per key is **not** guaranteed across retries. If any topic needs per-key ordering, retry must block later entries for the same key. **The owner must say whether any topic needs that.**
- **Operations:**
  - five alerts, a runbook for "the DLQ is non-empty" (inspect, fix, replay);
  - a replay tool or procedure from `<topic>.dlq`.
- **Scale:** this is an architecture change to ADR-0009's delivery path. It belongs in its own ADR amendment and PR, not in #25.

## What the owner needs to answer

1. 1a or 1b?
2. The attempt limit and backoff (or accept 8 × exponential, capped at 15 min).
3. Accept 3a and `FOR UPDATE SKIP LOCKED`? How many relay instances?
4. 4a (DLQ topic plus `DEAD`) or 4b?
5. The alert thresholds and who is paged.
6. The `retry_count` meaning (or accept "failed attempts").
7. Does any topic need per-key ordering?
8. **#25:** merge as-is? It is independent of these decisions; it only stops discarding `MarkFailed` errors.
