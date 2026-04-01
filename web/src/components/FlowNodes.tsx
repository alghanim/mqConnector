import { memo } from 'react'
import { Handle, Position, type NodeProps } from '@xyflow/react'
import {
  ArrowRightFromLine, ArrowRightToLine, Filter, Repeat,
  Languages, GitBranch, Code, ShieldCheck,
} from 'lucide-react'

const nodeStyles: Record<string, { color: string; icon: typeof Filter; label: string }> = {
  source:    { color: 'var(--green)',  icon: ArrowRightFromLine, label: 'Source Queue' },
  destination: { color: 'var(--red)',  icon: ArrowRightToLine,  label: 'Destination Queue' },
  filter:    { color: 'var(--accent)', icon: Filter,            label: 'Field Filter' },
  transform: { color: 'var(--purple)', icon: Repeat,            label: 'Transform' },
  translate: { color: 'var(--yellow)', icon: Languages,         label: 'Format Translate' },
  route:     { color: 'var(--orange)', icon: GitBranch,         label: 'Content Router' },
  script:    { color: 'var(--teal)',   icon: Code,              label: 'Script' },
  validate:  { color: 'var(--pink)',   icon: ShieldCheck,       label: 'Schema Validate' },
}

function FlowNodeComponent({ data, selected }: NodeProps) {
  const nodeType = (data.nodeType as string) || 'filter'
  const style = nodeStyles[nodeType] || nodeStyles.filter
  const Icon = style.icon
  const subtitle = (data.subtitle as string) || ''

  return (
    <div
      className="rounded-xl border min-w-[200px] transition-shadow"
      style={{
        background: 'var(--bg-card)',
        borderColor: selected ? style.color : 'var(--border)',
        boxShadow: selected ? `0 0 0 2px ${style.color}33` : '0 2px 8px rgba(0,0,0,0.3)',
      }}
    >
      {nodeType !== 'source' && (
        <Handle
          type="target"
          position={Position.Left}
          style={{
            background: style.color,
            border: '2px solid var(--bg-card)',
            width: 12,
            height: 12,
          }}
        />
      )}

      <div
        className="px-4 py-3 flex items-center gap-3 border-b"
        style={{ borderColor: 'var(--border)' }}
      >
        <div
          className="w-8 h-8 rounded-lg flex items-center justify-center shrink-0"
          style={{ background: `${style.color}22`, color: style.color }}
        >
          <Icon size={16} />
        </div>
        <div className="min-w-0">
          <p className="text-sm font-medium truncate">
            {(data.label as string) || style.label}
          </p>
          {subtitle && (
            <p className="text-[11px] truncate" style={{ color: 'var(--text-secondary)' }}>
              {subtitle}
            </p>
          )}
        </div>
      </div>

      {typeof data.detail === 'string' && data.detail && (
        <div className="px-4 py-2">
          <p className="text-[11px] font-mono truncate" style={{ color: 'var(--text-secondary)' }}>
            {data.detail}
          </p>
        </div>
      )}

      {nodeType !== 'destination' && (
        <Handle
          type="source"
          position={Position.Right}
          style={{
            background: style.color,
            border: '2px solid var(--bg-card)',
            width: 12,
            height: 12,
          }}
        />
      )}
    </div>
  )
}

export const FlowNode = memo(FlowNodeComponent)
export const nodeTypes = { flowNode: FlowNode }
export { nodeStyles }
