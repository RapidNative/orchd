// Family-site logos load from their own domains so they stay in sync when those
// sites update. RapidNative has no published logo URL, so it stays vendored.
const RELATED = [
  {
    name: 'RapidNative',
    href: 'https://rapidnative.com',
    logo: '/logos/rapidnative.svg',
    desc: 'AI that generates production-ready React Native apps and UIs from a prompt.',
  },
  {
    name: 'tinbase',
    href: 'https://tinbase.dev',
    logo: 'https://tinbase.dev/logo.svg',
    desc: 'A Supabase-compatible backend without Docker — one process, real Postgres, runs in the browser.',
  },
  {
    name: 'Lifo',
    href: 'https://lifo.sh',
    logo: 'https://lifo.sh/brand/lifo-logo.svg',
    desc: 'Linux APIs in the browser — real dev tooling with no VM and no container.',
  },
  {
    name: 'jetplane',
    href: 'https://sanketsahu.github.io/jetplane',
    logo: 'https://sanketsahu.github.io/jetplane/logo.svg',
    desc: 'A Metro plugin and a thin dev server for Expo — many dev environments per machine.',
  },
]

// A raw <img src> does not get basePath prepended the way next/image does, so
// vendored assets need it manually.
const BASE_PATH = process.env.NEXT_PUBLIC_BASE_PATH || ''
const asset = (p: string) => (p.startsWith('/') ? BASE_PATH + p : p)

export function Related() {
  return (
    <section className="mt-20 border-t border-line bg-bg-soft">
      <div className="mx-auto max-w-6xl px-5 py-14">
        <p className="font-mono text-[0.7rem] tracking-[0.18em] text-accent uppercase">
          from the same team
        </p>
        <h2 className="mt-2 text-lg font-semibold tracking-tight text-ink">Related projects</h2>
        <p className="mt-1 max-w-2xl text-sm text-muted">
          ORCHD is built by the makers of these open-source tools and products.
        </p>

        <div className="mt-6 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {RELATED.map((p) => (
            <a
              key={p.name}
              href={p.href}
              target="_blank"
              rel="noopener noreferrer"
              className="group flex items-start gap-3.5 rounded-xl border border-line bg-panel p-4 transition-colors hover:border-accent"
            >
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img
                src={asset(p.logo)}
                alt={`${p.name} logo`}
                width={36}
                height={36}
                className="h-9 w-9 shrink-0 rounded-md"
              />
              <div className="min-w-0">
                <span className="inline-flex items-center gap-1 font-semibold text-ink group-hover:text-accent-ink">
                  {p.name}
                  <span aria-hidden className="text-dim group-hover:text-accent">
                    ↗
                  </span>
                </span>
                <p className="mt-0.5 text-sm leading-relaxed text-muted">{p.desc}</p>
              </div>
            </a>
          ))}
        </div>
      </div>
    </section>
  )
}
