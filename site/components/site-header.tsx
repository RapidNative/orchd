import Link from 'next/link'
import { GITHUB } from './nav'
import { ThemeToggle } from './theme-toggle'

export function SiteHeader() {
  return (
    <header className="sticky top-0 z-30 border-b border-line bg-bg/85 backdrop-blur-md">
      <div className="mx-auto flex h-14 max-w-6xl items-center gap-5 px-5">
        <Link href="/" className="flex items-center gap-2.5 font-semibold tracking-tight">
          <Mark />
          <span>
            ORCHD
            <span className="ml-2 hidden font-mono text-[0.68rem] font-normal tracking-widest text-dim uppercase sm:inline">
              orchestrator
            </span>
          </span>
        </Link>

        <nav className="ml-auto flex items-center gap-1 text-sm">
          <NavLink href="/docs">Docs</NavLink>
          <NavLink href="/docs/api">API</NavLink>
          <NavLink href="/docs/cli">CLI</NavLink>
          <a
            href={GITHUB}
            className="rounded-md px-2.5 py-1.5 text-muted transition hover:bg-bg-soft hover:text-ink"
          >
            GitHub&nbsp;↗
          </a>
          <span className="mx-1 hidden h-5 w-px bg-line sm:block" />
          <ThemeToggle />
        </nav>
      </div>
    </header>
  )
}

function NavLink({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <Link
      href={href}
      className="rounded-md px-2.5 py-1.5 text-muted transition hover:bg-bg-soft hover:text-ink"
    >
      {children}
    </Link>
  )
}

export function Mark() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" className="text-accent">
      <circle cx="12" cy="12" r="3.2" fill="currentColor" />
      <circle cx="12" cy="12" r="8.4" fill="none" stroke="currentColor" strokeWidth="1.3" opacity="0.45" />
      <circle cx="12" cy="3.6" r="1.7" fill="currentColor" />
      <circle cx="19.3" cy="16.2" r="1.7" fill="currentColor" opacity="0.75" />
      <circle cx="4.7" cy="16.2" r="1.7" fill="currentColor" opacity="0.5" />
    </svg>
  )
}
