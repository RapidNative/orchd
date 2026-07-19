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
    <Section title="About tinbase cloud">
      <P>
        <B>tinbase cloud</B> is hosted, multi-tenant orchestration for{' '}
        <A href="https://tinbase.dev">tinbase</A> — a cheaper, faster, high-availability alternative
        to Supabase Cloud. The same control plane also powers on-demand{' '}
        <B>RapidNative dev environments</B>, so a "workload" here is either a tinbase backend or a
        running dev app (Expo, Vite, an API server).
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
          One-shot: <M>./deploy/deploy.sh</M> builds both, ships them to the box, and reloads{' '}
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
      <H>Building a custom image</H>
      <P>
        Pulling is in the panel; <B>building</B> is not yet (a build needs a Docker context upload,
        which is on the roadmap). Until then, build on the box the standard way and then reference
        it — no redeploy needed:
      </P>
      <Code title="On the box (or a region's Docker host)">{`# build from a Dockerfile
docker build -t my-runtime:dev .

# then create a workload against it
curl -X POST https://api.tinbase.dev/v1/projects/<id>/workloads \\
  -H "Authorization: Bearer $TINBASE_KEY" \\
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
      <Code title="Adaptor · implementations · how to switch">{`Runtime driver   Local · Docker(gVisor runsc) · Firecracker(future)   deploy env
State store      File(JSON) · Postgres(pgx) · Mem                     deploy env
Backup target    Local dir · S3 / R2 (SigV4)                          Settings
Event sink       Memory · Webhook · Multi                             Settings (webhook)
Metrics sink     Nop · Log · HTTP collector                           Settings`}</Code>
      <P>
        This is what keeps the platform portable: the FileStore can become Postgres, local backups
        can become R2, and the Docker driver can become Firecracker — each without touching the API
        surface.
      </P>
    </Section>
  )
}
