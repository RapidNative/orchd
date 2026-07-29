import Link from 'next/link'
import { GITHUB } from '@/components/nav'

export default function Home() {
  return (
    <div>
      {/* ---------------------------------------------------------------- hero */}
      <section className="relative overflow-hidden border-b border-line">
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 opacity-70"
          style={{
            background:
              'radial-gradient(760px 340px at 12% -10%, color-mix(in oklab, var(--accent) 16%, transparent), transparent 62%), radial-gradient(680px 320px at 92% 0%, color-mix(in oklab, var(--blue) 13%, transparent), transparent 58%)',
          }}
        />
        <div className="relative mx-auto max-w-6xl px-5 py-20 lg:py-28">
          <p className="font-mono text-[0.7rem] tracking-[0.18em] text-accent uppercase">
            the open-source, self-hosted Fly.io alternative
          </p>
          <h1 className="mt-5 max-w-3xl text-4xl leading-[1.05] font-bold tracking-tight text-ink sm:text-5xl lg:text-6xl">
            Run a thousand small apps
            <br />
            on <span className="text-accent">one box.</span>
          </h1>
          <p className="mt-6 max-w-2xl text-lg text-muted">
            Hostname routing, per-tenant isolation and scale-to-zero, without the platform bill.
            ORCHD is a single Go daemon that provisions per-tenant workloads, gives each one a
            hostname, caps and isolates it, puts it to sleep when nobody is using it, and wakes it on
            the next request in about a second. No cluster, no control-plane sprawl, no YAML.
          </p>

          <div className="mt-9 flex flex-wrap items-center gap-3">
            <Link
              href="/docs"
              className="rounded-lg bg-accent px-5 py-2.5 text-sm font-semibold text-bg transition hover:opacity-90"
            >
              Read the docs
            </Link>
            <Link
              href="/docs/quickstart"
              className="rounded-lg border border-line px-5 py-2.5 text-sm font-medium text-ink transition hover:border-accent"
            >
              Quickstart
            </Link>
            <a
              href={GITHUB}
              className="rounded-lg px-3 py-2.5 text-sm text-muted transition hover:text-ink"
            >
              GitHub ↗
            </a>
          </div>

          <div className="mt-14 grid max-w-4xl grid-cols-2 gap-x-8 gap-y-6 sm:grid-cols-4">
            <Stat n="~0.9 s" label="wake from zero" />
            <Stat n="~2.8 s" label="cold provision" />
            <Stat n="0 MB" label="RAM while idle" />
            <Stat n="1 binary" label="control plane + gateway" />
          </div>
          <p className="mt-4 font-mono text-[0.7rem] text-dim">
            measured on a Hetzner Cloud VM under gVisor, tinbase workload incl. initdb
          </p>
        </div>
      </section>

      {/* ------------------------------------------------------------- problem */}
      <Section
        eyebrow="the problem"
        title="Idle tenants are the expensive ones"
        lead="Preview environments, per-customer backends, per-project dev servers, agent sandboxes — they all have the same shape: many small workloads, each needing its own URL and its own data, almost all of them idle almost all of the time."
      >
        <div className="grid gap-4 sm:grid-cols-3">
          <Card title="A process manager is too little">
            systemd or pm2 will keep N processes alive, but nothing hands out hostnames, mints
            credentials, isolates a tenant from its neighbour, or reclaims the memory of the 900
            tenants nobody is using.
          </Card>
          <Card title="Kubernetes is too much">
            A cluster to run 40 MB dev servers means an API server, etcd, a CNI, an ingress
            controller and a knative-shaped add-on before the first request is served — and someone
            to keep all of it alive.
          </Card>
          <Card title="A PaaS is someone else's box">
            Per-app pricing on a platform you do not operate stops being an option the moment your
            unit is a free-tier tenant, or the code you run is untrusted, or the margin is the
            product.
          </Card>
        </div>
      </Section>

      {/* --------------------------------------------------------------- shape */}
      <Section
        eyebrow="the shape"
        title="One daemon, three records"
        lead="Everything ORCHD does is expressed with a project, a workload and a route. The control plane provisions and mints; the gateway resolves hostnames, wakes what is asleep, and proxies. Both live in the same binary."
      >
        <figure className="rounded-xl border border-line bg-panel p-5 shadow-[var(--shadow)]">
          <pre className="overflow-x-auto font-mono text-[0.78rem] leading-relaxed text-muted">
            {`                     *.example.com          (wildcard DNS -> the box)
                            |
                     +--------------+   gateway: Host -> route table -> workload,
                     |  gateway     |   wake if suspended, reverse-proxy
                     |  :8081       |   (HTTP, WebSockets, streaming)
                     +--------------+
                            |
        +-------------------+-------------------+
   +----------+        +----------+        +----------+     one instance per
   | workload |        | workload |        | workload |     workload, its own
   | + volume |        | + volume |        | + volume |     volume, suspended
   +----------+        +----------+        +----------+     when idle
                            ^
                     +--------------+   control plane: create projects and
                     |  API :8080   |   workloads, mint keys, attach domains,
                     +--------------+   back up, place in a region`}
          </pre>
          <figcaption>
            The runtime under the workloads is a swappable driver: host processes locally, Docker +
            gVisor on a Linux box, microVMs later. Nothing above the driver changes.
          </figcaption>
        </figure>

        <div className="mt-5 grid gap-4 sm:grid-cols-3">
          <Card title="Project" mono="grouping">
            A tenant, an environment, a customer. Owns workloads and cascades on delete.
          </Card>
          <Card title="Workload" mono="the unit">
            One routable, independently scheduled, scale-to-zero instance with a volume, resource
            caps and env.
          </Card>
          <Card title="Route" mono="addressing">
            A hostname pointing at a workload. Default subdomain, custom domains, or a local port.
          </Card>
        </div>
      </Section>

      {/* -------------------------------------------------------------- how-to */}
      <Section
        eyebrow="in practice"
        title="A project is one call away"
        lead="The control plane is an ordinary JSON API behind a bearer key. Everything the admin panel does, you can do with curl — or with a template, which describes a whole multi-workload project in one file that lives in the repo."
      >
        <div className="grid gap-4 lg:grid-cols-2">
          <CodeCard title="provision, then use the endpoint">{`curl -X POST https://api.example.com/v1/projects \\
  -H "Authorization: Bearer $ORCHD_KEY" \\
  -d '{"name":"acme","workloads":[{"preset":"vite"},{"preset":"api"}]}'

# -> workloads, each with routes, endpoints and keys.
#    First request through the gateway wakes them.`}</CodeCard>
          <CodeCard title="orchd.json — a template in the repo">{`{
  "name": "acme",
  "workloads": [
    { "name": "db",  "kind": "tinbase" },
    { "name": "api", "kind": "node", "dir": "api",
      "install": ["npm","install"],
      "run": ["node","index.js"], "port_env": "PORT" },
    { "name": "web", "kind": "static", "dir": "web" }
  ]
}`}</CodeCard>
        </div>
        <p className="mt-4 text-sm text-dim">
          The same file runs without a control plane at all:{' '}
          <code className="font-mono text-ink">npx orchd up</code> boots every workload on your
          machine, each on its own port. See <Link href="/docs/cli">the CLI</Link>.
        </p>
      </Section>

      {/* ------------------------------------------------------------ features */}
      <Section eyebrow="what you get" title="Built in, not bolted on">
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <Feature href="/docs/scale-to-zero" title="Scale-to-zero + wake">
            An idle reaper suspends what has not been asked for; the gateway wakes it on the next
            request. <code>keep_warm</code> pins the ones that must stay hot.
          </Feature>
          <Feature href="/docs/substrates" title="Isolation without KVM">
            The Docker driver runs tenants under gVisor, a userspace kernel — VM-grade syscall
            isolation on a plain cloud VM, plus memory, CPU and pid caps per container.
          </Feature>
          <Feature href="/docs/routing" title="Hostnames and TLS">
            Default subdomains, bring-your-own domains, path subroutes, and on-demand certificates
            gated so a cert is only ever issued for a host that resolves to a real workload.
          </Feature>
          <Feature href="/docs/templates" title="Templates">
            One <code>orchd.json</code> describes a multi-workload project. Provisioning is async,
            so a heavy first install never blocks the call.
          </Feature>
          <Feature href="/docs/images" title="Versioned images">
            Freeze a template into an immutable <code>v1</code>, <code>v2</code>… — a tarball plus a
            container image per workspace — push it to a registry, import it on another instance.
          </Feature>
          <Feature href="/docs/backups" title="Backups that stay small">
            Byte-exact volume snapshots to a local dir or S3/R2 over hand-rolled SigV4, excluding
            everything derived. The control-plane index is backed up too.
          </Feature>
          <Feature href="/docs/keys" title="Keys, roles, rate limits">
            A bootstrap key from a file on the box, minted keys stored hashed, readonly vs admin
            roles, and a per-key token bucket so one client cannot starve another.
          </Feature>
          <Feature href="/docs/adaptors" title="Replaceable parts">
            Runtime driver, state store, backup target, event sink and metrics sink are all
            interfaces. Most switch from the settings page; the rest are one env var.
          </Feature>
          <Feature href="/docs/local-dev" title="Runs on your laptop">
            The same control loop runs with no Docker at all — workloads as host processes on stable
            ports, or on a real wildcard <code>*.test</code> domain with trusted TLS.
          </Feature>
        </div>
      </Section>

      {/* ----------------------------------------------------------- adopters */}
      <Section
        eyebrow="who uses it"
        title="ORCHD is the substrate, not the product"
        lead="It is a standalone orchestrator with no opinion about what it runs. Two products are being built on it — and they are users of ORCHD, not part of it."
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Card title="tinbase cloud" mono="hosted backends">
            One dedicated Supabase-compatible backend per project, coupled the way Supabase Cloud
            couples them, but a single small process per tenant instead of a container stack.
          </Card>
          <Card title="RapidNative dev environments" mono="per-project runners">
            A web runner, a mobile runner, an API server and a dev database per generated project —
            mostly idle, running untrusted user code, which is exactly why the isolation matters.
          </Card>
        </div>
      </Section>

      {/* ---------------------------------------------------------------- cta */}
      <section className="mx-auto max-w-6xl px-5 pb-4">
        <div className="rounded-2xl border border-line bg-bg-soft px-6 py-10 text-center">
          <h2 className="text-2xl font-semibold tracking-tight text-ink">
            Start with why it exists
          </h2>
          <p className="mx-auto mt-3 max-w-xl text-muted">
            The docs open with the reasoning — the problem, the goals, and the things ORCHD
            deliberately refuses to do — before any endpoint.
          </p>
          <div className="mt-6 flex flex-wrap justify-center gap-3">
            <Link
              href="/docs/why"
              className="rounded-lg bg-accent px-5 py-2.5 text-sm font-semibold text-bg transition hover:opacity-90"
            >
              Why ORCHD exists
            </Link>
            <Link
              href="/docs/goals"
              className="rounded-lg border border-line px-5 py-2.5 text-sm font-medium text-ink transition hover:border-accent"
            >
              Goals &amp; non-goals
            </Link>
          </div>
        </div>
      </section>
    </div>
  )
}

/* ----------------------------------------------------------------- bits ---- */

function Stat({ n, label }: { n: string; label: string }) {
  return (
    <div>
      <div className="font-mono text-2xl font-semibold tracking-tight text-accent">{n}</div>
      <div className="mt-0.5 text-sm text-dim">{label}</div>
    </div>
  )
}

function Section({
  eyebrow,
  title,
  lead,
  children,
}: {
  eyebrow: string
  title: string
  lead?: string
  children: React.ReactNode
}) {
  return (
    <section className="mx-auto max-w-6xl px-5 py-16">
      <p className="font-mono text-[0.7rem] tracking-[0.18em] text-accent uppercase">{eyebrow}</p>
      <h2 className="mt-3 max-w-3xl text-2xl font-semibold tracking-tight text-ink sm:text-3xl">
        {title}
      </h2>
      {lead && <p className="mt-3 max-w-3xl text-muted">{lead}</p>}
      <div className="mt-8">{children}</div>
    </section>
  )
}

function Card({
  title,
  mono,
  children,
}: {
  title: string
  mono?: string
  children: React.ReactNode
}) {
  return (
    <div className="rounded-xl border border-line bg-panel p-5">
      <div className="flex items-baseline gap-2">
        <h3 className="font-semibold text-ink">{title}</h3>
        {mono && (
          <span className="font-mono text-[0.66rem] tracking-widest text-dim uppercase">{mono}</span>
        )}
      </div>
      <p className="mt-2 text-sm text-muted">{children}</p>
    </div>
  )
}

function Feature({
  href,
  title,
  children,
}: {
  href: string
  title: string
  children: React.ReactNode
}) {
  return (
    <Link
      href={href}
      className="group rounded-xl border border-line bg-panel p-5 transition hover:border-accent"
    >
      <h3 className="font-semibold text-ink">
        {title}
        <span className="ml-1 text-accent opacity-0 transition group-hover:opacity-100">→</span>
      </h3>
      <p className="mt-2 text-sm text-muted [&_code]:font-mono [&_code]:text-[0.85em] [&_code]:text-ink">
        {children}
      </p>
    </Link>
  )
}

function CodeCard({ title, children }: { title: string; children: string }) {
  return (
    <div className="overflow-hidden rounded-xl border border-line bg-panel">
      <div className="border-b border-line px-4 py-2 font-mono text-[0.68rem] tracking-widest text-dim uppercase">
        {title}
      </div>
      <pre className="overflow-x-auto p-4 font-mono text-[0.78rem] leading-relaxed text-muted">
        {children}
      </pre>
    </div>
  )
}
