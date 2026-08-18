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
RUN npm install -g rnrun@0.3.1 && npm cache clean --force

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

WORKDIR /app
ENV PORT=8080
EXPOSE 8080
# No --prewarm: 0.3.x persists the assembled bundle to disk (~/.rnrun/bundle-cache,
# keyed by platform + sources + env + flags.
# (the cache is redirected to /data above so it survives the wake). On a wake the
# serve path finds that cache and returns in ~0.5s. --prewarm would
# instead force an unconditional rebuild on every start, and a scan arriving mid-
# rebuild coalesces onto it (~32s) rather than reading the cache — measured, it
# defeats the cache on exactly the wake path we care about. A brand-new project
# still builds once on its first request; warm it at provision time with
# `rnrun bundle --platform ios` rather than paying it per wake.
CMD ["rnrun", "start", "--port", "8080"]
