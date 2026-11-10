#!/bin/sh
# Page checks on the laptop sandbox: layout fit, contrast, the screenshot set, and the UI flows.
# One PASS/FAIL line per check; exits 1 on any FAIL. Screenshots go to $SHOTS (default /tmp/pihub-shots).
#   hub/web/check.sh
set -u
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
PW="$HOME/.claude/tools/playwright"
SHOTS="${SHOTS:-${TMPDIR:-/tmp}/pihub-shots}"
DEV="${PIHUB_DEV:-${TMPDIR:-/tmp}/pihub-dev}"
URL=http://127.0.0.1:18090
SC="$REPO/hub/dev/scenario.sh"
fails=0
report() { if [ "$2" = 0 ]; then echo "PASS  $1"; else echo "FAIL  $1"; fails=$((fails + 1)); fi; }
[ -e "$PW/shot.mjs" ] || { echo "FAIL  playwright tools missing at $PW (screenshots skill)"; exit 1; }
mkdir -p "$SHOTS"

PIHUB_DEV_NO_SERVICES=1 "$REPO/hub/dev/run.sh" >/dev/null || { echo "FAIL  sandbox start"; exit 1; }
node "$PW/shot.mjs" "$URL/" "$SHOTS/hub-empty.png" >/dev/null
curl -s "$URL/" | grep -q 'No services yet'; report "hub: empty state when no service is listed" $?

"$REPO/hub/dev/run.sh" >/dev/null || { echo "FAIL  sandbox start"; exit 1; }
page="$(curl -s -H 'Host: pi.test:80' "$URL/")"
echo "$page" | grep -q 'href="http://pi.test:8081/"'; report "hub: Camera button links to port 8081 of the host used" $?
echo "$page" | grep -q 'class="lock" href="/admin" aria-label="Admin"'; report "hub: lock link to /admin with a label" $?
curl -s -c "$DEV/jar" -o /dev/null -d password=sandbox-pass "$URL/admin/api/login"
COOKIE="$(awk '/pihub_session/ {print $6 "=" $7}' "$DEV/jar")"
[ -n "$COOKIE" ]; report "sandbox login" $?
printf 'hint text\n' >"$DEV/state/password-hint.txt"

for t in "" --dark; do
  node "$PW/check-fit.mjs" "$URL/" --sizes 1366x768,1920x1080 --hscroll-only 390x844 $t >/dev/null; report "fit: hub ${t:-light}" $?
  node "$PW/check-fit.mjs" "$URL/admin" --sizes 1366x768,1920x1080 --hscroll-only 390x844 --cookie "$COOKIE" $t >/dev/null; report "fit: admin logged in ${t:-light}" $?
done
"$SC" wifi keep; "$SC" services warnings
node "$PW/check-fit.mjs" "$URL/admin" --sizes 1366x768,1920x1080 --hscroll-only 390x844 --cookie "$COOKIE" >/dev/null; report "fit: admin with Keep banner and warnings" $?
"$SC" reset

node "$REPO/hub/web/check-contrast.mjs" >/dev/null; report "contrast: every pair meets WCAG AA (both themes)" $?

shot() { # shot <name> <url> [shot.mjs flags]
  n="$1"; u="$2"; shift 2
  for t in light dark; do
    d=""; [ $t = dark ] && d=--dark
    node "$PW/shot.mjs" "$u" "$SHOTS/$n-$t.png" $d "$@" >/dev/null 2>"$SHOTS/$n-$t.err" || { report "shot $n-$t" 1; continue; }
    if grep -q '^page error' "$SHOTS/$n-$t.err"; then report "shot $n-$t without page errors ($(head -1 "$SHOTS/$n-$t.err"))" 1; fi
  done
}
A="$URL/admin"; C="--cookie $COOKIE"
for s in 390x844 1366x768 1920x1080; do shot "hub-$s" "$URL/" --size $s; done
shot login "$A"
shot admin-1366 "$A" $C --delay 900
shot admin-1920 "$A" $C --delay 900 --size 1920x1080
shot admin-390 "$A" $C --delay 900 --size 390x844 --full
for w in hotspot-demand hotspot-fallback keep none broken; do "$SC" wifi $w; shot "wifi-$w" "$A" $C --delay 900; done
"$SC" wifi client
for v in off warnings; do "$SC" services $v; shot "services-$v" "$A" $C --delay 900; done
"$SC" services normal
for v in none copying; do "$SC" camera $v; shot "camera-$v" "$A" $C --delay 900; done
"$SC" camera ok
shot scan "$A" $C --wait '#panel:not([hidden])' --click '#scanbtn' --delay 2500
shot add-open "$A" $C --wait '#panel:not([hidden])' --click '#adddetails summary' --delay 500
shot hint-open "$A" $C --wait '#panel:not([hidden])' --click '#bulb' --delay 500
shot password-invalid "$A" $C --wait '#panel:not([hidden])' --click '#pwform button' --delay 500
shot paused "$A" $C --wait '#panel:not([hidden])' --click '#pause' --delay 500

node "$REPO/hub/dev/flows.mjs" "$URL" "$SHOTS"; report "flows: switch, Wi-Fi, password, hint, logout through the real page" $?
"$SC" reset

echo "screenshots: $SHOTS"
[ "$fails" = 0 ] && echo "ALL PASS" || echo "$fails FAIL"
[ "$fails" = 0 ]
