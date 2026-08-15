FROM node:22-slim
# ca-certificates for the binary download; gosu to drop privileges; the rest are
# shared libraries the embedded native Postgres 17 binaries (theseus-rs) link
# against — without them initdb fails (e.g. libgssapi_krb5.so.2 not found).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates gosu \
      libgssapi-krb5-2 libssl3 libldap-2.5-0 libsasl2-2 \
      libreadline8 libedit2 zlib1g libxml2 liblz4-1 libzstd1 \
 && rm -rf /var/lib/apt/lists/*

RUN npm install -g tinbase@0.13.1

# Warm the native Postgres 17 binaries into the image as the unprivileged `node`
# user (Postgres initdb refuses to run as root), so the cache lands in
# /home/node/.cache and first boot pays neither the download nor a root failure.
USER node
RUN tinbase migrate --dir /tmp/warm --data-dir /tmp/warm/db || true; rm -rf /tmp/warm
USER root

# Entrypoint starts as root only to take ownership of the mounted volume, then
# drops to `node` to run the requested command (initdb must not be root). It
# honors CMD/args, so `docker run <img> tinbase keys ...` works too — critical,
# because the control plane mints keys that way.
COPY <<'SH' /entrypoint.sh
#!/bin/sh
set -e
mkdir -p /data
chown node:node /data 2>/dev/null || true
exec gosu node "$@"
SH
RUN chmod +x /entrypoint.sh

EXPOSE 54321
ENTRYPOINT ["/entrypoint.sh"]
# Bind 0.0.0.0 *inside the container* so Docker's port forwarding (host loopback
# -> container eth0) reaches tinbase. The host only publishes to 127.0.0.1, so
# the orchestrator gateway remains the sole front door.
CMD ["tinbase", "start", "--host", "0.0.0.0", "--port", "54321", "--dir", "/data", "--data-dir", "/data/db"]
