# deploy

Everything needed to run tinbase-cloud on a box, tracked in the repo. The repo is
the source of truth; nothing on the server should diverge from what is here.

## What's on the box

| Path on box | Tracked here | What |
| --- | --- | --- |
| `/etc/caddy/Caddyfile` | `Caddyfile` | front door: TLS, routing (see below) |
| `/etc/systemd/system/orchd.service` | `orchd.service` | orchestrator service unit |
| `/etc/systemd/system/caddy.service` | `caddy.service` | Caddy service unit |
| `/opt/tinbase-cloud/images/*/Dockerfile` | `../orchestrator/images/*` | workload images |
| `/opt/tinbase-cloud/admin/` | `../admin` (Vite app; `dist/` deployed) | admin UI SPA |
| `/opt/tinbase-cloud/site/index.html` | `../site/index.html` | overview page |
| `/opt/tinbase-cloud/orchd` | built from `../orchestrator` | orchd binary (artifact) |
| `/opt/tinbase-cloud/secrets/admin.key` | **never tracked** | control-plane API key |
| `/opt/tinbase-cloud/data/` | **never tracked** | per-project volumes + state |

Installed by `bootstrap.sh` (not tracked as files, they're upstream binaries):
Docker, gVisor (`runsc`), and the Caddy static binary at `/usr/bin/caddy`.

## Provision a fresh box (one time)

```bash
# 1. sync files up (orchd may not start yet — that's fine)
deploy/deploy.sh root@HOST || true
# 2. install Docker/gVisor/Caddy, make the key, build images, start services
ssh root@HOST 'bash -s' < deploy/bootstrap.sh
```

Then point DNS at the box (a wildcard `A` record — see the root README) and the
first HTTPS request to each host triggers a Let's Encrypt cert.

## Deploy updates (ongoing)

```bash
deploy/deploy.sh            # defaults to root@167.233.215.115
```

Builds orchd for linux/amd64, syncs configs + static + image sources, restarts
orchd (tenant containers keep running and re-attach), and reloads Caddy in place.
It does **not** rebuild workload images; if a Dockerfile changed, rebuild on the
box: `ssh root@HOST 'docker build -t <tag> /opt/tinbase-cloud/images/<name>'`.

## Routing (Caddyfile)

- `admin.tinbase.dev` → admin UI (+ same-origin `/api` proxy to the control API)
- `api.tinbase.dev` → control-plane API `:8080` (API-key protected)
- `*.tinbase.dev` → gateway `:8081` (host-based workload routing)
- `cloud.rapidnative.com` → same, behind Cloudflare (self-signed origin cert);
  also exposes `/w/<key>` subroutes as a fallback
- tinbase.dev subdomains get real Let's Encrypt certs via on-demand TLS, gated by
  orchd's `/internal/tls-allow` so certs are only minted for real hosts.
