# Audio fixture and evaluation tooling

**Phase 11D** · Python 3.12 · offline only

Threshold analysis, confusion matrices and latency aggregation for
`packages/go/audiointel`.

---

## What lives here, and what does not

**Here:** offline analysis of results the Go engine produced. Sweeping a
threshold across a labelled corpus, turning detector outcomes into a confusion
matrix, aggregating latency percentiles, and rendering the tables that go into
`docs/audio-intelligence/EVALUATION_REPORT.md`.

**Not here:** anything the production runtime does. §23 of the phase brief is
explicit — the runtime is Go, and no detection logic may move into Python. The
scripts in this directory never see a sample. They read JSON that the Go
evaluation harness emitted, and every number in it was computed by the same code
that runs in production.

That boundary is what makes the analysis meaningful. A Python reimplementation
of the detector would let the two drift, and the report would then describe
something that never ran on a call.

## The data contract

`audiointel`'s evaluation harness emits one JSON object per scenario run:

```json
{
  "scenario": "04_normal_speech",
  "language": "hi-in",
  "frames": 96,
  "expected_speech_frames": 40,
  "detected_speech_frames": 39,
  "onsets": 1,
  "expected_onsets": 1,
  "endpoints": 1,
  "endpoint_silence_ms": 260,
  "barge_ins": 0,
  "false_triggers": 0,
  "quality": "good"
}
```

Scalars only. **No audio, no levels per frame, nothing that could be
reassembled into a recording** — the same rule the event model follows, applied
to the evaluation path so a corpus of JSON left on a laptop is not a privacy
incident.

## Running it

```bash
# From the repository root, with the workspace virtualenv active.
go test ./packages/go/audiointel/ -run TestScenarios -v > /tmp/scenarios.txt
python tools/audio-fixtures/analyse.py --input results.json
```

`analyse.py --help` lists the analyses. With no input it runs a self-check on
its own arithmetic and exits, which is what CI does — there is no committed
corpus, because there is no committed audio.

## Why there is no committed corpus

Every fixture this phase uses is generated from arithmetic with a fixed seed
(`audiointel.FixtureSeed`). Nothing is recorded, nothing is checked in, and
every run produces byte-identical audio on every machine.

A corpus of real speech would be a recording with all the retention obligations
that implies, it would live in git forever, and it would make a threshold
regression reproducible only for whoever has the files. The synthetic fixtures
are reproducible for everybody.

The cost is stated plainly in `EVALUATION_REPORT.md`: these fixtures validate
**timing behaviour and metadata propagation, not recognition accuracy in any
language**.
