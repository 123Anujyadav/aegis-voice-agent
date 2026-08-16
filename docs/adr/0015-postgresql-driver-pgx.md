# ADR-0015: PostgreSQL persistence — pgx/v5, the first third-party dependency

- **Status:** Accepted
- **Date:** 2026-08-16
- **Deciders:** repository owner
- **Consulted:** ADR-0009 (database backbone); `evaluation.Repository`'s own
  design notes; Phase 12 discovery §6 and §12
- **Informed:** anyone reviewing the dependency surface or running `govulncheck`

---

## Context

Production blocker **B2** is that nothing durable survives a restart. The
evaluation platform's **goldens** — approved baselines that encode *human
decisions no process can reproduce* — live only in `MemoryRepository`. A process
restart loses them, which means the verification platform cannot be trusted
across a deploy.

`evaluation.Repository` was designed for this. Its own documentation says so:

> *"NOT BOUND TO A DATABASE. Nothing in this interface mentions SQL, a
> connection, a transaction or a driver. An Aurora implementation, a filesystem
> implementation and the in-memory one in this file are all substitutable."*

and its conformance suite says:

> *"Written against the interface rather than the concrete type, so the Aurora
> implementation Phase 11 adds is verified by exactly this code."*

**ADR-0009 already chose the engine**: Aurora PostgreSQL, one cluster per
bounded context. This ADR does not re-decide that. It decides the one thing
ADR-0009 left open — *how Go talks to it* — and that is consequential because
**every hand-written module on the platform has zero third-party Go
dependencies.**

Stated precisely, because a looser version of this claim is easy to repeat and
wrong: measured across all 43 workspace modules, exactly one has third-party
requires today — `packages/go/contracts-go`, which is **generated** protobuf and
Connect binding code and necessarily depends on `google.golang.org/protobuf`.
Every module a human wrote, including all eight Phase-10 AI-plane modules and
every Phase-11 module, requires nothing outside the standard library.

That is not an accident. It is why `govulncheck` currently reports only standard
library advisories against the AI plane, and why the Phase 11E supply chain is
auditable by reading one `go.mod`. **This ADR ends it for hand-written code**,
which is a real cost and belongs in an ADR rather than in a commit.

## Decision Drivers

- **Correctness of the driver under the contract we must honour** — `Repository`
  requires atomic batches, typed errors, context cancellation and legal-hold
  invariants.
- **Smallest defensible dependency surface**, since this sets the precedent.
- **No ORM, no second persistence abstraction.** The port already exists.
- **Failures must be typed and visible**, never silent.
- **Reversibility** — this must be removable without touching the engines.

## Considered Options

1. **`github.com/jackc/pgx/v5`** — PostgreSQL-native driver and toolkit.
2. `github.com/lib/pq` — the older `database/sql` driver.
3. Stay stdlib-only: no persistence, keep goldens in memory.
4. `database/sql` with a driver chosen at deploy time.

## Decision Outcome

**Chosen: Option 1 — `github.com/jackc/pgx/v5`.**

- **`lib/pq` is in maintenance mode**; its own README directs new work
  elsewhere. Adopting a maintenance-mode library as a platform's *first*
  dependency is the wrong precedent.
- **pgx speaks the PostgreSQL wire protocol natively.** It carries real types —
  `jsonb`, `timestamptz`, arrays — without the round-trip through
  `database/sql`'s `driver.Value`, which matters because `Record.Payload` is a
  JSON document and the retention rules are `timestamptz` comparisons.
- **Context cancellation is honoured at the protocol level**, which the
  conformance suite explicitly tests (`TestRepository_RespectsContextCancellation`).
- **`pgxpool` provides a supervised connection pool**, so lifecycle is the
  driver's job rather than something this repository reimplements.
- It is the driver AWS documents for Aurora PostgreSQL from Go, consistent with
  ADR-0008/0009.

Option 3 leaves B2 open. Option 4 buys deployment flexibility nobody asked for
and costs the native type handling that motivates the choice.

### Dependency and security implications

**This ends the zero-third-party-dependency property for hand-written code.**
Stated plainly because it is the main cost.

`pgx/v5` pulls exactly five modules into the build, verified with
`go list -deps` rather than assumed: `jackc/pgpassfile`, `jackc/pgservicefile`,
`jackc/puddle/v2` (the pool), `golang.org/x/sync` (semaphore) and
`golang.org/x/text` (SASL/SCRAM string preparation). All are small,
single-purpose, and by the same author or the Go project itself. No ORM, no
framework, no transitive web of packages.

`go list -m all` additionally shows `testify`, `go-spew`, `go-difflib` and
similar. Those are pgx's **own test dependencies**; they appear in the module
graph and are **not** in the build. The distinction matters when reading a
vulnerability report.

Consequences accepted:

- `govulncheck` output is no longer trivially "stdlib only". That is the point
  of running it, and Phase 12 T1 already wired it.
- Dependabot (already configured) will now have Go updates to propose.
- **Containment:** only the new adapter module depends on pgx. `evaluation`,
  `runtime`, `governance`, `toolruntime`, `media`, `telephony` and every other
  hand-written module keep **zero** third-party dependencies. Measured across all
  43 workspace modules after this change: 41 have none, `contracts-go` has its
  pre-existing generated-protobuf requires, and `evalstore` has pgx. A
  `go list -m all` in any other module must stay empty of non-first-party
  modules, and that is verified.

### Connection and lifecycle policy

- **`pgxpool.Pool`, owned by the adapter**, created in its constructor and
  released by `Close`. `Close` is idempotent, as `Repository.Close` requires.
- **Configuration comes from the environment**, never from source. A DSN is read
  from `AEGIS_EVALSTORE_DSN`, falling back to standard `PG*` variables that
  libpq and pgx already honour. **No credential, password, or DSN containing one
  is ever written to source or git.** Tests skip rather than invent one.
- **Every query takes the caller's `context.Context.`** No background query
  outlives its caller, and cancellation surfaces as a typed error.
- **No unbounded retry.** The adapter does not retry at all: a failed operation
  returns a typed error and the caller decides. A driver-level reconnect is the
  pool's business and is bounded by it.

### Migration policy

- **Forward-only, numbered, embedded** in the binary via `embed`, so the schema
  travels with the code that expects it and cannot drift from a deploy artefact.
- **A `schema_migrations` ledger table** records applied versions. Applying the
  chain to an empty database creates everything; re-applying is a no-op. This
  matches the idempotency the existing `schema.sql` reaches for with
  `CREATE TABLE IF NOT EXISTS`, but makes "which version is this database at" a
  question with an answer.
- **Each migration runs inside a transaction**, so a failing migration leaves
  the database at the previous version rather than half-applied.
- **No down-migrations.** A down-migration that drops a table holding approved
  goldens is a data-loss tool disguised as a safety feature. Rollback is
  addressed below.

*(Note: the pre-existing `packages/go/persistence/schema.sql` is **not** part of
this chain. It targets a different bounded context, contains invalid SQL —
`msisdn VARCHAR(20) NOT UNIQUE NOT NULL` — and has evidently never been applied.
Fixing it is out of scope and is recorded rather than silently repaired.)*

### Failure behaviour

Every failure maps to the port's existing typed errors, never to a bare driver
error and never to a silent success:

| Condition | Result |
|---|---|
| Record absent | `ErrRecordNotFound` |
| Delete under legal hold | `ErrLegalHold`, record retained |
| Record schema newer than build | `ErrSchemaTooNew`, write refused |
| Use after `Close` | `ErrClosed` |
| Database unreachable | wrapped driver error; **no partial success reported** |
| Context cancelled/timed out | `context.Canceled` / `DeadlineExceeded` propagated |
| Batch partially failing | whole batch rolled back |

`PutBatch` is executed **in one transaction**. The interface deliberately does
not *promise* atomicity — *"a backend that cannot provide it should not be
excluded and should not lie about it"* — but PostgreSQL can, so this backend
provides it and says so.

### Rollback implications

- **The dependency is reversible**: delete the adapter module and the `go.mod`
  entry. No engine imports it; they depend on the port.
- **The data is not.** Once goldens are durable, dropping back to
  `MemoryRepository` loses approvals recorded since. `Snapshot`/`Restore` already
  exist on the port for exactly this, and taking a snapshot before a migration is
  the documented reason they exist.
- **Schema rollback is deliberately manual.** Forward-only migrations plus a
  snapshot beat an automated `DROP` against approved baselines.

### Why this does not disturb the frozen architecture

**No frozen module is modified.** The adapter is a new leaf module that
*implements* `evaluation.Repository`; `evaluation` does not know it exists and
does not import it. Dependency direction is preserved — adapter → port, never
the reverse — which is precisely the arrangement the port's documentation
describes.

`MemoryRepository` is untouched and remains the default. Nothing that works
today changes behaviour.

**One honest limitation.** The conformance suite is `TestRepository_Conformance`
in `packages/go/evaluation/persistence_test.go`. It is an *internal* test in a
frozen module, so it cannot be imported, and adding a `"postgres"` factory to its
table would both modify frozen code and invert the dependency (evaluation would
have to import a driver). The adapter therefore carries a **faithful port of that
suite**, run against real PostgreSQL. It is labelled as a port, not claimed to be
"the existing suite executed unchanged". Making the frozen suite reusable — by
exporting it from a `evaluationtest` package — is a worthwhile later change and
is **not** taken here.

### Consequences

**Positive**

- B2's golden-durability half closes: approvals survive a restart.
- The port gains its second implementation, which is what proves the abstraction
  was real rather than aspirational.
- `govulncheck` starts doing the job it was wired up for.

**Negative**

- **Zero third-party dependencies for hand-written code is over**, permanently
  and platform-wide in perception even though it is contained to one module.
- A supply chain to monitor, and a driver version to keep current.
- Integration tests now need a real database, so the gate is environment-dependent
  in a way the rest of the suite is not.

**Neutral**

- `packages/go/persistence` and `packages/go/repository` are untouched. They
  serve a different bounded context and adopting pgx there is a separate
  decision.

### Confidence

**High** on the driver choice — pgx/v5 is the mainstream, actively maintained
option and the contract it must satisfy is written down and executable.
**Medium** on the schema's long-term shape: the first schema for a record store
usually learns something in its first year of queries, which the migration ledger
exists to absorb.

### Revisit Trigger

Revisit when **any** of the following is first observed:

- pgx/v5 enters maintenance mode, or a v6 changes the pool API.
- A second bounded context needs PostgreSQL, making a shared adapter worthwhile
  rather than duplicated.
- Golden record count exceeds **1,000,000**, at which point `List` without
  keyset pagination becomes the bottleneck.
- A `govulncheck` finding lands in pgx or a transitive dependency — the first
  real test of this decision's cost.

## References

- `packages/go/evaluation/persistence.go` — the `Repository` port and its two
  backend rules
- `packages/go/evaluation/persistence_test.go` — `TestRepository_Conformance`
- ADR-0009 — Aurora PostgreSQL per bounded context
- ADR-0008 — AWS `ap-south-1`, EKS
- ADR-0012 — DPDP retention and legal hold, which the port encodes
- <https://github.com/jackc/pgx> · <https://github.com/lib/pq> (maintenance mode)
- Supersedes: none. Superseded by: none.
