import { Link, Outlet } from '@tanstack/react-router'
import { PageHeader } from '@/components/bits'
import { SECTIONS } from './parts'

export function DocsLayout() {
  return (
    <div>
      <PageHeader
        title="Documentation"
        subtitle="What tinbase cloud is, how it fits together, and every API"
        actions={
          <a
            href="/docs.md"
            target="_blank"
            rel="noreferrer"
            className="rounded-md border border-border px-3 py-1.5 text-sm text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            Raw markdown ↗
          </a>
        }
      />

      <div className="grid gap-6 lg:grid-cols-[200px_1fr]">
        {/* TOC */}
        <nav className="hidden lg:block">
          <div className="sticky top-6 flex flex-col gap-1 text-sm">
            {SECTIONS.map((s) => (
              <Link
                key={s.path}
                to={s.path === '' ? '/docs' : '/docs/' + s.path}
                activeOptions={{ exact: s.path === '' }}
                className="rounded px-2 py-1 text-muted-foreground hover:bg-muted hover:text-foreground"
                activeProps={{ className: 'bg-muted text-foreground' }}
              >
                {s.title}
              </Link>
            ))}
          </div>
        </nav>

        <div className="min-w-0">
          <Outlet />
        </div>
      </div>
    </div>
  )
}
