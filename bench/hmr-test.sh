#!/usr/bin/env bash
# jetplane HMR test: boot a mobile app, then mutate files on a timed script
# while you watch the phone / browser.
#
#   bench/hmr-test.sh API_URL KEY [IMAGE]        # default image: latest fullstack-supabase
#
# Timeline (announced as it runs):
#   t+0     create project -> QR + URLs printed as soon as mobile serves
#   t+40s   ADD a new route file app/(app)/hmr-one.tsx   (known jetplane gap:
#           new files can't enter the prebuilt bundle via HMR — expect NO
#           change until a restart/rebundle)
#   t+~80s  UPDATE the existing home screen               (real HMR: expect the
#           open screen to hot-swap in a few seconds)
#   t+120s  ADD another new route file app/(app)/hmr-two.tsx
#
# The project is KEPT after the run so you can keep testing; the script prints
# the restart + delete commands at the end.
set -euo pipefail

API="${1:?API_URL}"; KEY="${2:?KEY}"; IMAGE="${3:-}"
auth=(-H "Authorization: Bearer $KEY")
api() { curl -s "${auth[@]}" "$@"; }
say() { echo; echo "== [$(date +%H:%M:%S)] $*"; }

if [ -z "$IMAGE" ]; then
  IMAGE=$(api "$API/v1/built-images" | python3 -c "
import json,sys
for im in json.load(sys.stdin):
    if im['template']=='fullstack-supabase': print(im['template']+'@'+im['version']); break")
fi

say "creating project from $IMAGE"
REF=$(api -X POST "$API/v1/projects" -H 'content-type: application/json' \
  -d "{\"name\":\"hmr-test\",\"image\":\"$IMAGE\"}" | python3 -c "import json,sys; print(json.load(sys.stdin)['id'])")
MOBILE=$(api "$API/v1/projects/$REF" | python3 -c "
import json,sys; print([w['id'] for w in json.load(sys.stdin)['workloads'] if w.get('workspace')=='mobile'][0])")

until api "$API/v1/workloads/$MOBILE" | python3 -c "import json,sys; sys.exit(0 if json.load(sys.stdin)['state']=='running' else 1)" 2>/dev/null; do sleep 3; done
HOST="$REF.rnproject.dev"
until curl -sk -o /dev/null -w '%{http_code}' --max-time 15 "https://$HOST/" 2>/dev/null | grep -q 200; do sleep 3; done

say "mobile is serving"
echo "  web:      https://$HOST/"
echo "  expo go:  exps://$HOST"
echo "  project:  $REF   mobile workload: $MOBILE"
# QR for Expo Go (best effort: qrencode, then python qrcode, else skip)
if command -v qrencode >/dev/null; then qrencode -t ANSIUTF8 "exps://$HOST";
elif npx -y qrcode "exps://$HOST" 2>/dev/null; then :;
elif python3 -c "import qrcode" 2>/dev/null; then python3 -c "
import qrcode
qr = qrcode.QRCode(border=1); qr.add_data('exps://$HOST'); qr.make()
qr.print_ascii(invert=True)";
else echo "  (no qrencode/python-qrcode — scan from the URL above)"; fi

put() { # put <path> <<'EOF' ... EOF
  curl -s "${auth[@]}" -X PUT "$API/v1/workloads/$MOBILE/fs/file?path=$1" --data-binary @- -o /dev/null -w "  wrote $1 (%{http_code})\n"
}

say "t+40s countdown — open the app now (web or Expo Go)"
sleep 40

say "STEP 2: adding NEW route file app/(app)/hmr-one.tsx"
echo "  expectation: NO visible change (new files need a rebundle — jetplane roadmap)"
put "mobile/app/(app)/hmr-one.tsx" <<'EOF'
import { View, Text } from 'react-native';
export default function HmrOne() {
  return (
    <View className="flex-1 items-center justify-center bg-background">
      <Text className="text-foreground text-3xl">HMR ONE 🚀</Text>
    </View>
  );
}
EOF

sleep 40
STAMP=$(date +%H:%M:%S)
say "STEP 3: UPDATING existing home screen (real HMR test)"
echo "  expectation: the open screen hot-swaps to 'HMR LIVE $STAMP' within seconds"
put "mobile/app/(app)/index.tsx" <<EOF
import { View, Text } from 'react-native';
export default function Home() {
  return (
    <View className="flex-1 items-center justify-center bg-background">
      <Text className="text-foreground text-3xl">HMR LIVE ✅</Text>
      <Text className="text-foreground text-xl mt-4">updated at $STAMP</Text>
    </View>
  );
}
EOF

sleep 40
say "STEP 4: adding second NEW route file app/(app)/hmr-two.tsx"
put "mobile/app/(app)/hmr-two.tsx" <<'EOF'
import { View, Text } from 'react-native';
export default function HmrTwo() {
  return (
    <View className="flex-1 items-center justify-center bg-background">
      <Text className="text-foreground text-3xl">HMR TWO 🛰️</Text>
    </View>
  );
}
EOF

say "done — project kept alive for you"
echo "  to rebundle (makes hmr-one/hmr-two routes real):"
echo "    curl -X POST $API/v1/workloads/$MOBILE/restart -H 'Authorization: Bearer \$KEY'"
echo "  jetplane's decisions:  curl -s $API/v1/workloads/$MOBILE/logs?tail=50 -H 'Authorization: Bearer \$KEY'"
echo "  to delete:             curl -X DELETE $API/v1/projects/$REF -H 'Authorization: Bearer \$KEY'"
