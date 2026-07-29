#!/usr/bin/env bash
# Deploy tracked configs, image sources, static assets, and the orchd binary from
# this repo to the box, then reload services. The repo is the source of truth;
# nothing on the server should diverge from what is tracked here.
#
# Usage: deploy/deploy.sh [user@host]     (default: root@167.233.215.115)
#
# Does NOT rebuild workload images (they change rarely) — use bootstrap.sh or
# `docker build` on the box for that.
set -euo pipefail

HOST="${1:-root@167.233.215.115}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

say(){ printf '\n==> %s\n' "$*"; }

say "preflight (go vet/test, admin typecheck/lint)"
"$ROOT/deploy/preflight.sh"

say "build orchd (linux/amd64)"
( cd orchestrator && GOOS=linux GOARCH=amd64 go build -o /tmp/orchd-linux ./cmd/orchd )

say "build admin UI (vite)"
( cd admin && npm run build )

say "build public site + docs (next, static export at the domain root)"
( cd site && npm ci --silent && SITE_BASE_PATH= npm run build )

say "ensure dirs on $HOST"
ssh "$HOST" 'mkdir -p /opt/tinbase-cloud/admin /opt/tinbase-cloud/site \
  /opt/tinbase-cloud/images/tinbase /opt/tinbase-cloud/images/rn-api \
  /opt/tinbase-cloud/images/rn-vite /opt/tinbase-cloud/images/rn-expo'

say "sync static + image sources"
ssh "$HOST" 'rm -rf /opt/tinbase-cloud/site/*'
scp -qr site/out/. "$HOST:/opt/tinbase-cloud/site/"
ssh "$HOST" 'rm -rf /opt/tinbase-cloud/admin/*'
scp -qr admin/dist/. "$HOST:/opt/tinbase-cloud/admin/"
for i in tinbase rn-api rn-vite rn-expo; do
  scp -q "orchestrator/images/$i/Dockerfile" "$HOST:/opt/tinbase-cloud/images/$i/Dockerfile"
done

say "sync system configs"
scp -q deploy/Caddyfile     "$HOST:/etc/caddy/Caddyfile"
scp -q deploy/orchd.service "$HOST:/etc/systemd/system/orchd.service"
scp -q deploy/caddy.service "$HOST:/etc/systemd/system/caddy.service"

say "deploy orchd binary (stop -> copy -> start; tenant containers keep running)"
ssh "$HOST" 'systemctl stop orchd'
scp -q /tmp/orchd-linux "$HOST:/opt/tinbase-cloud/orchd"
ssh "$HOST" 'systemctl daemon-reload && systemctl start orchd'

say "reload caddy (graceful, in-place)"
ssh "$HOST" 'caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile'

say "status"
ssh "$HOST" 'echo "orchd=$(systemctl is-active orchd) caddy=$(systemctl is-active caddy)"; curl -s -H "Host: cloud.rapidnative.com" http://127.0.0.1/api/healthz; echo'
