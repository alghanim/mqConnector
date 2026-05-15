package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"mqConnector/internal/mq"
	"mqConnector/internal/mqcfg"
	"mqConnector/internal/storage"
)

// Manager owns one Executor per enabled pipeline and supports hot-reload:
// callers can mutate pipeline configuration in storage and call Reload to
// recycle the goroutines.
type Manager struct {
	store   *storage.Store
	pool    *mq.Pool
	metrics MetricsSink
	dlq     DLQSink
	logger  *slog.Logger

	mu      sync.Mutex
	active  map[string]context.CancelFunc // pipeline ID → cancel
	parent  context.Context
	stopped bool
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
		active:  map[string]context.CancelFunc{},
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
	for id, cancel := range m.active {
		cancel()
		delete(m.active, id)
	}
	m.mu.Unlock()

	pipelines, err := m.store.Pipelines.List(ctx)
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
	stageRows, err := m.store.Stages.ListByPipeline(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("list stages: %w", err)
	}
	transforms, err := m.store.Transforms.ListByPipeline(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("list transforms: %w", err)
	}
	routes, err := m.store.RoutingRules.ListByPipeline(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("list routing rules: %w", err)
	}

	source, err := m.store.Connections.Get(ctx, p.SourceID)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	dest, err := m.store.Connections.Get(ctx, p.DestinationID)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}

	// Pre-load every schema referenced either at the pipeline level or by a
	// validate stage's config, so the executor doesn't hit storage mid-loop.
	schemas, err := loadReferencedSchemas(ctx, m.store, p, stageRows)
	if err != nil {
		return fmt.Errorf("load schemas: %w", err)
	}

	stages, err := Build(BuildContext{
		Pipeline:     p,
		StageRows:    stageRows,
		Transforms:   transforms,
		RoutingRules: routes,
		Schemas:      schemas,
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
		destConn, err := m.store.Connections.Get(ctx, r.DestinationID)
		if err != nil {
			return fmt.Errorf("route destination %s: %w", r.DestinationID, err)
		}
		routeDests[r.DestinationID] = ToMQConfig(destConn)
	}

	pipelineCtx, cancel := context.WithCancel(m.parent)
	executor := &Executor{
		Pipeline:    p,
		Stages:      stages,
		Pool:        m.pool,
		Source:      ToMQConfig(source),
		SourceQueue: source.QueueName,
		DefaultDest: ToMQConfig(dest),
		DestQueue:   dest.QueueName,
		RouteDests:  routeDests,
		Metrics:     m.metrics,
		DLQ:         m.dlq,
		Logger:      m.logger,
	}

	m.mu.Lock()
	m.active[p.ID] = cancel
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.active, p.ID)
			m.mu.Unlock()
		}()
		if err := executor.Run(pipelineCtx); err != nil {
			m.logger.Error("executor exited with error",
				"pipeline_id", p.ID, "err", err)
		}
	}()
	return nil
}

// Stop cancels all running pipelines and refuses future Reloads.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
	for id, cancel := range m.active {
		cancel()
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
