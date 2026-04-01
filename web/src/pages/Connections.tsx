import { useEffect, useState } from 'react'
import { Cable, ExternalLink, Server, Copy, Check } from 'lucide-react'
import { Card, StatusBadge } from '../components/Card'
import { fetchConnections, fetchFilters } from '../lib/api'
import type { MQConnection } from '../lib/types'

export default function Connections() {
  const [connections, setConnections] = useState<MQConnection[]>([])
  const [filters, setFilters] = useState<any[]>([])
  const [loading, setLoading] = useState(true)
  const [copiedId, setCopiedId] = useState<string | null>(null)

  useEffect(() => {
    Promise.all([fetchConnections(), fetchFilters()])
      .then(([c, f]) => { setConnections(c as any); setFilters(f as any) })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  const copyId = (id: string) => {
    navigator.clipboard.writeText(id)
    setCopiedId(id)
    setTimeout(() => setCopiedId(null), 1500)
  }

  const getMQType = (conn: MQConnection) => {
    return conn.expand?.type?.TYPE || 'Unknown'
  }

  const getConnectionLabel = (conn: MQConnection) => {
    const type = getMQType(conn)
    if (type === 'IBM') return conn.connName || conn.queueName
    if (type === 'RabbitMQ') return conn.url || conn.queueName
    if (type === 'Kafka') return conn.brokers || conn.topic
    return conn.ownerName || conn.id
  }

  return (
    <div className="p-6 space-y-6 max-w-7xl">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-xl font-bold flex items-center gap-2">
            <Cable size={22} style={{ color: 'var(--teal)' }} />
            Connections
          </h2>
          <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>
            Configured message queue connections and active filters
          </p>
        </div>
        <a
          href="/_/"
          target="_blank"
          rel="noopener"
          className="flex items-center gap-1.5 text-xs px-3 py-2 rounded-lg border transition-colors"
          style={{ borderColor: 'var(--border)', color: 'var(--text-secondary)' }}
        >
          PocketBase Admin <ExternalLink size={12} />
        </a>
      </div>

      {loading ? (
        <div className="text-sm p-12 text-center" style={{ color: 'var(--text-secondary)' }}>Loading...</div>
      ) : (
        <>
          {/* MQ Connections */}
          <div>
            <h3 className="text-sm font-semibold mb-3 flex items-center gap-2">
              <Server size={16} style={{ color: 'var(--accent)' }} />
              MQ Connections
              <span className="text-xs ml-1 px-2 py-0.5 rounded-full"
                style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}
              >
                {connections.length}
              </span>
            </h3>
            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
              {connections.length === 0 ? (
                <Card>
                  <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>
                    No connections configured. Use PocketBase Admin to create MQ connections.
                  </p>
                </Card>
              ) : (
                connections.map((conn) => {
                  const type = getMQType(conn)
                  const typeColor = type === 'IBM' ? 'var(--accent)' : type === 'RabbitMQ' ? 'var(--orange)' : 'var(--teal)'

                  return (
                    <Card key={conn.id}>
                      <div className="flex items-start justify-between mb-3">
                        <div className="flex items-center gap-2">
                          <div
                            className="w-8 h-8 rounded-lg flex items-center justify-center text-xs font-bold"
                            style={{ background: `${typeColor}18`, color: typeColor }}
                          >
                            {type[0]}
                          </div>
                          <div>
                            <p className="text-sm font-medium">{conn.ownerName || 'Unnamed'}</p>
                            <p className="text-xs" style={{ color: 'var(--text-secondary)' }}>{type}</p>
                          </div>
                        </div>
                        <button
                          onClick={() => copyId(conn.id)}
                          className="p-1 rounded transition-colors"
                          title="Copy ID"
                          style={{ color: copiedId === conn.id ? 'var(--green)' : 'var(--text-secondary)' }}
                        >
                          {copiedId === conn.id ? <Check size={14} /> : <Copy size={14} />}
                        </button>
                      </div>
                      <div className="space-y-1.5 text-xs" style={{ color: 'var(--text-secondary)' }}>
                        <p className="font-mono truncate">{getConnectionLabel(conn)}</p>
                        {conn.queueName && <p>Queue: <span className="text-[var(--text-primary)]">{conn.queueName}</span></p>}
                        {conn.topic && <p>Topic: <span className="text-[var(--text-primary)]">{conn.topic}</span></p>}
                        <p className="font-mono text-[10px] opacity-50">{conn.id}</p>
                      </div>
                    </Card>
                  )
                })
              )}
            </div>
          </div>

          {/* Active Filters */}
          <div>
            <h3 className="text-sm font-semibold mb-3">Active Filters</h3>
            {filters.length === 0 ? (
              <Card>
                <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>
                  No filters configured. Create filters in PocketBase Admin to start message routing.
                </p>
              </Card>
            ) : (
              <div className="space-y-2">
                {filters.map((f: any) => (
                  <Card key={f.id}>
                    <div className="flex items-center gap-4 text-sm">
                      <span className="font-mono text-xs px-2 py-0.5 rounded"
                        style={{ background: 'var(--bg-tertiary)' }}
                      >
                        {f.id}
                      </span>
                      <span style={{ color: 'var(--green)' }}>Source: {f.expand?.source?.ownerName || f.source}</span>
                      <span style={{ color: 'var(--text-secondary)' }}>→</span>
                      <span style={{ color: 'var(--red)' }}>Dest: {f.expand?.destination?.ownerName || f.destination}</span>
                      {f.outputFormat && f.outputFormat !== 'same' && (
                        <StatusBadge status={f.outputFormat} />
                      )}
                    </div>
                  </Card>
                ))}
              </div>
            )}
          </div>
        </>
      )}
    </div>
  )
}
