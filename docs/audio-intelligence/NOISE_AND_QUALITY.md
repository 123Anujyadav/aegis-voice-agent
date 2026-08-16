# Noise and Quality

**Phase 11D** · `packages/go/audiointel/noise.go`, `quality.go`, `continuity.go`

---

## 1. The adaptive noise floor

A naive estimator tracks the recent average level. On a call it converges on the
level of the caller's voice — and then nothing is above the floor, nothing is
speech, and the agent never responds.

Three separate mechanisms prevent that, and each is needed.

### 1. Speech gating

The estimator does not observe frames the detector classified as speech or
candidate speech. This is the primary defence and in normal operation the only
one that acts.

Measured: 250 frames of loud gated speech move the floor by **exactly zero**.

### 2. Asymmetric adaptation

Downward fast (`FallAlpha` 0.05), upward slow (`RiseAlpha` 0.002). The gate is
not perfect, so the direction contamination pushes is the direction the
estimator resists.

The asymmetry also encodes which failure is worse. A floor stuck **above** the
true level suppresses real speech, and a missed word makes a caller repeat
themselves. A floor stuck **below** it causes a false trigger, which costs a
wasted cycle.

### 3. A hard rise clamp

`MaxRiseDBPerSecond` (6 dB/s) bounds upward movement regardless of the
coefficient. Even with the speech gate failing completely, one full-scale frame
raises the floor by at most 0.12 dB at a 20 ms cadence — and the fast downward
rate erases it within a few frames of the next silence.

`TestNoise_OneLoudFrameCannotRedefineTheFloor` is §4 of the brief in one test:
it presents a full-scale frame as background, asserts the rise is inside the
clamp, and then asserts the floor recovers to within 10% of the true background
after 100 quiet frames.

### Warm-up uses the minimum, not the mean

Before the detector exists there is no gate, so warm-up frames may contain
speech. The **minimum** observed level is used, because speech only ever *adds*
energy: the quietest moment in the warm-up window is the best estimate of what
lies underneath. An average over a warm-up in which somebody said hello
converges on hello.

Measured: a warm-up that is 80% loud speech still converges on the quiet 20%.

### Retraction

The frames between where an utterance begins and where the detector confirms it
reach the estimator labelled as background — exactly `MinOnsetFrames` of them.
Leaving them in dropped the floor's stability score from **1.000 to 0.246**, and
that score multiplies into every downstream confidence.

`NoiseAnalyzer.Retract` withdraws exactly those frames when an onset confirms.
The floor itself is deliberately *not* rewound: the clamp already bounds what
they did to it, and unwinding an exponential average exactly is not possible
without keeping the history this design exists to avoid.

Alternatives considered and rejected: a level band excluding loud frames would
also exclude the genuinely loud half of a bursty background and destroy
`NoiseTransient` detection; a trimmed or median statistic would be robust but
needs a sort on the frame path.

### Confidence

`coverage × stability`, where coverage is observation count over
`ConfidenceFrames` and stability is `1/(1+cv)` of the observed background.

`1/(1+cv)` rather than `1−cv` because the latter goes negative on a genuinely
chaotic background, and a negative confidence is meaningless.

## 2. Noise classes

| Class | Meaning |
|---|---|
| `unknown` | The floor has not converged |
| `quiet` | Background below `QuietFloor` |
| `stationary` | A fan, line hiss, an engine. Low background variation |
| `transient` | Traffic, a busy room, doors. High background variation |
| `clipping` | The input is being driven past full scale |
| `very_low` | Signal **and** background below usable level — a muted handset |

Ordered by severity when classifying: a clipping input is a clipping input
regardless of what its floor says, and calling it "stationary" because the level
happens to be steady would be technically true and operationally useless.

`very_low` tests the signal **and** the background. Testing the signal alone
would classify every inter-word gap as very low.

## 3. Audio quality

Four classes, from measurements and declared thresholds. Nothing here is a
subjective judgement dressed up as a number.

| Input | Degraded at | Unusable at |
|---|---|---|
| Clip ratio | 0.002 | 0.02 |
| Frame loss over the window | 0.02 | 0.10 |
| SNR estimate | below 12 dB | below 6 dB → poor |
| Signal level | — | below `MinSignalRMS` → poor |
| Crest factor | below 3 dB → flat | — |

`unknown` is a fifth value and **not** a fifth quality level. It means "not
measured yet", it ranks alongside `good` so it never triggers a degradation
alert, and reporting `good` before measuring would be a claim the engine has not
earned.

### The worst problem wins

A signal can be clipped **and** buried in noise **and** arriving in pieces.
Reporting all three leaves an operator with three things and no first one. The
order is what to fix first: destroyed samples cannot be recovered by anything
downstream, a stream arriving in pieces is a network problem, and a poor
signal-to-noise ratio is the caller's environment.

### Hysteresis

A new class must persist for `HysteresisFrames` (5) before adoption. A signal
sitting exactly on a boundary would otherwise alternate between `good` and
`degraded` on successive frames — a solid block of colour on a dashboard and a
page every few seconds in an alerting rule.

Applied in **both** directions, including recovery. An asymmetry — bad news
immediately, good news slowly — was considered and rejected: it makes the
reported class depend on which direction it was approached from, so two
identical lines report differently based on their history and nobody can
reproduce a complaint.

Measured: a signal alternating every frame between good and poor produces
**zero** adopted class changes.

## 4. Frame continuity

Phase 11B owns the jitter buffer, the reordering window and the gap fill. This
engine **consumes** what 11B publishes and says what it means. §13 forbids
duplicating it, and two jitter buffers in series is worse than one: the second
re-orders what the first already ordered, and the two disagree about "late".

| Fault | From |
|---|---|
| `missing_sequence` | A gap in `Frame.Sequence` |
| `duplicate` | A sequence already seen |
| `out_of_order` | A sequence below the highest seen |
| `timestamp_jump` | Media time leapt forward past `MaxTimestampJump` |
| `timestamp_reverse` | Media time ran backwards while sequence went forward |
| `synthesised` | `FlagSilence` — 11B covered a gap |
| `starvation` | From `media.PipelineResult`; no frame arrives to observe |

### Sequence is checked before timestamp, and the order is load-bearing

A frame late by sequence *necessarily* carries an old timestamp — that is what
being late means. Checking timestamps first reports every duplicate and every
reordering as a backwards media clock, which is a different and more alarming
fault, and hides the one an operator can act on.

So `timestamp_reverse` means what it should: sequence says forward, the media
clock says backward. A genuine upstream bug rather than ordinary reordering.
This was a real defect, found by test — see
[ENGINEERING_AUDIT.md](ENGINEERING_AUDIT.md) D-1.

### Duplicate detection is bounded at 64 frames

A 64-bit sliding window below the highest sequence seen. O(1) in time and space,
and 64 frames is 1.3 seconds at the default cadence — far beyond any reordering
Phase 11B would let through.

A duplicate older than that is reported as `out_of_order` instead. That is the
honest answer: at that distance the two are indistinguishable without the
unbounded memory this deliberately does not keep.
