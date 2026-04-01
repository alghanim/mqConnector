import { useCallback, useRef, useState } from 'react'
import {
  ReactFlow,
  Controls,
  MiniMap,
  Background,
  BackgroundVariant,
  addEdge,
  useNodesState,
  useEdgesState,
  type OnConnect,
  type Node,
  type Edge,
  type ReactFlowInstance,
} from '@xyflow/react'
import { Save, Upload, Trash2 } from 'lucide-react'
import { nodeTypes } from '../components/FlowNodes'
import NodePanel from '../components/NodePanel'
import PropertiesPanel from '../components/PropertiesPanel'

let idCounter = 0
const getId = () => `node_${++idCounter}`

const defaultEdgeStyle = {
  stroke: '#6366f1',
  strokeWidth: 2,
}

export default function FlowBuilder() {
  const reactFlowWrapper = useRef<HTMLDivElement>(null)
  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([])
  const [rfInstance, setRfInstance] = useState<ReactFlowInstance | null>(null)
  const [selectedNode, setSelectedNode] = useState<Node | null>(null)
  const [saved, setSaved] = useState(false)

  const onConnect: OnConnect = useCallback(
    (params) => setEdges((eds) => addEdge({ ...params, style: defaultEdgeStyle, animated: true }, eds)),
    [setEdges]
  )

  const onNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    setSelectedNode(node)
  }, [])

  const onPaneClick = useCallback(() => {
    setSelectedNode(null)
  }, [])

  const onDragStart = (event: React.DragEvent, nodeType: string) => {
    event.dataTransfer.setData('application/reactflow', nodeType)
    event.dataTransfer.effectAllowed = 'move'
  }

  const onDragOver = useCallback((event: React.DragEvent) => {
    event.preventDefault()
    event.dataTransfer.dropEffect = 'move'
  }, [])

  const onDrop = useCallback(
    (event: React.DragEvent) => {
      event.preventDefault()
      const nodeType = event.dataTransfer.getData('application/reactflow')
      if (!nodeType || !rfInstance || !reactFlowWrapper.current) return

      const bounds = reactFlowWrapper.current.getBoundingClientRect()
      const position = rfInstance.screenToFlowPosition({
        x: event.clientX - bounds.left,
        y: event.clientY - bounds.top,
      })

      const newNode: Node = {
        id: getId(),
        type: 'flowNode',
        position,
        data: {
          nodeType,
          label: '',
          subtitle: '',
          detail: '',
        },
      }

      setNodes((nds) => [...nds, newNode])
    },
    [rfInstance, setNodes]
  )

  const updateNodeData = useCallback(
    (nodeId: string, data: Record<string, unknown>) => {
      setNodes((nds) =>
        nds.map((n) => (n.id === nodeId ? { ...n, data } : n))
      )
      setSelectedNode((prev) => (prev?.id === nodeId ? { ...prev, data } : prev))
    },
    [setNodes]
  )

  const saveFlow = () => {
    if (!rfInstance) return
    const flow = rfInstance.toObject()
    localStorage.setItem('mqconnector_flow_v2', JSON.stringify(flow))
    setSaved(true)
    setTimeout(() => setSaved(false), 2000)
  }

  const loadFlow = () => {
    const saved = localStorage.getItem('mqconnector_flow_v2')
    if (!saved) return
    try {
      const flow = JSON.parse(saved)
      setNodes(flow.nodes || [])
      setEdges(flow.edges || [])
    } catch { /* ignore */ }
  }

  const clearFlow = () => {
    if (!confirm('Clear all nodes and connections?')) return
    setNodes([])
    setEdges([])
    setSelectedNode(null)
  }

  return (
    <div className="h-full flex flex-col">
      {/* Toolbar */}
      <div
        className="flex items-center justify-between px-4 py-2.5 border-b shrink-0"
        style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border)' }}
      >
        <h2 className="text-sm font-semibold">Flow Builder</h2>
        <div className="flex items-center gap-2">
          <button onClick={clearFlow} className="toolbar-btn" title="Clear">
            <Trash2 size={14} /> Clear
          </button>
          <button onClick={loadFlow} className="toolbar-btn" title="Load">
            <Upload size={14} /> Load
          </button>
          <button onClick={saveFlow} className="toolbar-btn primary" title="Save">
            <Save size={14} /> {saved ? 'Saved!' : 'Save'}
          </button>
        </div>
      </div>

      <style>{`
        .toolbar-btn {
          display: flex; align-items: center; gap: 6px;
          padding: 6px 12px; border-radius: 6px; font-size: 12px;
          background: var(--bg-tertiary); border: 1px solid var(--border);
          color: var(--text-secondary); cursor: pointer; transition: all 0.15s;
        }
        .toolbar-btn:hover { border-color: var(--accent); color: var(--text-primary); }
        .toolbar-btn.primary { background: var(--accent); border-color: var(--accent); color: #fff; }
        .toolbar-btn.primary:hover { background: var(--accent-hover); }
      `}</style>

      {/* Canvas area */}
      <div className="flex flex-1 overflow-hidden">
        <NodePanel onDragStart={onDragStart} />

        <div className="flex-1" ref={reactFlowWrapper}>
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onInit={setRfInstance}
            onDrop={onDrop}
            onDragOver={onDragOver}
            onNodeClick={onNodeClick}
            onPaneClick={onPaneClick}
            nodeTypes={nodeTypes}
            defaultEdgeOptions={{ style: defaultEdgeStyle, animated: true }}
            fitView
            proOptions={{ hideAttribution: true }}
            style={{ background: 'var(--bg-primary)' }}
          >
            <Controls />
            <MiniMap
              nodeStrokeWidth={3}
              style={{ background: 'var(--bg-secondary)' }}
            />
            <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="#2a2a4a" />
          </ReactFlow>
        </div>

        <PropertiesPanel node={selectedNode} onChange={updateNodeData} />
      </div>
    </div>
  )
}
