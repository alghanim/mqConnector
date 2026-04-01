import { useEffect, useState } from 'react'
import {
  Activity, AlertTriangle, Zap, HardDrive,
  ArrowUpDown, Gauge,
} from 'lucide-react'
import { Card, StatCard, StatusBadge } from '../components/Card'
import { fetchHealth, fetchMetrics } from '../lib/api'
import type { HealthStatus, MetricsResponse, ConnectionMetrics } from '../lib/types'

export default function Dashboard() {
  const [health, setHealth] = useState<HealthStatus | null>(null)
  const [metrics, setMetrics] = useState<MetricsResponse | null>(null)
  const [error, setError] = useState('')

  const load = async () => {
    try {
      const [h, m] = await Promise.all([fetchHealth(), fetchMetrics()])
      setHealth(h)
      setMetrics(m)
      setError('')
    } catch {
      setError('Failed to load data')
    }
  }

  useEffect(() => {
    load()
    const interval = setInterval(load, 5000)
    return () => clearInterval(interval)
  }, [])

  const conns = metrics?.connections ? Object.values(metrics.connections) : []
  const totalProcessed = conns.reduce((s, c) => s + c.messages_processed, 0)
  const totalFailed = conns.reduce((s, c) => s + c.messages_failed, 0)
  const totalBytes = conns.reduce((s, c) => s + c.bytes_processed, 0)
  const avgLatency = conns.length
    ? (conns.reduce((s, c) => s + c.avg_latency_ms, 0) / conns.length).toFixed(1)
    : '0'

  return (
    <div className="p-6 space-y-6 max-w-7xl">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-xl font-bold">Dashboard</h2>
          <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>
            System overview and real-time metrics
          </p>
        </div>
        {health && <StatusBadge status={health.status} />}
      </div>

      {error && (
        <div className="rounded-lg p-3 text-sm" style={{ background: 'var(--red)', color: '#fff' }}>
          {error}
        </div>
      )}

      {/* Stats grid */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <StatCard
          label="Messages Processed"
          value={totalProcessed.toLocaleString()}
          icon={<Zap size={20} />}
          color="var(--accent)"
          subtitle="Total across all connections"
        />
        <StatCard
          label="Failed Messages"
          value={totalFailed.toLocaleString()}
          icon={<AlertTriangle size={20} />}
          color="var(--red)"
          subtitle="Sent to dead letter queue"
        />
        <StatCard
          label="Avg Latency"
          value={`${avgLatency} ms`}
          icon={<Gauge size={20} />}
          color="var(--teal)"
          subtitle="Per message processing"
        />
        <StatCard
          label="Data Processed"
          value={formatBytes(totalBytes)}
          icon={<HardDrive size={20} />}
          color="var(--purple)"
          subtitle="Total payload bytes"
        />
      </div>

      {/* System info + connections */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        {/* System */}
        <Card>
          <h3 className="text-sm font-semibold mb-4 flex items-center gap-2">
            <Activity size={16} style={{ color: 'var(--accent)' }} />
            System
          </h3>
          <div className="space-y-3">
            <InfoRow label="Status" value={health?.status || '—'} />
            <InfoRow label="Database" value={health?.db_status || '—'} />
            <InfoRow label="Uptime" value={health?.uptime || '—'} />
            <InfoRow label="Active Routines" value={String(health?.active_routines ?? 0)} />
          </div>
        </Card>

        {/* Active Connections */}
        <div className="lg:col-span-2">
          <Card padding={false}>
            <div className="p-5 pb-3">
              <h3 className="text-sm font-semibold flex items-center gap-2">
                <ArrowUpDown size={16} style={{ color: 'var(--green)' }} />
                Active Connections
                <span
                  className="text-xs ml-auto px-2 py-0.5 rounded-full"
                  style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}
                >
                  {conns.length}
                </span>
              </h3>
            </div>

            {conns.length === 0 ? (
              <div className="p-8 text-center text-sm" style={{ color: 'var(--text-secondary)' }}>
                No active connections. Configure filters in PocketBase to get started.
              </div>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr style={{ borderTop: '1px solid var(--border)' }}>
                      {['Source', 'Destination', 'Processed', 'Failed', 'Latency', 'Status'].map(h => (
                        <th
                          key={h}
                          className="text-left px-5 py-2.5 text-xs font-medium"
                          style={{ color: 'var(--text-secondary)', borderBottom: '1px solid var(--border)' }}
                        >
                          {h}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {conns.map((c: ConnectionMetrics) => (
                      <tr
                        key={c.filter_id}
                        className="transition-colors"
                        style={{ borderBottom: '1px solid var(--border)' }}
                        onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg-tertiary)')}
                        onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
                      >
                        <td className="px-5 py-3 font-mono text-xs">{c.source_queue}</td>
                        <td className="px-5 py-3 font-mono text-xs">{c.dest_queue}</td>
                        <td className="px-5 py-3">{c.messages_processed.toLocaleString()}</td>
                        <td className="px-5 py-3" style={{ color: c.messages_failed > 0 ? 'var(--red)' : undefined }}>
                          {c.messages_failed}
                        </td>
                        <td className="px-5 py-3">{c.avg_latency_ms.toFixed(1)} ms</td>
                        <td className="px-5 py-3"><StatusBadge status={c.status} /></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </div>
      </div>
    </div>
  )
}

function InfoRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between items-center text-sm">
      <span style={{ color: 'var(--text-secondary)' }}>{label}</span>
      <span className="font-medium">{value}</span>
    </div>
  )
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i]
}
