---
name: provisioning
description: How orchd provisions and runs tenant workloads — the project/workload/template/image/route model, the two creation modes, the delta a caller layers over a template, lifecycle (create → suspend → wake → delete), the file-write paths, and how the docker and firecracker runtimes differ. Use when changing the manager, a runtime driver, the image pipeline, or when debugging why a workload won't boot, wake or serve.
---

# Provisioning in orchd

orchd is a single Go binary (plus Caddy as the front door) that runs tenant
workloads with scale-to-zero. It has no scheduler and no cluster: one box owns
its workloads, and a request for a sleeping workload starts it.

Read `internal/manager/` for the control plane, `internal/runtime/` for the
drivers, `internal/api/api.go` for the HTTP surface, `deploy/README.md` for the
box. This skill is the mental model that makes those readable.

## Object model

```
project            a tenant unit; owns workloads, env, region              (store.Project)
└─ workload        one runnable app: a db, an api, a dev server           (store.Workload)
   ├─ routes       hostnames the gateway maps to it                       (store.Route)
   └─ state        provisioning | running | suspended | stopped | failed
template           a folder with orchd.json declaring the workloads       (template.Manifest)
image              a frozen, versioned build of a template (v1, v2, …)    (store.Image)
```

A **template** is one monorepo whose `orchd.json` lists workloads. Each
workload is `kind: tinbase | node | static`, has a `dir` inside the template,
and (for node) an `install` and a `run` argv. At most one workload is
`primary` — it owns the project's bare hostname; the others get
`<ref>-<name>`.

An **image** freezes a template: a tarball of the tree, plus (when docker is
available) one container image per node/static workspace, plus a shared
extraction of the installed dependencies. Images are immutable and versioned;
projects boot from `template@version`.

## Two creation modes

`POST /v1/projects` takes either:

- `template: "<name>"` — boot from the live template folder. The local/process
  driver materializes the tarball into the project's data dir and runs each
  workload as a process. This is the local-development path.
- `image: "<name>@vN"` — boot from the frozen image. Container/microVM
  runtimes run the per-workspace artifact. This is the production path.

Both accept the caller's **delta**, which is how a tenant's own files reach a
generic template:

```jsonc
{
  "name": "<caller's stable id>",     // orchd names the project this; also its routing identity
  "delta":     { "path": "text content" },
  "delta_b64": { "path": "base64" },  // binary assets
  "deleted":   ["template/paths/to/remove"]
}
```

The caller decides what a delta is. A useful convention: compare the tenant's
files against the *compiled template* and send only what differs. Two rules
learned the hard way:

- **Never let a delta delete the workload manifest** (`orchd.json`,
  `Dockerfile.orchd.*`). They are template-owned; a caller whose file list
  predates them will otherwise "delete" them and the next boot loses every
  run-wrapper env var.
- **Strip dotenv files.** orchd injects env natively; a stray `.env` inside a
  workspace overrides the injected values (dotenv wins over process env) and
  the failure looks like "my service points at the wrong URL".

## Lifecycle

```
Create    clone/materialize the workspace, inject env, boot, wait until it serves
Suspend   scale to zero — free compute, keep data at rest (the idle reaper does this)
Start     wake: bring it back at the same address
Stop      halt without the expectation of a fast resume
Delete    release containers/VMs, volumes, mounts, routes and the data dir
```

The gateway drives waking: a request for a suspended workload calls
`EnsureRunning`, which starts it, waits for readiness and then proxies. Idle
timeout is `ORCHD_IDLE_TIMEOUT`; `keep_warm` exempts a workload.

**Idleness is refreshed per gateway request.** A client that reconnects in a
loop (a dev-server websocket, a background browser tab) keeps a workload awake
indefinitely — see ROADMAP.

## Writing files into a running workload

`PUT /v1/workloads/{id}/fs/file` (and `/fs/batch`) write into the workload.
Two paths, chosen by the runtime that owns the workload:

- **Host-workspace runtimes (docker/local)**: the file lands in the host
  workspace, which is bind-mounted into the container. A host-side write
  generates no inotify event inside a sandboxed container, so the manager
  re-writes it *through* the container to nudge watchers.
- **microVM runtime (firecracker)**: the guest owns its filesystem. The write
  must happen inside a live VM, so a write to a suspended workload **resumes
  it first**, then applies the write via the in-guest agent. Never mutate a
  snapshotted VM's disk from the host: a resumed guest kernel would hold stale
  page and dentry caches.

That wake-on-write rule is deliberate. It also means rebuild work (dependency
installs, migrations, incremental bundling) happens when the change arrives,
so a resumed workload is already current.

## Runtimes

| driver | how it isolates | wake | selection |
| --- | --- | --- | --- |
| `local` | plain OS processes | relaunch | `ORCHD_DRIVER` default |
| `docker` | containers, optionally gVisor (`ORCHD_DOCKER_RUNTIME=runsc`) | container start | `ORCHD_DRIVER=docker` |
| `mock` | nothing (in-memory) | instant | `ORCHD_DRIVER=mock` |
| `firecracker` | KVM microVMs, dm-thin rootfs | memory-snapshot restore | `ORCHD_FC_WORKLOADS` allowlist, layered over the default driver by `runtime.Mux` |

`Mux` (linux only) wraps the default driver. `ORCHD_FC_WORKLOADS` is a
comma-separated list of `template/workspace` pairs; unset means the mux is
never constructed and behavior is exactly the default driver's.

**The runtime is chosen once, at Create, and every later call routes by
ownership** (`fc.Knows(ref)`, durable via each VM's `meta.json`). Routing a
wake by the allowlist instead creates a second instance for a workload that
had fallen back to docker — two runtimes fighting over one ref, with the
gateway pointing at the dead one. A failed microVM create falls back to the
default driver and stays there for that workload's life.

Per-VM specifics live in `docs/firecracker-snapshots.md` (thin clones, the
uniform guest IP that keeps snapshots portable, compaction, `Spec.Ephemeral`).

## Image build pipeline

`POST /v1/templates/{name}/build` → `manager.BuildImage`:

1. Freeze the template tree as `base.tar.gz` under
   `<data>/images/<template>/<version>/`.
2. Per node/static workspace: generate a Dockerfile and build it. Emission
   order matters for size and inode churn — `setup` steps (toolchains) go
   **before** any app files are copied; then the dependency manifest alone,
   then the install; then the full tree; then `build` steps (cache warming);
   then `CMD` from `run`.
3. Extract the installed dependencies once per distinct lockfile into
   `<template>/shared-deps/<hash>-<workspace>`, with each version's
   `deps/<workspace>` a symlink to it. Every workload of that image
   bind-mounts it read-only, so a project's dependency tree costs ~nothing.
4. If the workspace is on the microVM allowlist, prepare its VM volumes.
5. Prune the builder cache to a ceiling and GC image versions beyond the
   newest few that no container references.

Rules encoded there, each from an incident:

- **Builds are serialized per template.** Killing the build *request* does not
  kill the build (it runs `WithoutCancel`), so a retry used to run
  concurrently with the original and both extracted dependencies into the same
  versioned directory — nesting `node_modules` inside itself, which makes
  module resolution find two copies of every package.
- **Never extract into a versioned directory that already exists.**
- **The builder cache and dependency layers are inode-dense.** Unbounded, they
  exhausted the filesystem's inodes at 56% disk use and every container start
  failed with "no space left on device" (`df -i`, not `df -h`, is the check).

## State and layout

State is a single JSON snapshot in one sqlite row (`ORCHD_STATE_SQLITE`, table
`state`, column `snapshot`, keys `projects/workloads/routes/regions/images/settings`).
Handy for surgery when the API is too slow: stop orchd, back the file up, edit
the JSON, start orchd. Data root (`ORCHD_DATA_ROOT`) holds:

```
projects/<project>/<workload>/      workspace + volumes (host-workspace runtimes)
images/<template>/<version>/        base.tar.gz, deps/ (symlinks), Dockerfiles
images/<template>/shared-deps/      one dependency extraction per lockfile
fc/                                 microVM state (see the firecracker doc)
state/                              the sqlite snapshot
backups/                            scheduled tarballs
```

## Routes and TLS

Routes are hostnames in the route table; the gateway proxies by `Host`.
`POST /v1/workloads/{id}/routes {"host":…}` attaches an extra hostname —
that is the one API behind both the admin Domains panel and any external
domain feature. `DELETE /v1/routes?host=` removes one.

Caddy gates on-demand TLS through `GET /internal/tls-allow`, so only
admin/api hosts and real route-table entries can trigger issuance. Wildcard
certificates are preferable where the DNS provider allows DNS-01: per-hostname
issuance hits Let's Encrypt's 50-certs-per-week-per-domain cap quickly when
every tenant mints several hostnames.

## Debugging order

1. `systemctl is-active orchd caddy`; `journalctl -u orchd -n 100`.
2. `GET /v1/projects/<ref>` — what state does each workload report?
3. Which runtime owns it? `<data>/fc/vms/<workload-id>` existing means microVM;
   otherwise look for the container.
4. Workload logs: `GET /v1/workloads/{id}/logs`.
5. Host pressure: `df -i` (inodes, not just space), `uptime`,
   `dmesg | grep rcu_preempt`. RCU starvation warnings are the real meltdown
   precursor — high load with no RCU lines is IO-wait and usually survivable.

## Related skills

`local-dev` runs the whole stack on a laptop (port mode or a
production-shaped wildcard domain). `production-deploy` is the operating
procedure for the box. Callers integrating with orchd document their own
template and image conventions on their side — orchd stays generic about what
a workload contains.
