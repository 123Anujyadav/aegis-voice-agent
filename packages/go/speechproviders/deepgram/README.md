# Deepgram streaming speech-to-text adapter

`packages/go/speechproviders/deepgram` adapts the frozen `speech.STTProvider`
port to Deepgram's streaming API (`wss://api.deepgram.com/v1/listen`).

## Status: not activated

- **ADR-0005 (Accepted):** Deepgram Nova-3 is the **secondary** recogniser and
  the failover target. Google STT v2 in `asia-south1` stays the primary.
- **ADR-0034 (approved by the owner 2026-11-12):** admits one WebSocket
  library, `github.com/coder/websocket` (ISC), contained to this module. No
  other module requires it.
- **Cross-border:** Deepgram has no Indian processing region. Every use sends
  caller audio abroad, so it is **consent-gated under ADR-0012**, and the
  routing decision must be audited (ADR-0005 §10).
- **Activation needs:** the speech spending category (deferred, OD-17), the
  DPDP review (E9), and ADR-0012's consent conditions. No service builds this
  provider, and no key file is mounted anywhere.

## What it does

- `New(cfg)` validates the configuration and reads the key file. It contacts
  nothing.
- `OpenSTT` accepts only mono PCM16 at 8 or 16 kHz, in a language declared in
  `Config.Languages`, then opens the WebSocket with:
  - `model` (default `nova-3`), `language` (the BCP-47 tag, unchanged),
    `encoding=linear16`, `sample_rate`, `channels=1`;
  - `interim_results=true`, `punctuate=true`;
  - `endpointing=false`: **endpointing is ours** (ADR-0005), so Deepgram's
    `speech_final` and `UtteranceEnd` are ignored.
- `Write` queues a copy of each frame; a sender goroutine writes them as binary
  messages.
- `CloseSend` sends `{"type":"CloseStream"}` after the queued audio. Results
  keep arriving until Deepgram closes the connection.
- `Results` yields interim and final segments, mapped from `Results`
  messages: first alternative's transcript (empty ones skipped), confidence,
  `is_final`, `start` and `duration` as offsets. `Metadata`, `SpeechStarted`
  and `UtteranceEnd` are ignored.
- `Err` reports the failure that ended the stream; `Close` returns it.

## Security properties

- **The key** comes from an absolute-path mounted file holding a single token,
  never an environment variable. It is sent only as `Authorization: Token …`
  in the upgrade request.
- **Host pinning:** only `wss://api.deepgram.com` (port 443), or a loopback
  host (`wss`, or `ws` for tests). Other hosts, `http`/`https`/`ws` to
  Deepgram, user info, paths, queries and fragments are refused.
- **No redirects:** a redirected upgrade fails, and the target receives nothing.
- **Nothing is quoted:** audio and transcripts are personal data. No error
  quotes the key, a response body, a close reason or a provider message.
  Failures are classified by HTTP status or close code only.
- **Bounded:** `Write` never blocks (`speech.ErrBackpressure` when
  `MaxPendingFrames` is full), frames are at most one second, provider messages
  are bounded by `MaxMessageBytes`, and the upgrade by `OpenTimeout`.
- **Failures are never silence:** a non-normal close, a normal close before the
  end of audio, a malformed or unknown message, a lost connection, or no
  message for `ResultTimeout` after `CloseSend` is recorded in `Err`. Results
  then closes, and `Close` returns the failure. A caller's `Close` is not a
  failure.

## Tests

The tests run against a local fake Deepgram: an `httptest` TLS server on
loopback, upgraded with `coder/websocket`'s server side. They never contact
Deepgram and use no real credential.

```sh
cd packages/go/speechproviders/deepgram
go test -count=1 ./...
go test -race -count=1 ./...        # as CI runs it
GOWORK=off go build ./... && GOWORK=off go vet ./...
golangci-lint run ./...
```

They cover:

- `providerconformance.RunSTT`;
- the request shape and the result mapping;
- every failure path, including timeouts, cancellation and backpressure;
- the key-file, host-pinning and redirect rules;
- goroutine leaks.
