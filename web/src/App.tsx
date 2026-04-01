import { Routes, Route, Navigate } from 'react-router-dom'
import { useState, useEffect } from 'react'
import Sidebar from './components/Sidebar'
import Dashboard from './pages/Dashboard'
import FlowBuilder from './pages/FlowBuilder'
import DeadLetterQueue from './pages/DeadLetterQueue'
import Connections from './pages/Connections'
import LoginPage from './pages/Login'
import { isAuthenticated } from './lib/api'

export default function App() {
  const [authed, setAuthed] = useState(isAuthenticated())

  useEffect(() => {
    const interval = setInterval(() => {
      setAuthed(isAuthenticated())
    }, 1000)
    return () => clearInterval(interval)
  }, [])

  if (!authed) {
    return <LoginPage onLogin={() => setAuthed(true)} />
  }

  return (
    <div className="flex h-screen overflow-hidden">
      <Sidebar onLogout={() => setAuthed(false)} />
      <main className="flex-1 overflow-auto" style={{ background: 'var(--bg-primary)' }}>
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/flow" element={<FlowBuilder />} />
          <Route path="/dlq" element={<DeadLetterQueue />} />
          <Route path="/connections" element={<Connections />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </div>
  )
}
