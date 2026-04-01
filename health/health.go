package health

import (
	"mqConnector/metrics"

	"github.com/pocketbase/pocketbase"
)

// ConnectionHealth describes the health of a single connection.
type ConnectionHealth struct {
	FilterID    string `json:"filter_id"`
	Status      string `json:"status"`
	LastError   string `json:"last_error,omitempty"`
	SourceQueue string `json:"source_queue"`
	DestQueue   string `json:"dest_queue"`
}

// HealthStatus describes the overall system health.
type HealthStatus struct {
	Status         string             `json:"status"`
	DBStatus       string             `json:"db_status"`
	Uptime         string             `json:"uptime"`
	ActiveRoutines int                `json:"active_routines"`
	Connections    []ConnectionHealth `json:"connections,omitempty"`
}

// Check performs a health check and returns the full status.
func Check(app *pocketbase.PocketBase) HealthStatus {
	store := metrics.GetStore()

	// Check DB
	dbStatus := "ok"
	if err := app.Dao().DB().NewQuery("SELECT 1").Execute(); err != nil {
		dbStatus = "error: " + err.Error()
	}

	// Build connection health list
	allMetrics := store.GetAll()
	connections := make([]ConnectionHealth, 0, len(allMetrics))
	for _, m := range allMetrics {
		connections = append(connections, ConnectionHealth{
			FilterID:    m.FilterID,
			Status:      m.Status,
			LastError:   m.LastError,
			SourceQueue: m.SourceQueue,
			DestQueue:   m.DestQueue,
		})
	}

	// Determine overall status
	overallStatus := "healthy"
	if dbStatus != "ok" {
		overallStatus = "unhealthy"
	} else {
		for _, c := range connections {
			if c.Status == "error" {
				overallStatus = "degraded"
				break
			}
		}
	}

	return HealthStatus{
		Status:         overallStatus,
		DBStatus:       dbStatus,
		Uptime:         store.GetUptime().String(),
		ActiveRoutines: store.ActiveCount(),
		Connections:    connections,
	}
}
