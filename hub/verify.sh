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
echo "== deploy --check (before)"; "$REPO/platform/deploy.sh" --check platform hub || { echo "FAIL  deploy --check"; exit 1; }
echo "== deploy"; "$REPO/platform/deploy.sh" platform hub || { echo "FAIL  deploy"; exit 1; }
# A new unit is not running yet, so deploy.sh does not start it.
pi 'sudo -n systemctl enable --now pihub.service' >/dev/null 2>&1
echo "== deploy --check (after)"; "$REPO/platform/deploy.sh" --check platform hub
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
JAR="$(mktemp)"; ours_hash=1; T=""; T4=""; H4_TEST_CON=""; live_before=""; h3_live_starts=0
cleanup() {
  rm -f "$JAR"
  [ "$ours_hash" = 1 ] && pi "sudo -n rm -f $HASH" 2>/dev/null # only a hash this run created
  if [ -n "$T4" ]; then # H4: undo anything a failed check left behind
    pi "kill \$(cat $T4/pid) 2>/dev/null; sudo -n systemctl stop pihub-wifi-revert.timer pihub-verify-safety.timer 2>/dev/null
        rm -f /run/pihub/wifi-switching; nmcli -t -f NAME connection show | grep -qxF '$H4_TEST_CON' && [ -n '$H4_TEST_CON' ] && nmcli connection delete id '$H4_TEST_CON'
        rm -rf $T4" 2>/dev/null
  fi
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
starts_before="$(pi 'journalctl -b -u camera-live --no-pager | grep -c "Started camera-live"')"
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

  n="$(nonce_for switch)"; h3_live_starts=1
  secs="$(pi "s=\$(date +%s.%N); c=\$(curl -s -o /dev/null -m 60 -b $T/jar -w '%{http_code}' -d '{\"id\":\"camera\",\"nonce\":\"$n\"}' $H3/admin/api/switch)
    until [ \"\$(curl -s -o /dev/null -m 2 -w '%{http_code}' http://127.0.0.1:8081/)\" = 200 ]; do sleep 0.2; [ \$(( \$(date +%s) - \${s%.*} )) -gt 60 ] && { echo timeout; exit 1; }; done
    echo \$c \$(awk -v e=\$(date +%s.%N) -v s=\$s 'BEGIN{printf \"%.2f\", e-s}')")"
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
[ "$live_before" = active ] || pi 'sudo -n systemctl stop camera-live; sudo -n systemctl reset-failed camera-live' # no camera: the start leaves it failed
echo "INFO  after: camera-drive-web=$(pi 'systemctl is-active camera-drive-web') camera-live=$(pi 'systemctl is-active camera-live')"


# --- H4: Wi-Fi profiles. A throwaway hub on a high port (own hash and state dir) drives the
# real NetworkManager through polkit as joao. Anything that can cost the Pi its network is
# behind an independent 300 s revert (pihub-verify-safety) that is cancelled at the end.
echo "== H4 Wi-Fi"
H4=http://127.0.0.1:18082
PW4="verify4-$RANDOM$RANDOM$RANDOM"
T4="$(pi mktemp -d)"
api4() { pi "curl -s -m 60 -b $T4/jar -c $T4/jar $1"; }
nonce4() { api4 "-d '$2' $H4/admin/api/wifi/$1" | sed -n 's/.*"nonce":"\([0-9a-f]*\)".*/\1/p'; }
step4() { # step4 <op> <json-without-nonce> -> "<http code> <body>" of the confirmed call
  local n; n="$(nonce4 "$1" "$2")"
  api4 "-w ' %{http_code}' -d '${2%\}},\"nonce\":\"$n\"}' $H4/admin/api/wifi/$1"
}
echo "$PW4" | ssh "${SSH_OPTS[@]}" "$PI" "sudo -n /usr/local/bin/pihub passwd --file $T4/admin.hash" 2>&1 | grep -v '^$'
pi "setsid nohup /usr/local/bin/pihub --listen 127.0.0.1:18082 --hash-file $T4/admin.hash --state-dir $T4 --services-dir /etc/pihub/services.d >$T4/log 2>&1 </dev/null & echo \$! >$T4/pid"
wait_for 10 pi "curl -s -o /dev/null -m 2 $H4/"
if [ "$(pi "curl -s -o /dev/null -m 5 -c $T4/jar --data-urlencode password=$PW4 -w '%{http_code}' $H4/admin/api/login")" != 204 ]; then
  report "H4 throwaway hub starts and logs in" 1 "$(pi "tail -3 $T4/log")"
else
  # (e) the fallback timer is outside the hub and running
  t="$(pi 'systemctl is-active camera-net-fallback.timer; systemctl is-enabled camera-net-fallback.timer' | tr '\n' ' ')"
  [ "$t" = "active enabled " ] && report "fallback timer is active and enabled" 0 || report "fallback timer is active and enabled" 1 "$t"

  # (b) real nmcli output through the parsers
  w="$(api4 "$H4/admin/api/wifi")"; echo "INFO  wifi status: $w"
  SSID="$(py "$w" "print(d['ssid'])")"
  if py "$w" "sys.exit(0 if d['mode']=='client' and d['last_client']=='camera-drive-wifi' and d['active']=='camera-drive-wifi' and d['ip']=='$HOST' and d['fallback_timer']=='active' and {s['name'] for s in d['saved']}>={'camera-drive-wifi','camera-drive-ap'} and [s for s in d['saved'] if s['name']=='camera-drive-ap'][0]['hotspot'] and not [s for s in d['saved'] if s['name']=='camera-drive-wifi'][0]['hotspot'] else 1)"; then
    report "status: client mode, last_client from the active network, our SSID and IP, saved list marks the hotspot profile" 0
  else report "status: client mode, last_client from the active network, our SSID and IP, saved list marks the hotspot profile" 1; fi
  sc="$(api4 "$H4/admin/api/wifi/scan")"
  py "$sc" "sys.exit(0 if any(n['ssid']=='$SSID' and n['in_use'] for n in d['networks']) else 1)" &&
    report "scan in client mode lists the network we are on" 0 || report "scan in client mode lists the network we are on" 1 "$sc"
  for body in '{"name":"camera-drive-wifi"}' '{"name":"camera-drive-ap"}'; do
    c="$(api4 "-o /dev/null -w '%{http_code}' -d '$body' $H4/admin/api/wifi/forget")"
    [ "$c" = 400 ] && report "forget refused for $body" 0 || report "forget refused for $body" 1 "got $c"
  done
  c="$(api4 "-o /dev/null -w '%{http_code}' -d '{}' $H4/admin/api/fallback")"
  [ "$c" = 404 ] || [ "$c" = 405 ] && report "no hub route for the fallback ($c)" 0 || report "no hub route for the fallback" 1 "got $c"
  (cd "$REPO/hub" && go test -count=1 -run 'TestNoRouteTouchesFallback' ./... >/dev/null) && report "route table test: nothing touches camera-net-fallback" 0 || report "route table test: nothing touches camera-net-fallback" 1

  # (c) the fallback script stands down for a fresh flag and ignores a stale one (fake nmcli logs calls; real NM untouched)
  ssh "${SSH_OPTS[@]}" "$PI" "mkdir $T4/fake && cat >$T4/fake/nmcli" <<'SH'
#!/bin/sh
echo "$*" >> "$FAKE_LOG"
case "$*" in
  *GENERAL.STATE*) echo "100 (connected)" ;;
  *"connection show --active"*) a="${FAKE_ACTIVE-camera-drive-wifi}"; [ -n "$a" ] && echo "$a:wlan0" ;;
  *"connection show"*) echo "camera-drive-wifi:802-11-wireless" ;;
esac
SH
  runfb() { pi "chmod +x $T4/fake/nmcli; rm -f $T4/calls; touch $T4/calls; $2; PATH=$T4/fake:\$PATH FAKE_LOG=$T4/calls SWITCH_FLAG=$T4/flag HOLD_FLAG=$T4/hold /usr/local/bin/camera-net-fallback; wc -l <$T4/calls"; }
  fresh="$(runfb x 'touch '$T4'/flag')"; stale="$(runfb x 'touch -d "2 minutes ago" '$T4'/flag')"; none="$(runfb x 'rm -f '$T4'/flag')"
  echo "INFO  fake nmcli calls: fresh flag $fresh, stale flag $stale, no flag $none"
  [ "$fresh" = 0 ] && report "fallback does nothing while the flag is fresh" 0 || report "fallback does nothing while the flag is fresh" 1 "$fresh calls"
  # H4b: the hold. Hosting + hold: no scan, no rejoin (0 calls besides the state/active reads is not
  # required: no `wifi list`, no `connection up/down`). Hosting without a hold: as before. Hold with a
  # client active: ignored and removed. Hold with nothing active: not suppressed, still removed.
  hostc() { pi "grep -cE 'wifi list|connection (up|down)' $T4/calls"; }
  rm_flag='rm -f '$T4'/flag'
  runfb x "$rm_flag; touch $T4/hold; export FAKE_ACTIVE=camera-drive-ap; sudo -n rm -f /run/camera-net-fallback.scan" >/dev/null
  hold_ap="$(hostc)"; hold_ap_kept="$(pi "test -e $T4/hold && echo yes || echo no")"
  runfb x "$rm_flag; rm -f $T4/hold; export FAKE_ACTIVE=camera-drive-ap; sudo -n rm -f /run/camera-net-fallback.scan" >/dev/null; nohold_ap="$(pi "grep -c 'wifi list' $T4/calls")"
  pi 'sudo -n rm -f /run/camera-net-fallback.scan' # the throttle stamp is only a throttle; leave none behind
  runfb x "$rm_flag; touch $T4/hold; export FAKE_ACTIVE=camera-drive-wifi" >/dev/null; hold_client="$(hostc)/$(pi "test -e $T4/hold && echo kept || echo removed")"
  runfb x "$rm_flag; touch $T4/hold; export FAKE_ACTIVE=" >/dev/null; hold_none="$(pi "grep -c 'wifi list' $T4/calls")/$(pi "test -e $T4/hold && echo kept || echo removed")"
  echo "INFO  hold checks (fake nmcli): AP+hold scans/ups $hold_ap (hold $hold_ap_kept); AP, no hold: $nohold_ap scan(s); client+hold: $hold_client; nothing+hold: $hold_none"
  [ "$hold_ap" = 0 ] && [ "$hold_ap_kept" = yes ] && report "fallback: hotspot hold set and AP active: no scan, no rejoin" 0 || report "fallback: hotspot hold set and AP active: no scan, no rejoin" 1 "$hold_ap"
  [ "$nohold_ap" -ge 1 ] && report "fallback: no hold and AP active: scans as before" 0 || report "fallback: no hold and AP active: scans as before" 1 "$nohold_ap scans"
  [ "$hold_client" = "0/removed" ] && report "fallback: stale hold with a client active is ignored and removed" 0 || report "fallback: stale hold with a client active is ignored and removed" 1 "$hold_client"
  [ "${hold_none%/*}" -ge 1 ] && [ "${hold_none#*/}" = removed ] && report "fallback: hold never suppresses the nothing-active branch" 0 "$hold_none" || report "fallback: hold never suppresses the nothing-active branch" 1 "$hold_none"
  [ "$stale" -gt 0 ] && [ "$none" -gt 0 ] && report "fallback runs as before with a stale flag or none" 0 "$stale and $none calls" || report "fallback runs as before with a stale flag or none" 1 "$stale / $none"

  # (d) kill -9 pihub with a flag present: the fallback timer is untouched, the hub returns
  pi 'touch /run/pihub/wifi-switching /run/pihub/hotspot-hold; sudo -n kill -9 $(systemctl show -p MainPID --value pihub); sleep 4; tm=$(systemctl is-active camera-net-fallback.timer); hub=$(systemctl is-active pihub); rm -f /run/pihub/wifi-switching /run/pihub/hotspot-hold; echo $tm $hub' | grep -qx 'active active' &&
    report "kill -9 pihub with a flag and a hold present: fallback timer still active, hub back" 0 || report "kill -9 pihub with a flag and a hold present: fallback timer still active, hub back" 1

  # (a) wrong password for our own SSID: the Pi must come back by itself
  H4_TEST_CON="$SSID"
  if pi "nmcli -t -f NAME connection show | grep -qxF '$SSID'"; then
    H4_TEST_CON=""; report "wrong-password revert" skip "a profile named $SSID already exists"
  else
    pi 'sudo -n systemd-run --quiet --unit=pihub-verify-safety --on-active=300 nmcli connection up id camera-drive-wifi' &&
      echo "INFO  independent 300 s safety revert armed"
    r="$(step4 add "{\"ssid\":\"$SSID\",\"password\":\"wrong-pass-$RANDOM\"}")"
    case "$r" in *" 204") report "add a profile for our SSID with a wrong password" 0 ;; *) report "add a profile for our SSID with a wrong password" 1 "$r" ;; esac
    nonce="$(nonce4 connect "{\"name\":\"$SSID\"}")"
    ssh "${SSH_OPTS[@]}" "$PI" "cat >$T4/rec.sh; chmod +x $T4/rec.sh" <<SH
#!/bin/sh
cd $T4
date +%s.%N > t0
( echo "\$(curl -s -m 90 -b jar -w ' %{http_code}' -d '{"name":"$SSID","nonce":"$nonce"}' $H4/admin/api/wifi/connect)" > connect.out; date +%s.%N > t_connect_returned ) &
sleep 0.3
i=0; : > active.log
while [ \$i -lt 170 ]; do
  a=\$(nmcli -t -f NAME,DEVICE connection show --active | sed -n 's/:wlan0\$//p')
  echo "\$(date +%s.%N) \$a" >> active.log
  if [ "\$a" = camera-drive-wifi ] && [ -e t_connect_returned ] && [ ! -e /run/pihub/wifi-switching ] && ! systemctl -q is-active pihub-wifi-revert.service; then date +%s.%N > t1; break; fi
  i=\$((i+1)); sleep 1
done
echo done > finished
SH
    pi "setsid nohup $T4/rec.sh >/dev/null 2>&1 </dev/null &"
    t_start=$(date +%s)
    sleep 5
    echo "INFO  Pi may be unreachable now; waiting for it (no retry loop beyond the armed revert)"
    back() { pi "test -e $T4/finished" 2>/dev/null; }
    while ! back && [ $(( $(date +%s) - t_start )) -lt 330 ]; do sleep 3; done # bounded by the armed 300 s revert
    secs="$(pi "awk 'NR==FNR{a=\$1;next}{printf \"%.1f\", \$1-a}' $T4/t0 $T4/t1 2>/dev/null")"
    ssh_back=$(( $(date +%s) - t_start ))
    echo "INFO  connect returned: $(pi "cat $T4/connect.out | cut -c1-200")"
    echo "INFO  measured: connect call to Pi back on camera-drive-wifi = ${secs:-?} s; laptop saw ssh answer ${ssh_back} s after the connect call"
    [ -n "$secs" ] && [ "${secs%.*}" -le 15 ] && report "wrong password: back on camera-drive-wifi at once (well before the 30 s revert)" 0 "$secs s" ||
      report "wrong password: back on camera-drive-wifi at once (well before the 30 s revert)" 1 "${secs:-never} s"
    pi "grep -q 'back on \"camera-drive-wifi\"' $T4/connect.out" && report "connect error says which network the Pi ended on" 0 || report "connect error says which network the Pi ended on" 1
    [ "$(pi 'systemctl is-active pihub-wifi-revert.timer')" = inactive ] && report "revert timer cancelled by the immediate recovery" 0 || report "revert timer cancelled by the immediate recovery" 1
    [ "$(pi "nmcli -t -f NAME connection show --active | grep -c '^camera-drive-wifi$'; ls /run/pihub/wifi-switching 2>&1 | grep -c 'No such'")" = "$(printf '1\n1')" ] &&
      report "after the revert: camera-drive-wifi active, flag removed" 0 || report "after the revert: camera-drive-wifi active, flag removed" 1
    seen="$(pi "awk '{print \$2}' $T4/active.log | sort | uniq -c | tr '\n' ' '")"
    echo "INFO  seconds without camera-drive-wifi on wlan0 during the switch (1 s samples): $(pi "awk '\$2!=\"camera-drive-wifi\"' $T4/active.log | wc -l"); first sample back on it after leaving: $(pi "awk 'NR==FNR{a=\$1;next} \$2!=\"camera-drive-wifi\"{off=\$1} off&&\$2==\"camera-drive-wifi\"{printf \"%.1f s after the connect call\", \$1-a; exit}' $T4/t0 $T4/active.log")"
    echo "INFO  active connection seen each second during the switch: $seen"
    [ -n "$seen" ] && ! grep -q 'camera-drive-ap' <<<"$seen" && report "hotspot never raised while the switch was in flight" 0 "$seen" || report "hotspot never raised while the switch was in flight" 1 "${seen:-no samples}"
    # H4b: Switch availability (step one only: nothing runs, nothing is switched)
    sw() { api4 "-o /dev/null -w '%{http_code}' -d '$2' $H4/admin/api/wifi/$1"; }
    [ "$(sw connect "{\"name\":\"$SSID\"}")" = 200 ] && [ "$(sw connect '{"name":"camera-drive-wifi"}')" = 400 ] && [ "$(sw connect '{"name":"camera-drive-ap"}')" = 400 ] &&
      report "Switch offered for a saved network, refused for the active one and the hotspot profile" 0 || report "Switch offered for a saved network, refused for the active one and the hotspot profile" 1
    [ "$(sw hotspot-off '{}')" = 400 ] && report "leave-hotspot refused while on a client network" 0 || report "leave-hotspot refused while on a client network" 1
    [ "$(pi "nmcli -t -f NAME connection show --active | grep -cx camera-drive-ap; test -e /run/pihub/hotspot-hold && echo hold")" = 0 ] &&
      report "real hotspot never raised, no hold left" 0 || report "real hotspot never raised, no hold left" 1
    f="$(step4 forget "{\"name\":\"$SSID\"}")"
    case "$f" in *" 204") report "forget the test profile through the API" 0; H4_TEST_CON="" ;; *) report "forget the test profile through the API" 1 "$f" ;; esac
    pi 'sudo -n systemctl stop pihub-verify-safety.timer 2>/dev/null; systemctl is-active pihub-verify-safety.timer' | grep -qx inactive && echo "INFO  safety revert cancelled"
  fi
  rss="$(pi "awk '/VmRSS/{print \$2}' /proc/\$(cat $T4/pid)/status")"
  echo "INFO  throwaway hub VmRSS after H4 checks: ${rss:-?} kB"
  [ -n "$rss" ] && [ "$rss" -lt 23000 ] && report "pihub under 23000 kB after H4" 0 "$rss kB" || report "pihub under 23000 kB after H4" 1 "${rss:-?} kB"
fi
echo "INFO  after H4: active=$(pi "nmcli -t -f NAME connection show --active | head -1") profiles=$(pi "nmcli -t -f NAME connection show | tr '\n' ' '") timer=$(pi 'systemctl is-active camera-net-fallback.timer')"

. "$REPO/hub/verify_account.sh" # H7: password change, hint, system info, cpu_percent

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
  # The small journal rotates during long verify sessions, so fall back to offload's own status.json timestamp.
  if pi 'n=$(journalctl -b -t systemd --no-pager | grep -c "Starting camera-offload@"); b=$(date -d "$(uptime -s)" +%s); m=$(stat -c %Y /srv/camera-drive/status.json 2>/dev/null || echo 0); [ "$n" -ge 1 ] || [ "$m" -ge "$b" ]'; then report "camera plugged in (storage): camera-offload@ ran this boot" 0
  else report "camera plugged in (storage): camera-offload@ ran this boot" 1; fi
elif grep -q '08ca:2022' <<<"$usb"; then
  n="$(pi 'journalctl -b -u camera-live --no-pager | grep -c "Started camera-live"')"
  # Earlier runs this boot (and plug-ins) also start camera-live, so count only what changed during H3:
  # its Camera-on switch must have added exactly one start, and camera-live must be active.
  plugs="$(pi 'journalctl -b -k --no-pager | grep -c "idVendor=08ca, idProduct=2022"')"
  echo "INFO  camera-live starts this boot: $n (before H3: $starts_before, plug-ins: $plugs)"
  if [ "$n" = "$((starts_before + h3_live_starts))" ] && pi 'systemctl -q is-active camera-live'; then
    report "camera in live mode: camera-live active, H3 added exactly its own start" 0
  else
    report "camera in live mode: camera-live active, H3 added exactly its own start" 1 "$n starts, $starts_before before H3, $h3_live_starts expected from H3"
  fi
else
  [ "$had_camera" = 1 ] && report "camera came back after reboot" 1 "08ca:202x gone" ||
    report "camera plug-in starts camera-offload@ / camera-live once" skip "camera not plugged in"
fi

echo "$fails check(s) failed"
[ "$fails" = 0 ]
