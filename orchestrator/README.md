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
                   ┌──────▼───────┐   gateway (data plane): ref → project,
                   │   Gateway    │   wake on demand, reverse-proxy every
                   │  :8081       │   Supabase path incl. realtime WebSockets
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

- **`LocalDriver`** (today) runs each project as an OS process (`tinbase start`).
  Runs on macOS, so the whole control loop, gateway, wake sequencing, and backups
  are built and tested on a laptop. Suspend/Start = kill/relaunch (~0.3s locally).
- **`FirecrackerDriver`** (next, Linux bare metal) will implement the same
  interface with microVMs + jailer for VM-grade isolation and per-project
  snapshot/restore for sub-second wake. **Nothing above `runtime` changes.**

This is also the seam that makes RapidNative dev environments a second workload
type (`WorkloadRapidNativeDev`) on the same orchestrator.

## Packages

| Package | Role |
| --- | --- |
| `cmd/orchd` | daemon: wires everything, runs API + gateway + idle reaper |
| `internal/runtime` | `Runtime` interface, the generic `Workload` primitive, `LocalDriver` |
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
# provision a project
curl -X POST http://127.0.0.1:8080/v1/projects -d '{}'
#   → { "ref": "abc123...", "anon_key": "...", "endpoint": "http://abc123.lvh.me:8081", ... }

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
| `ORCHD_TINBASE_BIN` | `tinbase` | tinbase executable the LocalDriver spawns |
| `ORCHD_ENGINE` | (tinbase default) | `native` \| `wasm` \| `pgmem` |
| `ORCHD_IDLE_TIMEOUT` | `5m` | idle time before an instance scales to zero |
| `ORCHD_REGION` | `local` | single region served for now |

## Control-plane API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | liveness |
| `POST` | `/v1/projects` | provision a project (`{"type": "tinbase-project"}`) |
| `GET` | `/v1/projects` | list projects |
| `GET` | `/v1/projects/{ref}` | get one project (incl. keys + endpoint) |
| `DELETE` | `/v1/projects/{ref}` | stop + remove a project record |

## Not built yet

See [ROADMAP.md](./ROADMAP.md). Notably: backups, HA tier, connection pooling,
API auth, and the separate **admin panel** (a UI over this API, its own app).
