# ADR-0000: Template

> Copy this file to `NNNN-kebab-case-title.md`, incrementing `NNNN`. Delete this
> blockquote and every instruction in italics.
>
> Scaffold one with `task docs:adr TITLE="short title"`.

- **Status:** Proposed
- **Date:** YYYY-MM-DD
- **Deciders:** _names or team handles_
- **Consulted:** _people whose expertise was sought_
- **Informed:** _people who need to know the outcome_

---

## Context

_What forces are at play? State the problem, the constraints, and what is
already true. Someone reading this in two years must be able to understand why
the decision was live without reconstructing it from memory._

_Be specific about constraints that are not obvious: regulatory obligations,
latency budgets, team size, vendor commitments already made._

## Decision Drivers

_The criteria the options were judged against, most important first. Naming
these explicitly is what prevents a later reader assuming the decision was
arbitrary._

- _driver 1_
- _driver 2_

## Considered Options

1. _Option A_
2. _Option B_
3. _Option C_

## Decision Outcome

**Chosen: _Option X_.**

_Why this one, in terms of the drivers above._

### Consequences

_Both directions, honestly. An ADR listing only benefits is marketing, not a
record, and it is the negative consequences that a future reader most needs._

**Positive**

- _what this makes easier_

**Negative**

- _what this makes harder, or what it costs_

**Neutral**

- _what changes without being better or worse_

### Confidence

_High / Medium / Low, and why. A low-confidence decision is legitimate; recording
that it was low-confidence is what tells a future reader it is worth revisiting._

### Revisit Trigger

_The specific, observable condition under which this decision should be
reconsidered. "When we grow" is not a trigger. "When p99 first-token latency
exceeds 400 ms" or "when the Go service count exceeds 15" is._

## Options in Detail

### Option A: _name_

_Description._

- **Good:** _…_
- **Bad:** _…_

### Option B: _name_

_Description._

- **Good:** _…_
- **Bad:** _…_

## References

- _links to RFCs, benchmarks, vendor documentation, prior ADRs_
- _supersedes / superseded by: ADR-NNNN_
