# Security Hardening Report — Phase 10.5

**Scope:** the eight AI-plane modules.
**Method:** every claim below was checked by command, including the ones that
turned out to contradict the code's own documentation.

---

## 1. Position

The AI plane has an unusually small attack surface, and the reason is
structural rather than defensive: **these modules perform no I/O**. No network
client, no database driver, no filesystem access, no provider call. Everything
that reaches outside is an interface implemented in an adapter a service opts
into.

That single property removes most of a normal threat model. There is no
connection string to leak, no request to forge, no TLS configuration to get
wrong. What remains is data handling, and that is where the findings are.

---

## 2. Secrets and credentials

**None present.** Checked across all eight modules:

```console
$ grep -rniE 'password|secret|api[_-]?key|token|credential' --include=*.go \
    runtime conversation memory toolruntime governance evaluation metrics \
    evalsubjects | grep -v _test
```

The only matches are `TokenCounter`, `BytesPerToken` and similar — LLM context
tokens, not authentication tokens. No hardcoded credential, no default password,
no embedded key.

There is also no configuration loader in the AI plane that could read one:
`runtime.Config` is a typed struct populated by the caller, validated on
construction, with no environment or file access. A service supplies the values
and holds the secrets.

**No finding.**

---

## 3. Cryptography

| Use | Package | Assessment |
|---|---|---|
| Identifier generation | `crypto/rand` (`runtime/ids.go`, `toolruntime/ids.go`) | Correct. Not `math/rand`. |
| Fingerprints | `crypto/sha256` (`toolruntime/ids.go`) | Correct for an integrity fingerprint. |

`runtime/ids.go` documents that `crypto/rand.Read` never returns a short read,
and `toolruntime/ids.go` treats a `crypto/rand` failure as unrecoverable rather
than falling back to a weaker source — which is right: a silent fallback to
`math/rand` for identifier generation is how idempotency keys become guessable.

**No encryption at rest or in transit exists in the AI plane, and should not.**
Both belong to the storage and transport adapters. What the plane does now
provide is the hook: `Record.Payload` is opaque `[]byte`, so a repository
implementation can encrypt it without any domain type changing. See
[FINAL_PRODUCTION_READINESS_REPORT.md](FINAL_PRODUCTION_READINESS_REPORT.md).

---

## 4. Sensitive data handling

The platform models sensitivity as a first-class property rather than a
convention:

- `conversation.SensitiveValue` — a context value classification.
- `memory` sensitivity propagation on compression, with a deliberately
  conservative rule: a summary inherits the **maximum** sensitivity of its
  inputs, so one sensitive record raises the whole summary and every
  less-sensitive memory folded into it. Over-classifying is the safe direction.
- `memory.PreserveSensitive` exempts sensitive records from being merged at all.
- Governance carries `SubjectID` — the person an action is about — separately
  from `ActorID`, which is what makes subject-scoped consent expressible.

This is stronger than most systems at this stage, and it was designed in rather
than added.

### S1 — The runtime's documentation claimed a redaction control that does not exist *(fixed this phase)*

`runtime/doc.go` listed the platform's capabilities in a table, including:

```
//	Logging         [Kernel.Logger]       structured, redacting, contextual
```

while `Kernel.Logger` itself says the opposite, and says it in capitals:

```go
// PRIVACY. The returned logger redacts nothing on its own. Callers must not log
// message content, phone numbers or caller names — those are SENSITIVE or
// PERSONAL under annotations.proto.
```

The table is the first thing a reader of the package sees. An engineer who read
it and not the method could reasonably have concluded it was safe to log message
content — and the failure would have been silent, permanent and in the logs.

**Fixed.** The entry now reads `structured, contextual — NOT redacting`, with a
note recording the correction. Two adjacent entries in the same table were also
overclaiming and were corrected at the same time: tracing advertised "spans,
propagation, sampling" for a port with a no-op default and one span in the whole
plane, and health referenced a `HealthReporter` type that does not exist.

A documentation-only change, no behaviour affected, all tests pass.

### S2 — No redaction helper exists *(medium, open)*

The rule "never log content" is enforced by discipline and code review. The
platform provides nothing to make compliance easy: no `Redacted` wrapper type,
no `slog.LogValuer` implementation on the sensitive types that would render them
as `[REDACTED]` automatically.

`slog.LogValuer` is the natural fit — a type implementing it controls its own
log rendering, so a sensitive value passed to a logger by mistake redacts itself
rather than relying on the author having remembered.

**Recommendation:** implement `LogValuer` on `conversation`'s sensitive context
values and on memory record content. This turns a convention into a mechanism
and is a small, additive change.

### S3 — No memory zeroisation *(low, open)*

Nothing wipes sensitive values from memory after use.

```console
$ grep -rn 'Zeroi\|zeroize\|wipe\|Wipe' --include=*.go runtime memory conversation
# nothing
```

Stated honestly: **in Go this is close to unachievable and often theatre.**
Strings are immutable and may be interned; the garbage collector copies and
moves; a `[]byte` cleared after use may already have been duplicated during a
slice growth. Zeroisation is meaningful for a `[]byte` holding a key that is
about to be freed, and mostly illusory for a `string` holding a phone number.

**Where it would genuinely help:** the payload buffers in the repository layer,
which are `[]byte` and are the closest thing the platform has to a key-shaped
secret. That is a Phase 11 concern, when a real backend exists.

**Recommendation:** do not chase this generally. Do consider it for repository
payload buffers and for any future key material, and do not claim it as a
control anywhere until it is real — the S1 pattern.

---

## 5. PII exposure

### S4 — Evaluation recordings may contain personal data *(medium, open — carried from Phase 10F)*

The memory engine handles personal data by design, and its evaluation adapter
surfaces retrieved values into observations and goldens.

**Materially improved this phase.** Phase 10F recorded this with no mechanism
behind it; the platform now has:

- a retention schedule covering every record kind, with construction refusing
  any uncovered kind — an uncovered kind would be kept forever and nobody would
  be told
- 90-day retention on observations and runs, mirroring **ADR-0012**, which had
  no counterpart in the evaluation platform at all until now
- 180-day archival on goldens, on the argument that a golden is an approved human
  decision and "what did we consider correct last year" is a real question
- legal hold that outranks every deletion path
- a content-free audit trail, retained indefinitely and exempt from its own
  sweep

**What is still missing** is the rule that scenarios must not use production
data. The mechanism now exists to bound how long a recording lives; nothing
prevents the recording from containing a real caller's name in the first place.

**Recommendation:** state the constraint in `evalsubjects` package
documentation and enforce it in scenario review. This is a process control, and
saying so is better than pretending a technical one exists.

### S5 — Observations are unbounded in size *(medium, open — carried from Phase 10F)*

Nothing caps how much a subject may return in a `Value`. Phase 10C caps memory
records and refuses oversized ones (`INV-MEM-8`); the evaluation platform has no
equivalent. Adapter discipline is the only thing preventing unbounded growth of
observations, their clones and their goldens.

Unchanged this phase. Now more pressing, because durable storage means the
growth persists.

---

## 6. Audit logging — strengthened this phase

The new retention layer writes an audit entry for every lifecycle event:
deletion, sweep, legal hold placed or lifted, migration, restore.

Three properties worth naming:

1. **Content-free by construction.** An entry names keys, counts and reasons —
   never payloads. Enforced by `TestAudit_RoundTripsWithoutPayloadContent`. This
   is frozen invariant I7 applied to the longest-lived table in the system: an
   audit log that quoted the records it described would outlive the retention
   rule it exists to evidence.
2. **Attribution is required where a human acted.** `SetLegalHold` refuses an
   empty author or reason. An automatic sweep has no author, and that absence is
   itself informative — an unattributed deletion was systematic.
3. **Exempt from its own sweep.** Deleting the record of what was deleted
   destroys the proof that retention was honoured, alongside the data. Enforced
   by `TestSweep_NeverDeletesAuditRecords`, which stamps an expiry onto the
   audit records and sweeps ten years forward.

### S6 — Audit attribution is not authentication *(low, by design)*

`SetLegalHold` and `GoldenStore.Approve` take an author string and trust it.
There is no identity, no signature, no authorisation check. Correct for an
in-process library; wrong for a service.

Stated here so nobody later mistakes a recorded name for a verified one.

---

## 7. Legal hold — the strongest new control

Legal hold is enforced at three independent layers, because a hold that one code
path honours and another ignores is worse than none:

1. `Record.Expired()` returns false under hold, whatever the deadline says — so
   no backend has to restate the rule, and a second implementation cannot forget
   it.
2. `Delete` refuses with `ErrLegalHold`.
3. `Put` preserves an existing hold when a record is replaced. Re-storing a run
   to correct its label would otherwise lift the hold as a side effect of
   carrying `LegalHold: false` in the new value.
4. `Restore` preserves holds placed after the snapshot was taken. A restore is
   an operational action and must not become a way to discard a hold by
   accident.

Each has a test named for the failure it prevents.

---

## 8. Findings

| ID | Finding | Severity | Status |
|---|---|---|---|
| S1 | Documentation claimed a log-redaction control that does not exist | **high** | **fixed** |
| S2 | No redaction mechanism; the rule is convention only | medium | open |
| S3 | No memory zeroisation | low | open, mostly not worth pursuing |
| S4 | Evaluation recordings may hold personal data; no rule against it | medium | mechanism added, process rule open |
| S5 | Observations unbounded in size | medium | open |
| S6 | Audit attribution is not authentication | low | by design, documented |
| — | Secrets, credentials, hardcoded keys | — | none present |
| — | Cryptographic primitive misuse | — | none found |
| — | Legal hold enforcement | — | new, four layers |

No critical finding. S1 was the most serious and is fixed: a false security
claim in the most-read documentation in the package.

---

## 9. Related

- [REPOSITORY_AUDIT.md](REPOSITORY_AUDIT.md)
- [OBSERVABILITY_AUDIT.md](OBSERVABILITY_AUDIT.md)
- [DEPENDENCY_AUDIT.md](DEPENDENCY_AUDIT.md)
