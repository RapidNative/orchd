'use client'

import Link from 'next/link'
import { usePathname } from 'next/navigation'
import { DOC_GROUPS, docHref } from './nav'

export function DocsSidebar() {
  const pathname = usePathname().replace(/\/$/, '')

  return (
    <aside className="hidden w-56 shrink-0 lg:block">
      <nav className="sticky top-14 max-h-[calc(100vh-3.5rem)] overflow-y-auto py-12 pr-2 text-sm">
        {DOC_GROUPS.map((group) => (
          <div key={group.title} className="mb-6">
            <div className="mb-1.5 font-mono text-[0.66rem] tracking-[0.16em] text-dim uppercase">
              {group.title}
            </div>
            <ul className="space-y-px">
              {group.pages.map((page) => {
                const href = docHref(page.slug)
                const active = pathname === href
                return (
                  <li key={page.slug}>
                    <Link
                      href={href}
                      className={
                        'block rounded-md px-2 py-1 transition ' +
                        (active
                          ? 'bg-accent-soft font-medium text-accent-ink'
                          : 'text-muted hover:bg-bg-soft hover:text-ink')
                      }
                    >
                      {page.title}
                    </Link>
                  </li>
                )
              })}
            </ul>
          </div>
        ))}
      </nav>
    </aside>
  )
}
