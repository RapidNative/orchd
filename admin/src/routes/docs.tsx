import * as React from 'react'
import { PageHeader } from '@/components/bits'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { cn } from '@/lib/utils'

const API_BASE = 'https://api.tinbase.dev'

type Role = 'open' | 'readonly' | 'admin'
type Ep = {
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  path: string
  role: Role
  desc: string
  params?: string
  req?: string
  res?: string
}
type Group = { id: string; title: string; blurb?: string; endpoints: Ep[] }

const methodColor: Record<Ep['method'], string> = {
  GET: 'text-primary border-primary/40 bg-primary/10',
  POST: 'text-accent border-accent/40 bg-accent/10',
  PUT: 'text-warning border-warning/40 bg-warning/10',
  DELETE: 'text-destructive border-destructive/40 bg-destructive/10',
}

function Method({ m }: { m: Ep['method'] }) {
  return (
    <span className={cn('rounded-md border px-2 py-0.5 font-mono text-xs font-semibold', methodColor[m])}>
      {m}
    </span>
  )
}

function RoleTag({ role }: { role: Role }) {
  const label = role === 'open' ? 'no auth' : role === 'readonly' ? 'any key' : 'admin key'
  const cls =
    role === 'admin'
      ? 'text-warning border-warning/40'
      : role === 'open'
        ? 'text-muted-foreground border-border'
        : 'text-primary border-primary/40'
  return (
    <span className={cn('rounded-full border px-2 py-0.5 font-mono text-[10px] uppercase tracking-wide', cls)}>
      {label}
    </span>
  )
}

function Code({ title, children }: { title: string; children: string }) {
  return (
    <div className="mt-2">
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{title}</div>
      <pre className="mt-1 overflow-x-auto rounded-md border border-border bg-[#080b12] p-3 font-mono text-xs leading-relaxed text-muted-foreground">
        {children}
      </pre>
    </div>
  )
}

function Endpoint({ e }: { e: Ep }) {
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

// Prose helpers for the narrative guide sections.
const H = ({ children }: { children: React.ReactNode }) => (
  <h3 className="mb-1.5 mt-5 text-sm font-semibold text-foreground first:mt-0">{children}</h3>
)
const P = ({ children }: { children: React.ReactNode }) => (
  <p className="mb-2.5 text-sm leading-relaxed text-muted-foreground">{children}</p>
)
const UL = ({ children }: { children: React.ReactNode }) => (
  <ul className="mb-2.5 list-disc space-y-1 pl-5 text-sm leading-relaxed text-muted-foreground">
    {children}
  </ul>
)
const M = ({ children }: { children: React.ReactNode }) => (
  <code className="font-mono text-[0.8em] text-foreground">{children}</code>
)
const B = ({ children }: { children: React.ReactNode }) => (
  <b className="font-semibold text-foreground">{children}</b>
)

type Guide = { id: string; title: string; body: React.ReactNode }

const GUIDES: Guide[] = [
  {
    id: 'about',
    title: 'About tinbase cloud',
    body: (
      <>
        <P>
          <B>tinbase cloud</B> is hosted, multi-tenant orchestration for{' '}
          <a className="text-primary hover:underline" href="https://tinbase.dev" target="_blank" rel="noreferrer">
            tinbase
          </a>{' '}
          — a cheaper, faster, high-availability alternative to Supabase Cloud. The same control
          plane also powers on-demand <B>RapidNative dev environments</B>, so a "workload" here is
          either a tinbase backend or a running dev app (Expo, Vite, an API server).
        </P>
        <H>Why it is built this way</H>
        <UL>
          <li>
            <B>Coupled model</B> — one tinbase per project. Isolation and per-tenant backups are
            simple, and a noisy tenant can never touch another's data. This is how Supabase Cloud
            provisions too (a dedicated Postgres per project).
          </li>
          <li>
            <B>Docker + gVisor (<M>runsc</M>)</B> — VM-grade syscall isolation without needing KVM,
            so it runs on plain cloud VMs. Every workload gets cgroup caps (memory, CPU, PID count).
          </li>
          <li>
            <B>Scale-to-zero</B> — 90% of dev/preview workloads sit idle. Idle containers are
            reaped after a timeout and cold-booted on the next request; <M>keep_warm</M> pins the
            hot ones.
          </li>
          <li>
            <B>Adaptor pattern everywhere</B> — the runtime driver, state store, backup target,
            event sink and metrics sink are all swappable interfaces. Most are switchable from the{' '}
            <B>Settings</B> page without a redeploy. See <a className="text-primary hover:underline" href="#adaptors">Adaptors</a>.
          </li>
        </UL>
        <H>The object model</H>
        <P>
          <B>Project → Workload → Route.</B> A <B>project</B> groups workloads. A <B>workload</B> is
          one routable, scale-to-zero container instance (built from a <a className="text-primary hover:underline" href="#images">preset or image</a>).
          A <B>route</B> maps a hostname to a workload; every workload gets a default subdomain and
          can attach custom domains. Workloads are placed in a <a className="text-primary hover:underline" href="#regions">region</a>.
        </P>
      </>
    ),
  },
  {
    id: 'repo',
    title: 'Repository & layout',
    body: (
      <>
        <P>
          Source lives at{' '}
          <a className="text-primary hover:underline" href="https://github.com/RapidNative/cloud" target="_blank" rel="noreferrer">
            github.com/RapidNative/cloud
          </a>
          . Everything that runs on the box is tracked — including the Caddy config and deploy
          scripts — so the server holds no untracked code.
        </P>
        <Code title="Layout">{`cloud/
├── orchestrator/        Go control plane (stdlib + pgx only)
│   ├── cmd/orchd/       daemon: control API :8080, gateway :8081
│   └── internal/
│       ├── api/         HTTP routes + auth middleware
│       ├── runtime/     Runtime drivers: Local / Docker(gVisor) / (Firecracker)
│       ├── store/       Store + Persister: File(JSON) / Mem / Postgres(pgx)
│       ├── backup/      backup Store adaptor: Local / S3-SigV4
│       ├── events/      event Sink adaptor: Memory / Webhook / Multi
│       └── metrics/     metrics Sink adaptor: Nop / Log / HTTP
├── admin/               this panel — Vite + React + TanStack + Tailwind
└── deploy/              Caddyfile, systemd units, deploy.sh`}</Code>
        <H>Build & deploy</H>
        <UL>
          <li>
            Control plane: <M>go build ./cmd/orchd</M> (single static binary, run under systemd).
          </li>
          <li>
            Admin panel: <M>cd admin && npm run build</M> → static files served by Caddy at{' '}
            <M>admin.tinbase.dev</M>.
          </li>
          <li>
            One-shot: <M>./deploy/deploy.sh</M> builds both, ships them to the box, and reloads
            <M>orchd</M> + Caddy.
          </li>
        </UL>
        <H>Routing on the box</H>
        <UL>
          <li>
            <M>admin.tinbase.dev</M> — this panel (static) + <M>/api/*</M> reverse-proxied to the
            control API.
          </li>
          <li>
            <M>api.tinbase.dev</M> — the control API directly.
          </li>
          <li>
            <M>*.tinbase.dev</M> — the gateway; each host is resolved against the route table to a
            workload. On-demand TLS is gated by <M>/internal/tls-allow</M> so certs are only issued
            for real hosts.
          </li>
        </UL>
      </>
    ),
  },
  {
    id: 'images',
    title: 'Images & presets',
    body: (
      <>
        <P>
          <B>Yes — an "image" is a Docker image tag</B> on the orchestrator's Docker daemon (for
          example <M>tinbase:0.10.0</M> or <M>rn-vite:dev</M>). A workload is just a container
          started from that image with a port and resource caps. There is no separate custom image
          format.
        </P>
        <H>Presets vs. raw images</H>
        <P>
          A <B>preset</B> is a friendly name that expands to an image + port + default limits, so
          you don't have to remember them. <M>GET /v1/presets</M> lists the built-ins:{' '}
          <M>tinbase</M>, <M>expo</M>, <M>vite</M>, <M>api</M>. You can always bypass presets and
          pass <M>image</M> / <M>port</M> / <M>memory_mb</M> / <M>cpus</M> explicitly on the
          workload spec.
        </P>
        <Code title="Same workload, two ways">{`// via preset
{ "preset": "vite" }

// explicit image (equivalent, minus the preset defaults)
{ "type": "rapidnative-dev", "image": "rn-vite:dev", "port": 8080,
  "memory_mb": 512, "cpus": 1.0 }`}</Code>
        <H>How do I add a new type of image?</H>
        <P>
          The image has to exist on the box's Docker daemon first. There is{' '}
          <B>no upload button in this panel yet</B> — you add images the standard Docker way:
        </P>
        <Code title="On the box (or any configured region's Docker host)">{`# option A — pull a published image
docker pull ghcr.io/acme/my-runtime:1.2.0

# option B — build one from a Dockerfile
docker build -t my-runtime:dev .

# then reference it when creating a workload — no redeploy needed
curl -X POST https://api.tinbase.dev/v1/projects/<id>/workloads \\
  -H "Authorization: Bearer $TINBASE_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"type":"custom","image":"my-runtime:dev","port":3000,"memory_mb":512}'`}</Code>
        <P>
          Requirements for a custom image: it must <B>listen on <M>0.0.0.0</M></B> at the{' '}
          <M>port</M> you declare (not <M>127.0.0.1</M>), and start in the foreground. To make it a
          first-class named preset (so it shows in <M>/v1/presets</M> and the create dropdown), add
          it to the preset catalog in <M>orchestrator/internal/runtime</M> and redeploy.
        </P>
        <P>
          <B>Roadmap:</B> a registry/build integration + an "add image" screen in this panel so new
          runtimes can be pushed without shell access. Until then, images are added on the host.
        </P>
      </>
    ),
  },
  {
    id: 'regions',
    title: 'Adding regions',
    body: (
      <>
        <P>
          <B>What.</B> A region is a <B>placement target</B> for workloads. It carries a{' '}
          <M>docker_host</M> that points the runtime driver at a Docker daemon — empty means the
          local daemon on the control-plane box; a <M>tcp://…</M> value points at a separate worker
          node. New projects land in the <B>default</B> region unless one is chosen at create time.
        </P>
        <P>
          <B>Why.</B> It's the seam for going multi-node and multi-geo: put workers near users to
          cut latency, keep tenant data in a required jurisdiction, and spread load past a single
          box's memory/disk ceiling. The control plane stays central; only where containers <i>run</i>{' '}
          moves.
        </P>
        <H>How to add one</H>
        <UL>
          <li>
            <B>1. Expose the worker's Docker daemon</B> to the control-plane box over a{' '}
            <B>private network</B> (never the public internet) — a TLS socket or an SSH tunnel to{' '}
            <M>tcp://node2:2375</M>.
          </li>
          <li>
            <B>2. Create the region</B> — in this panel under <B>System → Regions</B>, or{' '}
            <M>POST /v1/regions</M> with <M>{`{ "name": "EU West", "docker_host": "tcp://node2:2375" }`}</M>.
            The id is a slug of the name (<M>eu-west</M>).
          </li>
          <li>
            <B>3. Make sure the images exist there</B> — each region's Docker host needs the image
            tags you'll run (see <a className="text-primary hover:underline" href="#images">Images & presets</a>).
          </li>
          <li>
            <B>4. Place work</B> — pass <M>region</M> when creating a project, or{' '}
            <M>POST /v1/regions/{`{id}`}/default</M> to make it the new default.
          </li>
        </UL>
        <P>
          You can't delete the default region — set another default first, then delete. Full
          data-locality (per-region backup targets, private networking, an HA data tier) is the next
          hardware milestone; the API and model are already region-aware.
        </P>
      </>
    ),
  },
  {
    id: 'adaptors',
    title: 'Adaptors (replaceable parts)',
    body: (
      <>
        <P>
          Every pluggable subsystem is an interface with multiple implementations. The ones marked{' '}
          <B>Settings</B> are switchable live from the <a className="text-primary hover:underline" href="/settings">Settings</a> page;
          the rest are chosen at deploy via env.
        </P>
        <Code title="Adaptor · implementations · how to switch">{`Runtime driver   Local · Docker(gVisor runsc) · Firecracker(future)   deploy env
State store      File(JSON) · Postgres(pgx) · Mem                     deploy env
Backup target    Local dir · S3 / R2 (SigV4)                          Settings
Event sink       Memory · Webhook · Multi                             Settings (webhook)
Metrics sink     Nop · Log · HTTP collector                           Settings`}</Code>
        <P>
          This is what keeps the platform portable: the FileStore can become Postgres, local
          backups can become R2, and the Docker driver can become Firecracker — each without
          touching the API surface documented below.
        </P>
      </>
    ),
  },
]

const GROUPS: Group[] = [
  {
    id: 'health',
    title: 'Health',
    endpoints: [
      {
        method: 'GET',
        path: '/healthz',
        role: 'open',
        desc: 'Liveness probe. The only endpoint that never requires a key.',
        res: `{ "status": "ok", "auth": true }`,
      },
    ],
  },
  {
    id: 'projects',
    title: 'Projects',
    blurb:
      'A project is a logical grouping of workloads. Creating one provisions its workloads and returns their keys and endpoints.',
    endpoints: [
      {
        method: 'POST',
        path: '/v1/projects',
        role: 'admin',
        desc: 'Create a project + its workloads. Empty body creates one primary tinbase workload. `region` defaults to the default region.',
        req: `{
  "name": "my-app",           // optional
  "region": "local",          // optional (default region if omitted)
  "workloads": [              // optional (default: [{ "preset": "tinbase" }])
    { "preset": "tinbase" },
    { "preset": "vite" },
    { "preset": "api" }
  ]
}`,
        res: `{
  "id": "abc123",
  "name": "my-app",
  "region": "local",
  "created_at": "2026-07-19T...",
  "workloads": [
    {
      "id": "wid1", "type": "tinbase-project", "name": "",
      "state": "running", "memory_mb": 384, "cpus": 0.5,
      "keep_warm": false,
      "anon_key": "eyJ...", "service_role_key": "eyJ...",
      "routes": ["abc123.tinbase.dev"],
      "endpoints": ["https://abc123.tinbase.dev"],
      "subroutes": ["https://cloud.rapidnative.com/w/abc123"],
      "last_seen": "2026-07-19T..."
    }
  ]
}`,
      },
      { method: 'GET', path: '/v1/projects', role: 'readonly', desc: 'List all projects with their workloads.', res: `[ { "id": "...", "workloads": [...] } ]` },
      { method: 'GET', path: '/v1/projects/{id}', role: 'readonly', desc: 'Get one project (workloads, keys, endpoints, routes).' },
      { method: 'DELETE', path: '/v1/projects/{id}', role: 'admin', desc: 'Stop and remove a project, all its workloads/routes, and its on-disk data. Destructive — 204 No Content.' },
    ],
  },
  {
    id: 'workloads',
    title: 'Workloads',
    blurb:
      'A workload is one routable, scale-to-zero instance. Presets: tinbase, expo, vite, api (see GET /v1/presets). Custom: type/image/port/memory_mb/cpus.',
    endpoints: [
      {
        method: 'POST',
        path: '/v1/projects/{id}/workloads',
        role: 'admin',
        desc: 'Add a workload to a project.',
        req: `{
  "preset": "vite",           // or set type/image/port explicitly
  "name": "web",              // optional role
  "image": "rn-vite:dev",     // optional (preset supplies it)
  "port": 8080,               // optional
  "memory_mb": 512,           // optional cap override
  "cpus": 1.0                 // optional cap override
}`,
        res: `{ "id": "wid...", "type": "rapidnative-dev", "name": "web", ... }`,
      },
      { method: 'GET', path: '/v1/workloads/{id}', role: 'readonly', desc: 'Get one workload (state, limits, keys, routes, last_seen).' },
      { method: 'DELETE', path: '/v1/workloads/{id}', role: 'admin', desc: 'Stop and remove one workload and its routes + data. 204.' },
      {
        method: 'POST',
        path: '/v1/workloads/{id}/keepwarm',
        role: 'admin',
        desc: 'Toggle always-on. Enabling boots the workload now and exempts it from scale-to-zero.',
        req: `{ "enabled": true }`,
        res: `{ "keep_warm": true }`,
      },
      {
        method: 'GET',
        path: '/v1/workloads/{id}/stats',
        role: 'readonly',
        desc: 'Live memory + CPU (docker stats). Empty snapshot when the workload is not running.',
        res: `{ "mem_usage": "76.2MiB / 384MiB", "mem_perc": "19.8%", "cpu_perc": "1.0%" }`,
      },
      {
        method: 'GET',
        path: '/v1/workloads/{id}/logs',
        role: 'readonly',
        desc: 'Container logs (stdout+stderr).',
        params: 'tail=<n> (default 200)',
        res: `{ "logs": "…last N lines…" }`,
      },
    ],
  },
  {
    id: 'domains',
    title: 'Domains (routes)',
    blurb:
      'Every workload gets a default subdomain. Attach custom domains (bring-your-own): point the domain (CNAME/A) at the gateway; a Let’s Encrypt cert is issued on first HTTPS request.',
    endpoints: [
      {
        method: 'POST',
        path: '/v1/workloads/{id}/routes',
        role: 'admin',
        desc: 'Attach a hostname (custom domain) to a workload.',
        req: `{ "host": "app.customer.com" }`,
        res: `{ ...workload with the new route in "routes"... }`,
      },
      {
        method: 'DELETE',
        path: '/v1/routes',
        role: 'admin',
        desc: 'Detach a hostname. 204.',
        params: 'host=<hostname> (query, required)',
      },
    ],
  },
  {
    id: 'backups',
    title: 'Backups',
    blurb:
      'Byte-exact volume snapshots (tar+gz). Target is local or S3/R2 (see Settings). Restoring replaces a workload’s current data.',
    endpoints: [
      { method: 'POST', path: '/v1/workloads/{id}/backups', role: 'admin', desc: 'Back up one workload now.', res: `{ "id": "wid__20260719T...", "workload_id": "wid", "created_at": "...", "size_bytes": 4797652 }` },
      { method: 'POST', path: '/v1/projects/{id}/backups', role: 'admin', desc: 'Back up every workload in a project.', res: `[ { "id": "...", "workload_id": "...", "size_bytes": ... } ]` },
      { method: 'GET', path: '/v1/backups', role: 'readonly', desc: 'List all backups (newest first).', res: `[ { "id": "...", "workload_id": "...", "created_at": "...", "size_bytes": ... } ]` },
      { method: 'GET', path: '/v1/workloads/{id}/backups', role: 'readonly', desc: 'List backups for one workload.' },
      { method: 'POST', path: '/v1/workloads/{id}/restore', role: 'admin', desc: 'Restore a workload from a backup (replaces current data, then reboots it).', req: `{ "backup_id": "wid__20260719T..." }`, res: `{ "status": "restored" }` },
      { method: 'DELETE', path: '/v1/backups/{id}', role: 'admin', desc: 'Delete a backup. 204.' },
    ],
  },
  {
    id: 'regions',
    title: 'Regions',
    blurb:
      'A region is a placement target; docker_host points it at a worker node’s Docker daemon (empty = local). Projects are placed in the default region unless one is chosen at create.',
    endpoints: [
      { method: 'GET', path: '/v1/regions', role: 'readonly', desc: 'List regions.', res: `[ { "id": "local", "name": "local", "docker_host": "", "is_default": true, "created_at": "..." } ]` },
      { method: 'POST', path: '/v1/regions', role: 'admin', desc: 'Create a region (id is a slug of the name).', req: `{ "name": "EU West", "docker_host": "tcp://node2:2375" }`, res: `{ "id": "eu-west", "name": "EU West", "docker_host": "tcp://node2:2375", "is_default": false }` },
      { method: 'DELETE', path: '/v1/regions/{id}', role: 'admin', desc: 'Delete a region (not the default — set another default first). 204.' },
      { method: 'POST', path: '/v1/regions/{id}/default', role: 'admin', desc: 'Make a region the default.', res: `{ "default": "eu-west" }` },
    ],
  },
  {
    id: 'keys',
    title: 'API keys',
    blurb:
      'Multiple named keys with roles. The bootstrap key (server key file) is always admin. Keys are stored hashed; the plaintext is shown once at creation.',
    endpoints: [
      { method: 'GET', path: '/v1/keys', role: 'readonly', desc: 'List keys (no secrets).', res: `[ { "id": "...", "name": "ci-bot", "role": "readonly", "created_at": "..." } ]` },
      { method: 'POST', path: '/v1/keys', role: 'admin', desc: 'Create a key. The plaintext key is returned exactly once.', req: `{ "name": "ci-bot", "role": "readonly" }`, res: `{ "key": "tbk_9f8e…", "meta": { "id": "...", "name": "ci-bot", "role": "readonly" } }` },
      { method: 'DELETE', path: '/v1/keys/{id}', role: 'admin', desc: 'Revoke a key. 204.' },
    ],
  },
  {
    id: 'settings',
    title: 'Settings',
    blurb: 'Runtime-configurable platform settings. Secrets are stored server-side and never returned.',
    endpoints: [
      {
        method: 'GET',
        path: '/v1/settings',
        role: 'readonly',
        desc: 'Current backup target (secret masked), event webhook, and metrics sink.',
        res: `{
  "backup": { "type": "s3", "endpoint": "...", "bucket": "...", "region": "...", "access_key": "..." },
  "backup_secret_set": true,
  "webhook": { "url": "" },
  "metrics": { "type": "nop" }
}`,
      },
      {
        method: 'PUT',
        path: '/v1/settings/backup',
        role: 'admin',
        desc: 'Set the backup destination. Leave secret_key blank on an s3 target to keep the existing one.',
        req: `{
  "type": "s3",               // "local" | "s3"
  "endpoint": "https://<acct>.r2.cloudflarestorage.com",
  "bucket": "backups", "region": "auto", "prefix": "backups",
  "access_key": "…", "secret_key": "…"
}`,
      },
      { method: 'PUT', path: '/v1/settings/webhook', role: 'admin', desc: 'Set the event webhook URL (blank = off). Control-plane events are POSTed here as JSON.', req: `{ "url": "https://example.com/hooks" }` },
      { method: 'PUT', path: '/v1/settings/metrics', role: 'admin', desc: 'Set the metrics sink.', req: `{ "type": "http", "url": "https://collector/metrics" }  // type: "nop" | "log" | "http"` },
    ],
  },
  {
    id: 'observability',
    title: 'Metrics & events',
    endpoints: [
      { method: 'GET', path: '/v1/metrics', role: 'readonly', desc: 'Live fleet snapshot.', res: `{ "time": "...", "projects": 3, "workloads": 6, "running": 2, "suspended": 4, "mem_mb_allocated": 768 }` },
      { method: 'GET', path: '/v1/events', role: 'readonly', desc: 'Recent control-plane events (audit feed), newest first.', params: 'limit=<n> (default 100)', res: `[ { "id": "...", "time": "...", "type": "project.created", "project_id": "abc123", "message": "" } ]` },
    ],
  },
  {
    id: 'meta',
    title: 'Metadata',
    endpoints: [
      { method: 'GET', path: '/v1/presets', role: 'readonly', desc: 'Available workload presets.', res: `[ "api", "expo", "tinbase", "vite" ]` },
      {
        method: 'GET',
        path: '/v1/info',
        role: 'readonly',
        desc: 'System configuration: driver, region, base domain, idle timeout, default resource limits, rate limit, backups/metrics status, presets.',
        res: `{
  "driver": "docker+runsc", "region": "local", "base_domain": "tinbase.dev",
  "idle_timeout": "2m0s", "image": "tinbase:0.10.0", "rate_limit_per_min": 600,
  "limits": { "tinbase_mem_mb": 384, "tinbase_cpus": 0.5, "dev_mem_mb": 512, "dev_cpus": 1, "pids_limit": 512 },
  "backups": { "enabled": true, "interval": "24h0m0s", "retain": 7 },
  "metrics": { "type": "nop" },
  "presets": ["api","expo","tinbase","vite"]
}`,
      },
    ],
  },
  {
    id: 'internal',
    title: 'Internal',
    blurb: 'Used by the platform itself, not for general clients.',
    endpoints: [
      {
        method: 'GET',
        path: '/internal/tls-allow',
        role: 'open',
        desc: 'Caddy on-demand-TLS gate: returns 200 for admin/api and any host in the route table, 403 otherwise — so certificates are only issued for real hosts.',
        params: 'domain=<hostname>',
      },
    ],
  },
]

export function Docs() {
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
            <div className="px-2 pb-1 pt-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground/70">
              Guide
            </div>
            {GUIDES.map((g) => (
              <a
                key={g.id}
                href={`#${g.id}`}
                className="rounded px-2 py-1 text-muted-foreground hover:bg-muted hover:text-foreground"
              >
                {g.title}
              </a>
            ))}
            <div className="px-2 pb-1 pt-3 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground/70">
              API reference
            </div>
            <a href="#auth" className="rounded px-2 py-1 text-muted-foreground hover:bg-muted hover:text-foreground">
              Authentication
            </a>
            {GROUPS.map((g) => (
              <a
                key={g.id}
                href={`#${g.id}`}
                className="rounded px-2 py-1 text-muted-foreground hover:bg-muted hover:text-foreground"
              >
                {g.title}
              </a>
            ))}
          </div>
        </nav>

        <div className="min-w-0">
          {/* Guide sections */}
          {GUIDES.map((g) => (
            <Card key={g.id} id={g.id} className="mb-6 scroll-mt-6">
              <CardHeader>
                <CardTitle className="text-base">{g.title}</CardTitle>
              </CardHeader>
              <CardContent className="pt-0">{g.body}</CardContent>
            </Card>
          ))}

          <div className="mb-4 mt-2 border-t border-border pt-6">
            <h2 className="text-lg font-semibold text-foreground">API reference</h2>
            <p className="text-sm text-muted-foreground">
              Base URL: <code className="font-mono">{API_BASE}</code>
            </p>
          </div>

          {/* Auth */}
          <Card id="auth" className="mb-6 scroll-mt-6">
            <CardHeader>
              <CardTitle className="text-base">Authentication</CardTitle>
            </CardHeader>
            <CardContent className="text-sm text-muted-foreground">
              <p>
                Every <code className="font-mono">/v1/*</code> endpoint requires an API key.{' '}
                <code className="font-mono">/healthz</code> is the only open endpoint.
              </p>
              <ul className="mt-3 list-disc space-y-1 pl-5">
                <li>
                  Pass the key as <code className="font-mono">Authorization: Bearer &lt;key&gt;</code> or{' '}
                  <code className="font-mono">X-API-Key: &lt;key&gt;</code>.
                </li>
                <li>
                  The <b className="text-foreground">bootstrap key</b> (from the server key file) is always{' '}
                  <b className="text-foreground">admin</b>. You can mint more keys (<code className="font-mono">POST /v1/keys</code>) with a role.
                </li>
                <li>
                  <b className="text-foreground">Roles:</b> <span className="text-primary">any key</span> (readonly)
                  may call every <code className="font-mono">GET</code>;{' '}
                  <span className="text-warning">admin</span> is required for any{' '}
                  <code className="font-mono">POST/PUT/DELETE</code>. A readonly key on a mutating call → 403.
                </li>
                <li>Missing/invalid key → 401. Over the per-key rate limit → 429 (Retry-After).</li>
              </ul>
              <Code title="Example">{`curl ${API_BASE}/v1/projects \\
  -H "Authorization: Bearer $TINBASE_KEY"

curl -X POST ${API_BASE}/v1/projects \\
  -H "Authorization: Bearer $TINBASE_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"workloads":[{"preset":"tinbase"}]}'`}</Code>
              <p className="mt-3">
                Errors are JSON: <code className="font-mono">{`{ "error": "…" }`}</code>. Status codes: 200/201 ok,
                204 no content, 400 bad request, 401 unauthorized, 403 forbidden (role), 404 not found, 409 conflict,
                429 rate limited.
              </p>
            </CardContent>
          </Card>

          {/* Endpoint groups */}
          {GROUPS.map((g) => (
            <Card key={g.id} id={g.id} className="mb-6 scroll-mt-6">
              <CardHeader>
                <CardTitle className="text-base">{g.title}</CardTitle>
                {g.blurb && <p className="text-sm text-muted-foreground">{g.blurb}</p>}
              </CardHeader>
              <CardContent className="pt-0">
                {g.endpoints.map((e) => (
                  <Endpoint key={e.method + e.path} e={e} />
                ))}
              </CardContent>
            </Card>
          ))}
        </div>
      </div>
    </div>
  )
}
