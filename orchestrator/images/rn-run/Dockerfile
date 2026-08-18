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
RUN npm install -g rnrun@0.2.3 && npm cache clean --force

# A writable, stable home: rnrun's package cache (~/.rnrun/pkg-cache) lands in
# the container layer and survives stop/start cycles (scale-to-zero wakes).
ENV HOME=/cache
RUN mkdir -p /cache

WORKDIR /app
ENV PORT=8080
EXPOSE 8080
CMD ["rnrun", "start", "--port", "8080"]
