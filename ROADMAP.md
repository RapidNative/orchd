# Roadmap

Parked work with context, so it survives the gap until we pick it up.
(Background: the Aug-2026 RapidNative dev-server round; details in the
rapidnative-website template history and this repo's bench/ harnesses.)

## Dev-server: hybrid boot handoff (jetplane + Metro)

Today (fullstack-supabase@v37): mobile workloads run real Metro (`expo start`)
with baked caches — cold first bundle ~43s, warm rebundles ~1s, stock HMR. The
43s is expo/metro startup + an ~80k-file node_modules crawl under gVisor, and
recurs on every scale-to-zero wake.

Plan: jetplane thin-serve answers instantly from the baked bundle image while
Metro warms behind it with the shared caches; jetplane transparently proxies
manifest/bundle//hot//message to Metro once its first bundle is ready, then
frees its own bundle memory. Instant wakes + stock dev semantics. Test ladder
exists (jetplane repo tests/: host suite, simulator ground-truth, docker,
prebuilt image, then orchd). See ~/.claude/plans/dev-server-reset.md Phase 2.

## Dev-server: CDN vendor layer / alternative bundlers (evaluation)

Sanket's idea: extend browser-metro to native platforms and ship pre-transpiled
android/ios packages from esm.reactnative.run — the immutable vendor layer
(98%+ of a bundle) served from a network cache instead of per-machine disk.
Would also solve cross-project cache sharing with no shared volume. Hard parts
to size before code: module-id linking across separately delivered chunks,
Hermes bytecode vs plain-JS eval cost, strict RN/SDK version coupling, offline
behavior in Expo Go. Also evaluate Re.Pack (re-pack.dev, Rspack-based RN
bundler: real HMR + caching story) as an off-the-shelf alternative. Decision
gate: pursue only if hybrid wake numbers disappoint or fleet economics demand
CDN sharing. See ~/.claude/plans/dev-server-reset.md Phase 3.

## Gateway/manager: idle & storage hygiene

- Idle starvation after client loss: idleness is only refreshed per gateway
  request (EnsureRunning -> touch). Abandoned dev clients (Expo Go's
  reconnecting websocket, background browser tabs) retry every few seconds and
  keep workloads awake indefinitely — observed 14 workloads (~12GB) pinned
  after the 2026-08-09 provider network outage. Consider classifying reconnect
  storms vs real use, or capping touches from failed upgrades.
- Per-project disk: DONE 2026-08-09 (template v38) — read-through FileStore
  chain replaced the ~800MB per-project cache copy; measured ~100MB/project
  (58MB mobile + 40MB db). Note for future measurements: du on a workload dir
  counts the deps overlay's MERGED mountpoint (~700MB shared layer) — read
  .deps/upper for the real per-project delta.
- Archive strategy: project source of truth is the website DB; orchd projects
  are reconstructible. A "delete workloads after N days idle, re-provision on
  return" reaper turns cold storage cost to ~zero. Not urgent at current scale
  (65 projects = 48GB of 915GB).
- Project deletion leaks the deps overlayfs mount: 76 orphaned .deps/merged
  mounts found on 2026-08-09 after deleting all projects — DeleteProject must
  umount the overlay before removing the workload dir.
- npm registry cache (verdaccio) on the box for faster installs/builds.
- bootstrap.sh: install the custom caddy build (vercel-dns) instead of stock.
- Image layer hygiene (2026-08-12, after the box ran out of INODES at 56% disk
  — 164GB of buildkit cache from a week of builds; "no space left on device"
  on every container start). Per-version unique bytes are ~2.2GB because the
  Dockerfile emission (manager/image.go) puts everything after COPY: the
  apt+bun step (567MB) doesn't depend on app files and belongs before COPY;
  npm install (1.19GB) should COPY package.json+lockfile first so the layer
  caches across versions; apt lists + npm cache should be cleaned in-RUN.
  Also: bound builder cache after each build (docker builder prune
  --keep-storage 30GB), and GC image versions no live workload references
  (722MB deps extraction each, 5 versions retained today).
- Evaluate microVM runtime (firecracker) to replace gVisor. /dev/kvm is
  available on the box. Cheapest path: keep the docker driver and swap the
  OCI runtime runsc -> kata-containers with the firecracker (or
  cloud-hypervisor) backend + containerd devmapper thin-pool snapshotter —
  block-device rootfs means guest filesystems own their inodes (kills the
  host-inode exhaustion class entirely), a real guest kernel drops gVisor's
  syscall tax on Metro's 80k-file crawls, and FC memory snapshots could make
  scale-to-zero wakes near-instant (fly.io's model). Costs to size: per-VM
  kernel+agent overhead (~30-50MB) vs shared page cache, virtiofs vs block
  for the deps overlay and write-through file API, docker-exec equivalents
  via the guest agent.
- Image builds must be serialized per template and extract deps atomically.
  2026-08-10: killing the build REQUEST doesn't kill the build (WithoutCancel),
  so a retry ran concurrently with the original; both extracted deps into the
  same versioned dir and the second copy landed NESTED
  (deps/mobile/node_modules with all 685 packages duplicated). Node resolution
  climbs through node_modules/node_modules first, so bundles evaluated two
  copies of expo-router/safe-area-context — "Tried to register two views with
  the same name RNCSafeAreaProvider" on device. Fix: per-template build lock
  (reject or queue concurrent builds) + extract to a temp dir and atomically
  rename into place; a versioned dir that already exists must never be
  extracted into again.
