#!/usr/bin/env bash
# One-time provisioning of a fresh Ubuntu box for tinbase-cloud: Docker + gVisor,
# Caddy, directories, the control-plane API key, workload images, and services.
#
# Run the sync first so configs + image sources exist on the box, then bootstrap:
#   deploy/deploy.sh root@HOST                 # syncs files (orchd start may fail
#                                              # until services exist; that is ok)
#   ssh root@HOST 'bash -s' < deploy/bootstrap.sh
#
# Idempotent: re-running skips anything already present.
set -euo pipefail

# 1. Docker
command -v docker >/dev/null || curl -fsSL https://get.docker.com | sh

# 2. gVisor (runsc) — VM-grade isolation without KVM (works on a plain Cloud VM)
if ! command -v runsc >/dev/null; then
  curl -fsSL https://gvisor.dev/archive.key | gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" \
    > /etc/apt/sources.list.d/gvisor.list
  apt-get update -qq && apt-get install -y -qq runsc
  runsc install            # registers the runsc runtime with Docker
  systemctl restart docker
fi

# 3. Caddy (static binary)
command -v caddy >/dev/null || {
  curl -fsSL -o /usr/bin/caddy "https://caddyserver.com/api/download?os=linux&arch=amd64"
  chmod +x /usr/bin/caddy
}

# 4. dirs + control-plane API key (never leaves the box; not in the repo)
mkdir -p /opt/tinbase-cloud/{admin,site,images,secrets,data}
chmod 700 /opt/tinbase-cloud/secrets
if [ ! -s /opt/tinbase-cloud/secrets/admin.key ]; then
  openssl rand -hex 32 > /opt/tinbase-cloud/secrets/admin.key
  chmod 600 /opt/tinbase-cloud/secrets/admin.key
  echo "generated /opt/tinbase-cloud/secrets/admin.key"
fi

# 5. build workload images (needs images/* synced by deploy.sh first)
docker build -t tinbase:0.10.0 /opt/tinbase-cloud/images/tinbase
docker build -t rn-api:dev     /opt/tinbase-cloud/images/rn-api
docker build -t rn-vite:dev    /opt/tinbase-cloud/images/rn-vite
docker build -t rn-expo:dev    /opt/tinbase-cloud/images/rn-expo

# 6. enable + start services (needs unit files + orchd binary synced first)
systemctl daemon-reload
systemctl enable --now caddy orchd
echo "bootstrap complete: orchd=$(systemctl is-active orchd) caddy=$(systemctl is-active caddy)"
