#!/usr/bin/env bash
# Run the whole stack locally on ports — no domain, no Caddy, no TLS.
#
#   dev/local.sh [driver]      driver: docker (default) | mock | local
#
# It starts orchd (control API + gateway) and the admin (Vite proxying /api to
# the local control API), all on localhost ports. Workloads are reached by port
# through the gateway:
#   http://localhost:8081/w/<key>        subroute (pure ports, no DNS)
#   http://<key>.localhost:8081          subdomain (browsers resolve to loopback)
#
# Drivers:
#   docker  real containers (runc, no gVisor needed) — needs Docker + local images
#   mock    in-memory: exercises the whole control plane + admin with no Docker
#           (workloads don't serve real traffic)
#   local   runs the tinbase binary directly (needs ORCHD_TINBASE_BIN)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DRIVER="${1:-docker}"
DATA="$ROOT/.localdev"
KEYFILE="$DATA/dev.key"
DEV_KEY="local-dev-key"
API_PORT=8080
GW_PORT=8081
ADMIN_PORT=5173

case "$DRIVER" in
  docker) RUNTIME_ENV=(ORCHD_DRIVER=docker ORCHD_DOCKER_RUNTIME=runc) ;;
  mock)   RUNTIME_ENV=(ORCHD_DRIVER=mock) ;;
  local)  RUNTIME_ENV=(ORCHD_DRIVER=local) ;;
  *) echo "unknown driver '$DRIVER' (use: docker | mock | local)"; exit 1 ;;
esac

say(){ printf '\n\033[1;32m==> %s\033[0m\n' "$*"; }

mkdir -p "$DATA/state" "$DATA/backups"
[ -f "$KEYFILE" ] || printf '%s' "$DEV_KEY" > "$KEYFILE"

say "build orchd"
( cd orchestrator && go build -o "$DATA/orchd" ./cmd/orchd )

say "start orchd ($DRIVER driver) on :$API_PORT (api) + :$GW_PORT (gateway)"
env ORCHD_LOCAL=1 \
    "${RUNTIME_ENV[@]}" \
    ORCHD_API_ADDR="127.0.0.1:$API_PORT" \
    ORCHD_GATEWAY_ADDR="127.0.0.1:$GW_PORT" \
    ORCHD_DATA_ROOT="$DATA" \
    ORCHD_STATE_SQLITE="$DATA/state/orchd.db" \
    ORCHD_BACKUP_DIR="$DATA/backups" \
    ORCHD_API_KEY_FILE="$KEYFILE" \
    ORCHD_IDLE_TIMEOUT=120s \
    "$DATA/orchd" &
ORCHD_PID=$!
trap 'kill $ORCHD_PID 2>/dev/null || true' EXIT INT TERM

# Wait for the control API to answer.
for _ in $(seq 1 50); do
  curl -sf "http://127.0.0.1:$API_PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.2
done

say "install admin deps (first run only)"
[ -d admin/node_modules ] || ( cd admin && npm install )

cat <<EOF

  ┌─────────────────────────────────────────────────────────────┐
  │  ORCHD — local (ports, no domain, no Caddy)                  │
  ├─────────────────────────────────────────────────────────────┤
  │  Admin    http://localhost:$ADMIN_PORT                             │
  │  API      http://localhost:$API_PORT                             │
  │  Gateway  http://localhost:$GW_PORT   (workloads: /w/<key>)      │
  │  API key  $DEV_KEY   (paste into the admin gate)      │
  │  Driver   $DRIVER
  └─────────────────────────────────────────────────────────────┘

  Ctrl-C to stop everything.
EOF

say "start admin (Vite) on :$ADMIN_PORT"
cd admin
API_PROXY_TARGET="http://127.0.0.1:$API_PORT" \
  npx vite --host 127.0.0.1 --port "$ADMIN_PORT" --strictPort
