# tinbase-cloud orchestrator roadmap

## North Star

A **cheaper, faster HA replacement for Supabase Cloud**. Same coupled model (one
dedicated backend per project), but each backend is one ~66 MB tinbase process
instead of a 12-container stack, so density (and therefore cost) wins decisively.
Idle projects scale to zero; active ones wake in well under a second.

## Positioning decision (settled)

Aiming at **production HA Supabase-Cloud competition**, not just previews. The
honest constraint: HA + rock-bottom cost + scale-to-zero cannot all be true for
the *same* tier, so durability is **tiered** (as Supabase itself does):

| Tier | Compute | Durability | Audience |
| --- | --- | --- | --- |
| Free / preview | scale-to-zero, densely packed | nightly + WAL to object storage | RapidNative apps, hobby |
| Pro | always-on, kept warm | PITR | production apps |
| Team / Enterprise | primary + standby, auto-failover | true HA + read replicas | the "HA replacement" claim |

Free fleet stays absurdly cheap (tinbase's superpower); HA is the premium tier
that funds itself.

## Shared substrate

The orchestrator is generic over a `Workload` primitive. Two workload types ride
the same control plane / gateway / driver:

- `tinbase-project` — hosted Supabase-compatible backends (this product)
- `rapidnative-dev` — RapidNative dev environments (later; untrusted user code,
  which is *why* VM-grade isolation matters for the substrate)

## Phases

### Phase 0 — Local control loop (LocalDriver) ✅
- [x] `Runtime` interface + generic `Workload` primitive
- [x] `LocalDriver` (tinbase as OS processes) — runs on macOS
- [x] Control-plane API: provision / list / get / delete projects
- [x] Key minting (JWT secret → anon/service via `tinbase keys`)
- [x] Gateway: `<ref>.<base-domain>` routing + reverse proxy (REST/Auth/Storage/Studio, WebSockets)
- [x] Scale-to-zero (idle reaper) + wake-on-request (verified: ~0.3s local resume)
- [x] File-backed durable project store

### Phase 1.2 — Multi-domain: Project → Workload → Route ✅
- [x] Split the fused project/instance/route into Project (grouping) → Workload
      (routable, scale-to-zero unit) → Route (hostname → workload)
- [x] Exact-match route table in the gateway (supports `<ref>-<role>.<base>` and
      custom domains alike); runtime lifecycle keyed by workload id
- [x] API for workloads + routes; one project can own many isolated instances
- [x] verified on the box: a RapidNative-shaped project (`app`/`web`/`tinbase`/`api`)
      → four subdomains, four isolated gVisor containers, delete cascades cleanly
- [x] **subroutes** (`/w/<key>`) as the interim addressing before wildcard
      subdomains: gateway resolves by path prefix and rewrites, Caddy proxies `/w/*`
- [x] **heterogeneous workload images**: `rn-expo` (expo web export), `rn-vite`
      (vite dev), `rn-api` (hono) built + wired via a preset catalog; verified a
      four-image RapidNative project (tinbase/expo/vite/api) routing under gVisor
- [x] **wildcard subdomains + TLS** on tinbase.dev: `admin`/`api`/`<ref>` hosts via
      Caddy with real Let's Encrypt certs; project subdomains use on-demand TLS gated
      by orchd's `/internal/tls-allow` (certs only for real workload hosts)
- [ ] custom-domain routes (tenant-supplied domains) + on-demand TLS
- [ ] full Metro expo dev server (heavier) as a bare-metal-tier variant

### Phase 1 — Make it real, safely
- [x] **API auth** — bearer-token API key on the control plane (`/v1/*`), read from
      a file on the box, never logged. `/healthz` stays open. The control API is now
      reachable via Caddy at `/api/*` behind the key; a small **admin UI** (`/admin`)
      drives it. Follow-ups: multiple keys / roles, rate limiting, audit log
- [ ] **Connection pooling in tinbase** — the single-writer limit is the #1 production blocker; open N connections to the embedded PG (prerequisite for the Pro tier)
- [x] **Backups** — byte-exact volume snapshots (tar+gzip of the data dir, taken
      with the instance briefly suspended so Postgres is consistent), scheduled +
      on-demand, per-workload **and per-project** (generic across workload types),
      with retention. Restore verified end-to-end (data rolls back to the exact
      backup point).
- [x] **Off-box target (S3/R2)** — `Store` interface with `LocalStore` and an
      `S3Store` (hand-rolled SigV4, zero deps; works with S3/R2/B2/MinIO). Target is
      runtime-configurable from the admin panel (persisted in settings, secret
      masked). Verified against MinIO on the box (the on-box S3 mock): backup upload,
      list, and **restore all via SigV4**. Follow-ups: hot/WAL backups to remove the
      brief suspend; backups surviving project delete for undo; per-node S3 offload
- [x] **Per-container resource limits (cgroups)** — memory / CPU / pids caps via the
      DockerDriver, defaults by workload type (tinbase 384 MB·0.5 CPU, dev 512 MB·1.0
      CPU, 512 pids), env-tunable and per-workload override-able. One tenant can no
      longer starve the box. Applied at create; existing pre-limit containers keep
      their old config until recreated. Follow-up: recreate-on-wake so limits apply
      fleet-wide; fair-use quota accounting
- [x] **Data-dir reclaim on delete** — deleting a project/workload now removes its
      on-disk volume (guarded to stay within the data root), not just the record
- [x] **Events / audit + webhook adaptor** — control-plane actions emit events to a
      pluggable `Sink` (MemorySink activity feed + optional WebhookSink, fan-out via
      MultiSink). Admin: Activity page + webhook URL setting. Adaptor pattern, admin-configurable
- [ ] Structured per-project logs/metrics (replace shared stderr)

### Adaptors & admin settings (make replaceable components pluggable + configurable)
Everything replaceable should be an adaptor behind an interface, selectable/config
from the admin **Settings**. Done: backup store (local/S3), events sink (memory/webhook).
- [ ] **Regions CRUD in the admin panel** — currently a single hardcoded `local`
      region; make regions first-class (create/list/delete), each mapping to a
      node/data plane, so projects can be placed per region (prereq for multi-region)
- [ ] DNS provider adaptor (manual / Cloudflare / Vercel) for custom domains
- [ ] Mailer adaptor surfaced per project (console / SMTP / webhook)
- [ ] Metrics sink adaptor (none / log / statsd / prometheus)
- [ ] Control-plane auth adaptor (single key today → multiple keys + roles)

### Phase 1.5 — Docker + gVisor substrate on a Linux box ✅
Runs on a plain Hetzner **Cloud** VM (no KVM required — gVisor's `systrap`
platform is a userspace kernel).
- [x] `DockerDriver` behind the `Runtime` interface (docker CLI, gVisor `runsc`)
- [x] tinbase workload image (`images/tinbase`, native PG warmed in, non-root via gosu)
- [x] provisioned + verified on the box: **2.8s cold provision**, **~0.9s wake** from
      scale-to-zero, data persisted across suspend/resume (auth user survived)
- [x] orchd runs as a systemd service on the box
- [ ] resource limits / cgroups per container; per-tenant log sinks

### Phase 2 — Firecracker driver (the speed + isolation play)
Needs a **Linux bare-metal box** (e.g. Hetzner dedicated) — the current box is a
Cloud VM with **no `/dev/kvm`**, so Firecracker/Kata cannot run there.
**Action item: acquire a dedicated (bare-metal) box** for the microVM tier;
gVisor covers isolation until then.
- [ ] rootfs + guest-kernel build pipeline for the tinbase image (small, static)
- [ ] `FirecrackerDriver` behind the existing `Runtime` interface (API + `jailer`)
- [ ] tap/bridge networking + gateway → VM IP wiring
- [ ] per-project **snapshot/restore** = sub-second wake (reconfigure net/MAC + clock/entropy on resume)
- [ ] data on a per-project virtio-block device, decoupled from instance lifecycle

### Phase 3 — Multi-node, one region
- [ ] Scheduler / placement across nodes (custom now; evaluate Nomad vs k8s+Kata later)
- [ ] Store → managed Postgres; control plane becomes HA itself
- [ ] Warm-pool / keep-alive policy for hot projects

### Phase 4 — HA tier + multi-region
- [ ] Per-project streaming replication + automatic failover (Team/Enterprise)
- [ ] Read replicas
- [ ] Geo/anycast routing; control-plane federation across regions
- [ ] PITR productization

## Separate components (not this repo)

- **Admin panel** — a UI over the control-plane API, shipped as its own app once
  the API surface stabilizes. Kept separate from the orchestrator on purpose.
- **Billing** — metering + plans, wired to the tiers above.
