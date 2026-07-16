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
- [ ] custom-domain routes + on-demand TLS (Caddy in front) for prod
- [ ] heterogeneous workload images (web/expo runners) — Spec.Image/Port are wired;
      needs the RapidNative runner images

### Phase 1 — Make it real, safely
- [ ] **API auth** — the control-plane API is unauthenticated today; add tokens/roles before it leaves localhost
- [ ] **Connection pooling in tinbase** — the single-writer limit is the #1 production blocker; open N connections to the embedded PG (prerequisite for the Pro tier)
- [ ] **Backups** — scheduled `pg_dump` + WAL archiving to S3-compatible storage (R2/Backblaze); restore-on-wake
- [ ] Per-project resource limits + fair-use quotas (free tier)
- [ ] Structured per-project logs/metrics (replace shared stderr)
- [ ] Graceful data-dir reclaim on delete (currently record-only delete)

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
