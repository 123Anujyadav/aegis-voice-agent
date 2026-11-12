# speechproviders/google: Google Cloud Speech-to-Text v2 streaming adapter

Implements the frozen `speech.STTProvider` port (`packages/go/speech`) over
Google Cloud Speech-to-Text v2 `StreamingRecognize` (gRPC).

## Status: built and tested, **not activated**

- **ADR-0005 (Accepted):** Google STT v2 is the primary recogniser, with the
  `telephony` model, pinned to **`asia-south1`** so that call audio is processed
  in India. The adapter refuses any other location, including `global`.
- **ADR-0034 (approved by the owner on 2026-11-12):** adds gRPC and Google's
  generated Speech v2 client, contained to this module. No other module
  requires them.
- **No service builds this provider.** Activation needs:
  - the speech spending category (deferred with **OD-17**);
  - consent (**OD-4**);
  - the processor terms (**E9**);
  - **Workload Identity Federation** from EKS
    (`docs/phase19/PLAY-INTEGRITY-DECISION-MEMO.md` §7). **Never a
    service-account key.**

## What it sends

The first message carries the recogniser
`projects/{Project}/locations/asia-south1/recognizers/_` and the whole
configuration:
- explicit decoding: `LINEAR16`, the stream's rate (8000 or 16000 Hz), one
  channel;
- the stream's language code and the `telephony` model;
- automatic punctuation;
- interim results.

**Vendor endpointing is off (ADR-0005 §5).** Voice-activity events are not
requested. The v2 knob `StreamingRecognitionFeatures.endpointing_sensitivity`
exists and is left unset. A final result only marks a segment that will not be
revised; it never ends a turn.

Every later message carries audio only: one request per frame, split at
15000 bytes. The v2 API documents the audio field as "Maximum size for this
field is 15 KB per request". A 20 ms frame is 320 bytes at 8 kHz, 640 at
16 kHz. Google also documents a per-stream audio-length limit; a stream that
exceeds it ends with a status, which is recorded as a failure. `Capabilities`
does not declare a limit, because the exact value has not been verified
against the live API.

## Behaviour

| Port call | Behaviour |
|---|---|
| `New` | Validates only. Reads no credentials, contacts nothing. |
| `OpenSTT` | Refuses an undeclared language (`ErrUnsupportedLanguage`), and any format but mono PCM16 at 8 or 16 kHz (`ErrInvalidAudio`). The first call creates the client (below), then opens the stream. |
| `Write` | **Never blocks.** It copies the payload into a bounded queue (`MaxPendingFrames`); a full queue is `ErrBackpressure`. A frame in another format is `ErrInvalidAudio`. After `CloseSend` or `Close` it is `ErrSpeechSessionClosed`. |
| `CloseSend` | Queued audio is sent, then the gRPC stream is half-closed. Results keep arriving until Google ends the stream. Silence for `ResultTimeout` is `ErrProviderTimeout`. |
| `Results` | One segment per non-empty top alternative: interim and final, confidence (Google's 0 means unknown), end offset, and the declared language. Closed exactly once. |
| `Err` / `Close` | `Err` returns the recorded failure, so a crash never looks like silence. `Close` is idempotent and prompt, and returns the same failure. A caller's `Close` or context cancellation is **not** a failure. |
| `Provider.Close` | Releases the gRPC connection (`io.Closer`). |

**gRPC codes are mapped by code alone:**

| Code | Sentinel |
|---|---|
| `Unauthenticated`, `PermissionDenied` | `ErrProviderUnavailable` (the port has no credentials sentinel) |
| `ResourceExhausted` | `ErrProviderRateLimited` |
| `DeadlineExceeded` | `ErrProviderTimeout` |
| `InvalidArgument`, `NotFound`, `FailedPrecondition`, `OutOfRange`, `Unimplemented`, `AlreadyExists` | `ErrInternalFailure` |
| `Unavailable`, `Internal` and any other code | `ErrProviderUnavailable` |

## Security properties

- **No audio or transcript in logs or errors.**
  - Google's status message is never quoted, because it can echo what was said.
  - The client library's logger is replaced with a discarding one, so an
    environment variable cannot enable request logging.
  - The library's OpenTelemetry instrumentation is disabled.
- **Residency.** Location must be `asia-south1`. The recogniser path is built
  from validated parts (project ID or number, recogniser ID).
- **Endpoint pinning.** `host:port` with a `*.googleapis.com` host on port 443,
  or a loopback host for tests. Anything else is refused at `New`.
- **Credentials.**
  - With no `ClientOptions`, the client uses Google's default credentials chain.
    In production that must resolve to a Workload Identity Federation
    configuration.
  - The lookup happens at the first `OpenSTT`, not at `New`.
  - The gRPC connection is non-blocking: it connects on the first stream.
- **Bounded.** Audio by `MaxPendingFrames`, results by `MaxPendingResults`, the
  end of results by `ResultTimeout`, and the whole stream by `STTConfig.Timeout`
  when set.

## Tests

The tests run against an in-process fake `speechpb.SpeechServer` on
`google.golang.org/grpc/test/bufconn`. Every provider is built with
`option.WithGRPCConn` and `option.WithoutAuthentication`. **No test touches the
network or reads real credentials.** The one credentials test points
`GOOGLE_APPLICATION_CREDENTIALS` at a file that does not exist.

```sh
cd packages/go/speechproviders/google
go test -count=1 ./...
go test -race -count=1 ./...                         # CI (needs cgo)
GOWORK=off go build ./... && GOWORK=off go vet ./...
```

They cover:
- `providerconformance.RunSTT`;
- the request shape;
- interim and final mapping;
- every mapped status code, with no status text in the error;
- `ResultTimeout` and the stream timeout;
- backpressure, `Write` after `CloseSend` and `Close`, format mismatch, and an
  undeclared language;
- residency and endpoint pinning;
- lazy credentials;
- `Close` while the server blocks, and caller cancellation;
- goroutine leaks.

## Dependencies

`go 1.26.0`: the direct dependencies below all declare it, and CI's go1.26.11
satisfies it.

| Module | Version | Licence |
|---|---|---|
| `cloud.google.com/go/speech` | v1.38.1 | Apache-2.0 |
| `google.golang.org/grpc` | v1.85.0 | Apache-2.0 (`NOTICE.txt`) |
| `google.golang.org/api` | v0.310.0 | BSD-3-Clause |
| `google.golang.org/protobuf` | v1.36.12 | BSD-3-Clause |

The non-test build links 32 third-party modules. Each carries Apache-2.0,
BSD-3-Clause or MIT. `docs/hardening/DEPENDENCY_AUDIT.md` §14 lists them.
