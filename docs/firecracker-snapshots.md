# Firecracker snapshots: instant wakes, write-triggered boots

Status: design + spike plan (2026-08-13). Follows the inode exhaustion of
2026-08-12 and the measured cold-wake UX failure (Expo Go times out at ~60s
while the first bundle takes 60–90s; users need three scan attempts).

## Why microVMs

- **Inodes become a per-guest resource.** Rootfs is a block device
  (device-mapper thin snapshot); the host filesystem stops being a shared
  inode pool that one tenant's node_modules can exhaust.
- **Real guest kernel.** gVisor's syscall interception dominates Metro's
  80k-file crawls (~1.5ms/file). Near-native fs in a guest kernel shrinks
  the cold path even before snapshots.
- **Memory snapshots make wake = resume.** Firecracker restores a paused
  VM's memory in hundreds of ms (lazy uffd paging makes first-touch even
  cheaper). A resumed Metro still holds its transform caches, file map and
  bundle — no rebundle at all.

## Architecture

A new orchd runtime driver (`firecracker`), sibling to `docker`, plus an
image pipeline addition. gVisor/docker remains the fallback runtime; the
driver interface (Create/Start/Stop/Exec/WriteFile/Logs) already abstracts
what the gateway and manager need.

### Image pipeline

Image build (unchanged docker build, cache-stable layers) gains a final
stage: export the built workspace image to an ext4 base volume in a dm-thin
pool, boot a **template VM** from it, start the dev server, warm the bundle
(the existing zz-warm route), then pause and snapshot. Artifact per image
version: `{base ext4 volume, kernel, warm memory snapshot}` — one per
template version, shared by every project, like today's shared deps
extraction.

### Project lifecycle

- **Create**: thin-clone the base volume (block-level CoW — bytes and
  "inodes" cost ~zero), resume from the *template* snapshot, agent applies
  the project's file delta, Metro incrementally rebuilds (seconds, warm
  caches), then the VM idles normally.
- **Suspend** (scale-to-zero): pause VM, write memory snapshot to disk,
  kill. RAM cost of a suspended project: zero.
- **Wake on request**: restore from the project's own snapshot (~0.5s) and
  serve. The bundle is already in memory.
- **Wake on write** (Sanket's proposal, adopted as a core rule): a file
  write targeting a suspended VM RESUMES it first, then the guest agent
  applies the write inside the VM. The dev server processes the change live
  (Metro incremental rebuild, npm install on package.json via the boot
  wrapper's hash guard, tinbase migration on supabase/ changes), and the
  idle reaper re-snapshots it later. Consequences:
  - The snapshot is always warm *with the latest code* — by the time a
    generation burst finishes, the project is scan-ready.
  - No mutating a snapshotted VM's disk behind its back (the consistency
    trap of today's host-side write-through: a resumed guest kernel must
    not discover its filesystem changed under it — page cache and dentry
    caches would be stale; every write goes through a live guest).
  - Rebuild steps run exactly when needed, never at wake time.

### Write path / exec path

No virtiofs in Firecracker: the guest agent (tiny static binary as PID 1's
child) speaks vsock — file put/delete/batch, exec, log streaming, health.
The gateway keeps talking HTTP to the VM's tap IP; the manager's
WriteWorkloadFile maps to agent calls (resuming first per the rule above).

### Snapshot storage budget

A memory snapshot ≈ RSS (Metro steady-state ~600MB–1GB). 300 projects ×
1GB does not fly. Two-tier answer:

- **Hot tier (bounded LRU, ~30–50 projects)**: per-project snapshots for
  recently-active projects — instant resume.
- **Everything else**: no per-project snapshot; wake = resume the shared
  *template* snapshot on the project's thin volume + agent-applies delta +
  incremental rebuild. Target ~10s, still 6–9x better than today, with
  zero per-project storage.

Evict/restore between tiers by recency. Measured 2026-08-13: a Full
snapshot is the entire guest RAM (3073MB on disk, not sparse — no delta),
but zstd -3 takes it to 182MB (17:1 — untouched pages are zeros, JS heaps
compress well). So the working tiering is: hot LRU raw (117ms wake), warm
zstd'd (~180MB/project, ~2-4s wake, compressed async after suspend; 300
projects ~= 55GB), cold none. fallocate --dig-holes is a free intermediate
(punches zero pages, ~1.2GB sparse, no CPU). FC Diff snapshots
(dirty-pages-only) are the true delta for re-suspends and come after the
compress tier — they need base+merge bookkeeping on restore.

## Spike plan (box-first; macOS has no KVM)

S1 — DONE 2026-08-13, verdict: GO. Firecracker v1.16.1, CI kernel 6.1.141,
rootfs = docker export of fullstack-supabase@v44 mobile + 20-line init,
4 vCPU / 3GB. Measured on the production box:

| metric                        | gVisor today | firecracker |
|-------------------------------|--------------|-------------|
| boot -> Metro listening       | ~14s         | 5.9s        |
| cold first bundle             | 43-90s       | 20.9s       |
| warm request                  | ~1s          | 0.24s       |
| restore -> serving            | n/a (60-90s) | 61ms        |
| first bundle after restore    | n/a          | 0.30s       |
| snapshot create / mem size    | n/a          | 8.8s / 3.1G |

The guest kernel alone halves-to-quarters the cold path (gVisor's syscall
tax confirmed); snapshot restore makes wake effectively free. The 3.1GB
mem file (= full guest RAM) is the cost the two-tier design + balloon-
before-snapshot must manage. Spike harness: /opt/fc-spike on the box.

S2 — DONE 2026-08-13, verdict: GO with two lessons. Loopback-backed thin
pool, base volume = S1 rootfs + 10-line in-guest HTTP agent baked into init.

| metric                                   | measured        |
|------------------------------------------|-----------------|
| thin clone creation                      | ~47ms each      |
| clone space cost (12 clones)             | ~0 (CoW blocks) |
| fresh boot from thin clone -> Metro      | 6.9s            |
| ONE template snapshot restored on a      |                 |
|   DIFFERENT clone (relative drive path)  | 81ms            |
| delta apply (agent) + incremental        |                 |
|   rebundle = the cold-tier wake          | 3.3s            |
| 12 concurrent fresh boots -> all serving | 8.0s            |

The relative-path trick works: the snapshot records "rootfs.blk" and each
VM's cwd symlinks it to its own thin device — one template snapshot serves
every project (production should use the jailer's chroot for the same
effect). Cold-tier wake beat its 10s target 3x: 0.08s restore + 3.3s
incremental rebundle.

Lessons: (1) the 12 simultaneous cold boots drove host load to 735 —
loopback-file pools serialize IO horribly; production needs the pool on a
real LV/partition and the driver must stage concurrent cold boots (wakes
via restore are gentle by comparison). (2) the template bundle returned
500 on the clone (S1's rootfs was captured dirty — never cleanly unmounted,
with stale in-guest Metro caches); S3's image pipeline must build the base
volume from a clean export and warm it in one boot, not reuse a
crash-copied filesystem.

S3 — driver DONE 2026-08-13 (manager wiring remains for S4). The
FirecrackerDriver (internal/runtime/firecracker.go + fc_host.go +
fc_image.go) implements the Runtime interface; `fcharness` (cmd/fcharness)
drives it end to end on the box. Full lifecycle, measured:

| step                                  | measured |
|---------------------------------------|----------|
| image prep (once per version)         | 2m22s    |
| Create: thin clone + env + fresh boot | 7.3s     |
| first bundle                          | 20.3s    |
| Suspend (3GB mem snapshot write)      | 25-49s   |
| Start = restore, to serving           | 117-159ms|
| bundle right after restore            | 0.3-0.9s |
| agent file write                      | 70ms     |

Repeatable across suspend/restore cycles. Hard-won rules encoded in the
driver: the warm volume must come from a clean RUNNING halt (a paused VM
can't process /halt; pause-then-kill tears cache writes and every clone
500s — hit twice), the image CMD assumes docker's WORKDIR so init must cd
/app, and kill() must reap the VMM before a respawn (the tap fd EBUSYs).
Suspend's 25-49s snapshot write wants a real-LV pool + memory balloon;
fine for a reaper, not for anything user-facing.

Snapshot economics implemented 2026-08-13: Suspend hands the raw mem file
to an async Compact() (zstd -3, measured 148MB for the 3GB guest); wake
transparently decompresses when only the .zst remains (3.4s to serving)
and restores raw in ~110ms when it hasn't been compacted yet — the hot/warm
tiering falls out of compaction timing, no LRU machinery needed yet. And
Spec.Ephemeral marks disposable workloads: suspend is teardown (119ms, no
snapshot at all — the CoW clone stays so files survive), wake is a fresh
boot. Intended split: tinbase db workloads persist, everything else can be
ephemeral because project files re-sync from the website. The
which-workloads-are-ephemeral policy is a next-phase decision.

Remaining for S4: manager multi-runtime selection (pilot template flag),
wake-on-write via driver WriteFile, reaper calling Compact, backups export,
HTTP agent -> vsock hardening, jailer.

S4 — gateway pilot: fullstack-supabase on FC end to end (create → scan →
suspend → write → auto-resume → scan), then flip the default.

## Open questions

- Kernel/rootfs maintenance: pin one guest kernel per orchd release.
- tinbase workloads: same driver or keep on docker initially (their wake
  path is cheap; pilot mobile-only first).
- Backups: today's tarball flow reads host workspaces; with in-VM disks the
  agent needs an export call (or mount thin volumes read-only host-side
  while suspended).
- Expo Go websockets across suspend/resume: clients reconnect; verify HMR
  registration survives a resume (it should — same process memory).
