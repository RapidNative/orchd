#!/usr/bin/env bash
# Build prod images from a template's orchd.json — the prod counterpart of the
# local process driver. For each node/static workload it generates a Dockerfile
# (mirroring the manifest: deps baked, source COPYd, the workspace's run command
# as CMD) and builds it, tagged with the workload's `image`. Push the tags to a
# registry your prod/staging ORCHD can pull (see the Images page / docs).
#
#   deploy/build-template.sh <template-dir> [--push]
#
# Needs Docker + jq. tinbase workloads are skipped (they run the tinbase image).
# The user's files live on a mounted volume in prod (not baked), so a restore /
# backup is just the delta — same model as local.
set -euo pipefail

TMPL="${1:?usage: build-template.sh <template-dir> [--push]}"
PUSH="${2:-}"
MANIFEST="$TMPL/orchd.json"
[ -f "$MANIFEST" ] || { echo "no orchd.json in $TMPL"; exit 1; }

say(){ printf '\n==> %s\n' "$*"; }

count=$(jq '.workloads | length' "$MANIFEST")
for i in $(seq 0 $((count - 1))); do
  kind=$(jq -r ".workloads[$i].kind" "$MANIFEST")
  image=$(jq -r ".workloads[$i].image // empty" "$MANIFEST")
  dir=$(jq -r ".workloads[$i].dir // \".\"" "$MANIFEST")
  [ "$kind" = "tinbase" ] && continue
  [ -z "$image" ] && { echo "skip $(jq -r ".workloads[$i].name" "$MANIFEST"): no image"; continue; }

  ctx="$TMPL/$dir"
  df="$ctx/Dockerfile.orchd"
  if [ "$kind" = "static" ]; then
    cat > "$df" <<'DF'
FROM nginx:alpine
COPY . /usr/share/nginx/html
EXPOSE 8080
CMD ["nginx","-g","daemon off;","-c","/etc/nginx/nginx.conf"]
DF
    # serve on 8080
    printf 'events{} http{ server{ listen 8080; root /usr/share/nginx/html; location / { try_files $uri /index.html; } } }\n' > "$ctx/nginx.orchd.conf"
    sed -i.bak 's#/etc/nginx/nginx.conf#/etc/nginx/nginx.orchd.conf#' "$df" && rm -f "$df.bak"
    cp "$ctx/nginx.orchd.conf" "$ctx/nginx.orchd.conf" 2>/dev/null || true
  else
    # node: bake deps, run the manifest command with PORT=8080
    run=$(jq -c ".workloads[$i].run" "$MANIFEST")
    cat > "$df" <<DF
FROM node:20-alpine
WORKDIR /app
COPY package*.json ./
RUN npm install --no-audit --no-fund || true
COPY . .
ENV PORT=8080
EXPOSE 8080
# run command from orchd.json (\$PORT is 8080 in the image)
CMD ${run//\$PORT/8080}
DF
  fi

  say "build $image  (kind=$kind, ctx=$ctx)"
  docker build -f "$df" -t "$image" "$ctx"
  rm -f "$df" "$ctx/nginx.orchd.conf" 2>/dev/null || true
  if [ "$PUSH" = "--push" ]; then
    say "push $image"
    docker push "$image"
  fi
done

say "done — built images from $TMPL"
