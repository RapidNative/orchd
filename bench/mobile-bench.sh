#!/usr/bin/env bash
# Mobile workload boot/wake benchmark.
#
# Times the lifecycle a user actually feels: provision -> server ready ->
# first web bundle -> first iOS manifest+bundle -> wake after suspend ->
# cold resume after a container recreate. Run it before and after image
# changes; the numbers are the whole point.
#
#   bench/mobile-bench.sh API_URL KEY IMAGE            # create, bench, delete
#   bench/mobile-bench.sh API_URL KEY IMAGE --keep     # leave the project up
#   bench/mobile-bench.sh API_URL KEY --project REF    # bench an existing project
#
# Example:
#   bench/mobile-bench.sh https://api.rnproject.dev "$KEY" fullstack-supabase@v4
set -euo pipefail

API="${1:?API_URL}"; KEY="${2:?KEY}"; shift 2
IMAGE=""; PROJECT=""; KEEP=0
while [ $# -gt 0 ]; do
  case "$1" in
    --project) PROJECT="$2"; shift 2;;
    --keep) KEEP=1; shift;;
    *) IMAGE="$1"; shift;;
  esac
done
[ -n "$IMAGE$PROJECT" ] || { echo "need IMAGE or --project REF"; exit 2; }

auth=(-H "Authorization: Bearer $KEY")
declare -a ROWS
now_ms() { python3 -c 'import time; print(int(time.time()*1000))'; }
row() { local line; line=$(printf '%-28s %8.1fs' "$1" "$(python3 -c "print(($3-$2)/1000)")"); ROWS+=("$line"); echo "  $line"; }

api() { curl -s "${auth[@]}" "$@"; }
jq_py() { python3 -c "import json,sys; $1"; }

# ---- create or adopt ----
CREATED=0
if [ -z "$PROJECT" ]; then
  T0=$(now_ms)
  PROJECT=$(api -X POST "$API/v1/projects" -H 'content-type: application/json' \
    -d "{\"name\":\"bench\",\"image\":\"$IMAGE\"}" | jq_py 'print(json.load(sys.stdin)["id"])')
  CREATED=1
  echo "project: $PROJECT (from $IMAGE)"
else
  T0=$(now_ms)
  echo "project: $PROJECT (existing)"
fi

proj() { api "$API/v1/projects/$PROJECT"; }
MOBILE=$(proj | jq_py 'p=json.load(sys.stdin); print([w["id"] for w in p["workloads"] if (w.get("workspace") or w["name"] or "mobile")=="mobile" or w.get("workspace")=="mobile"][0])')
HOST=$(proj | jq_py "p=json.load(sys.stdin); print([w['routes'][0] for w in p['workloads'] if w['id']=='$MOBILE'][0])")
BASE="https://$HOST"
echo "mobile:  $MOBILE @ $BASE"

wait_state() { # wait_state running|suspended
  until api "$API/v1/workloads/$MOBILE" | jq_py "import sys as s; s.exit(0 if json.load(sys.stdin)['state']=='$1' else 1)" 2>/dev/null; do sleep 2; done
}
wait_200() { # wait_200 URL [extra curl args...]
  local url="$1"; shift
  until curl -sk -o /dev/null -w '%{http_code}' --max-time 20 "$@" "$url" 2>/dev/null | grep -q 200; do sleep 2; done
}

# ---- 1. provision ----
wait_state running; T1=$(now_ms); row "provision -> running" "$T0" "$T1"

# ---- 2. server ready (first 200 on the route) ----
wait_200 "$BASE/"; T2=$(now_ms); row "running -> serving" "$T1" "$T2"

# ---- 3. web bundle ----
WEB_URL="$BASE/node_modules/expo-router/entry.bundle?platform=web&dev=true&hot=false&transform.routerRoot=app"
T3a=$(now_ms)
if ! curl -sk -o /dev/null -w '%{http_code}' --max-time 600 "$WEB_URL" | grep -q 200; then
  # jetplane serves any *.bundle path; also probe its canonical web bundle
  curl -sk -o /dev/null --max-time 600 "$BASE/jetplane-web.bundle" || true
fi
T3=$(now_ms); row "web bundle" "$T3a" "$T3"

# ---- 4. iOS manifest + bundle ----
T4a=$(now_ms)
IOS_PATH=$(curl -sk --max-time 120 "$BASE/" -H 'expo-platform: ios' -H 'accept: application/expo+json,application/json' \
  | jq_py 'm=json.load(sys.stdin); u=m["launchAsset"]["url"]; print(u.split("/",3)[3] if "://" in u else u.lstrip("/"))' 2>/dev/null || echo "")
if [ -n "$IOS_PATH" ]; then
  curl -sk -o /dev/null --max-time 600 "$BASE/$IOS_PATH"
fi
T4=$(now_ms); row "ios manifest+bundle" "$T4a" "$T4"

# ---- 5. wake after suspend ----
api -X POST "$API/v1/workloads/$MOBILE/stop" -o /dev/null; wait_state suspended
T5a=$(now_ms); wait_200 "$BASE/"; T5=$(now_ms); row "wake (suspend -> 200)" "$T5a" "$T5"

# ---- 6. cold resume (container recreate) ----
T6a=$(now_ms)
api -X POST "$API/v1/workloads/$MOBILE/restart" -o /dev/null --max-time 600
wait_200 "$BASE/"; T6=$(now_ms); row "cold resume (recreate)" "$T6a" "$T6"

# rebundle-after-recreate cost (web again)
T7a=$(now_ms); curl -sk -o /dev/null --max-time 600 "$WEB_URL" || true
T7=$(now_ms); row "web bundle after resume" "$T7a" "$T7"

# ---- teardown ----
if [ "$CREATED" = 1 ] && [ "$KEEP" = 0 ]; then
  api -X DELETE "$API/v1/projects/$PROJECT" -o /dev/null; echo "deleted $PROJECT"
fi

echo; echo "== results ($PROJECT, ${IMAGE:-existing}) =="
printf '%s\n' "${ROWS[@]}"
