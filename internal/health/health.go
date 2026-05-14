// Package health is the liveness/readiness check. It pings the database,
// inspects per-pipeline status, and rolls everything up into a single status.
package health

import (
	"context"
	"time"

	"mqConnector/internal/metrics"
	"mqConnector/internal/storage"
)

// Connection is the per-pipeline portion of the response.
type Connection struct {
	PipelineID  string `json:"pipeline_id"`
	Status      string `json:"status"`
	LastError   string `json:"last_error,omitempty"`
	SourceQueue string `json:"source_queue"`
	DestQueue   string `json:"dest_queue"`
}

// Status is the full response envelope.
type Status struct {
	Status      string       `json:"status"`
	Version     string       `json:"version"`
	DBStatus    string       `json:"db_status"`
	Uptime      string       `json:"uptime"`
	Active      int          `json:"active_pipelines"`
	Connections []Connection `json:"connections,omitempty"`
}

// Checker assembles a Status. It's stateless beyond its dependencies.
type Checker struct {
	store   *storage.Store
	metrics *metrics.Store
	version string
}

// NewChecker constructs a Checker.
func NewChecker(store *storage.Store, ms *metrics.Store, version string) *Checker {
	return &Checker{store: store, metrics: ms, version: version}
}

// Check runs the probes and returns the rolled-up Status.
func (c *Checker) Check(ctx context.Context) Status {
	dbStatus := "ok"
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if c.store != nil && c.store.DB != nil {
		if err := c.store.DB.PingContext(pingCtx); err != nil {
			dbStatus = "error: " + err.Error()
		}
	} else {
		dbStatus = "not configured"
	}

	conns := []Connection{}
	if c.metrics != nil {
		for _, m := range c.metrics.Snapshot() {
			conns = append(conns, Connection{
				PipelineID:  m.PipelineID,
				Status:      m.Status,
				LastError:   m.LastError,
				SourceQueue: m.SourceQueue,
				DestQueue:   m.DestQueue,
			})
		}
	}

	overall := "healthy"
	if dbStatus != "ok" {
		overall = "unhealthy"
	} else {
		for _, c := range conns {
			if c.Status == "error" {
				overall = "degraded"
				break
			}
		}
	}

	uptime := ""
	if c.metrics != nil {
		uptime = c.metrics.Uptime().Round(time.Second).String()
	}

	return Status{
		Status:      overall,
		Version:     c.version,
		DBStatus:    dbStatus,
		Uptime:      uptime,
		Active:      len(conns),
		Connections: conns,
	}
}
