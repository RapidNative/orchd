# tinbase-cloud orchestrator

The control plane + data plane that turns a single [tinbase](https://tinbase.dev)
process into a hosted, multi-tenant, Supabase-compatible platform.

It follows Supabase Cloud's **coupled** model (one dedicated backend per project)
but collapses Supabase's ~12-container per-project stack into one ~66 MB tinbase
process, so a single box packs far more projects. Idle projects **scale to zero**
and **wake on the first request**.

## Architecture

```
                 *.tinbase.cloud  (wildcard TLS in prod; *.lvh.me locally)
                          │
                   ┌──────▼───────┐   gateway (data plane): host → route →
                   │   Gateway    │   workload, wake on demand, reverse-proxy
                   │  :8081       │   every Supabase path incl. realtime WS
                   └──────┬───────┘
          ┌───────────────┼───────────────┐
   ┌──────▼──────┐  ┌─────▼──────┐   ┌─────▼──────┐   data plane: one tinbase
   │  tinbase    │  │  tinbase   │   │  tinbase   │   instance per project,
   │  + data dir │  │  + data dir│   │  + data dir│   suspended when idle
   └─────────────┘  └────────────┘   └────────────┘
                          ▲
                   ┌──────┴───────┐   API (control plane): provision, mint
                   │   API :8080  │   keys, lifecycle, region placement
                   └──────────────┘   backed by the store (JSON now, PG later)
```

### The runtime driver seam (why this runs on macOS today)

Everything above `internal/runtime` is substrate-agnostic. The control plane never
touches a VMM directly; it talks to a `Runtime` interface:

- **`LocalDriver`** runs each project as an OS process (`tinbase start`). Runs on
  macOS, so the whole control loop, gateway, and wake sequencing are built and
  tested on a laptop. Suspend/Start = kill/relaunch (~0.3s locally).
- **`DockerDriver`** runs each project as a container, optionally under **gVisor
  (`runsc`)** for VM-grade isolation *without KVM* — the Linux substrate for a
  shared box (Hetzner Cloud). Suspend/Start = `docker stop`/`docker start`;
  verified on real hardware at **2.8s cold provision** (incl. initdb) and
  **~0.9s wake** from scale-to-zero, with data persisted on the volume.
- **`FirecrackerDriver`** (next, Linux **bare metal**) will implement the same
  interface with microVMs + jailer and per-project snapshot/restore for
  sub-second wake. **Nothing above `runtime` changes.**

> gVisor needs no nested virtualization, so it runs on a plain Cloud VM;
> Firecracker/Kata require `/dev/kvm` and therefore a dedicated bare-metal box.

This is also the seam that makes RapidNative dev environments a second workload
type (`WorkloadRapidNativeDev`) on the same orchestrator.

## Packages

| Package | Role |
| --- | --- |
| `cmd/orchd` | daemon: wires everything, runs API + gateway + idle reaper |
| `internal/runtime` | `Runtime` interface, the generic `Workload` primitive, `LocalDriver`, `DockerDriver` (gVisor) |
| `images/tinbase` | Dockerfile for the tinbase workload image (Linux, native PG warmed in) |
| `internal/store` | durable project records (JSON now, managed Postgres later) |
| `internal/manager` | provisioning, key minting, wake / scale-to-zero lifecycle |
| `internal/gateway` | `<ref>.<base-domain>` routing + wake + reverse proxy |
| `internal/api` | control-plane / platform API (what the admin panel will call) |
| `internal/config` | env-driven config with local defaults |

## Run it locally

```bash
go build -o /tmp/orchd ./cmd/orchd

ORCHD_TINBASE_BIN=/path/to/tinbase \
ORCHD_DATA_ROOT=$PWD/.data \
  /tmp/orchd
```

Then:

```bash
# provision a project (empty body → one primary tinbase workload)
curl -X POST http://127.0.0.1:8080/v1/projects -d '{}'
#   → { "id": "abc123", "workloads": [ { "anon_key": "...",
#         "endpoints": ["http://abc123.lvh.me:8081"], ... } ] }

# point supabase-js (or curl) at the endpoint; the gateway wakes the instance
curl -H "Host: abc123.lvh.me" -H "apikey: <anon_key>" http://127.0.0.1:8081/rest/v1/
```

`*.lvh.me` resolves to `127.0.0.1`, so subdomain routing works locally with no
`/etc/hosts` edits.

### Config (env vars)

| Var | Default | Meaning |
| --- | --- | --- |
| `ORCHD_API_ADDR` | `127.0.0.1:8080` | control-plane API listen addr |
| `ORCHD_GATEWAY_ADDR` | `127.0.0.1:8081` | tenant-facing gateway listen addr |
| `ORCHD_BASE_DOMAIN` | `lvh.me` | host suffix stripped to get the project ref |
| `ORCHD_DATA_ROOT` | `~/.tinbase-cloud` | per-project data dirs + state file |
| `ORCHD_DRIVER` | `local` | runtime substrate: `local` (processes) or `docker` (containers) |
| `ORCHD_TINBASE_BIN` | `tinbase` | tinbase executable the LocalDriver spawns |
| `ORCHD_ENGINE` | (tinbase default) | `native` \| `wasm` \| `pgmem` (LocalDriver) |
| `ORCHD_IMAGE` | `tinbase:0.13.1` | container image the DockerDriver runs |
| `ORCHD_DOCKER_RUNTIME` | `runsc` | Docker runtime for tenant containers (`runsc` = gVisor; empty = runc) |
| `ORCHD_DOCKER_HOST` | (local daemon) | point the docker CLI at a remote daemon |
| `ORCHD_IDLE_TIMEOUT` | `5m` | idle time before an instance scales to zero |
| `ORCHD_REGION` | `local` | single region served for now |
| `ORCHD_TINBASE_MEM_MB` / `ORCHD_TINBASE_CPUS` | `384` / `0.5` | default memory/CPU cap for tinbase workloads |
| `ORCHD_DEV_MEM_MB` / `ORCHD_DEV_CPUS` | `512` / `1.0` | default memory/CPU cap for rapidnative-dev workloads |
| `ORCHD_PIDS_LIMIT` | `512` | max processes per container (fork-bomb backstop) |
| `ORCHD_BACKUP_DIR` | `<DataRoot>/backups` | local backup store root |
| `ORCHD_BACKUP_INTERVAL` | `0` (off) | auto-backup interval for tinbase workloads (e.g. `24h`) |
| `ORCHD_BACKUP_RETAIN` | `5` | backups kept per workload |

## Model: Project → Workload → Route

A **project** is a logical grouping (tenant/env). It owns one or more
**workloads** — the routable, independently-scheduled, scale-to-zero units. Each
workload has one or more **routes** (hostnames), resolved by an exact-match route
table, so both convention subdomains (`<ref>-<role>.<base-domain>`) and custom
domains work identically. The runtime lifecycle is keyed by workload id.

- A plain tinbase project = one project, one (primary) workload, one route
  `<ref>.<base-domain>`.
- A RapidNative project = one project, many workloads (`app`, `web`, `tinbase`,
  `api`), each on `<ref>-<name>.<base-domain>`, each its own isolated instance.

## Control-plane API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | liveness |
| `POST` | `/v1/projects` | create a project + workloads (empty body → one primary tinbase workload) |
| `GET` | `/v1/projects` | list projects with their workloads |
| `GET` | `/v1/projects/{id}` | get a project (workloads, routes, endpoints, keys) |
| `DELETE` | `/v1/projects/{id}` | stop + remove a project and all its workloads/routes |
| `POST` | `/v1/projects/{id}/workloads` | add a workload (`{"type","name","image","port"}`) |
| `GET` | `/v1/workloads/{id}` | get one workload |
| `DELETE` | `/v1/workloads/{id}` | stop + remove one workload and its routes |
| `POST` | `/v1/workloads/{id}/routes` | attach an extra hostname (`{"host"}`) to a workload |

Create a multi-workload project:

```bash
curl -X POST http://127.0.0.1:8080/v1/projects -d '{
  "name": "my-app",
  "workloads": [
    {"type": "tinbase-project", "name": ""},
    {"type": "tinbase-project", "name": "api"}
  ]
}'
# → primary at <ref>.<base>, api at <ref>-api.<base>, each an isolated instance
```

## Not built yet

See [ROADMAP.md](./ROADMAP.md). Notably: backups, HA tier, connection pooling,
API auth, and the separate **admin panel** (a UI over this API, its own app).
