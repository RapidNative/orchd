import type { Metadata } from 'next'
import Link from 'next/link'
import './globals.css'
import { SiteHeader } from '@/components/site-header'
import { GITHUB } from '@/components/nav'
import { Related } from '@/components/related'

export const metadata: Metadata = {
  title: {
    default: 'ORCHD — the open-source, self-hosted Fly.io alternative',
    template: '%s · ORCHD',
  },
  description:
    'The open-source, self-hosted alternative to Fly.io. ORCHD is a single Go daemon that provisions per-tenant workloads, routes them by hostname, isolates them, sleeps them when idle and wakes them on the next request.',
}

// Set the theme class before first paint so there is no flash. Kept inline and
// tiny on purpose; the toggle component takes over afterwards.
const themeScript = `
try {
  var stored = localStorage.getItem('orchd-theme');
  var dark = stored ? stored === 'dark' : window.matchMedia('(prefers-color-scheme: dark)').matches;
  if (dark) document.documentElement.classList.add('dark');
} catch (e) {}
`

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeScript }} />
      </head>
      <body>
        <SiteHeader />
        {children}
        <Related />
        <footer className="border-t border-line bg-bg">
          <div className="mx-auto flex max-w-6xl flex-col gap-3 px-5 py-8 text-sm text-dim sm:flex-row sm:items-center">
            <div>
              <span className="text-muted">ORCHD</span> — one orchestrator, one box, many tenants.
            </div>
            <div className="flex gap-4 sm:ml-auto">
              <Link href="/docs" className="hover:text-ink">
                Docs
              </Link>
              <Link href="/docs/api" className="hover:text-ink">
                API
              </Link>
              <a href={GITHUB} className="hover:text-ink">
                GitHub
              </a>
            </div>
          </div>
        </footer>
      </body>
    </html>
  )
}
