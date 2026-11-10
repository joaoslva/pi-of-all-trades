#!/usr/bin/env bash
#
# Build and deploy the hub, then check the real Pi over SSH.
# One PASS/FAIL/SKIP line per check; exits non-zero on any FAIL.
# Reboots the Pi only when PIHUB_TEST_POWER=1.
#
#   hub/verify.sh                    # PI=joao@192.168.1.206 by default
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PI="${PI:-joao@192.168.1.206}"
KEY="${KEY:-$HOME/.ssh/pi_camera_drive}"
HOST="${PI#*@}"
SSH_OPTS=(-i "$KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o BatchMode=yes)
pi() { ssh "${SSH_OPTS[@]}" "$PI" "$@" </dev/null; }

fails=0
report() { # report <name> <ok:0|1|skip> [detail]
  case "$2" in
    0) echo "PASS  $1${3:+ ($3)}" ;;
    skip) echo "SKIP  $1${3:+ ($3)}" ;;
    *) echo "FAIL  $1${3:+ ($3)}"; fails=$((fails + 1)) ;;
  esac
}
code() { curl -s -o /dev/null -m 5 -w '%{http_code}' "$1"; }
wait_for() { # wait_for <seconds> <command...>
  local n=$1; shift
  while [ "$n" -gt 0 ]; do "$@" && return 0; sleep 1; n=$((n - 1)); done
  return 1
}

# One run at a time, for the whole run: the lock lives while this pipe is open.
coproc LOCK { ssh "${SSH_OPTS[@]}" "$PI" 'sudo -n flock -n /run/pihub-verify.lock sh -c "echo locked; cat >/dev/null" || echo busy'; }
read -r -t 20 lock <&"${LOCK[0]}"
[ "$lock" = locked ] || { echo "FAIL  lock (another verify is running, or the Pi is unreachable)"; exit 1; }

make -C "$REPO/hub" build || { echo "FAIL  build"; exit 1; }
echo "== deploy --check (before)"; "$REPO/platform/deploy.sh" --check hub || { echo "FAIL  deploy --check"; exit 1; }
echo "== deploy"; "$REPO/platform/deploy.sh" hub || { echo "FAIL  deploy"; exit 1; }
# A new unit is not running yet, so deploy.sh does not start it.
pi 'sudo -n systemctl enable --now pihub.service' >/dev/null 2>&1
echo "== deploy --check (after)"; "$REPO/platform/deploy.sh" --check hub
echo "== checks"

landing_ok() { [ "$(code "http://$HOST/")" = 200 ]; }
if wait_for 10 landing_ok; then
  page="$(curl -s -m 5 "http://$HOST/")"
  if grep -q "href=\"http://$HOST:8081/\"" <<<"$page" && grep -q '>Camera<' <<<"$page"; then
    report "landing page on :80 has Camera button to :8081" 0
  else
    report "landing page on :80 has Camera button to :8081" 1 "no matching link"
  fi
else
  report "landing page on :80 has Camera button to :8081" 1 "port 80 not 200"
fi

c="$(code "http://$HOST:8081/")"
[ "$c" = 200 ] && report "camera app answers on :8081" 0 || report "camera app answers on :8081" 1 "got $c"

# Memory after 400 requests.
for _ in $(seq 400); do echo "http://$HOST/"; done | xargs -P 4 -n 1 curl -s -o /dev/null -m 5
rss="$(pi 'awk "/VmRSS/{print \$2}" /proc/$(systemctl show -p MainPID --value pihub)/status')"
echo "INFO  pihub VmRSS after 400 requests: ${rss:-?} kB"
[ -n "$rss" ] && [ "$rss" -lt 20000 ] && report "pihub under 20000 kB" 0 "$rss kB" || report "pihub under 20000 kB" 1 "${rss:-?} kB"

# kill -9 -> a new pid answers on :80 within 5 s (timed on the Pi, not over ssh round trips)
if pi 'old=$(systemctl show -p MainPID --value pihub); sudo -n kill -9 $old
       for i in $(seq 50); do sleep 0.1
         n=$(systemctl show -p MainPID --value pihub)
         [ "$n" != 0 ] && [ "$n" != "$old" ] && curl -s -o /dev/null -m 1 http://127.0.0.1/ && exit 0
       done; exit 1'; then
  report "pihub restarts within 5 s of kill -9" 0
else
  report "pihub restarts within 5 s of kill -9" 1
fi

# Stopping the web app takes camera-live with it (PartOf), whether or not a camera is plugged in.
if pi 'systemctl show camera-live -p PartOf --value' | grep -q camera-drive-web.service; then
  report "camera-live is PartOf camera-drive-web" 0
else
  report "camera-live is PartOf camera-drive-web" 1
fi

# --- H2: admin login, stats, power. The throwaway password's hash is removed on exit.
HASH=/etc/pihub/admin.hash
JAR="$(mktemp)"; ours_hash=1; T=""; live_before=""
cleanup() {
  rm -f "$JAR"
  [ "$ours_hash" = 1 ] && pi "sudo -n rm -f $HASH" 2>/dev/null # only a hash this run created
  [ -n "$T" ] || return 0
  pi "sudo -n systemctl stop pihub-verify-stray 2>/dev/null; sudo -n systemctl reset-failed pihub-verify-stray 2>/dev/null
      kill \$(cat $T/pid) 2>/dev/null; rm -rf $T
      systemctl -q is-active camera-drive-web || sudo -n systemctl start camera-drive-web
      [ \"$live_before\" = active ] || sudo -n systemctl stop camera-live" 2>/dev/null
}
trap cleanup EXIT
PW="verify-$RANDOM$RANDOM$RANDOM"
admin_ok=0
if pi "test -e $HASH"; then
  ours_hash=0 # not ours: leave the operator's password alone
  report "admin checks" skip "$HASH already exists, not overwriting it"
else
  [ "$(code "http://$HOST/admin")" = 503 ] && report "/admin without a password answers 503" 0 || report "/admin without a password answers 503" 1
  echo "$PW" | ssh "${SSH_OPTS[@]}" "$PI" "sudo -n /usr/local/bin/pihub passwd" 2>&1 | grep -v '^$'
  [ "$(code "http://$HOST/admin")" = 200 ] && report "pihub passwd then /admin answers 200" 0 || report "pihub passwd then /admin answers 200" 1
  w="$(curl -s -o /dev/null -m 10 -w '%{http_code}' -d password=wrong "http://$HOST/admin/api/login")"
  [ "$w" = 401 ] && report "wrong password gets 401" 0 || report "wrong password gets 401" 1 "got $w"
  [ "$(code "http://$HOST/admin/api/stats")" = 401 ] && report "stats without cookie gets 401" 0 || report "stats without cookie gets 401" 1
  start=$(date +%s.%N)
  r="$(curl -s -o /dev/null -m 10 -c "$JAR" -w '%{http_code}' --data-urlencode "password=$PW" "http://$HOST/admin/api/login")"
  echo "INFO  login took $(echo "$(date +%s.%N) - $start" | bc) s"
  if [ "$r" = 204 ] && grep -q HttpOnly "$JAR"; then admin_ok=1; report "login with the password works (HttpOnly cookie)" 0; else report "login with the password works (HttpOnly cookie)" 1 "got $r"; fi
fi
if [ "$admin_ok" = 1 ]; then
  stats="$(curl -s -m 5 -b "$JAR" "http://$HOST/admin/api/stats")"
  ref="$(pi 'echo $(free -m | awk "/^Mem:/{print \$7}") $(( $(date +%s) - $(date -d "$(uptime -s)" +%s) ))')"
  if python3 - "$stats" "$ref" <<'PY'
import json, sys
s = json.loads(sys.argv[1]); free_mb, up = map(float, sys.argv[2].split())
within = lambda a, b, slack=0: abs(a - b) <= max(0.05 * b, slack)
print("INFO  stats", s, "| free -m available", free_mb, "| uptime s", up)
sys.exit(0 if within(s["mem_available_kb"] / 1024, free_mb, 2) and within(s["uptime_s"], up, 3) and s["temp_c"] > 0 else 1)
PY
  then report "stats match free -m and uptime within 5 percent" 0; else report "stats match free -m and uptime within 5 percent" 1; fi

  post() { curl -s -m 10 -b "$JAR" -w ' %{http_code}' -d "$1" "http://$HOST/admin/api/power"; }
  out="$(post '{"action":"reboot"}')"; nonce="$(sed -n 's/.*"nonce":"\([0-9a-f]*\)".*/\1/p' <<<"$out")"
  [ -n "$nonce" ] && report "power step one returns a nonce (nothing runs)" 0 || report "power step one returns a nonce (nothing runs)" 1 "$out"
  # A reused nonce must fail; the confirm below uses a wrong action so nothing reboots.
  bad="$(post "{\"action\":\"poweroff\",\"nonce\":\"$nonce\"}")"; again="$(post "{\"action\":\"reboot\",\"nonce\":\"$nonce\"}")"
  case "$bad$again" in *" 400"*" 400") report "spent or reused nonce is rejected" 0 ;; *) report "spent or reused nonce is rejected" 1 "$bad / $again" ;; esac
  x="$(curl -s -o /dev/null -m 5 -b "$JAR" -H 'Sec-Fetch-Site: cross-site' -d 'x' -w '%{http_code}' "http://$HOST/admin/api/power")"
  [ "$x" = 403 ] && report "cross-site POST gets 403" 0 || report "cross-site POST gets 403" 1 "got $x"
fi
mode="$(pi "stat -c '%a %U:%G' $HASH 2>/dev/null")"
if [ "$admin_ok" = 1 ]; then
  case "$mode" in "640 root:joao") report "hash file is 0640 root:joao, not world-readable" 0 ;; *) report "hash file is 0640 root:joao, not world-readable" 1 "$mode" ;; esac
  n="$(pi "journalctl -u pihub --no-pager | grep -cF '$PW'")"
  [ "$n" = 0 ] && report "journal has no password text" 0 || report "journal has no password text" 1 "$n lines"
  rss="$(pi 'awk "/VmRSS/{print \$2}" /proc/$(systemctl show -p MainPID --value pihub)/status')"
  echo "INFO  pihub VmRSS after admin checks: ${rss:-?} kB"
  [ -n "$rss" ] && [ "$rss" -lt 23000 ] && report "pihub under 23000 kB" 0 "$rss kB" || report "pihub under 23000 kB" 1 "${rss:-?} kB"
fi

# --- H3: switching. The operator's password is unknown, so an extra hub runs on a high
# port with a throwaway hash and state dir. It uses the real systemd, so the switches are real.
H3=http://127.0.0.1:18081
PW3="verify3-$RANDOM$RANDOM$RANDOM"
T="$(pi mktemp -d)"
live_before="$(pi 'systemctl is-active camera-live')"
cam_before="$(pi 'systemctl is-active camera-drive-web')"
echo "INFO  before: camera-drive-web=$cam_before camera-live=$live_before"
api() { pi "curl -s -m 60 -b $T/jar -c $T/jar $1"; }
start_inst() {
  pi "setsid nohup /usr/local/bin/pihub --listen 127.0.0.1:18081 --hash-file $T/admin.hash --state-dir $T --services-dir $T/svc >$T/log 2>&1 </dev/null & echo \$! >$T/pid"
  wait_for 10 pi "curl -s -o /dev/null -m 2 $H3/"
  [ "$(pi "curl -s -o /dev/null -m 5 -c $T/jar --data-urlencode password=$PW3 -w '%{http_code}' $H3/admin/api/login")" = 204 ]
}
stop_inst() { pi "kill \$(cat $T/pid); sleep 0.5"; }
svc_json() { api "$H3/admin/api/services"; }
nonce_for() { api "-d '{\"id\":\"camera\"}' $H3/admin/api/$1" | sed -n 's/.*"nonce":"\([0-9a-f]*\)".*/\1/p'; }
py() { python3 -c "import json,sys; d=json.loads(sys.argv[1]); $2" "$1"; }

ssh "${SSH_OPTS[@]}" "$PI" "mkdir $T/svc && cp /etc/pihub/services.d/*.json $T/svc/ && cat >$T/svc/stray.json" <<'JSON'
{"id":"stray","name":"Stray","description":"verify fixture","class":"exclusive","units":["pihub-verify-stray.service"],"port":18090}
JSON
echo "$PW3" | ssh "${SSH_OPTS[@]}" "$PI" "sudo -n /usr/local/bin/pihub passwd --file $T/admin.hash" 2>&1 | grep -v '^$'
if [ "$cam_before" != active ]; then
  report "H3 checks" skip "camera-drive-web was not active before the run; not switching anything"
elif ! start_inst; then
  report "high-port hub starts and logs in" 1 "$(pi "tail -3 $T/log")"
else
  j="$(svc_json)"
  if py "$j" "c=[s for s in d['services'] if s['id']=='camera'][0]; print('INFO  camera:', c); sys.exit(0 if c['class']=='exclusive' and c['running'] and len(c['units'])==2 and (c['memory_bytes'] is None or c['memory_bytes']>0) and 'warnings' in d else 1)"; then
    report "services list shows camera exclusive, running, units, memory or n/a" 0
  else report "services list shows camera exclusive, running, units, memory or n/a" 1 "$j"; fi
  py "$j" "sys.exit(0 if [s for s in d['services'] if s['id']=='offload'][0]['class']=='background' else 1)" &&
    report "offload listed as background" 0 || report "offload listed as background" 1

  n="$(nonce_for stop)"
  bad="$(api "-o /dev/null -w '%{http_code}' -d '{\"id\":\"camera\",\"nonce\":\"$n\"}' $H3/admin/api/switch")"
  [ "$bad" = 400 ] && report "stop nonce cannot confirm a switch" 0 || report "stop nonce cannot confirm a switch" 1 "got $bad"
  n="$(nonce_for stop)"
  code="$(api "-o /dev/null -w '%{http_code}' -d '{\"id\":\"camera\",\"nonce\":\"$n\"}' $H3/admin/api/stop")"
  both_off="$(pi 'systemctl is-active camera-drive-web camera-live | sort -u | tr "\n" " "')"
  [ "$code" = 204 ] && [ "$both_off" = "inactive " ] && report "Camera off stops camera-drive-web and camera-live together" 0 || report "Camera off stops camera-drive-web and camera-live together" 1 "http $code, units: $both_off"
  [ "$(pi "cat $T/state.json")" = '{"active":""}' ] && report "state says off after Camera off" 0 || report "state says off after Camera off" 1 "$(pi "cat $T/state.json")"

  n="$(nonce_for switch)"
  secs="$(pi "s=\$(date +%s.%N); c=\$(curl -s -o /dev/null -m 60 -b $T/jar -w '%{http_code}' -d '{\"id\":\"camera\",\"nonce\":\"$n\"}' $H3/admin/api/switch)
    until [ \"\$(curl -s -o /dev/null -m 2 -w '%{http_code}' http://127.0.0.1:8081/)\" = 200 ]; do sleep 0.2; [ \$(( \$(date +%s) - \${s%.*} )) -gt 60 ] && { echo timeout; exit 1; }; done
    echo \$c \$(echo \"\$(date +%s.%N) - \$s\" | bc)")"
  echo "INFO  confirm to camera page answering again: $secs (http code, seconds)"
  on="$(pi 'systemctl is-active camera-drive-web camera-live | sort -u | tr "\n" " "')"
  case "$secs" in 204*) [ "$on" = "active " ] && report "Camera on starts both units and :8081 answers" 0 "$secs" || report "Camera on starts both units and :8081 answers" 1 "units: $on" ;;
    *) report "Camera on starts both units and :8081 answers" 1 "$secs" ;; esac

  stop_inst; start_inst
  j="$(svc_json)"
  py "$j" "sys.exit(0 if [s for s in d['services'] if s['id']=='camera'][0]['selected'] else 1)" && [ "$(pi "cat $T/state.json")" = '{"active":"camera"}' ] &&
    report "state survives a hub restart" 0 || report "state survives a hub restart" 1 "$(pi "cat $T/state.json")"

  pi 'sudo -n systemd-run --quiet --unit=pihub-verify-stray sleep 600'
  sleep 1
  j="$(svc_json)"
  py "$j" "print('INFO  warnings:', d['warnings']); sys.exit(0 if any(w['kind']=='many-exclusive' for w in d['warnings']) else 1)" &&
    report "a stray second Exclusive unit raises a warning" 0 || report "a stray second Exclusive unit raises a warning" 1 "$j"
  pi 'sudo -n systemctl stop pihub-verify-stray'
  j="$(svc_json)"
  py "$j" "sys.exit(0 if not any(w['kind']=='many-exclusive' for w in d['warnings']) else 1)" &&
    report "warning clears when the stray unit stops" 0 || report "warning clears when the stray unit stops" 1

  deps="$(pi 'systemctl show camera-offload@sda1.service -p PartOf,Requires,BindsTo,Wants,After')"
  grep -qE 'camera-(drive-web|live)' <<<"$deps" && report "camera-offload@ has no dependency on the camera app" 1 "$deps" || report "camera-offload@ has no dependency on the camera app" 0

  [ "$(pi 'cat /var/lib/pihub/state.json')" = '{"active":"camera"}' ] && report "production hub state.json says camera" 0 || report "production hub state.json says camera" 1 "$(pi 'cat /var/lib/pihub/state.json 2>&1')"
  rss="$(pi 'awk "/VmRSS/{print \$2}" /proc/$(systemctl show -p MainPID --value pihub)/status')"
  echo "INFO  pihub VmRSS after H3 checks: ${rss:-?} kB"
  [ -n "$rss" ] && [ "$rss" -lt 23000 ] && report "pihub under 23000 kB after H3" 0 "$rss kB" || report "pihub under 23000 kB after H3" 1 "${rss:-?} kB"
fi
# Leave the Pi as found: camera-live was only started by the switch, not by a camera in live mode.
[ "$live_before" = active ] || pi 'sudo -n systemctl stop camera-live'
echo "INFO  after: camera-drive-web=$(pi 'systemctl is-active camera-drive-web') camera-live=$(pi 'systemctl is-active camera-live')"

if [ "${PIHUB_TEST_POWER:-}" = 1 ] && [ -z "${NO_REBOOT:-}" ]; then
# Never reboot in the middle of an offload.
idle() { [ -z "$(pi 'systemctl list-units "camera-offload@*" --state=activating,active --no-legend')" ]; }
wait_for 900 idle || echo "INFO  offload still running after 15 min, rebooting anyway"

# Reboot, then both units active.
boot_id() { pi cat /proc/sys/kernel/random/boot_id; }
before="$(boot_id)"
had_camera=0; pi lsusb | grep -q '08ca:202[23]' && had_camera=1
if [ "$admin_ok" = 1 ]; then # reboot from the page, as an operator would
  n="$(curl -s -m 10 -b "$JAR" -d '{"action":"reboot"}' "http://$HOST/admin/api/power" | sed -n 's/.*"nonce":"\([0-9a-f]*\)".*/\1/p')"
  curl -s -o /dev/null -m 10 -b "$JAR" -d "{\"action\":\"reboot\",\"nonce\":\"$n\"}" "http://$HOST/admin/api/power"
else
  pi 'sudo -n systemctl reboot' >/dev/null 2>&1
fi
sleep 15
if wait_for 180 pi true 2>/dev/null && [ "$(boot_id)" != "$before" ]; then
  both() { [ "$(pi 'systemctl is-active pihub camera-drive-web' | sort -u)" = active ]; }
  wait_for 60 both && report "after reboot pihub and camera-drive-web are active" 0 || report "after reboot pihub and camera-drive-web are active" 1
else
  report "after reboot pihub and camera-drive-web are active" 1 "Pi did not come back"
fi
else
  report "reboot from the admin page brings the Pi back" skip "set PIHUB_TEST_POWER=1"
fi
had_camera=${had_camera:-0}

# Camera checks look at this boot's journal only; they never touch the camera's storage.
# The camera enumerates late in boot, so wait for it if it was there before.
[ "$had_camera" = 0 ] || wait_for 120 pi "lsusb | grep -q '08ca:202[23]'"
sleep 5
usb="$(pi lsusb)"
if grep -q '08ca:2023' <<<"$usb"; then
  n="$(pi 'journalctl -b -u "camera-offload@*" --no-pager | grep -c "Starting camera-offload@"')"
  [ "$n" = 1 ] && report "camera plugged in (storage) starts camera-offload@ once" 0 || report "camera plugged in (storage) starts camera-offload@ once" 1 "started $n times"
elif grep -q '08ca:2022' <<<"$usb"; then
  n="$(pi 'journalctl -b -u camera-live --no-pager | grep -c "Started camera-live"')"
  if [ "$n" = 1 ] && pi 'systemctl -q is-active camera-live'; then
    report "camera plugged in (live) has camera-live active, started once" 0
  else
    report "camera plugged in (live) has camera-live active, started once" 1 "started $n times"
  fi
else
  [ "$had_camera" = 1 ] && report "camera came back after reboot" 1 "08ca:202x gone" ||
    report "camera plug-in starts camera-offload@ / camera-live once" skip "camera not plugged in"
fi

echo "$fails check(s) failed"
[ "$fails" = 0 ]
