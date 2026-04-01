export interface HealthStatus {
  status: 'healthy' | 'degraded' | 'unhealthy'
  db_status: string
  uptime: string
  active_routines: number
  connections: ConnectionHealth[]
}

export interface ConnectionHealth {
  filter_id: string
  status: 'connected' | 'disconnected' | 'error'
  last_error?: string
  source_queue: string
  dest_queue: string
}

export interface MetricsResponse {
  uptime: string
  connections: Record<string, ConnectionMetrics>
}

export interface ConnectionMetrics {
  filter_id: string
  source_queue: string
  dest_queue: string
  messages_processed: number
  messages_failed: number
  bytes_processed: number
  last_message_time: string
  avg_latency_ms: number
  status: string
  last_error?: string
}

export interface DLQEntry {
  id: string
  original_message: string
  error_reason: string
  filter_id: string
  source_queue: string
  retry_count: number
  created: string
}

export interface DLQResponse {
  page: number
  perPage: number
  total: number
  items: DLQEntry[]
}

export interface MQConnection {
  id: string
  type: string
  queueManager: string
  connName: string
  channel: string
  user: string
  queueName: string
  url: string
  brokers: string
  topic: string
  ownerName: string
  expand?: {
    type?: { TYPE: string }
  }
}
