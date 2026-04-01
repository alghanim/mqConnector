import { NavLink } from 'react-router-dom'
import { LayoutDashboard, GitBranch, Inbox, Cable, LogOut } from 'lucide-react'
import { logout } from '../lib/api'

const navItems = [
  { to: '/', icon: LayoutDashboard, label: 'Dashboard' },
  { to: '/flow', icon: GitBranch, label: 'Flow Builder' },
  { to: '/dlq', icon: Inbox, label: 'Dead Letters' },
  { to: '/connections', icon: Cable, label: 'Connections' },
]

export default function Sidebar({ onLogout }: { onLogout: () => void }) {
  return (
    <aside
      className="w-60 flex flex-col border-r shrink-0"
      style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border)' }}
    >
      <div className="p-5 border-b" style={{ borderColor: 'var(--border)' }}>
        <h1 className="text-lg font-bold tracking-tight">
          <span style={{ color: 'var(--accent)' }}>mq</span>Connector
        </h1>
        <p className="text-xs mt-1" style={{ color: 'var(--text-secondary)' }}>
          Message Queue Platform
        </p>
      </div>

      <nav className="flex-1 p-3 space-y-1">
        {navItems.map(({ to, icon: Icon, label }) => (
          <NavLink
            key={to}
            to={to}
            end={to === '/'}
            className={({ isActive }) =>
              `flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm transition-all ${
                isActive
                  ? 'font-medium'
                  : 'hover:translate-x-0.5'
              }`
            }
            style={({ isActive }) => ({
              background: isActive ? 'var(--bg-tertiary)' : 'transparent',
              color: isActive ? 'var(--text-primary)' : 'var(--text-secondary)',
              border: isActive ? '1px solid var(--border)' : '1px solid transparent',
            })}
          >
            <Icon size={18} />
            {label}
          </NavLink>
        ))}
      </nav>

      <div className="p-3 border-t" style={{ borderColor: 'var(--border)' }}>
        <button
          onClick={() => { logout(); onLogout() }}
          className="flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm w-full transition-all hover:translate-x-0.5"
          style={{ color: 'var(--text-secondary)' }}
        >
          <LogOut size={18} />
          Sign Out
        </button>
      </div>
    </aside>
  )
}
