# deploy

Everything needed to run orchd on a box, tracked in the repo. The repo is
the source of truth; nothing on the server should diverge from what is here.

## What's on the box

| Path on box | Tracked here | What |
| --- | --- | --- |
| `/etc/caddy/Caddyfile` | `Caddyfile` | front door: TLS, routing (see below) |
| `/etc/systemd/system/orchd.service` | `orchd.service` | orchestrator service unit |
| `/etc/systemd/system/caddy.service` | `caddy.service` | Caddy service unit |
| `/opt/orchd/images/*/Dockerfile` | `../orchestrator/images/*` | workload images |
| `/opt/orchd/admin/` | `../admin` (Vite app; `dist/` deployed) | admin UI SPA |
| `/opt/orchd/site/` | `../site` (Next.js; `out/` deployed) | public site + docs |
| `/opt/orchd/orchd` | built from `../orchestrator` | orchd binary (artifact) |
| `/opt/orchd/secrets/admin.key` | **never tracked** | control-plane API key |
| `/opt/orchd/data/` | **never tracked** | per-project volumes + state |

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
box: `ssh root@HOST 'docker build -t <tag> /opt/orchd/images/<name>'`.

## Backups (S3/R2) and the on-box S3 mock

Backups go to a `Store` chosen at runtime from the admin **Settings** page (or
`PUT /v1/settings/backup`), persisted in the orchestrator state: `local` (on the
box) or `s3` (any S3-compatible endpoint — S3, R2, Backblaze, MinIO). The S3
adaptor signs requests with hand-rolled SigV4 (no SDK).

For testing the S3 path without external credentials, a **MinIO** container runs
on the box as an S3-compatible mock:

```bash
docker run -d --name minio -p 127.0.0.1:9000:9000 -p 127.0.0.1:9001:9001 \
  -e MINIO_ROOT_USER=minioadmin -e MINIO_ROOT_PASSWORD=minioadmin123 \
  -v /opt/orchd/minio-data:/data minio/minio server /data --console-address ":9001"
docker run --rm --network host --entrypoint sh minio/mc -c \
  'mc alias set loc http://127.0.0.1:9000 minioadmin minioadmin123 && mc mb -p loc/tbc-backups'
```

Then point the backup target at `http://127.0.0.1:9000`, bucket `tbc-backups`,
region `us-east-1`. Swap in real R2 credentials for genuine off-box durability.

## State store (file or Postgres)

Control-plane state (projects, workloads, routes, regions, keys, settings) lives
behind a `store.Store` interface. By default it is a JSON file
(`/opt/orchd/data/state/projects.json`). Set `ORCHD_STATE_DSN` to store it
in Postgres instead (`postgres://user:pass@host/db?sslmode=disable`) — the seam for
a distributed control plane. An on-box Postgres for testing:

```bash
docker run -d --name pg -p 127.0.0.1:5432:5432 \
  -e POSTGRES_PASSWORD=pgpass -e POSTGRES_DB=orchd postgres:17-alpine
# then set ORCHD_STATE_DSN=postgres://postgres:pgpass@127.0.0.1:5432/orchd?sslmode=disable
```

## Routing (Caddyfile)

- `admin.tinbase.dev` → admin UI (+ same-origin `/api` proxy to the control API)
- `api.tinbase.dev` → control-plane API `:8080` (API-key protected)
- `*.tinbase.dev` → gateway `:8081` (host-based workload routing)
- `cloud.rapidnative.com` → same, behind Cloudflare (self-signed origin cert);
  also exposes `/w/<key>` subroutes as a fallback
- tinbase.dev subdomains get real Let's Encrypt certs via on-demand TLS, gated by
  orchd's `/internal/tls-allow` so certs are only minted for real hosts.
