# Phase 16 T11 — Real Speech Fixture & Transcript Evidence

**Status: ACHIEVED for the words. One field — `ASRConfidence` — remains
BLOCKED by a contract mismatch that must not be papered over.**

| Link in the chain | Status |
|---|---|
| committed speech fixture → real whispercli subprocess | **PROVEN** |
| subprocess → real transcript | **PROVEN** — exact, stable over 10 runs |
| transcript → generation / response path | **PROVEN** |
| real transcript → real classifier → `request_transfer` | **PROVEN** — joined by data flow, not a literal |
| a real *turn* carrying its own confidence → intent | **BLOCKED** — see §4 |

The distinction in the last two rows is the whole remaining gap. The real
recogniser's real output resolves to the real intent through the real
classifier. What a real turn cannot yet do is *carry its own confidence* across
that seam, because the value the provider honestly reports is outside the domain
the frozen field documents.

`-race NOT RUN LOCALLY`. No frozen module changed. No lexicon or threshold
changed. No API key, network or model download.

---

## 1. The fixture

`services/go/voice/cmd/server/testdata/speech_transfer_16k_mono_pcm16.raw` —
2.27 s of headerless 16 kHz mono PCM16 saying **"Transfer me to Rajesh"**, the
phrase this repository already uses throughout its dialogue fixtures.

Produced **once, by hand**, with Windows SAPI writing directly at 16 kHz mono —
so **no ffmpeg and no resampling** — and the RIFF `data` chunk extracted
verbatim, so no `LIST`/`INFO` metadata exists in the file. Verified: 12.4 %
ASCII-printable bytes, no text runs, entirely synthetic, nobody's voice, no
personal data.

**Committed rather than generated** so the test depends on nothing but these
bytes. SAPI exists only on Windows; a fixture regenerated per run would make the
asserted transcript a property of whichever speech engine happened to be
installed.

## 2. The measured transcript

Three consecutive CLI runs against this exact audio, and every pipeline run
since:

```
 Transfer me to Rajesh.
```

Asserted as `transfer me to rajesh.` — normalised **only** for surrounding
whitespace and case. The model's full stop is asserted, not trimmed: trimming it
would be widening the assertion until it passed.

## 3. Two more real defects, both found by wiring real speech

### D4 — the routing language was sent to the tool

The adapter passed `speech.Language` straight into `--language`. This platform
routes on BCP-47 (`en-IN`, `hi-IN`) because region is load-bearing for Indian
telephony; whisper's `--language` is an argparse **choice** over ISO-639-1 codes,
and `en-IN` is not one:

```
whisper: error: argument --language: invalid choice: 'en-IN'
```

argparse prints usage, exits 2, and the stream closes empty — **indistinguishable
from a caller who said nothing.** Every recognition attempt failed that way and
looked exactly like silence.

Not caught earlier because the adapter's own tests configure `"en"`, a value
already valid for the tool. Wiring it to the real router, which speaks the
platform's vocabulary, is what exposed it.

**Fix:** `toolLanguage()` reduces the tag to its primary subtag for the argv
only. The capability the provider *declares* is unchanged, so routing still sees
the full tag.

### D5 — an honest provider is classified as noise *(BLOCKER)*

See §4. Not fixed, because every available repair is a policy decision this task
is forbidden to make.

## 4. The blocker: an out-of-domain value at a non-frozen seam

**The diagnosis has been sharpened since the first draft of this document, and
the correction matters for who owns the fix.**

```
conversation/intent.go:127   // ASRConfidence is the recogniser's confidence, 0..1.
speech.ConfidenceUnknown = -1.0        // a SENTINEL, not a measurement
voice/pipeline.go:1074       ASRConfidence: final.Confidence,   // forwards -1.0
```

The frozen field documents its domain as **0..1**. `ConfidenceUnknown` is
`-1.0`, which is **outside that domain**. So this is not the frozen engine
applying a threshold badly — the engine is behaving correctly on a value it
should never have been handed. **The defect is in `packages/go/voice`, which is
NOT frozen: the pipeline leaks one package's sentinel into another package's
numeric domain.**

That reframing does not make the fix automatic, because the frozen contract has
no in-domain way to say "not reported" — see the decision required below. But it
does mean the repair belongs in the pipeline, and that no frozen amendment is
required to *start*.

```
conversation: if u.ASRConfidence < MinASRConfidence (0.40) → IntentNoise
```

`ConfidenceUnknown`'s own documentation says exactly why it exists:

> *"a provider reporting zero confidence and a provider reporting nothing at all
> are different situations, and a router that conflated them would treat silence
> as certainty of failure"* — `speech/transcript.go:107`

The pipeline forwards that sentinel straight into the frozen gate. `-1.0 < 0.40`,
so **every utterance from a provider that honestly declines to invent a
confidence is classified as noise.** openai-whisper is exactly such a provider —
the adapter reports `ConfidenceUnknown` deliberately, and
`TestAdapter_ReportsNoFabricatedConfidence` protects that.

**Measured:**

```
transcript " Transfer me to Rajesh."  ASRConfidence=-1
  → intent="unknown"  action=clarify  reason="noise_repeat"
```

The behaviour is not catastrophic — the agent asks the caller to repeat — but a
real recogniser can never resolve an intent, and it will ask forever.

### Why it was not fixed here

The *location* of the defect is now clear (the non-frozen pipeline). The
*value* to put there is not, and every candidate is a policy decision outside
this task's scope:

| Option | Why not |
|---|---|
| lower `MinASRConfidence` | a threshold change, explicitly forbidden; and it would disable the gate for providers that *do* report confidence |
| map the sentinel to a passing value | fabricating the confidence the provider refused to fabricate — and it silently disables an acoustic safety gate for every whisper-class provider |
| refuse to forward the utterance at all | in-domain and honest, but a real recogniser's turns would then never reach the conversation, which is worse than the current behaviour |
| express "unknown" properly | needs a field the frozen `conversation.Utterance` does not have |

Note the asymmetry the second row hides: treating "I do not measure confidence"
as "certainly noise" is **precisely the conflation `ConfidenceUnknown`'s own
documentation exists to prevent**, committed one layer above it. That is an
argument that the current behaviour is wrong; it is not, by itself, an argument
for any particular replacement number.

So it is **measured, pinned in an executable test, and reported**:
`TestT11_RealTranscriptIsGatedAsNoiseByUnknownConfidence`, which fails loudly if
the semantics ever change.

### The decision required

> **Does an utterance from a recogniser that reports no confidence pass the noise
> gate, and how is "no confidence reported" expressed to the frozen engine?**

This is a product/architecture decision, and the shape of the fix (pipeline-only
vs. a frozen amendment) follows from the answer. What T11 has established is
that the question is answerable **without** a frozen amendment if the answer is
"pick an in-domain value in the pipeline" — the seam that needs to change is
`voice/pipeline.go:1074`, which is not frozen. A frozen amendment is required
only if the answer is "the engine must be able to tell unknown from low".

## 5. The chain that *is* proven

Three tests. The third exists because the first two were **joined by a string a
human copied out of a log**, which is not a chain:

1. **fixture → real subprocess → exact transcript → generation** —
   `TestT11_RealSpeechProducesTheExpectedTranscript`. The router's selection and
   the descriptor's engine are asserted *before* any audio is fed, so the test
   cannot pass on a fake.
2. **that transcript → real classifier → `request_transfer`** —
   `TestT11_TheIntentIsTheClassifiersAndNotAFallback`, using the raw transcript
   including its leading space, capital and full stop. The classifier's own
   normalisation handles all three; pre-normalising would have tested the test.
   Its transcript is a **literal**, and that is its limitation.
3. **the real subprocess's own output → real classifier → `request_transfer`** —
   `TestT11_TheRealTranscriptItselfResolvesToTheRealIntent`. The string handed
   to the classifier is the one captured from *this run's* generation request,
   downstream of the pipeline's consumption point. **No literal stands between
   the recogniser and the intent.** If the recogniser's output ever drifted,
   test 1 would fail and test 2 would keep passing against a stale constant;
   test 3 is what makes that impossible.

   The one substituted value is `ASRConfidence`, and it is exactly and only the
   value §4 is about. It is stated in the test's own doc comment rather than
   hidden.

## 6. Mutations — 8 executed, **8 CAUGHT**

| | Mutation | Result |
|---|---|---|
| M1 | FakeSTTProvider scripted with the real transcript | **CAUGHT** — after strengthening; see below |
| M2 | real transcript discarded (`Content: ""`) | **CAUGHT** — tests 1 and 3 |
| M3 | transcript replaced with `"please hold the line"` | **CAUGHT** — test 3 catches it *as a wrong intent* |
| M4 | classifier hard-codes `request_transfer` for every utterance | **CAUGHT** — by the existing T4 discrimination suite, **not** by the T11 tests alone |
| M5 | final STT result suppressed | **CAUGHT** — tests 1 and 3 |
| M6 | completion event suppressed | **CAUGHT** — test 1 |
| M7 | D4 reverted: routing tag sent to the tool | **CAUGHT** — empty transcript, immediately |
| M8 | transcript withheld from the conversation | **INERT while §4 stands — not credited** |

**M1 genuinely escaped, and was caught only after the test was strengthened.**
Test 3 as first written asserted the transcript and the intent but *not the
provider*. Registering the frozen `FakeSTTProvider` and scripting it with
`"Transfer me to Rajesh."` satisfied every assertion — **in 0.21 s instead of
the ~5 s a real inference takes.** A fake told the answer was indistinguishable
from a real recogniser. Fixed by asserting the router's selection and the
descriptor's engine before any audio is fed, exactly as test 1 does; the probe
then failed with `the router selected "t7-stt"`. This is recorded because it is
the precise failure mode this task was written to prevent, and it was present in
the first draft.

**M3 escaped an earlier draft too.** The test asserted the transcript where the
*observer* is told about it — upstream of where the pipeline *consumes* it, so
corruption in between was invisible. Both tests now assert in the **generation
request**, downstream of consumption.

**M4 is credited to the suite, not to these tests.** Test 3 expects
`request_transfer`, so a classifier that returns `request_transfer` for
everything passes it. What fails is `TestT4_UnknownLowConfidenceAndAmbiguity
StayDistinct` and three sibling T4 tests, which is the existing non-vacuity
machinery this task was told to reuse rather than duplicate. Stated plainly so
the coverage is not over-claimed.

**M8 is INERT and is not credited.** Withholding the transcript from the
conversation changes nothing observable *because §4 already gates the classifier
off* on the live path — noise either way. It becomes catchable the moment the
blocker is resolved, and should be re-run then.

## 7. Negative cases

Non-speech through the same real path → **zero** segments, no intent, no
completion, session healthy. Missing fixture → a test failure, not a skip.
Unavailable recogniser → the existing typed refusal with its remedy.

## 7a. Determinism — measured, not assumed

`-count=10 -shuffle=on` over the three real-inference tests: **10/10 identical**
on every semantic field.

| Field | Value, all 10 runs |
|---|---|
| normalised transcript | `transfer me to rajesh.` |
| captured transcript (test 3) | `Transfer me to Rajesh.` |
| intent / action / reason | `request_transfer` / `respond` / `intent_accepted` |
| completions / abandonments / interruptions | 1 / 0 / 0 |
| final floor / state | caller / not terminal |

Wall time varied 4.5–5.7 s and is excluded, along with PIDs, temp paths and log
order. No retries, no sleeps, no polling, and nothing was rerun until green.

## 7b. Security

Fixture: 72 640 bytes, headerless (no `RIFF`/`WAVE`/`LIST`/`INFO`/`ID3`
anywhere), 12.4 % printable bytes whose longest run is 11 characters of noise
such as `D./f*R7nGDH`; the strings `Rajesh` and `transfer` do not occur in it.
Entirely synthetic, no personal data.

**"No transcript in logs" is now executable rather than asserted.** Test 3 fails
if `rajesh` appears anywhere in the run's captured log sink — checked on the one
path where real recognised text crosses the adapter, pipeline, conversation and
generator. Prose cannot notice a leak introduced later; this can.

## 8. Boundary

**No accuracy claim.** One fixture under an existing contract is not a
measurement of recognition quality. **No telephony claim.** **No production
readiness claim** — whispercli remains development-class and `ModeProduction`
still refuses it.
