# ORCHD — documentation

**ORCHD** is a generic control plane for hosted, multi-tenant orchestration. A single deployment is
named by its operator in **Settings** — this one runs as **tinbase cloud** (hosted
[tinbase](https://tinbase.dev), a cheaper, faster, high-availability alternative to Supabase Cloud);
another instance might be **RapidNative Cloud** for on-demand dev environments. Either way a
*workload* is a tinbase backend or a running dev app (Expo, Vite, an API server).

- **API base URL:** `https://api.tinbase.dev`
- **Admin panel:** `https://admin.tinbase.dev`
- **Source:** https://github.com/RapidNative/cloud

---

## Contents

**Guide**
1. [About tinbase cloud](#about)
2. [Repository & layout](#repo)
3. [Images & presets](#images)
4. [Adding regions](#regions)
5. [Adaptors (replaceable parts)](#adaptors)

**API reference**
6. [Authentication](#authentication)
7. [Endpoints](#endpoints) — Health · Projects · Workloads · Domains · Images · Backups · Regions · API keys · Settings · Metrics & events · Metadata · Internal

---

<a id="about"></a>
## 1. About ORCHD

ORCHD is a generic control plane for hosted, multi-tenant orchestration. A deployment is named by
its operator in **Settings** (`PUT /v1/settings/name`) — this one runs as **tinbase cloud** (hosted
tinbase, a cheaper/faster/HA Supabase-Cloud alternative); another instance might be **RapidNative
Cloud** for on-demand dev environments.

### Why it is built this way

- **Coupled model** — one tinbase per project. Isolation and per-tenant backups stay simple, and a
  noisy tenant can never touch another's data. This is how Supabase Cloud provisions too (a
  dedicated Postgres per project).
- **Docker + gVisor (`runsc`)** — VM-grade syscall isolation without needing KVM, so it runs on
  plain cloud VMs. Every workload gets cgroup caps (memory, CPU, PID count).
- **Scale-to-zero** — most dev/preview workloads sit idle. Idle containers are reaped after a
  timeout and cold-booted on the next request; `keep_warm` pins the hot ones.
- **Adaptor pattern everywhere** — the runtime driver, state store, backup target, event sink and
  metrics sink are all swappable interfaces. Most switch from the Settings page without a redeploy
  (see [Adaptors](#adaptors)).

### The object model

**Project → Workload → Route.**

- A **project** groups workloads.
- A **workload** is one routable, scale-to-zero container instance, built from a
  [preset or image](#images).
- A **route** maps a hostname to a workload. Every workload gets a default subdomain and can attach
  custom domains.
- Workloads are placed in a [region](#regions).

---

<a id="repo"></a>
## 2. Repository & layout

Source lives at https://github.com/RapidNative/cloud. Everything that runs on the box is tracked —
including the Caddy config and deploy scripts — so the server holds no untracked code.

```
cloud/
├── orchestrator/        Go control plane (stdlib + pgx only)
│   ├── cmd/orchd/       daemon: control API :8080, gateway :8081
│   └── internal/
│       ├── api/         HTTP routes + auth middleware
│       ├── runtime/     Runtime drivers: Local / Docker(gVisor) / (Firecracker)
│       ├── store/       Store + Persister: File(JSON) / Mem / Postgres(pgx)
│       ├── backup/      backup Store adaptor: Local / S3-SigV4
│       ├── events/      event Sink adaptor: Memory / Webhook / Multi
│       └── metrics/     metrics Sink adaptor: Nop / Log / HTTP
├── admin/               admin panel — Vite + React + TanStack + Tailwind
└── deploy/              Caddyfile, systemd units, deploy.sh
```

### Build & deploy

- Control plane: `go build ./cmd/orchd` (single static binary, run under systemd).
- Admin panel: `cd admin && npm run build` → static files served by Caddy at `admin.tinbase.dev`.
- One-shot: `./deploy/deploy.sh` builds both, ships them to the box, and reloads `orchd` + Caddy.

### Routing on the box

- `admin.tinbase.dev` — the panel (static) + `/api/*` reverse-proxied to the control API.
- `api.tinbase.dev` — the control API directly.
- `*.tinbase.dev` — the gateway; each host is resolved against the route table to a workload.
  On-demand TLS is gated by `/internal/tls-allow`, so certificates are only issued for real hosts.

### Run locally (ports, no domain)

For local testing there is **no domain, TLS, or Caddy** — everything runs on localhost ports.
`dev/local.sh [docker|mock|local]` starts orchd (control API + gateway) and the admin, and prints the
URLs and a dev API key. It sets `ORCHD_LOCAL=1`, which switches the base domain to `localhost` and
points endpoints at the gateway port.

```
Admin     http://localhost:5173
API       http://localhost:8080
Gateway   http://localhost:8081

# reach a workload by port — two equivalent ways:
http://localhost:8081/w/<key>       # subroute (pure ports, no DNS)
http://<key>.localhost:8081         # subdomain (browsers resolve to loopback)
```

Drivers: `docker` runs real containers (runc, no gVisor needed); `mock` boots the whole control
plane + admin with no Docker (workloads don't serve real traffic); `local` runs the tinbase binary
directly. Any explicit `ORCHD_*` var still overrides the local defaults.

---

<a id="images"></a>
## 3. Images & presets

**An "image" is a Docker image tag** on the orchestrator's Docker daemon (for example
`tinbase:0.10.0` or `rn-vite:dev`). A workload is just a container started from that image with a
port and resource caps. There is no separate custom image format.

### Presets vs. raw images

A **preset** is a friendly name that expands to an image + port + default limits, so you don't have
to remember them. `GET /v1/presets` lists the built-ins: `tinbase`, `expo`, `vite`, `api`. You can
always bypass presets and pass `image` / `port` / `memory_mb` / `cpus` explicitly.

```jsonc
// via preset
{ "preset": "vite" }

// explicit image (equivalent, minus the preset defaults)
{ "type": "rapidnative-dev", "image": "rn-vite:dev", "port": 8080,
  "memory_mb": 512, "cpus": 1.0 }
```

### Managing images from the panel

The **Images** page (and the `/v1/images` API) lists, pulls and removes images on a region's Docker
host — no shell needed to **pull** a published tag. Pick the region, paste a ref like
`ghcr.io/acme/app:1.2.0`, and pull; delete removes a tag (with a forced-removal fallback when a
container still holds it). See the [Images API](#images-api).

### Building a custom image

Pulling is in the panel; **building** is not yet (a build needs a Docker context upload, which is on
the roadmap). Until then, build on the box the standard way and reference it — no redeploy needed:

```bash
# build from a Dockerfile
docker build -t my-runtime:dev .

# then reference it when creating a workload
curl -X POST https://api.tinbase.dev/v1/projects/<id>/workloads \
  -H "Authorization: Bearer $TINBASE_KEY" \
  -H "Content-Type: application/json" \
  -d '{"type":"custom","image":"my-runtime:dev","port":3000,"memory_mb":512}'
```

Requirements for a custom image: it must **listen on `0.0.0.0`** at the `port` you declare (not
`127.0.0.1`), and start in the foreground. To make it a first-class named preset (so it shows in
`/v1/presets` and the create dropdown), add it to the preset catalog in
`orchestrator/internal/runtime` and redeploy.

---

<a id="regions"></a>
## 4. Adding regions

**What.** A region is a **placement target** for workloads. It carries a `docker_host` that points
the runtime driver at a Docker daemon — empty means the local daemon on the control-plane box; a
`tcp://…` value points at a separate worker node. New projects land in the **default** region unless
one is chosen at create time.

**Why.** It's the seam for going multi-node and multi-geo: put workers near users to cut latency,
keep tenant data in a required jurisdiction, and spread load past a single box's memory/disk ceiling.
The control plane stays central; only where containers *run* moves.

### How to add one

1. **Expose the worker's Docker daemon** to the control-plane box over a **private network** (never
   the public internet) — a TLS socket or an SSH tunnel to `tcp://node2:2375`.
2. **Create the region** — in the panel under **System → Regions**, or
   `POST /v1/regions` with `{ "name": "EU West", "docker_host": "tcp://node2:2375" }`. The id is a
   slug of the name (`eu-west`).
3. **Make sure the images exist there** — each region's Docker host needs the image tags you'll run
   (see [Images & presets](#images)).
4. **Place work** — pass `region` when creating a project, or `POST /v1/regions/{id}/default` to
   make it the new default.

You can't delete the default region — set another default first, then delete. Full data-locality
(per-region backup targets, private networking, an HA data tier) is the next hardware milestone; the
API and model are already region-aware.

---

<a id="adaptors"></a>
## 5. Adaptors (replaceable parts)

Every pluggable subsystem is an interface with multiple implementations. The ones marked **Settings**
switch live from the Settings page; the rest are chosen at deploy via env.

| Adaptor        | Implementations                                       | Switch via        |
|----------------|-------------------------------------------------------|-------------------|
| Runtime driver | Local · Docker (gVisor runsc) · Firecracker (future)  | deploy env        |
| State store    | JSON file · SQLite (WAL) · Postgres (pgx) · Mem        | deploy env        |
| Backup target  | Local dir · S3 / R2 (SigV4)                            | Settings          |
| Event sink     | Memory · Webhook · Multi                               | Settings (webhook)|
| Metrics sink   | Nop · Log · HTTP collector                             | Settings          |

This is what keeps the platform portable: the FileStore can become Postgres, local backups can
become R2, and the Docker driver can become Firecracker — each without touching the API surface
below.

### Control-plane state store

The project/workload index has three durable backends, selected at deploy:

- **JSON file** (default) — simple, single file.
- **SQLite (WAL)** — set `ORCHD_STATE_SQLITE=/path/orchd.db`. Recommended for a single-box control
  plane: atomic, crash-safe, incremental writes, and a real `.db` file. Switching auto-migrates an
  existing `projects.json` sitting next to it on first boot.
- **Postgres** — set `ORCHD_STATE_DSN`. For a distributed/HA control plane.

The index is small but high-value, so it is backed up **off-box on the backup schedule** (and via
`POST /v1/system/backup`), stored under the reserved key `_control-plane`; for SQLite the WAL is
checkpointed first so the snapshot is consistent. Restoring it is a manual op (fetch + extract),
since it re-seeds the whole control plane.

---

<a id="authentication"></a>
## 6. Authentication

Every `/v1/*` endpoint requires an API key. `/healthz` is the only open endpoint.

- Pass the key as `Authorization: Bearer <key>` **or** `X-API-Key: <key>`.
- The **bootstrap key** (from the server key file) is always **admin**. You can mint more keys
  (`POST /v1/keys`) with a role.
- **Roles:** *any key* (readonly) may call every `GET`; **admin** is required for any
  `POST`/`PUT`/`DELETE`. A readonly key on a mutating call → `403`.
- Missing/invalid key → `401`. Over the per-key rate limit → `429` (with `Retry-After`).

```bash
curl https://api.tinbase.dev/v1/projects \
  -H "Authorization: Bearer $TINBASE_KEY"

curl -X POST https://api.tinbase.dev/v1/projects \
  -H "Authorization: Bearer $TINBASE_KEY" \
  -H "Content-Type: application/json" \
  -d '{"workloads":[{"preset":"tinbase"}]}'
```

Errors are JSON: `{ "error": "…" }`. Status codes: `200/201` ok, `204` no content, `400` bad
request, `401` unauthorized, `403` forbidden (role), `404` not found, `409` conflict, `429` rate
limited, `501` not implemented (capability off, e.g. image management on `LocalDriver`).

**Role legend below:** *no auth* = open · *any key* = readonly ok · *admin key* = admin only.

---

<a id="endpoints"></a>
## 7. Endpoints

### Health

#### `GET /healthz` — *no auth*
Liveness probe. The only endpoint that never requires a key.
```json
{ "status": "ok", "auth": true }
```

### Projects

A project is a logical grouping of workloads. Creating one provisions its workloads and returns
their keys and endpoints.

#### `POST /v1/projects` — *admin key*
Create a project + its workloads. Empty body creates one primary tinbase workload. `region` defaults
to the default region.
```jsonc
// request
{
  "name": "my-app",           // optional
  "region": "local",          // optional (default region if omitted)
  "workloads": [              // optional (default: [{ "preset": "tinbase" }])
    { "preset": "tinbase" },
    { "preset": "vite" },
    { "preset": "api" }
  ]
}
```
```jsonc
// response
{
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
}
```

#### `GET /v1/projects` — *any key*
List all projects with their workloads.

#### `GET /v1/projects/{id}` — *any key*
Get one project (workloads, keys, endpoints, routes).

#### `DELETE /v1/projects/{id}` — *admin key*
Stop and remove a project, all its workloads/routes, and its on-disk data. Destructive — `204`.

### Workloads

A workload is one routable, scale-to-zero instance. Presets: `tinbase`, `expo`, `vite`, `api` (see
`GET /v1/presets`). Custom: `type` / `image` / `port` / `memory_mb` / `cpus`.

#### `POST /v1/projects/{id}/workloads` — *admin key*
Add a workload to a project.
```jsonc
{
  "preset": "vite",           // or set type/image/port explicitly
  "name": "web",              // optional role
  "image": "rn-vite:dev",     // optional (preset supplies it)
  "port": 8080,               // optional
  "memory_mb": 512,           // optional cap override
  "cpus": 1.0                 // optional cap override
}
```

#### `GET /v1/workloads/{id}` — *any key*
Get one workload (state, limits, keys, routes, `last_seen`).

#### `DELETE /v1/workloads/{id}` — *admin key*
Stop and remove one workload and its routes + data. `204`.

#### `POST /v1/workloads/{id}/keepwarm` — *admin key*
Toggle always-on. Enabling boots the workload now and exempts it from scale-to-zero.
```jsonc
// request
{ "enabled": true }
// response
{ "keep_warm": true }
```

#### `GET /v1/workloads/{id}/stats` — *any key*
Live memory + CPU (docker stats). Empty snapshot when the workload is not running.
```json
{ "mem_usage": "76.2MiB / 384MiB", "mem_perc": "19.8%", "cpu_perc": "1.0%" }
```

#### `GET /v1/workloads/{id}/logs` — *any key*
Container logs (stdout+stderr). Params: `tail=<n>` (default 200).
```json
{ "logs": "…last N lines…" }
```

### Domains (routes)

Every workload gets a default subdomain. Attach custom domains (bring-your-own): point the domain
(CNAME/A) at the gateway; a Let's Encrypt cert is issued on the first HTTPS request.

#### `POST /v1/workloads/{id}/routes` — *admin key*
Attach a hostname (custom domain) to a workload.
```jsonc
// request
{ "host": "app.customer.com" }
```

#### `DELETE /v1/routes` — *admin key*
Detach a hostname. `204`. Params: `host=<hostname>` (query, required).

<a id="images-api"></a>
### Images

Manage the Docker images a region's daemon can launch as workloads. Requires the Docker driver —
`LocalDriver` returns `501`. Select a region with the optional `region` query/body param (empty =
default region).

#### `GET /v1/images` — *any key*
List images on a region's Docker host (excludes dangling layers). Params: `region=<id>` (optional).

`digest` is the **stable content identity** to pin against — the registry manifest digest, or the
config digest for locally built/loaded images. It is driver-neutral: any registry-backed runtime
(Docker today, firecracker-containerd later) can report it. `ref` / `id` are the mutable,
human-readable pointers.
```json
[ { "repository": "rn-vite", "tag": "dev", "ref": "rn-vite:dev",
    "id": "a1b2c3d4e5f6", "digest": "sha256:9f86d081…", "size": "412MB",
    "created_at": "2 days ago" } ]
```

#### `POST /v1/images/pull` — *admin key*
Pull an image tag onto a region's Docker host. Blocks until the pull finishes; returns CLI output.
```jsonc
// request
{ "ref": "ghcr.io/acme/app:1.2.0", "region": "" }
// response
{ "ref": "ghcr.io/acme/app:1.2.0", "output": "…docker pull output…" }
```

#### `DELETE /v1/images` — *admin key*
Remove an image by ref or id. Fails if a container still uses it unless `force=true`. `204`.
Params: `ref=<ref|id>` (required) · `region=<id>` (optional) · `force=true` (optional).

### Backups

Byte-exact volume snapshots (tar+gz). A backup is **per-workload**; a project backup just fans out
over its workloads. Target is local or S3/R2 (see Settings). Restoring replaces a workload's current
data.

#### `POST /v1/workloads/{id}/backups` — *admin key*
Back up one workload now.
```json
{ "id": "wid__20260719T...", "workload_id": "wid", "created_at": "...", "size_bytes": 4797652 }
```

#### `POST /v1/projects/{id}/backups` — *admin key*
Back up every workload in a project.
```json
[ { "id": "...", "workload_id": "...", "size_bytes": 4797652 } ]
```

#### `GET /v1/backups` — *any key*
List all backups (newest first).

#### `GET /v1/workloads/{id}/backups` — *any key*
List backups for one workload.

#### `POST /v1/workloads/{id}/restore` — *admin key*
Restore a workload from a backup (replaces current data, then reboots it).
```jsonc
// request
{ "backup_id": "wid__20260719T..." }
// response
{ "status": "restored" }
```

#### `DELETE /v1/backups/{id}` — *admin key*
Delete a backup. `204`.

#### `POST /v1/system/backup` — *admin key*
Snapshot the **control-plane state** (the project/workload index) off-box, under the reserved key
`_control-plane`. Runs automatically on the backup schedule too; SQLite WAL is checkpointed first.
```json
{ "id": "_control-plane__20260719T...", "workload_id": "_control-plane", "size_bytes": 8123 }
```

### Regions

A region is a placement target; `docker_host` points it at a worker node's Docker daemon (empty =
local). Projects are placed in the default region unless one is chosen at create. See
[Adding regions](#regions).

#### `GET /v1/regions` — *any key*
List regions.
```json
[ { "id": "local", "name": "local", "docker_host": "", "is_default": true, "created_at": "..." } ]
```

#### `POST /v1/regions` — *admin key*
Create a region (id is a slug of the name).
```jsonc
// request
{ "name": "EU West", "docker_host": "tcp://node2:2375" }
// response
{ "id": "eu-west", "name": "EU West", "docker_host": "tcp://node2:2375", "is_default": false }
```

#### `DELETE /v1/regions/{id}` — *admin key*
Delete a region (not the default — set another default first). `204`.

#### `POST /v1/regions/{id}/default` — *admin key*
Make a region the default.
```json
{ "default": "eu-west" }
```

### API keys

Multiple named keys with roles. The bootstrap key (server key file) is always admin. Keys are stored
hashed; the plaintext is shown once at creation.

#### `GET /v1/keys` — *any key*
List keys (no secrets).
```json
[ { "id": "...", "name": "ci-bot", "role": "readonly", "created_at": "..." } ]
```

#### `POST /v1/keys` — *admin key*
Create a key. The plaintext key is returned exactly once.
```jsonc
// request
{ "name": "ci-bot", "role": "readonly" }
// response
{ "key": "tbk_9f8e…", "meta": { "id": "...", "name": "ci-bot", "role": "readonly" } }
```

#### `DELETE /v1/keys/{id}` — *admin key*
Revoke a key. `204`.

### Settings

Runtime-configurable platform settings. Secrets are stored server-side and never returned.

#### `GET /v1/settings` — *any key*
Instance name, current backup target (secret masked), event webhook, and metrics sink.
```json
{
  "instance_name": "tinbase cloud",
  "backup": { "type": "s3", "endpoint": "...", "bucket": "...", "region": "...", "access_key": "..." },
  "backup_secret_set": true,
  "webhook": { "url": "" },
  "metrics": { "type": "nop" }
}
```

#### `PUT /v1/settings/name` — *admin key*
Name this deployment (shown in the sidebar). ORCHD is the generic engine; the name is per-instance.
```jsonc
// request
{ "instance_name": "tinbase cloud" }
// response
{ "instance_name": "tinbase cloud" }
```

#### `PUT /v1/settings/backup` — *admin key*
Set the backup destination. Leave `secret_key` blank on an s3 target to keep the existing one.
```jsonc
{
  "type": "s3",               // "local" | "s3"
  "endpoint": "https://<acct>.r2.cloudflarestorage.com",
  "bucket": "backups", "region": "auto", "prefix": "backups",
  "access_key": "…", "secret_key": "…"
}
```

#### `PUT /v1/settings/webhook` — *admin key*
Set the event webhook URL (blank = off). Control-plane events are POSTed here as JSON.
```jsonc
{ "url": "https://example.com/hooks" }
```

#### `PUT /v1/settings/metrics` — *admin key*
Set the metrics sink (`type`: `nop` | `log` | `http`).
```jsonc
{ "type": "http", "url": "https://collector/metrics" }
```

### Metrics & events

#### `GET /v1/metrics` — *any key*
Live fleet snapshot.
```json
{ "time": "...", "projects": 3, "workloads": 6, "running": 2, "suspended": 4, "mem_mb_allocated": 768 }
```

#### `GET /v1/events` — *any key*
Recent control-plane events (audit feed), newest first. Params: `limit=<n>` (default 100).
```json
[ { "id": "...", "time": "...", "type": "project.created", "project_id": "abc123", "message": "" } ]
```

### Metadata

#### `GET /v1/presets` — *any key*
Available workload presets.
```json
[ "api", "expo", "tinbase", "vite" ]
```

#### `GET /v1/info` — *any key*
System configuration: driver, region, base domain, idle timeout, default resource limits, rate
limit, backups/metrics status, presets.
```json
{
  "instance_name": "tinbase cloud",
  "driver": "docker+runsc", "region": "local", "base_domain": "tinbase.dev",
  "idle_timeout": "2m0s", "image": "tinbase:0.10.0", "rate_limit_per_min": 600,
  "limits": { "tinbase_mem_mb": 384, "tinbase_cpus": 0.5, "dev_mem_mb": 512, "dev_cpus": 1, "pids_limit": 512 },
  "backups": { "enabled": true, "interval": "24h0m0s", "retain": 7 },
  "images_supported": true,
  "metrics": { "type": "nop" },
  "presets": ["api","expo","tinbase","vite"]
}
```

### Internal

Used by the platform itself, not for general clients.

#### `GET /internal/tls-allow` — *no auth*
Caddy on-demand-TLS gate: returns `200` for admin/api and any host in the route table, `403`
otherwise — so certificates are only issued for real hosts. Params: `domain=<hostname>`.
