import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { A, B, Code, H, M, P, UL } from './parts'

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{title}</CardTitle>
      </CardHeader>
      <CardContent className="pt-0">{children}</CardContent>
    </Card>
  )
}

export function About() {
  return (
    <Section title="About ORCHD">
      <P>
        <B>ORCHD</B> is a generic control plane for hosted, multi-tenant orchestration. A single
        deployment is named by its operator in <A href="/settings">Settings</A> — this one runs as{' '}
        <B>tinbase cloud</B> (hosted <A href="https://tinbase.dev">tinbase</A>, a cheaper, faster,
        high-availability alternative to Supabase Cloud); another instance might be{' '}
        <B>RapidNative Cloud</B> for on-demand dev environments. Either way a "workload" is a tinbase
        backend or a running dev app (Expo, Vite, an API server).
      </P>
      <H>Why it is built this way</H>
      <UL>
        <li>
          <B>Coupled model</B> — one tinbase per project. Isolation and per-tenant backups are
          simple, and a noisy tenant can never touch another's data. This is how Supabase Cloud
          provisions too (a dedicated Postgres per project).
        </li>
        <li>
          <B>
            Docker + gVisor (<M>runsc</M>)
          </B>{' '}
          — VM-grade syscall isolation without needing KVM, so it runs on plain cloud VMs. Every
          workload gets cgroup caps (memory, CPU, PID count).
        </li>
        <li>
          <B>Scale-to-zero</B> — 90% of dev/preview workloads sit idle. Idle containers are reaped
          after a timeout and cold-booted on the next request; <M>keep_warm</M> pins the hot ones.
        </li>
        <li>
          <B>Adaptor pattern everywhere</B> — the runtime driver, state store, backup target, event
          sink and metrics sink are all swappable interfaces. Most are switchable from the{' '}
          <B>Settings</B> page without a redeploy. See <A href="/docs/adaptors">Adaptors</A>.
        </li>
      </UL>
      <H>The object model</H>
      <P>
        <B>Project → Workload → Route.</B> A <B>project</B> groups workloads. A <B>workload</B> is
        one routable, scale-to-zero container instance (built from a{' '}
        <A href="/docs/images">preset or image</A>). A <B>route</B> maps a hostname to a workload;
        every workload gets a default subdomain and can attach custom domains. Workloads are placed
        in a <A href="/docs/regions">region</A>.
      </P>
    </Section>
  )
}

export function Repo() {
  return (
    <Section title="Repository & layout">
      <P>
        Source lives at <A href="https://github.com/RapidNative/cloud">github.com/RapidNative/cloud</A>.
        Everything that runs on the box is tracked — including the Caddy config and deploy scripts —
        so the server holds no untracked code.
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
├── deploy/              Caddyfile, systemd units, deploy.sh
└── template-examples/   bundled example templates (tinbase, rapidnative)`}</Code>
      <H>Build & deploy</H>
      <UL>
        <li>
          Control plane: <M>go build ./cmd/orchd</M> (single static binary, run under systemd).
        </li>
        <li>
          Admin panel: <M>cd admin && npm run build</M> → static files served by Caddy at{' '}
          <M>admin.rnproject.dev</M>.
        </li>
        <li>
          One-shot: <M>./deploy/deploy.sh</M> builds both, ships them to the box, and reloads{' '}
          <M>orchd</M> + Caddy.
        </li>
      </UL>
      <H>Routing on the box</H>
      <UL>
        <li>
          <M>admin.rnproject.dev</M> — this panel (static) + <M>/api/*</M> reverse-proxied to the
          control API.
        </li>
        <li>
          <M>api.rnproject.dev</M> — the control API directly.
        </li>
        <li>
          <M>*.rnproject.dev</M> — the gateway; each host is resolved against the route table to a
          workload. On-demand TLS is gated by <M>/internal/tls-allow</M> so certs are only issued
          for real hosts.
        </li>
        <li>
          <M>*.tinbase.dev</M> — also served (a Caddy wildcard block + <M>ORCHD_ALT_DOMAINS</M>), so
          existing workloads and admin/api on the old domain keep working.
        </li>
      </UL>
      <H>Run locally (a port per workload, no domain)</H>
      <P>
        For local testing there is <B>no domain, TLS, or Caddy</B> — everything runs on localhost
        ports, and <B>each workload gets its own stable port</B> instead of a subdomain.{' '}
        <M>dev/local.sh [docker|mock|local]</M> starts orchd + the admin and prints the URLs and a
        dev API key. It sets <M>ORCHD_LOCAL=1</M> and <M>ORCHD_PORT_BASE=8100</M>, so workloads are
        assigned <M>8100</M>, <M>8101</M>, <M>8102</M>… as they're created and are reachable directly.
      </P>
      <Code title="Local layout">{`ORCHD API    http://localhost:8090
Gateway      http://localhost:8091
Admin        http://localhost:8092

# a project's workloads each get their own port (shown as the endpoint):
tinbase      http://localhost:8100
app (web)    http://localhost:8101
app (api)    http://localhost:8102
# next workload -> 8103, and so on`}</Code>
      <P>
        Port addressing is driven by <M>ORCHD_PORT_BASE</M> (empty in prod, where the
        gateway/subdomain model is used instead). Each workload's assigned port is stable across
        restarts and surfaced as its <M>endpoint</M> in the panel and API. The gateway subroute
        (<M>/w/&lt;key&gt;</M>) and <M>&lt;key&gt;.localhost</M> subdomain still work as
        alternatives. <B>Local can differ from prod/staging</B>: it's all env — prod sets a base
        domain and leaves <M>ORCHD_PORT_BASE</M> unset.
      </P>
      <P>
        <M>local</M> (default) is a <B>full no-Docker stack</B>: tinbase and the RapidNative dev
        apps (web/api/app) each run as a real local process from a scaffolded source dir —{' '}
        <B>Vite</B> for web, <B>Hono</B> for api, <B>Expo</B> for app — npm-installed on first boot
        (needs node/npm). <M>docker</M> runs the real container images (runc, no gVisor needed);{' '}
        <M>mock</M> boots the whole control plane and admin with no Docker at all (workloads don't
        serve real traffic). Any explicit <M>ORCHD_*</M> var still overrides the local defaults.
      </P>
    </Section>
  )
}

export function Templates() {
  return (
    <Section title="Templates">
      <P>
        A <B>template</B> is a project folder (a monorepo) with an{' '}
        <M>orchd.json</M> at its root — ORCHD's own descriptor, independent of any framework's
        manifest. It lists the <B>workloads</B> a project should run. One template = one project;
        each workload is a workspace on its own port (local) or an image (prod).
      </P>
      <Code title="orchd.json">{`{
  "name": "rapidnative",
  "backup_exclude": ["node_modules", ".git", "dist", ".expo", "build"],
  "workloads": [
    { "name": "db",     "kind": "tinbase" },
    { "name": "api",    "kind": "node",   "dir": "api",
      "install": ["npm","install"], "run": ["node","--watch","index.js"], "port_env": "PORT",
      "env": { "NODE_ENV": "development" }, "image": "rn-api:dev" },
    { "name": "web",    "kind": "static", "dir": "web",    "image": "rn-web:dev" },
    { "name": "mobile", "kind": "node",   "dir": "mobile",
      "install": ["npm","install"], "run": ["npx","expo","start","--web","--port","$PORT"],
      "image": "rn-mobile:dev" }
  ]
}`}</Code>
      <P>
        Kinds: <M>tinbase</M> (a tinbase backend), <M>node</M> (<M>install</M> once, then <M>run</M>{' '}
        with <M>$PORT</M> or <M>port_env</M>), <M>static</M> (a built-in zero-dep server for a
        folder).
      </P>
      <H>Register & create</H>
      <P>
        Bundled examples in <M>template-examples/</M> are <B>auto-registered on startup</B> (via{' '}
        <M>ORCHD_TEMPLATES_DIR</M>), so they're available on a fresh clone with no setup. Add your
        own in <A href="/settings">Settings → Templates</A> (name → local path). Then on{' '}
        <A href="/projects">Projects</A> use <B>New project → &lt;template&gt;</B>. That provisions
        one workload per manifest entry; <B>provisioning is async</B> (the call returns immediately,
        workloads go <M>provisioning → running/failed</M>) so a heavy first <M>npm install</M> never
        blocks it.
      </P>
      <H>Environment variables</H>
      <P>
        Each workload can carry env vars: a workload's <M>env</M> in <M>orchd.json</M> (defaults),{' '}
        <M>env</M> on the create/workload API, and the <B>Env</B> editor on a project's page (
        <M>PUT /v1/workloads/&lt;id&gt;/env</M>). Setting env <B>reboots</B> the workload to apply
        it. Platform vars (e.g. the tinbase JWT secret) are injected too; your keys can override.
      </P>
      <H>Local vs prod (same template)</H>
      <UL>
        <li>
          <B>Local (per-workload copy):</B> the template is copied into each workload's dir (minus{' '}
          <M>node_modules</M>/derived), deps installed, and the workspace's dev server runs on its
          port. Edits are live and isolated per project.
        </li>
        <li>
          <B>Prod (image):</B> the template's Dockerfiles (mirroring the manifest, one per{' '}
          <M>image</M>) build to the registry; prod pulls and runs them, with the user's files on a
          mounted volume. Build with <M>deploy/build-template.sh &lt;path&gt;</M>.
        </li>
      </UL>
      <H>Templates vs. images (versioned freezes)</H>
      <P>
        A <B>template</B> is live, mutable source. An <B>image</B> is an immutable, versioned{' '}
        <B>freeze</B> of a template: a base <B>tarball</B> stored on this ORCHD instance{' '}
        <M>plus</M>, per workspace, a <B>docker image tag</B>. Build one from{' '}
        <A href="/images">Images → Build image → &lt;template&gt;</A> or{' '}
        <M>POST /v1/templates/&lt;name&gt;/build</M>; the version auto-increments (<M>v1</M>,{' '}
        <M>v2</M>, …). The tarball is always written (a box without Docker still produces a usable
        image for local); docker builds are best-effort per node/static workspace.
      </P>
      <P>
        <B>Booting from an image</B> (<A href="/projects">New project → From image</A>, or{' '}
        <M>{`{"image":"template@version"}`}</M> on create): <B>local</B> restores the working tree
        from the tarball, <B>prod</B> runs the versioned docker tag — and the driver auto-selects
        (no flag). A create-time delta still overlays on top, so the same base+delta model applies
        whether you boot from the live template or a frozen image.
      </P>
      <H>Backups are the delta</H>
      <P>
        A backup captures only the user's files — <M>node_modules</M>/<M>.git</M>/<M>dist</M>/
        <M>.expo</M>/build caches are excluded (deps come from install or the image, never a
        backup). So a project backup is small: source + tinbase data. Same rule local and prod.
      </P>
    </Section>
  )
}

export function ImagesDoc() {
  return (
    <Section title="Images & presets">
      <P>
        <B>An "image" is a Docker image tag</B> on a region's Docker daemon (for example{' '}
        <M>tinbase:0.10.0</M> or <M>rn-vite:dev</M>). A workload is just a container started from
        that image with a port and resource caps. There is no separate custom image format.
      </P>
      <H>Presets vs. raw images</H>
      <P>
        A <B>preset</B> is a friendly name that expands to an image + port + default limits, so you
        don't have to remember them. <M>GET /v1/presets</M> lists the built-ins: <M>tinbase</M>,{' '}
        <M>expo</M>, <M>vite</M>, <M>api</M>. You can always bypass presets and pass <M>image</M> /{' '}
        <M>port</M> / <M>memory_mb</M> / <M>cpus</M> explicitly on the workload spec.
      </P>
      <Code title="Same workload, two ways">{`// via preset
{ "preset": "vite" }

// explicit image (equivalent, minus the preset defaults)
{ "type": "rapidnative-dev", "image": "rn-vite:dev", "port": 8080,
  "memory_mb": 512, "cpus": 1.0 }`}</Code>
      <H>Managing images from the panel</H>
      <P>
        The <A href="/images">Images</A> page (and the <M>/v1/images</M> API) lists, pulls and
        removes images on a region's Docker host — no shell needed to <B>pull</B> a published tag.
        Pick the region, paste a ref like <M>ghcr.io/acme/app:1.2.0</M>, and pull; delete removes a
        tag (with a forced-removal fallback when a container still holds it).
      </P>
      <H>Two kinds of "image" in ORCHD</H>
      <P>
        This page shows <B>two</B> things. The <B>Built from templates</B> card at the top lists{' '}
        ORCHD's own <B>versioned freezes</B> of a template (tarball + per-workspace docker tags) —
        build them with <B>Build image</B>, boot projects from them (see{' '}
        <A href="/docs#templates">Templates</A>). The tables below are the raw{' '}
        <B>Docker daemon images</B> on a region's host, which you pull/remove directly.
      </P>
      <H>Building a raw custom image</H>
      <P>
        ORCHD builds template images for you. For an <B>arbitrary</B> custom image (not tied to a
        template), build it on the box the standard way and reference it — no redeploy needed:
      </P>
      <Code title="On the box (or a region's Docker host)">{`# build from a Dockerfile
docker build -t my-runtime:dev .

# then create a workload against it
curl -X POST https://api.tinbase.dev/v1/projects/<id>/workloads \\
  -H "Authorization: Bearer $ORCHD_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"type":"custom","image":"my-runtime:dev","port":3000,"memory_mb":512}'`}</Code>
      <P>
        Requirements for a custom image: it must{' '}
        <B>
          listen on <M>0.0.0.0</M>
        </B>{' '}
        at the <M>port</M> you declare (not <M>127.0.0.1</M>), and start in the foreground. To make
        it a first-class named preset (so it shows in <M>/v1/presets</M> and the create dropdown),
        add it to the preset catalog in <M>orchestrator/internal/runtime</M> and redeploy.
      </P>
    </Section>
  )
}

export function Regions() {
  return (
    <Section title="Adding regions">
      <P>
        <B>What.</B> A region is a <B>placement target</B> for workloads. It carries a{' '}
        <M>docker_host</M> that points the runtime driver at a Docker daemon — empty means the local
        daemon on the control-plane box; a <M>tcp://…</M> value points at a separate worker node. New
        projects land in the <B>default</B> region unless one is chosen at create time.
      </P>
      <P>
        <B>Why.</B> It's the seam for going multi-node and multi-geo: put workers near users to cut
        latency, keep tenant data in a required jurisdiction, and spread load past a single box's
        memory/disk ceiling. The control plane stays central; only where containers <i>run</i> moves.
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
          <M>POST /v1/regions</M> with{' '}
          <M>{`{ "name": "EU West", "docker_host": "tcp://node2:2375" }`}</M>. The id is a slug of the
          name (<M>eu-west</M>).
        </li>
        <li>
          <B>3. Make sure the images exist there</B> — each region's Docker host needs the image
          tags you'll run (pull them from the <A href="/images">Images</A> page with that region
          selected).
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
    </Section>
  )
}

export function Adaptors() {
  return (
    <Section title="Adaptors (replaceable parts)">
      <P>
        Every pluggable subsystem is an interface with multiple implementations. The ones marked{' '}
        <B>Settings</B> are switchable live from the <A href="/settings">Settings</A> page; the rest
        are chosen at deploy via env.
      </P>
      <Code title="Adaptor · implementations · how to switch">{`Runtime driver   Local · Docker(gVisor runsc) · Mock · Firecracker(future)   deploy env
State store      JSON file · SQLite(WAL) · Postgres(pgx) · Mem              deploy env
Backup target    Local dir · S3 / R2 (SigV4)                               Settings
Event sink       Memory · Webhook · Multi                                  Settings (webhook)
Metrics sink     Nop · Log · HTTP collector                                Settings`}</Code>
      <P>
        This is what keeps the platform portable: the FileStore can become Postgres, local backups
        can become R2, and the Docker driver can become Firecracker — each without touching the API
        surface.
      </P>
      <H>Control-plane state store</H>
      <P>
        The project/workload index has three backends, selected at deploy:{' '}
        <M>JSON file</M> (default, simple), <M>SQLite</M> in WAL mode (
        <M>ORCHD_STATE_SQLITE</M> — recommended for a single-box control plane: atomic,
        crash-safe, incremental writes, and a real <M>.db</M> file), and{' '}
        <M>Postgres</M> (<M>ORCHD_STATE_DSN</M> — for a distributed/HA control plane). Switching to
        SQLite auto-migrates an existing <M>projects.json</M> on first boot.
      </P>
      <P>
        The index is small but high-value, so it is backed up <B>off-box on the backup schedule</B>{' '}
        (and via <M>POST /v1/system/backup</M>), stored under the reserved key <M>_control-plane</M>.
        For SQLite the WAL is checkpointed first so the snapshot is consistent.
      </P>
    </Section>
  )
}
