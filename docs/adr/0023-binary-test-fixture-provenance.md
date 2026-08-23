# ADR-0023: Binary test fixture provenance

- **Status:** Accepted
- **Date:** 2026-08-22
- **Deciders:** Phase 16 T12 (§K/§L audit) — approved for T15
- **Consulted:** Phase 16 T10/T11 real-provider evidence work
- **Informed:** anyone adding a non-text file under a `testdata/` directory

---

## Context

Phase 16 needed to prove one specific chain end to end:

```
real speech audio -> real whisper subprocess -> real transcript -> real intent
```

A tone cannot stand in for speech. A tone can only demonstrate the *absence* of
hallucination; it can never show that a real transcript reaches the real
classifier. So the evidence requires audio of someone saying actual words.

Three facts about this repository shaped the decision, and all three were
established by source inspection during the T12 audit rather than assumed:

1. **There is no tracked binary artifact of any kind.** `git ls-files` returns no
   `.wav`, `.raw`, `.pcm` or similar anywhere. The only tracked `testdata/`
   entries are two `.golden` text files. A committed audio fixture is a new
   artifact class.
2. **No existing policy governs it.** `CONTRIBUTING.md` says nothing about
   fixtures, testdata or binaries. `docs/phase13/EVALUATION_FIXTURES.md` governs
   *text* fixtures and establishes the norm this ADR inherits — fixtures are
   **measured, never hoped for**, and a fixture pass rate is explicitly *not* an
   accuracy claim.
3. **An external speech toolchain is already committed precedent.**
   `packages/go/voice/e2e_test.go` shells out to PowerShell + Windows SAPI *and*
   `ffmpeg` at test runtime, skipping when either is absent, and
   `docs/superpowers/specs/2026-08-11-voice-provider-integration-design.md`
   lists ffmpeg as "available for fixture conversion".

The immediate forcing issue: the Phase 16 T11 tests were written against a
fixture that was never added to the index. `TestT11_AMissingFixtureIsATestFailure
NotASkip` calls `t.Fatalf` when the file is absent — deliberately, so the
evidence cannot silently degrade into a skip. On a fresh checkout that test, and
the real-speech suite around it, **fail rather than skip**. The tests assert the
fixture is committed; it is not. That inconsistency is what this ADR resolves.

## Decision Drivers

Most important first:

- **Evidence integrity.** The asserted transcript was measured against exact
  bytes. Anything that lets those bytes vary invalidates the assertion.
- **No external toolchain at test or build time.** A test that shells out to a
  speech engine passes or skips depending on what happens to be installed.
- **Reviewability.** A binary in a diff cannot be read. Whatever is committed
  must be describable and verifiable by other means.
- **Content safety.** Audio is personal data if it is anyone's actual voice.
- **Supply-chain minimalism**, the same argument `packages/go/conversation`'s
  `go.mod` makes for its dependency set.

## Considered Options

1. Commit the fixture and write this policy
2. Generate the fixture at test runtime with SAPI + ffmpeg
3. Synthesise it with the existing `audiointel.SignalGenerator`
4. Drop the real-speech evidence entirely

## Decision Outcome

**Chosen: Option 1 — commit the fixture, under the provenance requirements
below.**

Option 1 is the only one that satisfies *evidence integrity* and *no external
toolchain* simultaneously. It is also strictly more conservative than the
precedent already in the tree: the committed fixture invokes **zero** external
tools at test time, where `e2e_test.go` invokes two on every run.

### The policy

A non-text file may be committed under a `testdata/` directory when **all** of
the following hold. These are requirements, not guidance.

1. **A sibling `README.md` records its provenance**, covering: what it is; its
   exact format; how it was produced, including the tool and the platform; why
   it is committed rather than generated; and a content-safety statement.
2. **Integrity is pinned.** The README records the **SHA-256** and the exact
   **byte size**. A test asserting behaviour derived from the bytes must also
   assert the size, so that a silent substitution fails loudly.
3. **No external toolchain is required to run or build the tests.** Whatever
   produced the file may be unavailable — and usually will be, since it ran once
   on one machine.
4. **The content is synthetic.** No real person's voice, no personal data, no
   credential, no account or phone number. For audio this means a synthesiser,
   never a recording.
5. **It is as small as the evidence permits.**
6. **Regeneration is not casual.** Any assertion measured against the bytes must
   be re-measured, and the README and checksum updated in the same change.

Producing a fixture with SAPI, ffmpeg or any other local tool is permitted **as a
one-time authoring step**. It is not permitted as a test-time or build-time
dependency for new work; `e2e_test.go` predates this ADR and is unaffected.

**This ADR authorises the artifact class and the accompanying fixture. It does
not authorise recorded human speech, media of any size, or vendored binaries,
none of which were considered.**

### Consequences

**Positive**

- The real-speech evidence chain becomes reproducible on any checkout, with no
  speech engine installed, offline.
- The transcript assertion rests on fixed bytes rather than on whichever
  synthesiser a given machine happens to have.
- `TestT11_AMissingFixtureIsATestFailureNotASkip` becomes true rather than
  aspirational.

**Negative**

- A 72,640-byte binary enters the repository permanently, and git stores every
  future revision of it in full. Reviewers cannot read it in a diff and must
  rely on the README, the checksum, and the automated content-safety checks.
- It sets a precedent that will be cited for larger media. The size and
  synthetic-content requirements above exist to bound that, and are the parts
  most likely to be argued with later.

**Neutral**

- The fixture's origin (Windows SAPI) is now a historical fact recorded in a
  README rather than a live dependency of anything.

### Confidence

**Medium.** The decision is right for one small synthetic fixture and is
well-supported by the precedent already in the tree. It is *not* a considered
position on binary artifacts generally, and the requirements above are drawn
from a single example — which is exactly the circumstance in which a policy is
most likely to prove under-specified.

### Revisit Trigger

Reconsider when any of these first occurs:

- a proposed `testdata/` binary exceeds **256 KB**, or the total tracked binary
  size under `testdata/` exceeds **1 MB**;
- a fixture is proposed that contains a real person's voice or any non-synthetic
  content;
- a second fixture needs the same one-time authoring toolchain, which would make
  the "one-time authoring step" carve-out a de facto dependency worth declaring
  properly.

## Options in Detail

### Option 2: generate at test runtime with SAPI + ffmpeg

Follows `e2e_test.go`'s existing pattern exactly.

- **Good:** nothing binary enters the repository; precedent already exists.
- **Bad:** the asserted transcript becomes a property of whichever speech engine
  is installed, so the assertion is only meaningful on machines that have the
  same one. SAPI is Windows-only, so the evidence would skip on CI and on every
  non-Windows developer machine — the evidence would exist almost nowhere.

### Option 3: synthesise with `audiointel.SignalGenerator`

The repository's sanctioned, seeded, dependency-free fixture generator.

- **Good:** already approved, fully deterministic, no new artifact class.
- **Bad:** **it cannot produce intelligible words.** `fixtures.go:182-245`
  generates tones, noise and speech-*like* amplitude envelopes. It is the right
  tool for VAD and endpointing fixtures and cannot express this evidence at all.
  This is a capability limit, not a preference.

### Option 4: drop the real-speech evidence

- **Good:** no artifact, no policy, no cost.
- **Bad:** abandons the one chain Phase 16 T10–T14 exists to prove, and would
  mean deleting passing tests that already demonstrate a real subprocess
  producing a real transcript that resolves to a real intent.

## References

- `services/go/voice/cmd/server/testdata/README.md` — the fixture's provenance
  record, and the worked example of requirements 1–2
- `docs/phase13/EVALUATION_FIXTURES.md` — the "measured, not hoped for" norm and
  the explicit no-accuracy-claim rule this ADR inherits
- `docs/phase16/REAL_SPEECH_EVIDENCE.md` — what the fixture is evidence *for*
- `packages/go/voice/e2e_test.go` — the pre-existing SAPI + ffmpeg runtime
  precedent this ADR deliberately does not extend
- `packages/go/audiointel/fixtures.go` — the sanctioned generator, and why it
  cannot cover this case
