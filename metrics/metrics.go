package metrics

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ConnectionMetrics tracks metrics for a single filter/connection.
type ConnectionMetrics struct {
	FilterID          string    `json:"filter_id"`
	SourceQueue       string    `json:"source_queue"`
	DestQueue         string    `json:"dest_queue"`
	MessagesProcessed int64     `json:"messages_processed"`
	MessagesFailed    int64     `json:"messages_failed"`
	BytesProcessed    int64     `json:"bytes_processed"`
	LastMessageTime   time.Time `json:"last_message_time"`
	AvgLatencyMs      float64   `json:"avg_latency_ms"`
	Status            string    `json:"status"` // "connected", "disconnected", "error"
	LastError         string    `json:"last_error,omitempty"`

	totalLatencyMs float64
	mu             sync.Mutex
}

// MetricsStore is a thread-safe singleton for all connection metrics.
type MetricsStore struct {
	connections map[string]*ConnectionMetrics
	mu          sync.RWMutex
	startTime   time.Time
}

var (
	store *MetricsStore
	once  sync.Once
)

// GetStore returns the singleton MetricsStore instance.
func GetStore() *MetricsStore {
	once.Do(func() {
		store = &MetricsStore{
			connections: make(map[string]*ConnectionMetrics),
			startTime:   time.Now(),
		}
	})
	return store
}

// Register adds a new connection to the metrics store.
func (s *MetricsStore) Register(filterID, sourceQueue, destQueue string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connections[filterID] = &ConnectionMetrics{
		FilterID:    filterID,
		SourceQueue: sourceQueue,
		DestQueue:   destQueue,
		Status:      "connected",
	}
}

// Unregister removes a connection from the metrics store.
func (s *MetricsStore) Unregister(filterID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.connections, filterID)
}

// SetStatus updates the status and optional error for a connection.
func (s *MetricsStore) SetStatus(filterID, status, lastError string) {
	s.mu.RLock()
	conn, ok := s.connections[filterID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.Status = status
	conn.LastError = lastError
}

// RecordSuccess records a successfully processed message.
func (s *MetricsStore) RecordSuccess(filterID string, bytesProcessed int64, latencyMs float64) {
	s.mu.RLock()
	conn, ok := s.connections[filterID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.MessagesProcessed++
	conn.BytesProcessed += bytesProcessed
	conn.LastMessageTime = time.Now()
	conn.totalLatencyMs += latencyMs
	conn.AvgLatencyMs = conn.totalLatencyMs / float64(conn.MessagesProcessed)
}

// RecordFailure records a failed message processing attempt.
func (s *MetricsStore) RecordFailure(filterID string) {
	s.mu.RLock()
	conn, ok := s.connections[filterID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.MessagesFailed++
}

// GetAll returns a snapshot of all connection metrics.
func (s *MetricsStore) GetAll() map[string]ConnectionMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]ConnectionMetrics, len(s.connections))
	for k, v := range s.connections {
		v.mu.Lock()
		result[k] = *v
		v.mu.Unlock()
	}
	return result
}

// GetUptime returns the duration since the metrics store was initialized.
func (s *MetricsStore) GetUptime() time.Duration {
	return time.Since(s.startTime)
}

// ActiveCount returns the number of active connections.
func (s *MetricsStore) ActiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.connections)
}

// FormatPrometheus returns metrics in Prometheus exposition format.
func (s *MetricsStore) FormatPrometheus() string {
	all := s.GetAll()
	var sb strings.Builder

	sb.WriteString("# HELP mqconnector_messages_processed_total Total messages processed per connection\n")
	sb.WriteString("# TYPE mqconnector_messages_processed_total counter\n")
	for _, m := range all {
		sb.WriteString(fmt.Sprintf("mqconnector_messages_processed_total{filter_id=\"%s\",source=\"%s\",dest=\"%s\"} %d\n",
			m.FilterID, m.SourceQueue, m.DestQueue, m.MessagesProcessed))
	}

	sb.WriteString("# HELP mqconnector_messages_failed_total Total failed messages per connection\n")
	sb.WriteString("# TYPE mqconnector_messages_failed_total counter\n")
	for _, m := range all {
		sb.WriteString(fmt.Sprintf("mqconnector_messages_failed_total{filter_id=\"%s\",source=\"%s\",dest=\"%s\"} %d\n",
			m.FilterID, m.SourceQueue, m.DestQueue, m.MessagesFailed))
	}

	sb.WriteString("# HELP mqconnector_bytes_processed_total Total bytes processed per connection\n")
	sb.WriteString("# TYPE mqconnector_bytes_processed_total counter\n")
	for _, m := range all {
		sb.WriteString(fmt.Sprintf("mqconnector_bytes_processed_total{filter_id=\"%s\",source=\"%s\",dest=\"%s\"} %d\n",
			m.FilterID, m.SourceQueue, m.DestQueue, m.BytesProcessed))
	}

	sb.WriteString("# HELP mqconnector_avg_latency_ms Average processing latency in milliseconds\n")
	sb.WriteString("# TYPE mqconnector_avg_latency_ms gauge\n")
	for _, m := range all {
		sb.WriteString(fmt.Sprintf("mqconnector_avg_latency_ms{filter_id=\"%s\",source=\"%s\",dest=\"%s\"} %.2f\n",
			m.FilterID, m.SourceQueue, m.DestQueue, m.AvgLatencyMs))
	}

	sb.WriteString(fmt.Sprintf("# HELP mqconnector_uptime_seconds Uptime in seconds\n"))
	sb.WriteString(fmt.Sprintf("# TYPE mqconnector_uptime_seconds gauge\n"))
	sb.WriteString(fmt.Sprintf("mqconnector_uptime_seconds %.0f\n", s.GetUptime().Seconds()))

	return sb.String()
}
