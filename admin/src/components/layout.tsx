import { useState } from 'react'
import { Link, Outlet } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { auth, useApiKey } from '@/lib/auth'
import { KeyGate } from './key-gate'
import {
  IconActivity,
  IconBackups,
  IconDashboard,
  IconDatabase,
  IconDocs,
  IconImage,
  IconLogout,
  IconMenu,
  IconProjects,
  IconSettings,
  IconTemplate,
  BrandLogo,
} from './icons'

// The documentation is a separate static site (built from site/ in this repo).
const DOCS_URL = 'https://rapidnative.github.io/orchd/docs/'

const nav = [
  { to: '/', label: 'Dashboard', Icon: IconDashboard, exact: true },
  { to: '/projects', label: 'Projects', Icon: IconProjects, exact: false },
  { to: '/backups', label: 'Backups', Icon: IconBackups, exact: false },
  { to: '/templates', label: 'Templates', Icon: IconTemplate, exact: false },
  { to: '/images', label: 'Images', Icon: IconImage, exact: false },
  { to: '/activity', label: 'Activity', Icon: IconActivity, exact: false },
  { to: '/system', label: 'System', Icon: IconDatabase, exact: false },
  { to: '/settings', label: 'Settings', Icon: IconSettings, exact: false },
]

function Brand() {
  const info = useQuery({ queryKey: ['info'], queryFn: api.info })
  const name = info.data?.instance_name
  return (
    <div className="flex items-center gap-2 px-2">
      <BrandLogo className="text-2xl" />
      <div className="min-w-0 leading-tight">
        <div className="text-sm font-semibold">ORCHD</div>
        <div className="truncate text-xs text-muted-foreground">{name || 'unnamed instance'}</div>
      </div>
    </div>
  )
}

function SidebarBody({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <>
      <Brand />
      <nav className="mt-6 flex flex-col gap-1">
        {nav.map(({ to, label, Icon, exact }) => (
          <Link
            key={to}
            to={to}
            activeOptions={{ exact }}
            onClick={onNavigate}
            className="flex items-center gap-2.5 rounded-md px-3 py-2 text-sm text-muted-foreground hover:bg-muted hover:text-foreground"
            activeProps={{ className: 'bg-muted text-foreground' }}
          >
            <Icon /> {label}
          </Link>
        ))}
        {/* Docs live on the public site, not in the panel. */}
        <a
          href={DOCS_URL}
          target="_blank"
          rel="noreferrer"
          onClick={onNavigate}
          className="flex items-center gap-2.5 rounded-md px-3 py-2 text-sm text-muted-foreground hover:bg-muted hover:text-foreground"
        >
          <IconDocs /> Docs <span className="text-xs">↗</span>
        </a>
      </nav>
      <button
        onClick={() => auth.clear()}
        className="mt-auto flex cursor-pointer items-center gap-2.5 rounded-md px-3 py-2 text-sm text-muted-foreground hover:text-foreground"
      >
        <IconLogout /> Disconnect
      </button>
    </>
  )
}

export function RootLayout() {
  const key = useApiKey()
  const [open, setOpen] = useState(false)
  if (!key) return <KeyGate />

  return (
    <div className="flex min-h-screen">
      {/* Persistent sidebar (lg and up) */}
      <aside className="hidden w-56 shrink-0 flex-col border-r border-border p-4 lg:flex">
        <SidebarBody />
      </aside>

      {/* Off-canvas drawer (below lg) */}
      {open && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="absolute inset-0 bg-black/60" onClick={() => setOpen(false)} />
          <aside className="absolute inset-y-0 left-0 flex w-64 flex-col border-r border-border bg-background p-4">
            <SidebarBody onNavigate={() => setOpen(false)} />
          </aside>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        {/* Mobile top bar */}
        <header className="flex items-center gap-3 border-b border-border px-4 py-3 lg:hidden">
          <button
            onClick={() => setOpen(true)}
            aria-label="Open menu"
            className="flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            <IconMenu />
          </button>
          <BrandLogo className="text-xl" />
          <span className="text-sm font-semibold">ORCHD</span>
        </header>

        <main className="min-w-0 flex-1 px-4 py-5 sm:px-6 lg:px-8 lg:py-7">
          <div className="mx-auto max-w-6xl">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
