# RapidNative Cloud

One multi-tenant **workload orchestrator**, in a single codebase, powering two
products:

1. **tinbase-cloud** — hosted, Supabase-compatible backends. A cheaper, faster,
   HA-capable alternative to Supabase Cloud.
2. **RapidNative dev environments** — the per-project runners behind
   [RapidNative](https://rapidnative.com) (a web/expo runner, a web/react runner,
   a dev tinbase, an api server), each on its own subdomain.

Both are just **workloads** to the orchestrator: containers it provisions, routes
by hostname, isolates with gVisor, and scales to zero when idle. The only
difference between them is the image and the routing. Build the substrate once,
run both.

> Status: early. The control plane, gateway, scale-to-zero lifecycle, the
> Docker + gVisor substrate, and multi-domain routing all work and are verified
> on real hardware. The tinbase workload runs today; the RapidNative runner
> images, plus HA, connection pooling, backups, and the Firecracker tier, are on
> the roadmap.

---

## Why this exists

Both products need the same thing: run many small, per-tenant workloads cheaply,
give each one a subdomain, wake it fast, put it to sleep when idle, and isolate
it well enough to run untrusted user code. Rather than build that twice, it is
one orchestrator with a generic `Workload` primitive.

**tinbase-cloud.** tinbase speaks the Supabase wire protocol, so the official
`@supabase/supabase-js` SDK works against it unchanged (REST, Auth, Storage,
Realtime), but it is one ~66 MB process instead of a Docker Compose stack. Hosting
a fleet of those, coupled one-per-project like Supabase Cloud, is the whole
cost advantage.

**RapidNative dev environments.** Each generated project needs a set of runners on
demand, and most sit idle. That is exactly what scale-to-zero is for, and because
those runners execute untrusted user code, VM-grade isolation is not optional. The
same orchestrator, the same gateway, the same scale-to-zero, a different workload
type.

## Architecture

```
                *.  cloud.rapidnative.com   (Cloudflare -> box; wildcard TLS in prod)
                          |
                   +--------------+     Gateway (data plane): resolve request Host
                   |   Gateway    |     against the route table -> workload,
                   |   :8081      |     wake on demand, reverse-proxy every Supabase
                   +--------------+     path (REST/Auth/Storage/Realtime WS/Studio)
                          |
         +----------------+----------------+
   +-----------+    +-----------+     +-----------+     Data plane: one tinbase
   |  tinbase  |    |  tinbase  |     |  tinbase  |     instance per workload,
   | + volume  |    | + volume  |     | + volume  |     suspended when idle
   +-----------+    +-----------+     +-----------+
                          ^
                   +--------------+     Control plane API: provision projects,
                   |   API :8080  |     workloads, and routes; mint keys; manage
                   +--------------+     lifecycle. Backed by the store.
```

The control plane and gateway are one Go daemon (`orchd`). Everything above the
runtime driver is substrate-agnostic.

### Model: Project -> Workload -> Route

- **Project** is a logical grouping / tenant (ownership and, later, billing).
- **Workload** is the routable, independently-scheduled, scale-to-zero unit. The
  runtime lifecycle is keyed by workload id.
- **Route** maps a hostname to a workload, resolved by an exact-match route table,
  so both convention subdomains (`<ref>-<role>.<base>`) and custom domains work
  the same way. A workload can have many routes.

A plain tinbase project is one project with a single primary workload and one
route `<ref>.<base>`. A RapidNative project is one project with many workloads
(`app`, `web`, `tinbase`, `api`), each on `<ref>-<name>.<base>` and each its own
isolated instance.

### Workload types

The two products are the same primitive with a different image and routing:

| Type | Image | Routing | Purpose |
| --- | --- | --- | --- |
| `tinbase-project` | tinbase | `<ref>.<base>` | hosted Supabase-compatible backend (tinbase-cloud) |
| `rapidnative-dev` | per-runner (web/expo, web/react, api, dev tinbase) | `<ref>-<name>.<base>` | a RapidNative project's dev environment |

One control plane, one gateway, one driver; add a workload type by adding an
image. `Spec.Image`/`Spec.Port` are already plumbed through, so heterogeneous
workloads need only their runner images.

### Runtime driver seam

The control plane never talks to a VMM directly. It talks to a `Runtime`
interface, so the substrate is swappable:

| Driver | What it runs | Isolation | Where |
| --- | --- | --- | --- |
| `LocalDriver` | tinbase as an OS process | process | macOS dev |
| `DockerDriver` | tinbase as a container | **gVisor (`runsc`)**, VM-grade, no KVM | Linux box (today) |
| `FirecrackerDriver` (planned) | tinbase in a microVM | microVM + jailer, snapshot/restore | bare metal |

**gVisor** matters because it gives VM-grade isolation for untrusted code
*without nested virtualization*, so it runs on a plain Cloud VM. Firecracker and
Kata need `/dev/kvm` and therefore a dedicated bare-metal box; that is the future
speed tier (sub-second snapshot resume).

## Verified behavior (measured on the box)

| Property | Measured |
| --- | --- |
| Cold provision (incl. `initdb`) under gVisor | ~2.8 s |
| Wake from scale-to-zero | ~0.9 s |
| Runtime memory per running instance (incl. gVisor) | ~75-85 MB |
| Idle instance memory | 0 (container stopped) |
| Isolation | tenant container runs a `gVisor` kernel, not the host kernel |
| Data | persists across suspend/resume on a per-project volume |
| Multi-domain | one project, 4 subdomains, 4 isolated gVisor containers |

## Resource model (how it scales)

Both disk and memory scale with **warm/active** projects, not **total** projects:

- **Disk**: the workload image (with its cache baked in) is content-addressed and
  layer-shared by Docker's overlay2, so it is stored **once** regardless of how
  many containers run it. Per-project disk is just the data volume (~40 MB fresh).
  Long-idle project data can be tiered to object storage and restored on wake.
- **Memory**: scale-to-zero means an idle project holds a stopped container and 0
  RAM. Runtime memory tracks *concurrently warm* instances. Per-container cgroup
  limits keep packing predictable; add nodes when the concurrency ceiling is hit.

## Tiers (how HA stays honest)

HA, rock-bottom cost, and scale-to-zero cannot all be true for the same tier, so
durability is tiered (as Supabase itself does):

| Tier | Compute | Durability | Audience |
| --- | --- | --- | --- |
| Free / preview | scale-to-zero, densely packed | nightly + WAL to object storage | RapidNative apps, hobby |
| Pro | always-on, kept warm | PITR | production apps |
| Team / Enterprise | primary + standby, auto-failover | true HA + read replicas | the "HA replacement" tier |

## Repository layout

```
tinbase-cloud/
  orchestrator/            the Go control plane + gateway (orchd)
    cmd/orchd/             daemon entrypoint
    internal/runtime/      Runtime interface, Workload primitive, Local + Docker drivers
    internal/store/        Project / Workload / Route records
    internal/manager/      provisioning, key minting, wake / scale-to-zero
    internal/gateway/      host -> route -> workload, wake, reverse proxy
    internal/api/          control-plane API
    images/tinbase/        Dockerfile for the tinbase workload image
    README.md  ROADMAP.md  component docs + phased plan
  site/index.html          project overview page (served at cloud.rapidnative.com)
```

See [orchestrator/README.md](orchestrator/README.md) for the component detail and
run instructions, and [orchestrator/ROADMAP.md](orchestrator/ROADMAP.md) for the
phased plan.

## Deployment (current)

`orchd` runs as a systemd service on a Hetzner Cloud box (Ubuntu, Docker + gVisor).
The control API listens on `127.0.0.1:8080` and the gateway on `127.0.0.1:8081`.
A static overview page is served at `cloud.rapidnative.com` via Caddy.

## Quick start (local, macOS)

```bash
cd orchestrator
go build -o /tmp/orchd ./cmd/orchd
ORCHD_TINBASE_BIN=/path/to/tinbase ORCHD_DATA_ROOT=$PWD/.data /tmp/orchd

# provision a project (empty body -> one primary tinbase workload)
curl -X POST http://127.0.0.1:8080/v1/projects -d '{}'
# point supabase-js / curl at the returned endpoint; the gateway wakes it
```

`*.lvh.me` resolves to `127.0.0.1`, so subdomain routing works locally with no
hosts-file edits.
