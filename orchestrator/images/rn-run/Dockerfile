# Generic runtime for React Native dev-server workspaces (preset image: the
# manifest's `image:` runs verbatim — no per-template bake, no node_modules).
#
# rnrun is a drop-in `expo start` replacement: no Metro, packages arrive
# pre-bundled over HTTP from its package server (which operators typically
# front with an on-box cache via ORCHD_HOST_ALIASES). The project's files are
# volume-mounted at /app; rnrun reads app.json/package.json from there and
# inlines EXPO_PUBLIC_* from the environment orchd injects.
#
# Build (registry mirror honored via ORCHD_BUILD_ENV conventions):
#   docker build -t rn-run:dev orchestrator/images/rn-run
FROM node:22-slim

ARG npm_config_registry
# Pinned: a dev-server version change alters bundling behavior for every
# project at once; bumps are deliberate image rebuilds, never a boot-time pull.
RUN npm install -g rnrun@0.4.2 && npm cache clean --force

# HOME holds rnrun's pkg-cache. It lives in the ephemeral writable layer (gVisor
# resets it on stop/start) — fine, because a bundle-cache hit doesn't need it and
# the background rebuild re-fetches from the on-box esm cache. The caches that
# MUST survive a wake are redirected to /data below.
ENV HOME=/cache
RUN mkdir -p /cache

# gVisor wipes the container writable layer on docker stop/start, so rnrun's
# default caches under HOME=/cache do NOT survive a scale-to-zero wake — every
# wake would rebuild (measured: 33s vs 0.6s). Point the two expensive caches at
# /data instead, the per-mobile-workload bind mount orchd provides, which is
# real host storage and persists. pkg-cache stays in the ephemeral layer: a
# bundle-cache hit serves instantly and only the background rebuild re-fetches
# packages (from the on-box esm cache, fast). Verified: wake 0.6s, cache survives.
ENV RNRUN_BUNDLE_CACHE_DIR=/data/.rnrun/bundle-cache
ENV RNRUN_TOOLS_DIR=/data/.rnrun/tools
# 0.3.2+: the package cache is relocatable too. Co-locate it on /data so a
# wake stops re-fetching packages into the ephemeral layer entirely.
ENV RNRUN_PKG_CACHE_DIR=/data/.rnrun/pkg

# Hosted preview over a public URL: serve an anonymous manifest so Expo Go on
# physical iOS skips the SDK 57 sign-in gate (trades away Fast Refresh).
ENV RNRUN_EXPO_GO_ANONYMOUS=1

WORKDIR /app
ENV PORT=8080
EXPOSE 8080
# --prewarm ios: warms a genuinely-cold project during idle so its first-ever
# scan isn't the one paying the ~38s build. Safe on a warm cache as of 0.3.2:
# the Fast-Refresh graph rebuild that used to fire on every cache hit is now
# lazy (built on first edit), so a hit is a true no-op serve (~270ms) rather
# than a background full build. Bundle cache lives on /data (above) so it
# survives the gVisor writable-layer wipe on stop/start.
CMD ["rnrun", "start", "--port", "8080", "--prewarm", "ios"]
