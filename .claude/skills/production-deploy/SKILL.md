---
name: production-deploy
description: Provision or update the orchd production box — deploy/deploy.sh, bootstrap.sh, preflight, Caddy/systemd config, workload images, DNS and TLS. Use when asked to deploy, ship, push to prod, update or provision the server, rebuild workload images, or debug the live box.
---

# Production environment

Full reference: `deploy/README.md`. This skill is the operating procedure.

**Deploying is outward-facing and briefly restarts the live control plane.
Confirm with the user before running `deploy/deploy.sh`, every time, unless they
have just explicitly told you to deploy.**

## Shape of the box

One Ubuntu host. Caddy on 80/443 is the front door; orchd runs the control API
(`:8080`) and gateway (`:8081`); tenant workloads are Docker containers isolated
with gVisor (`runsc`). Everything on the box that isn't data or secrets is
tracked in `deploy/` — the repo is the source of truth and the server must not
diverge from it.

| Host | Goes to |
| --- | --- |
| `admin.<base>` | admin SPA + same-origin `/api` proxy |
| `api.<base>` | control API (API-key protected) |
| `*.<base>` | gateway, host-based workload routing |

Base domains: `rnproject.dev` (primary) and `tinbase.dev` (alt), plus
`cloud.rapidnative.com` behind Cloudflare. Certs are Let's Encrypt on-demand,
gated by orchd's `/internal/tls-allow` so only admin/api and real route-table
hosts can trigger issuance.

Config lives in `deploy/orchd.service` (all `ORCHD_*` env) and
`deploy/Caddyfile`. Change behaviour there and deploy — never edit files on the
box directly.

## Deploy an update

```bash
deploy/deploy.sh                 # defaults to root@167.233.215.115
deploy/deploy.sh root@HOST
```

It runs `deploy/preflight.sh` first (go vet, go test, admin typecheck, lint,
Playwright E2E) and aborts on failure. Then: builds orchd for linux/amd64,
builds the admin bundle, syncs static assets, image Dockerfiles, the Caddyfile
and unit files, restarts orchd, and reloads Caddy in place. Tenant containers
keep running across the restart and re-attach.

Never `SKIP_PREFLIGHT=1` or `SKIP_E2E=1` on your own initiative — only when the
user asks, and say plainly in your report that checks were skipped.

`deploy.sh` does **not** rebuild workload images. If a Dockerfile changed:

```bash
ssh root@HOST 'docker build -t <tag> /opt/tinbase-cloud/images/<name>'
```

Tags in use: `tinbase:0.10.0`, `rn-api:dev`, `rn-vite:dev`, `rn-expo:dev`.

## Provision a fresh box

```bash
deploy/deploy.sh root@HOST || true          # sync first; orchd may not start yet
ssh root@HOST 'bash -s' < deploy/bootstrap.sh
```

`bootstrap.sh` is idempotent: Docker, gVisor, the Caddy binary, directories, a
generated `/opt/tinbase-cloud/secrets/admin.key` (never tracked), the workload
images, then enables the services. Afterwards point a wildcard `A` record at the
box; the first HTTPS request per host mints its cert.

## Verify after deploying

```bash
ssh root@HOST 'systemctl is-active orchd caddy'
curl -s https://api.rnproject.dev/healthz
ssh root@HOST 'journalctl -u orchd -n 50 --no-pager'
```

Confirm an existing project still resolves through the gateway before calling
the deploy good. Report what actually happened, including anything that failed.

## Rules

- Secrets (`/opt/tinbase-cloud/secrets/`) and data (`/opt/tinbase-cloud/data/`)
  never enter the repo and never get printed into the transcript.
- Don't take destructive actions on the box (deleting data dirs, wiping state,
  `docker rm` of tenant containers) without explicit per-action confirmation.
- Local domain mode (`dev/README.md`, the `local-dev` skill) reproduces this
  routing on a laptop — reproduce a routing bug there before changing prod.
