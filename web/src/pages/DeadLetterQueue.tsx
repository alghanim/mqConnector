import { useEffect, useState } from 'react'
import { Inbox, RotateCcw, Trash2, ChevronLeft, ChevronRight, Eye } from 'lucide-react'
import { Card } from '../components/Card'
import { fetchDLQ, retryDLQ, deleteDLQ } from '../lib/api'
import type { DLQResponse, DLQEntry } from '../lib/types'

export default function DeadLetterQueue() {
  const [data, setData] = useState<DLQResponse | null>(null)
  const [page, setPage] = useState(1)
  const [selected, setSelected] = useState<DLQEntry | null>(null)
  const [loading, setLoading] = useState(true)

  const load = async () => {
    try {
      const result = await fetchDLQ(page)
      setData(result)
    } catch { /* ignore */ }
    setLoading(false)
  }

  useEffect(() => { load() }, [page])

  const handleRetry = async (id: string) => {
    await retryDLQ(id)
    load()
  }

  const handleDelete = async (id: string) => {
    await deleteDLQ(id)
    if (selected?.id === id) setSelected(null)
    load()
  }

  const totalPages = data ? Math.ceil(data.total / data.perPage) : 1

  return (
    <div className="p-6 space-y-6 max-w-7xl">
      <div>
        <h2 className="text-xl font-bold flex items-center gap-2">
          <Inbox size={22} style={{ color: 'var(--orange)' }} />
          Dead Letter Queue
        </h2>
        <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>
          Failed messages stored for inspection and retry
        </p>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        {/* Message list */}
        <div className="lg:col-span-2">
          <Card padding={false}>
            <div className="p-4 border-b flex items-center justify-between" style={{ borderColor: 'var(--border)' }}>
              <span className="text-sm font-medium">
                {data?.total ?? 0} message{(data?.total ?? 0) !== 1 ? 's' : ''}
              </span>
              <div className="flex items-center gap-2">
                <button
                  onClick={() => setPage(p => Math.max(1, p - 1))}
                  disabled={page <= 1}
                  className="p-1.5 rounded-lg disabled:opacity-30 transition-colors"
                  style={{ background: 'var(--bg-tertiary)' }}
                >
                  <ChevronLeft size={16} />
                </button>
                <span className="text-xs" style={{ color: 'var(--text-secondary)' }}>
                  {page} / {totalPages}
                </span>
                <button
                  onClick={() => setPage(p => Math.min(totalPages, p + 1))}
                  disabled={page >= totalPages}
                  className="p-1.5 rounded-lg disabled:opacity-30 transition-colors"
                  style={{ background: 'var(--bg-tertiary)' }}
                >
                  <ChevronRight size={16} />
                </button>
              </div>
            </div>

            {loading ? (
              <div className="p-12 text-center text-sm" style={{ color: 'var(--text-secondary)' }}>
                Loading...
              </div>
            ) : !data?.items?.length ? (
              <div className="p-12 text-center">
                <Inbox size={40} className="mx-auto mb-3 opacity-20" />
                <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>
                  No dead letters — all messages processed successfully
                </p>
              </div>
            ) : (
              <div className="divide-y" style={{ '--tw-divide-opacity': 1, borderColor: 'var(--border)' } as React.CSSProperties}>
                {data.items.map((entry) => (
                  <div
                    key={entry.id}
                    className="p-4 flex items-center gap-3 cursor-pointer transition-colors"
                    style={{
                      background: selected?.id === entry.id ? 'var(--bg-tertiary)' : undefined,
                      borderColor: 'var(--border)',
                    }}
                    onClick={() => setSelected(entry)}
                    onMouseEnter={e => { if (selected?.id !== entry.id) e.currentTarget.style.background = 'var(--bg-tertiary)' }}
                    onMouseLeave={e => { if (selected?.id !== entry.id) e.currentTarget.style.background = 'transparent' }}
                  >
                    <div
                      className="w-8 h-8 rounded-lg flex items-center justify-center shrink-0"
                      style={{ background: 'var(--red)18', color: 'var(--red)' }}
                    >
                      <Inbox size={14} />
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-medium truncate">{entry.error_reason}</p>
                      <p className="text-xs truncate" style={{ color: 'var(--text-secondary)' }}>
                        Filter: {entry.filter_id} · Source: {entry.source_queue} · Retries: {entry.retry_count}
                      </p>
                    </div>
                    <div className="flex items-center gap-1 shrink-0">
                      <button
                        onClick={e => { e.stopPropagation(); handleRetry(entry.id) }}
                        className="p-1.5 rounded-lg transition-colors"
                        style={{ color: 'var(--accent)' }}
                        title="Retry"
                      >
                        <RotateCcw size={14} />
                      </button>
                      <button
                        onClick={e => { e.stopPropagation(); handleDelete(entry.id) }}
                        className="p-1.5 rounded-lg transition-colors"
                        style={{ color: 'var(--red)' }}
                        title="Delete"
                      >
                        <Trash2 size={14} />
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Card>
        </div>

        {/* Detail panel */}
        <Card>
          <h3 className="text-sm font-semibold mb-4 flex items-center gap-2">
            <Eye size={16} style={{ color: 'var(--accent)' }} />
            Message Detail
          </h3>
          {selected ? (
            <div className="space-y-3 text-sm">
              <DetailRow label="ID" value={selected.id} mono />
              <DetailRow label="Error" value={selected.error_reason} />
              <DetailRow label="Filter" value={selected.filter_id} mono />
              <DetailRow label="Source" value={selected.source_queue} mono />
              <DetailRow label="Retries" value={String(selected.retry_count)} />
              <div>
                <p className="text-xs mb-1.5" style={{ color: 'var(--text-secondary)' }}>
                  Original Message
                </p>
                <pre
                  className="p-3 rounded-lg text-xs overflow-auto max-h-64 font-mono"
                  style={{ background: 'var(--bg-primary)', border: '1px solid var(--border)' }}
                >
                  {formatMessage(selected.original_message)}
                </pre>
              </div>
            </div>
          ) : (
            <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>
              Select a message to view details
            </p>
          )}
        </Card>
      </div>
    </div>
  )
}

function DetailRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <p className="text-xs" style={{ color: 'var(--text-secondary)' }}>{label}</p>
      <p className={`mt-0.5 ${mono ? 'font-mono text-xs' : ''}`}>{value}</p>
    </div>
  )
}

function formatMessage(msg: string): string {
  try {
    return JSON.stringify(JSON.parse(msg), null, 2)
  } catch {
    return msg
  }
}
