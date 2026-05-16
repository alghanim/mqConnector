# Request flow — broker-in to broker-out

This is the message-processing flow. For HTTP / admin API flow, see [API_FLOW.md](API_FLOW.md).

---

## The 30-second version

```
Source broker  →  source.ReceiveMessage(ctx)
                          │
                          ▼
                   RunStages(message)
                   │
                   ├── validate    (JSON Schema / XSD / Protobuf)
                   ├── filter      (drop fields by JSONPath / XPath)
                   ├── transform   (rename / mask / move / set / delete)
                   ├── translate   (JSON ↔ XML ↔ Protobuf)
                   ├── route       (compute destination list)
                   └── script      (sandboxed JS)
                          │
                          ▼
              ┌───────────┴───────────┐
              │ default destination   │  ◀── one publish
              │   or                  │
              │ N routed destinations │  ◀── fan-out, one publish per dest
              └───────────┬───────────┘
                          │
                          ▼
                    dest.Send(payload)
                          │
                          ▼
                    metrics.RecordSuccess
                          │
                  on any error in the chain:
                          │
                          ▼
                  DLQ.Push(entry) + metrics.RecordFailure
                  (continue to next message)
```

Everything happens inside one goroutine per pipeline, scheduled by `internal/pipeline.Executor.Run`. The MQ pool gives that goroutine cached connections by id.

---

## Boot — how pipelines start running

1. `cmd/mqconnector/main.go` constructs the `Manager` in `internal/pipeline/manager.go` and calls `Reload(ctx)`.
2. `Reload` reads every enabled pipeline row from storage, builds an `Executor` per row, and launches it on its own goroutine. The manager keeps a `map[pipelineID]cancel` so a subsequent `Reload` can diff and start / stop precisely the executors that changed.
3. Each executor reads its source + destination Connection rows, resolves them to `mq.Config` (via `internal/mqcfg` — this is the decryption boundary for stored broker passwords), and starts the receive loop.

```go
// internal/pipeline/manager.go (simplified)
func (m *Manager) Reload(ctx context.Context) (int, error) {
    rows, _ := m.store.Pipelines.ListEnabled(ctx)
    want := byID(rows)
    have := m.running

    for id, e := range have {
        if _, keep := want[id]; !keep || changed(e.spec, want[id]) {
            e.cancel()
        }
    }
    for id, row := range want {
        if _, alive := have[id]; alive { continue }
        executor := m.buildExecutor(row)
        ctx, cancel := context.WithCancel(ctx)
        m.running[id] = exec{cancel, row}
        go executor.Run(ctx)
    }
    return len(want), nil
}
```

Hot reload (`POST /api/v1/reload`) calls the same path. There is no separate "restart pipeline" — Reload is the only mutator.

---

## Receive — one message at a time

```go
// internal/pipeline/executor.go
func (e *Executor) Run(ctx context.Context) error {
    for {
        if err := ctx.Err(); err != nil { return nil }

        if err := e.processOne(ctx, logger); err != nil {
            // Infrastructure-level error (source connection lost).
            // Back off briefly to avoid a hot loop, then retry.
            e.Metrics.SetStatus(e.Pipeline.ID, "error", err.Error())
            select {
            case <-ctx.Done():
                return nil
            case <-time.After(2 * time.Second):
            }
        }
    }
}
```

`processOne`:

1. **Get a source connection from the pool** — `e.Pool.Get(ctx, "source-<id>", cfg)`. The pool caches by id; on a cache hit it returns the existing TCP/channel/session. On a miss it dials. Health checks run on a background ticker; broken connections are evicted so the next Get re-dials.

2. **Reset the pipeline's status to `connected`** — even before the first message arrives, having an alive source means we should clear any prior `error` status. Otherwise the UI sticks at "error" forever after a broker bounce.

3. **`source.ReceiveMessage(ctx)`** — this is the blocking call. Each connector implements it idiomatically:
   - RabbitMQ: reads from a delivery channel; auto-acks before returning.
   - Kafka: pulls from a consumer-group cursor; commits offset before returning.
   - MQTT: reads from the connector's internal 256-slot buffered channel (the connector bridges paho's callback-style API to a pull-style channel; drops on full to bound memory).
   - NATS / JetStream: PullSubscribe + Ack on JetStream; channel-buffered drain on core NATS.
   - AMQP 1.0: receiver with credit 16 (matching the default RabbitMQ prefetch).
   - IBM MQ: MQGET with infinite wait + ctx cancellation via a watchdog goroutine.

4. **Start a trace span** — `tracing.Start(ctx, logger, "pipeline.processOne")`. Every span attribute (pipeline_id, bytes_in, stages_run, etc.) lands as a structured log field.

---

## Run stages

```go
// internal/pipeline/run_stages.go
func RunStages(ctx context.Context, stages []Stage, msg []byte) (Outcome, error) {
    current := msg
    format := Detect(current)            // JSON / XML / Protobuf / unknown
    var route *RouteResult

    for _, s := range stages {
        next, nextFormat, result, err := s.Execute(ctx, current, format)
        if err != nil { return Outcome{}, err }
        current = next
        format = nextFormat
        if result != nil { route = result }
    }
    return Outcome{Body: current, Format: format, Route: route}, nil
}
```

Stage execution is single-pass. Each stage takes the current `(message bytes, format)` and returns the next. Stages mutate the message in place — by the time the next stage runs, the prior stage's effects are visible.

The format is auto-detected on the first stage entry. After translate, the format changes; subsequent stages re-detect or use the explicitly returned format. This is how `Filter_JSON` reliably parses JSON when the source was XML.

Stages can also return a `RouteResult` — only the `route` stage does — which the executor reads after the stage loop ends. Returning `RouteResult{Destinations: [...]}` overrides the default destination.

### Per-stage detail

| Stage | Implementation | What can fail |
|---|---|---|
| **validate** | `pipeline/validate.go` — looks up schema by id (cached in build), runs `gojsonschema.Validate` / `libxml2.SchemaValidate` / `protoreflect` decode. | Schema not found → build error (never reached at runtime). Validation error → returned to the DLQ. |
| **filter** | `pipeline/filter.go` — `mxj` for JSON, `etree` for XML. Each path is deleted; missing paths are a no-op. | Malformed source (parse error) → DLQ. |
| **transform** | `pipeline/transform.go` — declarative ops applied in storage `order`. | Same as filter — malformed source → DLQ. |
| **translate** | `pipeline/translate.go` — switch on `(format, target)`. Protobuf branches need a schema (built at config time). | Unsupported pair (e.g. XML → Protobuf without a schema) → DLQ. |
| **route** | `pipeline/route.go` — evaluates each rule against the message; appends matching destinations. | Returning zero matches with no default destination → DLQ. |
| **script** | `pipeline/script.go` — Goja with CPU + memory caps. | Script throw / timeout / OOM → DLQ. |

---

## Forward

```go
// internal/pipeline/executor.go (continued from processOne)
if routeResult != nil {
    for _, destID := range routeResult.Destinations {
        cfg := e.RouteDests[destID]
        if err := e.send(ctx, "route-"+destID, cfg, current); err != nil {
            sendErr = err  // record but keep going — try the others
        }
    }
} else {
    sendErr = e.send(ctx, "dest-"+e.Pipeline.ID, e.DefaultDest, current)
}
```

The executor pre-resolves every routing rule's destination at build time, so the per-message path is a `map[string]mq.Config` lookup — no DB round trip.

`e.send`:
1. Pool.Get on the destination key.
2. `dest.Send(ctx, payload)`.
3. Pool.Release (decrements ref count; idle connections evict on the health-check ticker).

A fan-out (route stage with 3 destinations) does three sequential sends. If any fails, the whole message goes to the DLQ — partial fan-out is recorded as a failure, with the error reason naming which destinations succeeded vs which failed.

### What "send" does per broker

- **RabbitMQ** — `channel.PublishWithContext` with `delivery_mode=2` (persistent) and `mandatory=true`. A return-listener goroutine catches unroutable returns.
- **Kafka** — sync producer: `producer.SendMessage(msg)` returns only after ack from the broker (configurable `RequiredAcks`).
- **MQTT** — `client.Publish` with the connection's QoS. QoS 1/2 wait for the broker's PUBACK before returning.
- **NATS** — `js.Publish` for JetStream (acked), `nc.Publish` for core (fire-and-forget; the connector flushes before returning).
- **AMQP 1.0** — `sender.Send(ctx, msg)`. Settle on accept.
- **IBM MQ** — MQPUT1 with the queue name from the connection row.

---

## Metrics

After a successful send, the executor calls `metrics.RecordSuccess`:

```go
e.Metrics.RecordSuccess(
    e.Pipeline.ID,
    int64(len(current)),
    float64(time.Since(start).Milliseconds()),
)
```

`MetricsSink` is implemented by `internal/metrics.Store` — a singleton that holds:

| Counter | What it is |
|---|---|
| `messages_processed` | cumulative successful sends |
| `messages_failed` | cumulative DLQ pushes |
| `bytes_processed` | cumulative outbound bytes |
| `avg_latency_ms` | rolling average across the last 256 sends |
| `last_message_time` | wall clock of the most recent send |
| `status` | `connected` / `error` / `starting` |
| `last_error` | most recent error string (cleared on next success) |

The Prometheus exposition reads from this store directly. The UI's SSE feed pushes new values whenever any counter changes.

---

## DLQ

Any error in the stage loop OR the send loop pushes a `DLQEntry`:

```go
_ = e.DLQ.Push(ctx, storage.DLQEntry{
    TenantID:    e.Pipeline.TenantID,
    PipelineID:  e.Pipeline.ID,
    SourceQueue: e.SourceQueue,
    OriginalMsg: message,        // the *source* bytes, pre-stage
    ErrorReason: err.Error(),
})
```

Note: `OriginalMsg` is the source payload, not the partially-transformed one. The retry path can therefore re-run every stage from scratch — there's no need to remember what stage failed.

The DLQ is a SQLite table with indexes on `(tenant_id, created_at)` and `(tenant_id, pipeline_id)`. The UI lists rows paginated; retry re-publishes the original through the same pipeline (increments retry_count, sets last_retry_at).

---

## Shutdown

```
SIGTERM
  ↓
context cancellation propagates from main.go down
  ↓
Manager.StopAndWait(timeout)
  - Cancels every running executor's ctx
  - Waits up to `timeout` for in-flight processOne calls to finish
  - Reports stuck executors via the slog logger
  ↓
mq.Pool.Close
  - Drains every cached connection (channel close, broker disconnect)
  ↓
storage.DB.Close
  - WAL checkpoint, then close
  ↓
process exits
```

In-flight messages: if a `processOne` is mid-stage-loop when shutdown fires, the ctx cancellation propagates into the stage. The stage returns an error wrapped from `context.Canceled`. The executor treats this as a transient error — the DLQ push is skipped because `context.Canceled` is not a real failure. On next boot, the source broker's redelivery (RabbitMQ requeue, Kafka uncommitted offset, etc.) brings the message back.

---

## Threading model

- **1 goroutine per pipeline** for the receive → process → send loop. Scheduled by `Manager`.
- **N goroutines from the MQ pool's health checker** (one per cached connection family; cheap, idle most of the time).
- **1 goroutine per SSE client** in the server layer (separate concern from pipelines).
- **1 goroutine per outbound webhook delivery**, spawned by the webhook dispatcher when an event fires.

The pipeline executors don't share state with each other. The pool is thread-safe (sync.Map keyed by connection id; `Get` returns the same `*Connection` to N callers, which is fine because each `Send` / `ReceiveMessage` uses its own broker channel).

No mutexes in the executor itself — context cancellation is the only signal it listens for.

---

## Where the bodies are buried

- **Source acks happen before pipeline runs.** RabbitMQ auto-ack and Kafka offset commit are part of `ReceiveMessage`. If the pipeline crashes mid-stage AFTER the receive but BEFORE the send, the message is lost from the source broker's perspective. The DLQ exists to catch this case in software — when a stage fails, the *original* payload is preserved in storage. Operators retry from the DLQ. This is a deliberate trade against duplicate-delivery: at-most-once with operator-driven recovery, not at-least-once.

- **Route destinations are pre-resolved at build time.** Adding a new connection that should be a route target requires `POST /api/v1/reload` (or it'll happen automatically on next config save). The hot path can't tolerate a DB round trip per message.

- **`Status: error` is sticky.** It clears on the first `RecordSuccess`. If a pipeline is idle (waiting on its first message after a broker bounce), it will read "error" forever until traffic resumes. To avoid spurious alerts: a pipeline transitioning to `error` and staying idle for > 30 s should be checked against `last_message_time` rather than `status`.

- **`avg_latency_ms` is a rolling average across the last 256 messages.** It is not a p95. A single outlier burst pushes it for ~256 messages then it recovers. For real latency SLOs, point Prometheus at `messages_processed` + `bytes_processed` and compute deltas yourself.
