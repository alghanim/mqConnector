import { nodeStyles } from './FlowNodes'

interface NodePanelProps {
  onDragStart: (event: React.DragEvent, nodeType: string) => void
}

export default function NodePanel({ onDragStart }: NodePanelProps) {
  const categories = [
    {
      title: 'CONNECTIONS',
      items: ['source', 'destination'],
    },
    {
      title: 'PROCESSING',
      items: ['filter', 'transform', 'translate', 'route', 'script', 'validate'],
    },
  ]

  return (
    <div
      className="w-56 border-r shrink-0 overflow-y-auto"
      style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border)' }}
    >
      {categories.map(cat => (
        <div key={cat.title} className="p-3">
          <p
            className="text-[10px] font-semibold tracking-widest mb-2 px-1"
            style={{ color: 'var(--text-secondary)' }}
          >
            {cat.title}
          </p>
          <div className="space-y-1.5">
            {cat.items.map(type => {
              const style = nodeStyles[type]
              if (!style) return null
              const Icon = style.icon
              return (
                <div
                  key={type}
                  draggable
                  onDragStart={e => onDragStart(e, type)}
                  className="flex items-center gap-2.5 px-3 py-2.5 rounded-lg cursor-grab active:cursor-grabbing border transition-all hover:translate-x-0.5"
                  style={{
                    background: 'var(--bg-tertiary)',
                    borderColor: 'var(--border)',
                  }}
                >
                  <div
                    className="w-6 h-6 rounded flex items-center justify-center shrink-0"
                    style={{ background: `${style.color}22`, color: style.color }}
                  >
                    <Icon size={13} />
                  </div>
                  <span className="text-xs font-medium">{style.label}</span>
                </div>
              )
            })}
          </div>
        </div>
      ))}
    </div>
  )
}
