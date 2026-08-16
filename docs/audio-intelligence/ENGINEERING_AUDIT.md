# Engineering Audit

**Phase 11D** · 2026-08-11

Defects found during construction, deviations from the approved plan, and
decisions that were reversed. Recorded because a phase that only documents what
worked teaches nothing about where the hazards are.

---

## Defects found

### D-1 · Continuity checked timestamps before sequence numbers

**Found by:** `TestContinuity_DetectsEveryTransportFault` and
`TestContinuity_DuplicateWindowIsBoundedAt64Frames`, both failing.

`ContinuityDetector.classify` evaluated the media timestamp before the sequence
number. A frame that is late or duplicated by sequence **necessarily** carries
an old timestamp — that is what being late means — so every duplicate and every
reordering was reported as `timestamp_reverse`.

That is a different and much more alarming fault: a backwards media clock
suggests an upstream bug, while reordering is ordinary transport behaviour. The
misreport hid the fault an operator could act on behind one they could not.

**Fix:** sequence questions are answered first; timestamp questions are asked
only of frames whose sequence says they are in order. `timestamp_reverse` now
means what it should — sequence forward, media clock backward. A missing
sequence outranks the timestamp jump it causes, because the gap is the fault and
the jump is its consequence.

**Lesson:** when two detectors can both fire on one event, the ordering between
them is a design decision, not an implementation detail.

### D-2 · Two leaked frames per utterance halved every downstream confidence

**Found by:** `TestOverlap_WalksTheFullLifecycle` failing — overlap never reached
`confirmed`. The cause was three layers away from the symptom.

The speech gate on the noise floor lags one frame, so `MinOnsetFrames` frames of
every utterance reach the estimator labelled as background. Two loud frames in a
hundred-frame ring drove the coefficient-of-variation stability score from
**1.000 to 0.246**, and that score multiplies into every downstream confidence.
So the reported confidence of every speech, endpoint and overlap decision fell
by roughly half for two seconds at the start of **every utterance**.

It presented as overlap detection never confirming, because `MinConfidence` is
the only threshold that reads confidence directly.

**Two wrong fixes were considered first.** A level band excluding loud frames
from the ring would also exclude the genuinely loud half of a bursty background
and destroy `NoiseTransient` detection. A trimmed or median statistic would be
robust but needs a sort on the frame path.

**Fix:** `NoiseAnalyzer.Retract` withdraws exactly the frames now known to have
been speech, called when an onset confirms. Two array writes, no effect on any
other case. The floor itself is deliberately not rewound — the rise clamp
already bounds what those frames did to it.

**Lesson:** a statistic that multiplies into everything downstream needs to be
robust to the small, systematic contamination its own architecture guarantees.
The gate's one-frame lag was documented from the start; its consequence for the
*stability* statistic was not noticed until a test three layers away failed.

### D-3 · Overlap state and confidence disagreed

**Found by:** the same failing test, while investigating D-2.

`OverlapDetector` promoted to `confirmed` on duration alone, then downgraded the
**reported** state to `possible` when confidence came out below `MinConfidence`.
That left `State` describing one thing while `Previous` and `Changed` described
the state machine's actual position — a decision struct that contradicted
itself.

**Fix:** `MinConfidence` gates the transition itself. State and confidence now
describe the same thing.

**Lesson:** a "clamp the output" fix applied to a state machine produces two
disagreeing sources of truth. Gate the transition instead.

### D-4 · The test harness had three copies of the analysis chain

**Found by:** D-2's fix not taking effect, because three sub-rigs each
reimplemented `Push`.

`endpointRig`, `bargeInRig` and `overlapRig` each duplicated the frame →
features → signal → VAD sequence. When the onset retraction was added to
`DetectorRig.Push`, the three copies did not get it and continued testing the
un-fixed behaviour.

**Fix:** `DetectorRig.PushView` is the single assembly point; every rig goes
through it.

**Lesson:** the ordering had three subtleties (the gate lag, the retraction, the
floor-before-window sequence). A rig that reimplemented it was always going to
get one wrong.

### D-5 · The window statistics cost seven times the frame measurement

**Found by:** `BenchmarkFeatureWindow_PushAndStats` at **2618 ns/op** against
`FrameAnalyzer_Analyze` at 454 ns.

`Stats` walked the ring through `At(n)`, which normalises a possibly negative
index on every call. Three hundred calls per frame.

**Fix:** open-coded chronological walks. **2618 ns → 353 ns**, a 7.4× reduction,
with the reproducibility property intact — the walk is still oldest-to-newest,
so summation order depends on window contents rather than on history.

**Lesson:** the abstraction that makes a ring buffer pleasant to read is the one
that makes it slow to traverse.

## Assertions that were wrong, not the code

Recorded separately because "the test failed" and "the code was wrong" are
different findings, and conflating them teaches the wrong lesson.

| # | Assertion | Reality |
|---|---|---|
| A-1 | A sine crossing zero 8 times in a frame yields ZCR 8/(n−1) | **7.** The eighth crossing lands exactly on the frame boundary, which is the documented within-frame limitation. The measurement was right. |
| A-2 | A perfectly steady signal reports modulation exactly 0 | **~1.7e-16.** Summing twenty copies of 0.01 does not land on exactly 0.2. Sixteen orders of magnitude below the threshold it feeds. |
| A-3 | Loud speech reports higher confidence than quiet speech across sessions | **No.** Confidence is evidence × floor confidence, and a loud talker starves the floor estimator. The test was measuring floor coverage while claiming to measure evidence. |
| A-4 | A "quiet line" fixture at 1e-4 RMS classifies as `quiet` | **`very_low`.** 1e-4 is below `MinSignalRMS`; it genuinely is very low. The fixture was wrong. |
| A-5 | The clipped fixture is `unusable` | **`degraded`.** A syllabic signal at 1.4× clips only on peaks — worst windowed ratio 0.0050 against an unusable threshold of 0.0200. |
| A-6 | Interrupting a Phase 11C session cancels its synthesis | **Only if it is still synthesising.** Phase 11C's fake finishes a short reply instantly and its frame queue holds 100. The first integration test reported "11C did not cancel" and was wrong; it had nothing to cancel. |

A-3 and A-6 are the two worth remembering. Both produced a *plausible* failure
message that would have been accepted as a real defect by anyone not willing to
trace it.

## Deviations from the approved plan

| Plan item | Deviation | Why |
|---|---|---|
| Task 10, separate onset/offset detection | Folded into `SpeechDetector` | Onset and offset are transitions of the same state machine. A separate detector would have had to duplicate the machine's state to know when a transition happened. |
| §29's `Media → Audio Intelligence → Speech` as an import edge | Data flow only; the import edge is never created | `speech` is frozen and already imports `media`. Flagged before approval; resolved with the `SpeechController` port and `audiobridge`. |
| Declared edge `silence → uncertain` | Removed | Floor convergence latches, so the edge is unreachable. An unreachable declared edge is worse than an absent one — the state machine test would have to fabricate a way to exercise it. |
| `harness.go` importing `testing` | Replaced with a two-method `TB` interface | No frozen phase's harness imports `testing` into a production file; it compiles into every linking binary. |
| Two new config fields not in the plan | `ModulationWindowFrames`, `ProfileGraceFrames` | Required by the measurement in the next section. |
| One new `SessionContext` field | `Language` | §22 requires preserving Phase 11C's language metadata. Carried, never interpreted. |

## Design changed by measurement

Before writing the VAD, the fixtures' actual statistics were measured rather
than assumed. Three findings changed the design:

**`ZCRMax` was too permissive at 0.60.** Measured: speech 0.157–0.346, white
noise 0.384–0.610. Set to 0.45 — above speech with margin for real fricatives,
below most of the noise range. It does **not** cleanly separate them, and is not
expected to.

**A 1 kHz tone measures ZCR 0.245 — identical to speech.** ZCR cannot reject it;
only modulation can. This is why there are three features rather than two, and
why the documentation says ZCR is a rejector of specific things rather than an
identifier of speech.

**At 40 ms after onset, noise and speech are indistinguishable by modulation,
because neither has had time to modulate.** A long modulation window is mostly
the preceding silence, so any sudden sound measures as strongly modulated. This
forced two additions: a short modulation window (200 ms) that fills with the new
sound and then tells the truth, and a `speech → noise` reclassification edge with
a grace period. A steady tone is now treated as speech for a bounded **340 ms**
before reclassification.

Had the thresholds been guessed rather than measured, the first two would have
shipped as latent tuning bugs and the third would have shipped as hold music
holding a turn open for the length of a call.

## Known limitations

Each is documented where it matters and none is a defect.

| Limitation | Where |
|---|---|
| Echo and double-talk are not separable | [OVERLAP_DETECTION.md](OVERLAP_DETECTION.md) |
| A clause boundary and a turn end are the same measurement at a 250 ms window | [ENDPOINTING.md](ENDPOINTING.md) |
| A steady sound is speech for up to ~440 ms before reclassification | [VAD_ARCHITECTURE.md](VAD_ARCHITECTURE.md) |
| Duplicate detection is bounded at 64 frames | [NOISE_AND_QUALITY.md](NOISE_AND_QUALITY.md) |
| Stereo and non-PCM codecs are refused | fail-closed at construction |
| Nothing expires an idle session | [AUDIO_INTELLIGENCE_ARCHITECTURE.md](AUDIO_INTELLIGENCE_ARCHITECTURE.md) §3 |
| No fixture is real speech in any language | [EVALUATION_REPORT.md](EVALUATION_REPORT.md) §3 |
| The race detector has not been run | [EVALUATION_REPORT.md](EVALUATION_REPORT.md) §1 |

## Frozen phases

Untouched. No file under `packages/go/media`, `packages/go/speech`,
`packages/go/runtime`, `packages/go/metrics`, `packages/go/telephony`,
`packages/go/conversation`, `packages/go/memory`, `packages/go/governance` or
`packages/go/toolruntime` was modified.

Three files outside the two new modules changed:

| File | Change |
|---|---|
| `go.work` | Two `use` entries added |
| `pyproject.toml` | One ruff per-file-ignore for the CLI script's `print` |
| `docs/superpowers/specs/…-design.md` | The approved design |
