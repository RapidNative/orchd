import { Link, Outlet } from '@tanstack/react-router'
import { auth, useApiKey } from '@/lib/auth'
import { KeyGate } from './key-gate'
import {
  IconActivity,
  IconBackups,
  IconDashboard,
  IconDatabase,
  IconDocs,
  IconLogout,
  IconProjects,
  IconSettings,
  TinbaseLogo,
} from './icons'

const nav = [
  { to: '/', label: 'Dashboard', Icon: IconDashboard, exact: true },
  { to: '/projects', label: 'Projects', Icon: IconProjects, exact: false },
  { to: '/backups', label: 'Backups', Icon: IconBackups, exact: false },
  { to: '/activity', label: 'Activity', Icon: IconActivity, exact: false },
  { to: '/system', label: 'System', Icon: IconDatabase, exact: false },
  { to: '/settings', label: 'Settings', Icon: IconSettings, exact: false },
  { to: '/docs', label: 'API Docs', Icon: IconDocs, exact: false },
]

export function RootLayout() {
  const key = useApiKey()
  if (!key) return <KeyGate />
  return (
    <div className="flex min-h-screen">
      <aside className="flex w-56 shrink-0 flex-col border-r border-border p-4">
        <div className="flex items-center gap-2 px-2 pb-6 pt-1">
          <TinbaseLogo className="text-2xl" />
          <span className="text-sm font-semibold">tinbase cloud</span>
        </div>
        <nav className="flex flex-col gap-1">
          {nav.map(({ to, label, Icon, exact }) => (
            <Link
              key={to}
              to={to}
              activeOptions={{ exact }}
              className="flex items-center gap-2.5 rounded-md px-3 py-2 text-sm text-muted-foreground hover:bg-muted hover:text-foreground"
              activeProps={{ className: 'bg-muted text-foreground' }}
            >
              <Icon /> {label}
            </Link>
          ))}
        </nav>
        <button
          onClick={() => auth.clear()}
          className="mt-auto flex cursor-pointer items-center gap-2.5 rounded-md px-3 py-2 text-sm text-muted-foreground hover:text-foreground"
        >
          <IconLogout /> Disconnect
        </button>
      </aside>
      <main className="min-w-0 flex-1 px-8 py-7">
        <div className="mx-auto max-w-6xl">
          <Outlet />
        </div>
      </main>
    </div>
  )
}
