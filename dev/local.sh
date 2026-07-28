#!/usr/bin/env bash
# Run the whole stack locally on ports — no domain, no Caddy, no TLS.
#
#   dev/local.sh [driver]      driver: local (default) | docker | mock
#
# Layout (override any with env vars of the same name):
#   ORCHD API    :8090   control plane
#   Gateway      :8091   host/subroute routing (optional in port mode)
#   Admin (Vite) :8092
#   Workloads    :8100+  each workload gets its OWN stable port (8100, 8101, …)
#                        via ORCHD_PORT_BASE — reach it directly at
#                        http://localhost:<port>, no gateway/subdomain needed.
#
# So a project's tinbase backend lands on 8100 and its RapidNative apps on
# 8101/8102/8103. The gateway subroute (http://localhost:8091/w/<key>) and
# subdomain (http://<key>.localhost:8091) still work as an alternative.
#
# Drivers:
# Domain mode (production-shaped routing, no port mapping):
#   DOMAIN=rnproject.test dev/local.sh local
# Registers nothing itself — run `dev/domain.sh setup` once and
# `dev/domain.sh add rnproject.test` per domain first. In this mode workloads get
# no host port; they are reached only at https://<key>.<DOMAIN> through Caddy ->
# gateway -> route table, exactly as in prod.
#
# Drivers:
#   local   (default) full no-Docker stack: tinbase + the RapidNative dev apps
#           (web/api/app) each run as a real local process from a scaffolded
#           source dir (Vite/Hono/Expo), npm-installed on first boot.
#           Needs node/npm; ORCHD_TINBASE_BIN for the tinbase workload.
#   docker  real containers (runc, no gVisor needed) — needs Docker + local images
#   mock    in-memory: exercises the whole control plane + admin with no Docker
#           (workloads don't serve real traffic)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DRIVER="${1:-local}"
DATA="$ROOT/.localdev"
KEYFILE="$DATA/dev.key"
DEV_KEY="local-dev-key"
API_PORT="${API_PORT:-8090}"
GW_PORT="${GW_PORT:-8091}"
ADMIN_PORT="${ADMIN_PORT:-8092}"
PORT_BASE="${PORT_BASE:-8100}"
DOMAIN="${DOMAIN:-}"

# Scale-to-zero needs every request to arrive through the gateway, which is what
# refreshes idleness and wakes a reaped workload. Port mode talks to workloads
# directly, so it must stay disabled there; domain mode routes by Host, so it
# gets the real prod behaviour. Override with IDLE_TIMEOUT (0 disables).
IDLE_TIMEOUT_SET="${IDLE_TIMEOUT+set}"
IDLE_TIMEOUT="${IDLE_TIMEOUT:-0}"

# Domain mode: drop port-per-workload addressing and route by Host through the
# local Caddy (dev/domain.sh), the same way prod does. Keep the ports for admin,
# API and gateway — Caddy proxies to them.
DOMAIN_ENV=()
if [ -n "$DOMAIN" ]; then
  PORT_BASE=0
  [ -n "$IDLE_TIMEOUT_SET" ] || IDLE_TIMEOUT=5m
  # No API key in domain mode: the control plane is open and the admin panel
  # connects without a gate (local dev only — it binds to loopback).
  KEYFILE=""
  DEV_KEY="(none — open control plane)"
  DOMAIN_ENV=(
    ORCHD_BASE_DOMAIN="$DOMAIN"
    ORCHD_PUBLIC_SCHEME=https
    ORCHD_PUBLIC_URL="https://$DOMAIN"
  )
  if ! grep -qxF "$DOMAIN" "$ROOT/.localdev/domains/domains.txt" 2>/dev/null; then
    echo "domain '$DOMAIN' is not registered — run: dev/domain.sh add $DOMAIN" >&2
    exit 1
  fi
fi

case "$DRIVER" in
  docker) RUNTIME_ENV=(ORCHD_DRIVER=docker ORCHD_DOCKER_RUNTIME=runc) ;;
  mock)   RUNTIME_ENV=(ORCHD_DRIVER=mock) ;;
  local)  RUNTIME_ENV=(ORCHD_DRIVER=local) ;;
  *) echo "unknown driver '$DRIVER' (use: docker | mock | local)"; exit 1 ;;
esac

say(){ printf '\n\033[1;32m==> %s\033[0m\n' "$*"; }

mkdir -p "$DATA/state" "$DATA/backups"
[ -z "$KEYFILE" ] || [ -f "$KEYFILE" ] || printf '%s' "$DEV_KEY" > "$KEYFILE"

say "build orchd"
( cd orchestrator && go build -o "$DATA/orchd" ./cmd/orchd )

if [ -n "$DOMAIN" ]; then
  say "start orchd ($DRIVER driver): api :$API_PORT · gateway :$GW_PORT · hosts *.$DOMAIN"
else
  say "start orchd ($DRIVER driver): api :$API_PORT · gateway :$GW_PORT · workloads :$PORT_BASE+"
fi
env ORCHD_LOCAL=1 \
    "${RUNTIME_ENV[@]}" \
    ${DOMAIN_ENV[@]+"${DOMAIN_ENV[@]}"} \
    ORCHD_API_ADDR="127.0.0.1:$API_PORT" \
    ORCHD_GATEWAY_ADDR="127.0.0.1:$GW_PORT" \
    ORCHD_PORT_BASE="$PORT_BASE" \
    ORCHD_TEMPLATES_DIR="$ROOT/template-examples" \
    ORCHD_DATA_ROOT="$DATA" \
    ORCHD_STATE_SQLITE="$DATA/state/orchd.db" \
    ORCHD_BACKUP_DIR="$DATA/backups" \
    ORCHD_API_KEY_FILE="$KEYFILE" \
    ORCHD_IDLE_TIMEOUT="$IDLE_TIMEOUT" \
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

if [ -n "$DOMAIN" ]; then
cat <<EOF

  ┌──────────────────────────────────────────────────────────────┐
  │  ORCHD — local (wildcard domain, Caddy, prod-shaped routing)   │
  ├──────────────────────────────────────────────────────────────┤
  │  Admin      https://admin.$DOMAIN
  │  API        https://api.$DOMAIN
  │  Workloads  https://<key>.$DOMAIN
  │  API key    none — open control plane, admin connects with no gate
  │  Idle       $IDLE_TIMEOUT — workloads scale to zero, gateway wakes them
  │  Driver     $DRIVER
  └──────────────────────────────────────────────────────────────┘

  No port mapping: every workload is reached by Host through the gateway's
  route table, the same path prod takes. Ctrl-C to stop the stack (Caddy and
  dnsmasq keep running — 'dev/domain.sh down' stops those).
EOF
else
cat <<EOF

  ┌──────────────────────────────────────────────────────────────┐
  │  ORCHD — local (ports, no domain, no Caddy)                    │
  ├──────────────────────────────────────────────────────────────┤
  │  Admin      http://localhost:$ADMIN_PORT
  │  API        http://localhost:$API_PORT
  │  Gateway    http://localhost:$GW_PORT
  │  Workloads  http://localhost:$PORT_BASE, :$((PORT_BASE+1)), :$((PORT_BASE+2)), …
  │  API key    $DEV_KEY   (paste into the admin gate)
  │  Driver     $DRIVER
  └──────────────────────────────────────────────────────────────┘

  Each workload's own port shows as its "endpoint" in the panel/API.
  Ctrl-C to stop everything.
EOF
fi

say "start admin (Vite) on :$ADMIN_PORT"
cd admin
API_PROXY_TARGET="http://127.0.0.1:$API_PORT" \
  npx vite --host 127.0.0.1 --port "$ADMIN_PORT" --strictPort
