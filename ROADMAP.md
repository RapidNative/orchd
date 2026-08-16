# Roadmap

Parked work with context, so it survives the gap until we pick it up. Items
are written from incidents on the reference deployment (a 40-core box running
JS dev-server workloads with scale-to-zero), because the numbers are what make
the trade-offs decidable.

## Gateway / manager

- **Idle starvation after client loss.** Idleness is only refreshed per gateway
  request (`EnsureRunning` → `touch`). A client that reconnects on a loop — an
  abandoned dev-server websocket, a background browser tab — retries every few
  seconds and keeps its workload awake forever. Observed: 14 workloads (~12GB)
  pinned after a provider network outage, zero suspends for three hours with a
  15-minute idle timeout. Consider classifying reconnect storms against real
  use, or not counting touches from failed protocol upgrades.
- **Project deletion vs live mounts.** `DeleteWorkload` unmounts the shared
  dependency overlay before reclaiming the data dir; `DeleteProject` used to
  skip it and left orphaned `merged` mounts (76 found after one mass delete).
  Fixed by releasing per-workload driver state in `DeleteProject` too, but a
  follow-up remains: during a 296-project mass delete, the images' versioned
  `deps/<workspace>` **symlinks** disappeared as well. Suspect deletion
  recursing through a live mount; `reclaimPath` must never run while a
  workload's mounts are active. Needs a reproduction.
- **Archive strategy.** A caller's database is the source of truth for project
  contents, so orchd projects are reconstructible: a "delete workloads after N
  days idle, re-provision on return" reaper takes cold storage to ~zero. Not
  urgent while the fleet fits the disk.

## Images and storage

- **Serialize builds per template; extract dependencies atomically.** Killing
  a build *request* does not kill the build (it runs `WithoutCancel`), so a
  retry ran concurrently with the original, both extracted dependencies into
  the same versioned directory, and the second landed nested
  (`deps/<ws>/node_modules` with every package duplicated). Node resolution
  climbs through the nested copy first, so bundles evaluated two copies of the
  same packages and native modules registered twice on device. Mitigated with
  a per-template build lock; still wanted: extract to a temp dir and rename
  into place, and refuse to extract into a versioned directory that exists.
- **Layer hygiene — mostly DONE, watch the next regression.** The box once ran
  out of *inodes* at 56% disk use (164GB of builder cache; every container
  start failed with "no space left on device"). Fixed by emitting
  toolchain steps before the app COPY, copying the dependency manifest before
  the install, deduplicating the dependency extraction by lockfile hash,
  pruning the builder cache after each build, and GC'ing unreferenced image
  versions. A deps-unchanged rebuild now costs ~25 inodes and seconds instead
  of ~160k and minutes. Remaining: clean package-manager caches in-RUN, and GC
  microVM image volumes the same way container images are GC'd.
- **~~Dependency registry cache~~** — done: ORCHD_WORKLOAD_ENV / ORCHD_BUILD_ENV
  carry a registry URL into every boot install and image build; the proxy
  itself is operator infrastructure, not orchd's.
- **Per-project deps families**: the FC shared node_modules volume shares the
  template's lockfile family; a project that diverges pays only its delta in
  the overlay upper, but two projects with the SAME divergence still pay it
  twice. Harvesting a booted project's node_modules into a new family volume
  would dedupe those — riskier (must quiesce the guest) and unproven demand.
- **Deps family GC**: family volumes are never deleted automatically; depsInUse
  enumerates references (running AND suspended VMs — snapshots restore with
  their drive set). Wire it into image-version GC.

## Runtime

- **Finish the microVM path** (see `docs/firecracker-snapshots.md` for design
  and measurements). Remaining: move the dm-thin pool off a loopback file onto
  a real logical volume (concurrent cold boots through loopback serialize IO
  badly — 12 at once drove load to 735); replace the in-guest HTTP agent with
  vsock; run VMs under the jailer; have the idle reaper call `Compact` instead
  of relying on the post-suspend goroutine; export backups through the agent
  since the guest owns its filesystem; clean up temp export dirs left behind
  when image prep is interrupted.
- **Storage-class choice for microVM rootfs.** Block-device rootfs is what
  removes host-inode exhaustion as a shared failure mode; if the driver is ever
  reworked around containerd, its devmapper snapshotter is the equivalent.

## Deployment

- `bootstrap.sh` installs stock Caddy; a deployment needing DNS-01 wildcard
  certificates has to build Caddy with its DNS provider plugin. Make that a
  bootstrap option rather than a manual step.
- Per-hostname on-demand TLS hits Let's Encrypt's 50-certs-per-week-per-domain
  cap when every tenant mints several hostnames. Wildcard-by-default where the
  DNS provider allows it.
