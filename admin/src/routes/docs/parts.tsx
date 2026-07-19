import * as React from 'react'
import { cn } from '@/lib/utils'

export const API_BASE = 'https://api.tinbase.dev'

// Sections drive both the docs layout TOC and the router. `path` is relative to
// /docs ('' = the index/About page).
export const SECTIONS = [
  { path: '', title: 'About tinbase cloud', short: 'About' },
  { path: 'repo', title: 'Repository & layout', short: 'Repository' },
  { path: 'images', title: 'Images & presets', short: 'Images' },
  { path: 'regions', title: 'Adding regions', short: 'Regions' },
  { path: 'adaptors', title: 'Adaptors', short: 'Adaptors' },
  { path: 'api', title: 'API reference', short: 'API reference' },
] as const

// ---- prose primitives ----
export const H = ({ children }: { children: React.ReactNode }) => (
  <h3 className="mb-1.5 mt-5 text-sm font-semibold text-foreground first:mt-0">{children}</h3>
)
export const P = ({ children }: { children: React.ReactNode }) => (
  <p className="mb-2.5 text-sm leading-relaxed text-muted-foreground">{children}</p>
)
export const UL = ({ children }: { children: React.ReactNode }) => (
  <ul className="mb-2.5 list-disc space-y-1 pl-5 text-sm leading-relaxed text-muted-foreground">
    {children}
  </ul>
)
export const M = ({ children }: { children: React.ReactNode }) => (
  <code className="font-mono text-[0.8em] text-foreground">{children}</code>
)
export const B = ({ children }: { children: React.ReactNode }) => (
  <b className="font-semibold text-foreground">{children}</b>
)
export const A = ({ href, children }: { href: string; children: React.ReactNode }) => (
  <a
    href={href}
    className="text-primary hover:underline"
    target={href.startsWith('http') ? '_blank' : undefined}
    rel={href.startsWith('http') ? 'noreferrer' : undefined}
  >
    {children}
  </a>
)

export function Code({ title, children }: { title?: string; children: string }) {
  return (
    <div className="mt-2">
      {title && (
        <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{title}</div>
      )}
      <pre className="mt-1 overflow-x-auto rounded-md border border-border bg-[#080b12] p-3 font-mono text-xs leading-relaxed text-muted-foreground">
        {children}
      </pre>
    </div>
  )
}

// ---- API reference primitives ----
export type Role = 'open' | 'readonly' | 'admin'
export type Ep = {
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  path: string
  role: Role
  desc: string
  params?: string
  req?: string
  res?: string
}
export type Group = { id: string; title: string; blurb?: string; endpoints: Ep[] }

const methodColor: Record<Ep['method'], string> = {
  GET: 'text-primary border-primary/40 bg-primary/10',
  POST: 'text-accent border-accent/40 bg-accent/10',
  PUT: 'text-warning border-warning/40 bg-warning/10',
  DELETE: 'text-destructive border-destructive/40 bg-destructive/10',
}

export function Method({ m }: { m: Ep['method'] }) {
  return (
    <span
      className={cn(
        'rounded-md border px-2 py-0.5 font-mono text-xs font-semibold',
        methodColor[m],
      )}
    >
      {m}
    </span>
  )
}

export function RoleTag({ role }: { role: Role }) {
  const label = role === 'open' ? 'no auth' : role === 'readonly' ? 'any key' : 'admin key'
  const cls =
    role === 'admin'
      ? 'text-warning border-warning/40'
      : role === 'open'
        ? 'text-muted-foreground border-border'
        : 'text-primary border-primary/40'
  return (
    <span
      className={cn(
        'rounded-full border px-2 py-0.5 font-mono text-[10px] uppercase tracking-wide',
        cls,
      )}
    >
      {label}
    </span>
  )
}

export function Endpoint({ e }: { e: Ep }) {
  return (
    <div className="border-b border-border/60 py-4 last:border-0">
      <div className="flex flex-wrap items-center gap-2.5">
        <Method m={e.method} />
        <code className="font-mono text-sm text-foreground">{e.path}</code>
        <RoleTag role={e.role} />
      </div>
      <p className="mt-1.5 text-sm text-muted-foreground">{e.desc}</p>
      {e.params && <p className="mt-1 text-xs text-muted-foreground">Params: {e.params}</p>}
      {e.req && <Code title="Request body">{e.req}</Code>}
      {e.res && <Code title="Response">{e.res}</Code>}
    </div>
  )
}
