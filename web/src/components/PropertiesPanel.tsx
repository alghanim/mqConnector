import { type Node } from '@xyflow/react'
import { Settings } from 'lucide-react'

interface PropertiesPanelProps {
  node: Node | null
  onChange: (id: string, data: Record<string, unknown>) => void
}

export default function PropertiesPanel({ node, onChange }: PropertiesPanelProps) {
  if (!node) {
    return (
      <div
        className="w-72 border-l p-5 shrink-0"
        style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border)' }}
      >
        <h3 className="text-sm font-semibold mb-3 flex items-center gap-2" style={{ color: 'var(--text-secondary)' }}>
          <Settings size={16} />
          Properties
        </h3>
        <p className="text-xs" style={{ color: 'var(--text-secondary)' }}>
          Select a node to edit its properties
        </p>
      </div>
    )
  }

  const data = node.data as Record<string, unknown>
  const nodeType = (data.nodeType as string) || 'filter'

  const update = (key: string, value: unknown) => {
    onChange(node.id, { ...data, [key]: value })
  }

  return (
    <div
      className="w-72 border-l p-5 shrink-0 overflow-y-auto"
      style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border)' }}
    >
      <h3 className="text-sm font-semibold mb-4 flex items-center gap-2">
        <Settings size={16} style={{ color: 'var(--accent)' }} />
        Properties
      </h3>

      <div className="space-y-4">
        <Field label="Label">
          <input
            value={(data.label as string) || ''}
            onChange={e => update('label', e.target.value)}
          />
        </Field>

        {(nodeType === 'source' || nodeType === 'destination') && (
          <Field label="Connection ID">
            <input
              value={(data.connectionId as string) || ''}
              onChange={e => update('connectionId', e.target.value)}
              placeholder="PocketBase MQS record ID"
              className="font-mono"
            />
          </Field>
        )}

        {nodeType === 'filter' && (
          <Field label="Field Paths (comma separated)">
            <textarea
              value={(data.paths as string) || ''}
              onChange={e => update('paths', e.target.value)}
              placeholder="phone, address.zip"
              rows={3}
            />
          </Field>
        )}

        {nodeType === 'transform' && (
          <>
            <Field label="Type">
              <select
                value={(data.transformType as string) || 'rename'}
                onChange={e => update('transformType', e.target.value)}
              >
                <option value="rename">Rename</option>
                <option value="mask">Mask</option>
                <option value="move">Move</option>
              </select>
            </Field>
            <Field label="Source Path">
              <input
                value={(data.sourcePath as string) || ''}
                onChange={e => update('sourcePath', e.target.value)}
                placeholder="order.phone"
              />
            </Field>
            <Field label="Target Path">
              <input
                value={(data.targetPath as string) || ''}
                onChange={e => update('targetPath', e.target.value)}
                placeholder="order.contact_number"
              />
            </Field>
            {data.transformType === 'mask' && (
              <>
                <Field label="Mask Pattern (regex)">
                  <input
                    value={(data.maskPattern as string) || ''}
                    onChange={e => update('maskPattern', e.target.value)}
                    placeholder="\\d{4}"
                  />
                </Field>
                <Field label="Replacement">
                  <input
                    value={(data.maskReplace as string) || ''}
                    onChange={e => update('maskReplace', e.target.value)}
                    placeholder="****"
                  />
                </Field>
              </>
            )}
          </>
        )}

        {nodeType === 'translate' && (
          <Field label="Output Format">
            <select
              value={(data.outputFormat as string) || 'same'}
              onChange={e => update('outputFormat', e.target.value)}
            >
              <option value="same">Same as input</option>
              <option value="JSON">JSON</option>
              <option value="XML">XML</option>
            </select>
          </Field>
        )}

        {nodeType === 'route' && (
          <>
            <Field label="Condition Path">
              <input
                value={(data.conditionPath as string) || ''}
                onChange={e => update('conditionPath', e.target.value)}
                placeholder="order.region"
              />
            </Field>
            <Field label="Operator">
              <select
                value={(data.operator as string) || 'eq'}
                onChange={e => update('operator', e.target.value)}
              >
                {['eq', 'neq', 'contains', 'regex', 'gt', 'lt', 'exists'].map(op => (
                  <option key={op} value={op}>{op}</option>
                ))}
              </select>
            </Field>
            <Field label="Value">
              <input
                value={(data.conditionValue as string) || ''}
                onChange={e => update('conditionValue', e.target.value)}
                placeholder="EU"
              />
            </Field>
          </>
        )}

        {nodeType === 'script' && (
          <Field label="Script">
            <textarea
              value={(data.script as string) || ''}
              onChange={e => update('script', e.target.value)}
              placeholder={'msg.timestamp = Date.now()\ndelete msg.internal\nmsg'}
              rows={6}
              className="font-mono text-xs"
            />
          </Field>
        )}

        {nodeType === 'validate' && (
          <>
            <Field label="Schema Type">
              <select
                value={(data.schemaType as string) || 'JSON_SCHEMA'}
                onChange={e => update('schemaType', e.target.value)}
              >
                <option value="JSON_SCHEMA">JSON Schema</option>
                <option value="XSD">XSD</option>
              </select>
            </Field>
            <Field label="Schema">
              <textarea
                value={(data.schema as string) || ''}
                onChange={e => update('schema', e.target.value)}
                rows={6}
                className="font-mono text-xs"
                placeholder={'{"type":"object","required":["name"]}'}
              />
            </Field>
          </>
        )}
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="block text-[11px] font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>
        {label}
      </label>
      <div
        className="[&_input]:w-full [&_input]:px-3 [&_input]:py-2 [&_input]:rounded-lg [&_input]:text-sm [&_input]:outline-none
                    [&_select]:w-full [&_select]:px-3 [&_select]:py-2 [&_select]:rounded-lg [&_select]:text-sm [&_select]:outline-none
                    [&_textarea]:w-full [&_textarea]:px-3 [&_textarea]:py-2 [&_textarea]:rounded-lg [&_textarea]:text-sm [&_textarea]:outline-none [&_textarea]:resize-y"
        style={{
          '--input-bg': 'var(--bg-primary)',
          '--input-border': 'var(--border)',
          '--input-color': 'var(--text-primary)',
        } as React.CSSProperties}
      >
        <style>{`
          .properties-input input, .properties-input select, .properties-input textarea {
            background: var(--bg-primary);
            border: 1px solid var(--border);
            color: var(--text-primary);
          }
        `}</style>
        <div className="properties-input">{children}</div>
      </div>
    </div>
  )
}
