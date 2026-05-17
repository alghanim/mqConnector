# Plugin System — Design Note

A design proposal for letting operators ship custom pipeline stages without forking mqConnector. Not implemented yet — this document captures the trade-offs so the eventual implementation lands on a thought-through shape.

## 1. Goal

Allow an operator to:

1. Write a stage in Go (or, eventually, in a sandboxed language) that implements a small surface (`Execute(ctx, message, format) → message`).
2. Distribute the stage as a single artefact alongside their config.
3. Have the running mqConnector load the stage and route pipelines to it via the same stage-config UI as built-in stages.

What this is **not**: a marketplace, an app store, or a generic compute fabric. The plugin surface is intentionally narrow — one method, no network access from the plugin, no filesystem access, no goroutine spawning. A plugin is a function from bytes to bytes.

## 2. The four candidate mechanisms

Picking the mechanism is the whole decision. Each option has a different attack surface, distribution story, and operator complexity.

### A. `plugin.Open` (stdlib)

Go's built-in plugin loader.

| Pros | Cons |
| --- | --- |
| No new dependency. | Linux-only (Darwin support is broken/partial; Windows is unsupported). |
| Native performance (compiled Go calling compiled Go). | Plugin must be built with exactly the same Go toolchain, exact same module versions, exact same build tags as the host. Mismatch → "plugin was built with a different version of package X" panic. |
| Operator's plugin code is regular Go. | No sandbox. A plugin has full process access — can call `os.Exit`, drop files, dial out. Untrusted plugins not safe. |

Verdict: too fragile in practice. The toolchain-pinning requirement makes distribution near-impossible across customer environments.

### B. WASM (wasmtime-go / wazero)

Compile the plugin to WebAssembly and run it in an embedded interpreter.

| Pros | Cons |
| --- | --- |
| Full sandbox by default: no FS, no network, no syscalls. | Slower than native Go (typically 2-5× depending on workload). |
| Cross-platform; one artefact runs on any OS. | Languages other than Rust / AssemblyScript / TinyGo have varying maturity. |
| Resource limits (memory, fuel) are first-class. | Need to bridge complex types (the stage's input/output is `[]byte` + format string — easy. But error reporting + structured config is trickier). |

Verdict: strongest candidate. The sandbox story matches the threat model (plugin = untrusted code), and the performance is acceptable for stage transforms where the broker round-trip dominates.

### C. Sidecar process over gRPC (hashicorp/go-plugin style)

Plugin runs as a separate OS process; the bridge talks to it over a Unix-domain socket using gRPC.

| Pros | Cons |
| --- | --- |
| Strong isolation: plugin crashes don't take down the bridge. | Adds an IPC hop per message — 50-200µs typical. Not free at high throughput. |
| Each plugin can be a different language without changes to the bridge. | Operator now manages N processes, not one binary. |
| Plugin restart on crash is easy. | Plugin lifecycle (start, healthcheck, restart) is non-trivial. |

Verdict: good for a marketplace future where plugins are heterogeneous. Overkill for "let our customer write a transform in Go." The added IPC cost matters at the throughput tier we target.

### D. Embedded scripting (existing `ScriptStage`)

We already ship a line-evaluator scripting stage (`internal/pipeline/script.go`).

| Pros | Cons |
| --- | --- |
| Already in production. Already sandboxed. | Strictly limited expression set — assignments, deletes, basic arithmetic. No loops, no functions, no map iteration. |
| No new build / distribution story. | Genuinely complex transforms are out of reach. Customers asking for "plugins" usually need this. |

Verdict: the floor, not the ceiling. The plugin system is what happens when ScriptStage isn't enough.

## 3. Proposed direction

**WASM via wazero**, with the following shape:

### 3.1. Plugin contract

A plugin exports one function with this signature (TinyGo example):

```go
//go:wasmexport execute
func execute(inPtr, inLen uint32) (outPtr, outLen uint32, errPtr, errLen uint32)
```

The host writes the input bytes into linear memory at a known offset, calls `execute`, and reads the result back. Two outputs: the transformed bytes, OR (mutually exclusive) an error string that lands the message in DLQ.

### 3.2. Resource limits

Configurable per stage instance, with these defaults:

| Limit       | Default        | Hard cap | Tripping it... |
| ----------- | -------------- | -------- | -------------- |
| Memory      | 32 MiB         | 256 MiB  | → DLQ          |
| Fuel        | 10M instructions / message | 1B | → DLQ          |
| Wall time   | 1 second / message | 30s | → DLQ          |
| Imports     | none allowed   | n/a      | refuse to load |

Trying to call any host function from inside the WASM module (FS, network, time) is a load-time error. The plugin's universe is the bytes it gets and the bytes it returns.

### 3.3. Distribution

Plugins are `.wasm` files. Operators:

1. Build the plugin (TinyGo / Rust / AssemblyScript).
2. Upload via `POST /api/v1/plugins` (admin-only, system-admin-only at first).
3. The blob is stored in a new `plugins` table (BLOB + sha256 + uploader + uploaded_at).
4. A new stage type `wasm` references the plugin by id; stage_config carries a JSON body that's passed to the plugin as init params.

### 3.4. Reload

Plugins reload like every other stage: hot, no restart. The wazero runtime is per-server; per-stage instances are pooled.

### 3.5. Security review checkpoints

The plugin path adds attack surface; before merge:

- Confirm wazero is set to no-imports / no-WASI for plugins. The bridge controls what host functions are exposed.
- Confirm the upload endpoint is system-admin only AND rate-limited via the `sensitiveLimiter`.
- Confirm the plugin blob is hashed at upload + verified at every load — a corrupt blob doesn't get cached as the "approved" one.
- Add a SECURITY.md entry under EoP describing the new trust boundary.
- Land the plugin contract first, behind a feature flag (`features.wasm_plugins: true`). Off by default for at least one release after merge.

## 4. What's out of scope

- A plugin marketplace.
- Multi-language plugin SDKs maintained by us (operators bring their own toolchain to produce the .wasm).
- Plugin upgrade / downgrade workflows (treat plugins as immutable blobs; a new version is a new upload).
- Telemetry from inside the plugin (no host calls = no metrics from the plugin; the host wraps execution timing).

## 5. Why this isn't done yet

- Wazero is a non-trivial dependency. We've kept the dep tree tight (single broker libs per type, no UI framework, etc.). Adding wazero is a deliberate decision.
- The current `ScriptStage` covers ~80% of asked-for use cases. The remaining 20% can route to a microservice over `route` + `bridge/publish` today.
- Once we add the plugin upload surface, we own a new trust boundary forever. We want to add it once, correctly, with the security review done first.

When customer demand crosses the line where the ScriptStage stops being enough — track this as inbound feature requests for "stage that does X" where X isn't expressible — pull this design out and ship it.

## 6. Reference implementations

Read these before starting on the actual port:

- Envoy WASM filter: how a high-throughput proxy handles the IPC + memory layout.
- Suborbital Atmo: heavier-weight but the contract design is clear.
- TiKV's coprocessor framework: an older example using protobuf-over-Unix-socket; useful as a baseline for the costs option C avoids.
