# Firecracker microVMs: instant wakes, write-triggered boots

Status: implemented and running in production on the reference deployment for
one workload class (a JS bundler dev server), behind the `ORCHD_FC_WORKLOADS`
allowlist. Code: `internal/runtime/firecracker.go`, `fc_host.go`,
`fc_image.go`, `mux.go`; integration harness: `cmd/fcharness`.

Two problems motivated it. Host **inode exhaustion**: thousands of dependency
trees sharing one filesystem exhausted the inode budget at 56% disk use, and
every container start then failed. And **cold-wake latency**: a suspended dev
server needed 60–90s to rebundle after waking, while its client gave up at
~60s, so users had to retry two or three times before a wake happened to be
ready.

## Why microVMs

- **Inodes become a per-guest resource.** Rootfs is a device-mapper thin
  volume; guest filesystems own their inodes, so the host's filesystem stops
  being a shared pool one tenant can exhaust.
- **A real guest kernel.** gVisor's syscall interception dominates workloads
  that crawl large dependency trees (~1.5ms/file on an 80k-file tree).
- **Memory snapshots make wake = resume.** A restored VM still holds the dev
  server's caches and its last built bundle, so there is nothing to rebuild.

## Architecture

A `firecracker` driver, sibling to `docker`, layered by `runtime.Mux`:
`ORCHD_FC_WORKLOADS` lists `template/workspace` pairs that Create routes to
microVMs; everything else stays on the default driver. Unset means the mux is
never constructed. A microVM create that fails falls back to the default
driver, and **the runtime choice is made once, at Create** — later calls route
by ownership, never by the allowlist (see `provisioning` skill for why).

### Image preparation

`PrepareImage` runs at build time for allowlisted workspaces:

1. `docker export` the built workspace image (a **clean** export, never a
   crash-copied filesystem) onto a thin volume, then bake in the guest init
   and agent.
2. Snapshot that base into a **warm** volume, boot one template VM from it,
   request the manifest's `warm` path once so caches and any bundle are hot,
   then halt the guest cleanly **while it is running**.
3. Project VMs are thin clones of the warm volume: ~47ms each, ~zero bytes.

The clean-halt ordering is load-bearing and cost two debugging rounds: a
*paused* VM cannot process a halt request, and pause-then-kill captures torn
page-cache writes. Clones of a dirty warm volume fail their first request with
corrupt-cache errors that look nothing like a filesystem problem.

### Per-VM networking

Every guest is identical inside its sandbox (`eth0` = 172.16.0.2/28 behind a
tap at 172.16.0.1). That uniformity is what makes a memory snapshot portable
between clones. Isolation comes from a network namespace per VM; the host
reaches each guest over a veth pair with a unique /30, DNAT'd to the guest.
The rootfs is attached by a **relative** path (`rootfs.blk` in the VM's cwd,
symlinked to its own thin device), which is what lets one snapshot restore
onto any clone. Under the jailer, its chroot achieves the same.

A namespace that is reused by a re-created ref keeps its old `ve0`, and veth
creation then fails with EEXIST — clear it first, and start a re-created ref
from a clean slate.

### Lifecycle

- **Create**: thin-clone the warm volume, mount it host-side to inject env and
  the caller's workspace files, fresh-boot in its own namespace.
- **Suspend**: pause, write a full memory snapshot, kill the VMM, then
  compress the snapshot off the caller's path.
- **Start**: restore the snapshot (decompressing first if needed). A failed
  restore drops the snapshot and falls back to a fresh boot — a slow wake
  beats no wake. A consumed snapshot is deleted so a crash cannot restore
  doubly-stale memory.
- **Wake on write**: a write to a suspended VM resumes it first, then applies
  the write through the guest agent, so the dev server processes the change
  live and the reaper re-snapshots later. The alternative — mutating the disk
  under a snapshotted kernel — corrupts its caches.
- **`Spec.Ephemeral`**: for workloads whose runtime state is disposable,
  suspend is teardown (no snapshot at all) and wake is a fresh boot. The CoW
  clone stays, so files survive for free.

### Snapshot economics

A full snapshot is the entire guest RAM — 3073MB on disk for a 3GB guest, not
sparse, no delta. But `zstd -3` takes it to ~150–180MB (17:1; untouched pages
are zeros and JS heaps compress well), which is what makes snapshotting every
suspended project affordable: ~300 suspended projects ≈ 45–55GB.

Tiering falls out of compaction timing rather than needing an LRU: a workload
woken soon after suspend restores from the raw file in ~0.1s; one that has been
idle long enough to compact pays ~3.4s and costs ~150MB at rest.
`fallocate --dig-holes` is a free intermediate (punches zero pages, no CPU).
Firecracker `Diff` snapshots are the true delta for re-suspends and need
base+merge bookkeeping on restore — worth doing after the compress tier.

## Measurements

All on the reference box, same workload, same image.

| step | docker + gVisor | firecracker |
| --- | --- | --- |
| create → dev server listening | ~15–20s | 6.9–7.4s |
| first request served (cold) | 43–90s | 5.7–20.9s |
| **wake → serving** | 40–60s | **0.11–0.16s** raw / 3.4s compressed |
| first request after wake | (part of the wake) | 0.30–0.9s |
| suspend (snapshot write) | n/a | 9–52s, then 3.1s to compact |
| snapshot size | n/a | 3073MB → 148MB |
| thin clone | n/a | 47ms |
| file write into the workload | write-through | 70ms via agent |
| per-project disk, running | ~100MB | ~0 (CoW) |
| per-project disk, suspended | ~100MB | +148MB snapshot |
| RAM, running | dev server RSS + ~29MB sentry | dev server RSS + ~50MB guest kernel |
| host inodes | ~80k/version + per-project caches | 0 |

Two caveats the numbers hide: containers share the host page cache for
dependency trees while each guest caches its own, so aggregate RAM at high
*running* density favors containers; and 12 simultaneous cold boots through a
loopback-backed thin pool drove host load to 735 (restores are gentle by
comparison, and fresh boots are now staggered by a semaphore).

## Operating it

```bash
# one-time: thin pool + binaries (see cmd/fcharness for a loopback pool helper)
fcharness pool-init
fcharness prepare <ignored> <docker-tag> "<run cmd>" "<warm path>"
fcharness create  <docker-tag> <ref> [KEY=VAL ...] [--ephemeral]
fcharness suspend <ref>          # also compacts
fcharness start   <docker-tag> <ref>
fcharness write   <ref> <guest-path> <local-file>
fcharness logs|status|delete <ref>
```

State lives under `<data>/fc`: `images/<name>/meta.json` (base and warm device
ids), `vms/<ref>/{meta.json,rootfs.blk,fc.sock,snap.*}`. A workload's runtime
is identifiable by whether `<data>/fc/vms/<workload-id>` exists. The guest
agent answers on port 9000 of the VM's namespace address
(`10.201.<idx*4+2>`): `/file`, `/exec`, `/logs`, `/halt`, `/health` — the
fastest way to see what a guest thinks is wrong.

## Remaining work

Tracked in `ROADMAP.md` under Runtime: real LV for the pool, vsock instead of
HTTP for the agent, the jailer, reaper-driven compaction, backup export
through the agent, and GC of microVM image volumes.
