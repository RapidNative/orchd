'use client'

import Link from 'next/link'
import { usePathname } from 'next/navigation'
import { DOC_PAGES, docHref, neighbours } from './nav'

// Prev/next links, derived from the current path so MDX pages carry no wiring.
export function PageNav() {
  const pathname = usePathname().replace(/\/$/, '')
  const current = DOC_PAGES.find((p) => docHref(p.slug) === pathname)
  if (!current) return null
  const { prev, next } = neighbours(current.slug)
  if (!prev && !next) return null

  return (
    <nav className="mt-14 grid gap-3 border-t border-line pt-6 sm:grid-cols-2">
      {prev ? (
        <Link
          href={docHref(prev.slug)}
          className="rounded-lg border border-line px-4 py-3 transition hover:border-accent"
        >
          <div className="font-mono text-[0.66rem] tracking-[0.14em] text-dim uppercase">
            ← Previous
          </div>
          <div className="mt-0.5 text-sm font-medium text-ink">{prev.title}</div>
        </Link>
      ) : (
        <span />
      )}
      {next && (
        <Link
          href={docHref(next.slug)}
          className="rounded-lg border border-line px-4 py-3 text-right transition hover:border-accent sm:col-start-2"
        >
          <div className="font-mono text-[0.66rem] tracking-[0.14em] text-dim uppercase">
            Next →
          </div>
          <div className="mt-0.5 text-sm font-medium text-ink">{next.title}</div>
        </Link>
      )}
    </nav>
  )
}
