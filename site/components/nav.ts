// The docs tree. Order here is the order in the sidebar and in prev/next.
export type DocPage = { slug: string; title: string; blurb: string }
export type DocGroup = { title: string; pages: DocPage[] }

export const GITHUB = 'https://github.com/RapidNative/orchd'

export const DOC_GROUPS: DocGroup[] = [
  {
    title: 'Introduction',
    pages: [
      {
        slug: '',
        title: 'What ORCHD is',
        blurb: 'One daemon that provisions, routes, sleeps and wakes per-tenant workloads.',
      },
      {
        slug: 'why',
        title: 'Why it exists',
        blurb: 'The gap between a process manager and Kubernetes, and why nothing filled it.',
      },
      {
        slug: 'goals',
        title: 'Goals & non-goals',
        blurb: 'What ORCHD promises, what it deliberately refuses, and what that costs.',
      },
      {
        slug: 'quickstart',
        title: 'Quickstart',
        blurb: 'Build the daemon, provision a project, hit the endpoint.',
      },
    ],
  },
  {
    title: 'Concepts',
    pages: [
      {
        slug: 'concepts',
        title: 'Project, workload, route',
        blurb: 'The three records the whole system is built from.',
      },
      {
        slug: 'scale-to-zero',
        title: 'Scale-to-zero',
        blurb: 'The idle reaper, the wake path on the gateway, and keep-warm.',
      },
      {
        slug: 'substrates',
        title: 'Substrates & isolation',
        blurb: 'Host processes, Docker, gVisor, and why the driver is an interface.',
      },
      {
        slug: 'routing',
        title: 'Routing & TLS',
        blurb: 'Hostnames, subroutes, ports, custom domains, on-demand certificates.',
      },
      {
        slug: 'templates',
        title: 'Templates',
        blurb: 'orchd.json: one file that describes a multi-workload project.',
      },
      {
        slug: 'images',
        title: 'Images',
        blurb: 'Built images, docker images, registry pushes, import specs.',
      },
      {
        slug: 'keys',
        title: 'Keys & access',
        blurb: 'Bootstrap key, minted keys, roles, rate limits, error codes.',
      },
      {
        slug: 'backups',
        title: 'Backups',
        blurb: 'Per-workload snapshots, S3/R2 targets, control-plane state.',
      },
      {
        slug: 'regions',
        title: 'Regions',
        blurb: 'Placement targets, and the seam to multi-node.',
      },
      {
        slug: 'adaptors',
        title: 'Adaptors',
        blurb: 'Every replaceable part, its implementations, and how to switch.',
      },
    ],
  },
  {
    title: 'Using it',
    pages: [
      {
        slug: 'cli',
        title: 'The orchd CLI',
        blurb: 'The npm package that runs an orchd.json anywhere, with no control plane.',
      },
      {
        slug: 'local-dev',
        title: 'Local development',
        blurb: 'Port mode, wildcard-domain mode, and the three drivers.',
      },
      {
        slug: 'deploy',
        title: 'Deploying',
        blurb: 'The box, Caddy, systemd, and the full environment reference.',
      },
      {
        slug: 'api',
        title: 'API reference',
        blurb: 'Every /v1 endpoint, its role, request and response.',
      },
    ],
  },
  {
    title: 'Appendix',
    pages: [
      {
        slug: 'artifacts-and-substrates',
        title: 'Design note: artifacts',
        blurb: 'Splitting "where the bits come from" from "what executes them".',
      },
      { slug: 'roadmap', title: 'Roadmap', blurb: 'What is built, what is next, what is deferred.' },
    ],
  },
]

export const DOC_PAGES: DocPage[] = DOC_GROUPS.flatMap((g) => g.pages)

export function docHref(slug: string) {
  return slug === '' ? '/docs' : `/docs/${slug}`
}

export function neighbours(slug: string) {
  const i = DOC_PAGES.findIndex((p) => p.slug === slug)
  return { prev: i > 0 ? DOC_PAGES[i - 1] : null, next: i >= 0 ? (DOC_PAGES[i + 1] ?? null) : null }
}
