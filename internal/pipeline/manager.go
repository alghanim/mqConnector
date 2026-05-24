package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"

	"mqConnector/internal/events"
	"mqConnector/internal/mq"
	"mqConnector/internal/mqcfg"
	"mqConnector/internal/storage"
)

// EventSink is the minimal interface the manager needs to emit
// lifecycle events. internal/events.Publisher satisfies it; tests can
// pass a nil sink to skip emission.
type EventSink interface {
	Publish(ctx context.Context, e events.Event) int
}

// Manager owns one Executor per enabled pipeline and supports hot-reload:
// callers can mutate pipeline configuration in storage and call Reload to
// recycle the goroutines.
// executorHandle is the per-pipeline lifecycle handle the Manager keeps
// in its active map. cancel ends the executor's Run via ctx propagation;
// done closes when the executor goroutine has fully unwound (including
// its deferred Metrics.Unregister + lifecycle-event emit). Reload waits
// on done before spawning a replacement, which closes a race where the
// old executor's Unregister fired AFTER the new executor's Register and
// silently wiped the live registration off the metrics store.
type executorHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type Manager struct {
	store   *storage.Store
	pool    *mq.Pool
	metrics MetricsSink
	dlq     DLQSink
	logger  *slog.Logger

	mu sync.Mutex
	// active maps pipeline ID → its running executor handle. Reload
	// waits on each handle's done channel before spawning a
	// replacement; see executorHandle's doc for the race this closes.
	active  map[string]*executorHandle
	parent  context.Context
	stopped bool

	// wg tracks every executor goroutine the manager has started but
	// not yet observed exit. StopAndWait blocks on this so SIGTERM
	// can give in-flight messages a chance to finish before the
	// process exits.
	wg sync.WaitGroup

	// Optional event sink. nil means "don't emit lifecycle events".
	events EventSink

	// Optional WASM runtime — injected by the server. nil means
	// stages with stage_type=wasm will fail Build.
	wasmRuntime wazero.Runtime

	// Optional dedup store — injected by main during boot. nil
	// disables dedup regardless of any pipeline's DedupWindowSeconds
	// (the executor honours the field only when both the deduper is
	// installed and the window is non-zero).
	dedup Deduper

	// tenantBudgets caches one *budget per tenant_id that has a
	// non-zero MaxMsgsPerMinute. Built lazily on Reload — pipelines
	// share the same budget instance across executors, so one runaway
	// pipeline can't burn the tenant's entire quota and starve
	// siblings. budgetMu protects the map; the budgets themselves are
	// thread-safe.
	budgetMu      sync.Mutex
	tenantBudgets map[string]*budget
}

// tenantBudgetFor returns the shared budget for a tenant, building it
// on first sight. budgetPerMinute=0 returns nil (no cap configured).
func (m *Manager) tenantBudgetFor(tenantID string, budgetPerMinute int) *budget {
	if budgetPerMinute <= 0 {
		return nil
	}
	m.budgetMu.Lock()
	defer m.budgetMu.Unlock()
	if m.tenantBudgets == nil {
		m.tenantBudgets = make(map[string]*budget)
	}
	if b, ok := m.tenantBudgets[tenantID]; ok {
		return b
	}
	b := newBudget(budgetPerMinute, time.Minute)
	m.tenantBudgets[tenantID] = b
	return b
}

// SetDeduper installs the destination-side dedup store used by every
// executor this Manager spawns. Idempotent; nil is a deliberate
// "disable dedup for this process" knob useful in tests.
func (m *Manager) SetDeduper(d Deduper) { m.dedup = d }

// SetWasmRuntime installs the wazero runtime used to compile plugin
// blobs at pipeline build time. The server owns the runtime
// lifecycle and passes it in; Manager treats it as borrowed.
func (m *Manager) SetWasmRuntime(rt wazero.Runtime) { m.wasmRuntime = rt }

// loadWasmPluginsForBuild fetches every plugin referenced by this
// pipeline's wasm stages, compiles each blob through the runtime
// (validating exports + import-emptiness + memory caps), and
// returns a name → WasmStage map for BuildContext.WasmPlugins.
//
// Compile happens here rather than inside Build so a corrupt plugin
// fails the whole pipeline reload — partial pipelines aren't
// useful, and the operator wants to see the failure on the next
// /api/v1/reload rather than mid-pipeline at message-receive time.
func (m *Manager) loadWasmPluginsForBuild(
	ctx context.Context,
	p *storage.Pipeline,
	stageRows []*storage.Stage,
) (map[string]*WasmStage, error) {
	// Gather the plugin names this pipeline actually uses so we
	// don't compile every plugin in the tenant's table.
	names := map[string]bool{}
	for _, s := range stageRows {
		if !s.Enabled || s.StageType != "wasm" {
			continue
		}
		var cfg struct {
			Plugin string `json:"plugin"`
		}
		_ = json.Unmarshal([]byte(s.StageConfig), &cfg)
		if cfg.Plugin != "" {
			names[cfg.Plugin] = true
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	if m.wasmRuntime == nil {
		return nil, fmt.Errorf("pipeline uses wasm stages but no runtime is configured")
	}
	out := make(map[string]*WasmStage, len(names))
	for name := range names {
		plug, err := m.store.Plugins.Get(ctx, p.TenantID, name)
		if err != nil {
			return nil, fmt.Errorf("load plugin %q: %w", name, err)
		}
		mod, err := CompileWasm(ctx, m.wasmRuntime, plug.Blob, DefaultWasmLimits)
		if err != nil {
			return nil, fmt.Errorf("compile plugin %q: %w", name, err)
		}
		out[name] = &WasmStage{
			PluginName: name,
			Module:     mod,
			Runtime:    m.wasmRuntime,
			Limits:     DefaultWasmLimits,
		}
	}
	return out, nil
}

// SetEventSink installs a publisher to receive pipeline.started /
// pipeline.stopped lifecycle events. Idempotent. Pass nil to disable.
func (m *Manager) SetEventSink(sink EventSink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = sink
}

// emit fires an event when a sink is installed, swallows otherwise.
// Held under the manager's mutex would risk back-pressure on a slow
// subscriber — but the Publisher's non-blocking send keeps it cheap.
func (m *Manager) emit(ctx context.Context, evType, tenantID string, data map[string]any) {
	m.mu.Lock()
	sink := m.events
	m.mu.Unlock()
	if sink == nil {
		return
	}
	sink.Publish(ctx, events.Event{
		Type:     evType,
		TenantID: tenantID,
		Data:     data,
	})
}

// NewManager constructs a Manager but does not start anything until Reload is
// called.
func NewManager(parent context.Context, store *storage.Store, pool *mq.Pool, metrics MetricsSink, dlq DLQSink, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		store:   store,
		pool:    pool,
		metrics: metrics,
		dlq:     dlq,
		logger:  logger.With("component", "pipeline.Manager"),
		active:  map[string]*executorHandle{},
		parent:  parent,
	}
}

// Reload cancels every running pipeline and restarts from the current storage
// state. Safe to call repeatedly. Returns the count of pipelines started.
func (m *Manager) Reload(ctx context.Context) (int, error) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return 0, fmt.Errorf("pipeline: manager stopped")
	}
	// Snapshot the running handles, release the lock, then signal +
	// wait outside the critical section. Each handle's goroutine
	// re-acquires m.mu in its defer to delete itself from the map; if
	// we held m.mu while waiting on done we'd deadlock. The drain —
	// wait until the old executor's deferred Unregister has fired
	// before the new Register runs — is what closes the race the live
	// deploy hit (active_pipelines stuck at 0 after a gitops-triggered
	// reload).
	oldHandles := make([]*executorHandle, 0, len(m.active))
	for _, h := range m.active {
		oldHandles = append(oldHandles, h)
	}
	m.mu.Unlock()
	for _, h := range oldHandles {
		h.cancel()
		<-h.done
	}

	// ListAll walks every tenant's pipelines. The Manager is a
	// system-level component that runs workers regardless of which
	// human is currently logged into the UI — tenant scoping is
	// enforced at the HTTP layer, not here.
	pipelines, err := m.store.Pipelines.ListAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("list pipelines: %w", err)
	}

	started := 0
	for _, p := range pipelines {
		if !p.Enabled {
			continue
		}
		if err := m.startPipeline(ctx, p); err != nil {
			m.logger.Error("failed to start pipeline",
				"pipeline_id", p.ID, "pipeline_name", p.Name, "err", err)
			continue
		}
		started++
	}
	m.logger.Info("pipelines reloaded", "started", started)
	return started, nil
}

func (m *Manager) startPipeline(ctx context.Context, p *storage.Pipeline) error {
	// Internal walk over a known-tenant-pipeline. The *Unsafe variants
	// skip tenant scoping; the pipeline itself already carries its
	// tenant_id, so cross-tenant data flow can only happen if the
	// pipeline's storage rows were corrupted (which would be a different
	// bug class entirely).
	stageRows, err := m.store.Stages.ListByPipelineUnsafe(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("list stages: %w", err)
	}
	transforms, err := m.store.Transforms.ListByPipelineUnsafe(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("list transforms: %w", err)
	}
	routes, err := m.store.RoutingRules.ListByPipelineUnsafe(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("list routing rules: %w", err)
	}

	source, err := m.store.Connections.GetUnsafe(ctx, p.SourceID)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	dest, err := m.store.Connections.GetUnsafe(ctx, p.DestinationID)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	// Durability heads-up: core NATS as a source is at-most-once by
	// protocol (no per-message ack, no broker-side retention). The
	// Commit/Nack guarantees the rest of the pipeline gives you are
	// invisible to a fire-and-forget source — messages in flight when
	// mqconnector restarts are gone. Operators who want durable NATS
	// must point at a JetStream stream (set stream_name on the
	// connection).
	if srcType, _ := mq.ParseType(source.Type); srcType == mq.TypeNATS && source.StreamName == "" {
		m.logger.Warn("pipeline source is core NATS (no JetStream stream) — at-most-once delivery",
			"pipeline_id", p.ID,
			"pipeline_name", p.Name,
			"source_connection", source.Name,
			"recommendation", "set stream_name on the connection to use JetStream for durability")
	}
	// Sanity check: the pipeline must not reference a connection in a
	// different tenant. This is impossible via the HTTP API (handlers
	// validate); the check exists for defence in depth against a hand-
	// edited SQLite file.
	if source.TenantID != p.TenantID || dest.TenantID != p.TenantID {
		return fmt.Errorf("cross-tenant connection reference detected on pipeline %s", p.ID)
	}

	// Pre-load every schema referenced either at the pipeline level or by a
	// validate stage's config, so the executor doesn't hit storage mid-loop.
	schemas, err := loadReferencedSchemas(ctx, m.store, p, stageRows)
	if err != nil {
		return fmt.Errorf("load schemas: %w", err)
	}

	// Pre-compile any WASM plugins this pipeline references. Lookup
	// is by name in the tenant's plugins table; compilation runs
	// through the runtime's CompileWasm which validates exports +
	// memory caps + no-imports before the module is cached.
	wasmPlugins, err := m.loadWasmPluginsForBuild(ctx, p, stageRows)
	if err != nil {
		return fmt.Errorf("load wasm plugins: %w", err)
	}

	stages, err := Build(BuildContext{
		Pipeline:     p,
		StageRows:    stageRows,
		Transforms:   transforms,
		RoutingRules: routes,
		Schemas:      schemas,
		WasmPlugins:  wasmPlugins,
	})
	if err != nil {
		return fmt.Errorf("build stages: %w", err)
	}

	// Pre-resolve every routing destination so the executor doesn't hit
	// storage mid-loop.
	routeDests := map[string]mq.Config{}
	for _, r := range routes {
		if !r.Enabled {
			continue
		}
		if _, seen := routeDests[r.DestinationID]; seen {
			continue
		}
		destConn, err := m.store.Connections.GetUnsafe(ctx, r.DestinationID)
		if err != nil {
			return fmt.Errorf("route destination %s: %w", r.DestinationID, err)
		}
		if destConn.TenantID != p.TenantID {
			return fmt.Errorf("cross-tenant routing destination on pipeline %s", p.ID)
		}
		routeDests[r.DestinationID] = ToMQConfig(destConn)
	}

	pipelineCtx, cancel := context.WithCancel(m.parent)

	// Resolve the tenant's aggregate budget (if any). A nil store
	// (test code path) or a tenant that doesn't exist is treated as
	// "no cap" — the safe legacy behaviour.
	var tenantBudget *budget
	if m.store != nil && p.TenantID != "" {
		if t, err := m.store.Tenants.Get(ctx, p.TenantID); err == nil && t != nil {
			tenantBudget = m.tenantBudgetFor(p.TenantID, t.MaxMsgsPerMinute)
		}
	}

	executor := &Executor{
		Pipeline:     p,
		Stages:       stages,
		Pool:         m.pool,
		Source:       ToMQConfig(source),
		SourceQueue:  source.QueueName,
		DefaultDest:  ToMQConfig(dest),
		DestQueue:    dest.QueueName,
		RouteDests:   routeDests,
		Metrics:      m.metrics,
		DLQ:          m.dlq,
		Dedup:        m.dedup,
		TenantBudget: tenantBudget,
		Logger:       m.logger,
	}

	handle := &executorHandle{cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	m.active[p.ID] = handle
	m.mu.Unlock()

	// Lifecycle event: pipeline started. Emitted before the executor
	// goroutine spins up so a subscriber that lists pipelines straight
	// after will see the row as live.
	m.emit(ctx, events.TypePipelineStarted, p.TenantID, map[string]any{
		"pipeline_id":   p.ID,
		"pipeline_name": p.Name,
		"source_queue":  source.QueueName,
		"dest_queue":    dest.QueueName,
	})

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		// Close `done` AFTER the executor has fully unwound (including
		// its own deferred Unregister inside executor.Run). Reload
		// blocks on this channel before spawning a replacement so the
		// new executor's Register can't be wiped by a still-running
		// old defer.
		defer close(handle.done)
		defer func() {
			m.mu.Lock()
			// Only delete if this handle is still the live one — a
			// concurrent Reload may have already replaced it, in which
			// case wiping the map entry would erase the active executor.
			if cur, ok := m.active[p.ID]; ok && cur == handle {
				delete(m.active, p.ID)
			}
			m.mu.Unlock()
			// Lifecycle event: pipeline stopped. Reasons include
			// explicit Stop, Reload churn, or an executor error.
			m.emit(context.Background(), events.TypePipelineStopped, p.TenantID, map[string]any{
				"pipeline_id":   p.ID,
				"pipeline_name": p.Name,
			})
		}()
		if err := executor.Run(pipelineCtx); err != nil {
			m.logger.Error("executor exited with error",
				"pipeline_id", p.ID, "err", err)
			m.emit(context.Background(), events.TypePipelineError, p.TenantID, map[string]any{
				"pipeline_id":   p.ID,
				"pipeline_name": p.Name,
				"err":           err.Error(),
			})
		}
	}()
	return nil
}

// Stop cancels all running pipelines and refuses future Reloads. Final
// teardown; use StopAll instead if the process should remain reload-able
// (e.g. a passive replica that lost the leadership lease but may regain
// it).
//
// Stop is fire-and-forget: it does not wait for executor goroutines to
// observe the cancellation and exit. Use StopAndWait when the caller
// needs synchronous drain semantics (e.g. graceful shutdown on SIGTERM).
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
	for id, h := range m.active {
		h.cancel()
		delete(m.active, id)
	}
}

// StopAndWait cancels every running pipeline and blocks until every
// executor goroutine has actually exited — or the timeout elapses,
// whichever comes first. Returns true on a clean drain, false when
// the timeout hits before all goroutines acknowledged.
//
// Order of operations matters: cancel() each pipeline first so the
// blocking `source.ReceiveMessage(ctx)` calls unwind, *then* wait on
// the WaitGroup. Reversing the order would deadlock.
//
// Use this from the process-level SIGTERM handler so in-flight
// messages get to finish sending downstream before the binary exits.
// The default 30s budget is comfortable for the receive→stages→send
// fast path of a typical message.
func (m *Manager) StopAndWait(timeout time.Duration) bool {
	m.mu.Lock()
	m.stopped = true
	for id, h := range m.active {
		h.cancel()
		// We deliberately don't delete from m.active here — the
		// goroutine's own defer does that. Premature deletion races
		// with the goroutine's "am I still active?" checks during
		// teardown.
		_ = id
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		m.logger.Warn("StopAndWait: drain timeout — some executors did not exit",
			"timeout", timeout)
		return false
	}
}

// StopAll cancels every active pipeline without flipping the stopped
// flag, so a subsequent Reload can bring them back. Used by the
// leadership consumer when the lease is lost: workers must stop so the
// new leader can safely start its own, but a future re-acquire should
// be allowed to re-arm without bouncing the process.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, h := range m.active {
		h.cancel()
		delete(m.active, id)
	}
}

// ActiveCount reports how many pipelines are currently running.
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}

// ToMQConfig converts a storage.Connection to an mq.Config. The real work
// lives in internal/mqcfg — kept as a thin alias so existing callers (server
// handlers, etc.) continue to compile.
func ToMQConfig(c *storage.Connection) mq.Config {
	return mqcfg.From(c)
}
