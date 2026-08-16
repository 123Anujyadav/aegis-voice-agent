# Sequence Diagrams

**Phase 11A** · `packages/go/telephony`

Six sequences. Each is drawn from the code path a test exercises, and the test
is named.

---

## 1. Inbound screened call, accepted

*`TestLifecycle_InboundHappyPath`*

```mermaid
sequenceDiagram
    participant C as Carrier
    participant P as Provider Adapter
    participant CO as CallCoordinator
    participant S as CallScheduler
    participant R as CallRegistry
    participant L as CallLifecycle
    participant E as Publisher (Kafka)

    C->>P: call offered
    P->>CO: Begin(CallContext{inbound})
    CO->>CO: Validate()
    CO->>S: Admit(provider)
    S-->>CO: admitted (live/capacity)
    CO->>R: Create() → session at idle
    CO->>L: Incoming()
    L->>L: idle → incoming
    L->>E: CallCreated
    CO-->>P: *CallSession

    P->>L: Ring()
    L->>L: incoming → ringing
    L->>E: CallRinging

    P->>L: Screen()
    L->>L: ringing → screening
    L->>E: CallScreening

    Note over L: screening decision made upstream (10B/10E)

    P->>L: Accept("screening_passed")
    L->>P: provider.Answer() [ProviderTimeout]
    P->>C: answer
    C-->>P: ok
    P-->>L: ok
    L->>L: screening → accepted
    L->>E: CallAnswered

    P->>L: Connect()
    L->>L: accepted → connected
    Note over L: AnsweredAt set — talk clock starts
    L->>E: CallConnected
```

---

## 2. Screening rejects the call

*`TestLifecycle_ScreeningRejection`*

```mermaid
sequenceDiagram
    participant P as Provider Adapter
    participant L as CallLifecycle
    participant CO as CallCoordinator
    participant S as CallScheduler
    participant E as Publisher

    P->>L: Reject("screening_rejected")
    L->>L: checkReasonCode()
    L->>P: provider.Reject(reason)
    Note over L,P: a provider failure here is LOGGED, not fatal —<br/>the platform has already decided
    L->>L: screening → rejected
    L->>E: CallRejected

    Note over L: rejected is NOT terminal —<br/>the call still holds a channel

    P->>CO: End("teardown_complete")
    CO->>L: Disconnect()
    L->>L: rejected → ended
    L->>E: CallEnded
    CO->>S: Release(provider)
```

---

## 3. Broker outage — the call still completes

*`TestFailure_PublisherOutageDoesNotStopCalls`*

```mermaid
sequenceDiagram
    participant P as Provider Adapter
    participant L as CallLifecycle
    participant F as FSM
    participant E as Publisher (DOWN)
    participant M as RuntimeMetrics

    P->>L: Connect()
    L->>F: To(connected)
    F-->>L: ok
    Note over F: THE FSM MOVES FIRST —<br/>nothing after can undo it
    L->>M: Transitions++
    L->>E: publish(CallConnected)
    E-->>L: error: kafka unreachable
    L->>M: EventsDropped++
    Note over L: warn logged, transition STANDS
    L-->>P: nil (success)

    P->>L: Disconnect("caller_hung_up")
    L->>F: To(ended)
    F-->>L: ok
    Note over L,E: the call ENDS despite the broker being down.<br/>The alternative would turn an observability outage<br/>into a phone-system outage.
```

---

## 4. Provider hangs — the timeout binds

*`TestFailure_ProviderTimeoutIsBounded`*

```mermaid
sequenceDiagram
    participant L as CallLifecycle
    participant P as Provider (hung)
    participant F as FSM
    participant M as RuntimeMetrics

    L->>L: ctx, cancel := WithTimeout(ProviderTimeout)
    L->>P: Answer(pctx)
    Note over P: carrier SDK does not return
    P--xL: ctx deadline exceeded
    L->>F: To(failed)
    L->>M: ProviderErrors{answer}++
    L-->>L: error returned to caller

    Note over L,P: a provider that ignores cancellation holds<br/>one goroutine per call. The deadline is the<br/>last line of defence.
```

---

## 5. Crash recovery

*`TestRecovery_ConcludesCallsThatAreNoLongerLive`, `TestRecovery_ResumesCallsThatAreStillLive`*

```mermaid
sequenceDiagram
    participant OS as Orchestrator
    participant RT as TelephonyRuntime (new process)
    participant ST as SessionStore
    participant R as CallRegistry
    participant LV as LivenessCheck
    participant E as Publisher

    Note over OS,RT: previous process crashed with live calls

    OS->>RT: Start()
    RT->>ST: LoadAll()
    ST-->>RT: snapshots (sorted oldest first — deterministic)

    loop each snapshot
        RT->>RT: Restore() → StateRecovery
        Note over RT: NOT the snapshotted state.<br/>The call existed; whether it still does is unknown.
        RT->>R: Register()
        RT->>E: RecoveryStarted
        RT->>RT: Scheduler.Admit() — a resumed call holds a slot
        RT->>LV: is this call still up?

        alt still live
            LV-->>RT: true
            RT->>RT: recovery → connected
            RT->>E: RecoveryResumed
            RT->>ST: Delete(snapshot)
        else no longer live (the DEFAULT)
            LV-->>RT: false
            RT->>RT: recovery → ended
            RT->>E: RecoveryAbandoned
            RT->>ST: Delete(snapshot)
        end
    end

    RT->>RT: state = running — admission opens ONLY NOW
    RT->>RT: start sweeper, snapshot loop
```

Admission opens after recovery so a recovered call cannot lose its capacity slot
to a new call that arrived first.

---

## 6. Graceful shutdown

*`TestShutdown_SnapshotsLiveCallsAndIsIdempotent`, `TestShutdown_TerminatesWithLiveCalls`*

```mermaid
sequenceDiagram
    participant OS as Orchestrator (SIGTERM)
    participant RT as TelephonyRuntime
    participant CO as CallCoordinator
    participant R as CallRegistry
    participant ST as SessionStore

    OS->>RT: Stop()
    RT->>RT: state = draining
    Note over RT,CO: new calls refused IMMEDIATELY —<br/>ErrRuntimeStopped

    loop until drained or DrainTimeout (REAL time)
        RT->>R: Len()
        alt zero
            R-->>RT: 0 → done
        else still live
            Note over RT: poll interval scaled from DrainTimeout
        end
    end

    RT->>R: Each() → snapshots of non-terminal calls
    RT->>ST: SaveBatch(snapshots)
    Note over RT,ST: after the drain, so only what<br/>genuinely could not finish is captured

    RT->>RT: close(stop); wg.Wait()
    RT->>RT: state = stopped
    RT-->>OS: abandoned count
```

The drain budget is **real** time. Measuring it against the injected clock while
polling with a real ticker produced a shutdown that never terminated — see
ENGINEERING_AUDIT F1.

---

## 7. Late carrier callback

*`TestDispatcher_ClassifiesLateAndDuplicateSignals`*

```mermaid
sequenceDiagram
    participant C as Carrier
    participant P as Provider Adapter
    participant D as CallDispatcher
    participant R as CallRegistry
    participant M as RuntimeMetrics

    C->>P: "ringing" (late, call already connected)
    P->>D: Dispatch(id, ringing)
    D->>R: Get(id)
    R-->>D: session (connected)
    D->>D: CanTransition(connected, ringing)? no
    D->>M: InvalidTransitions++
    D-->>P: SignalNotApplicable, nil error

    C->>P: "ended" (for a call already removed)
    P->>D: Dispatch(unknown, ended)
    D->>R: Get() → ErrCallNotFound
    D-->>P: SignalUnknownCall, nil error

    Note over D,P: neither is an ERROR. A carrier that sends<br/>callbacks out of order, twice, and after teardown<br/>is a carrier, not a bug. Returning errors would<br/>fill the logs and hide a real fault.
```

---

## 8. Related

- [CALL_LIFECYCLE.md](CALL_LIFECYCLE.md)
- [TELEPHONY_ARCHITECTURE.md](TELEPHONY_ARCHITECTURE.md)
