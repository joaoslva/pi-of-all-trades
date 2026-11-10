# Sourced by verify.sh (uses pi, report, py, wait_for, HOST, SSH_OPTS). H7: password change,
# hint, system info, cpu_percent. A throwaway hub on a high port with its own hash, hint and
# state dir; the operator's real hash is only compared, never written or printed.
echo "== H7 account"
H7=http://127.0.0.1:18083
PW7="verify7-$RANDOM$RANDOM$RANDOM"; NEW7="changed7-$RANDOM$RANDOM$RANDOM"
T7="$(pi mktemp -d)"
h7_cleanup() { [ -n "$T7" ] && pi "kill \$(cat $T7/pid) 2>/dev/null; pkill -f 'while :; do :; done' 2>/dev/null; rm -rf $T7" 2>/dev/null; }
trap 'h7_cleanup; cleanup' EXIT
hint7() { api7 "-b $T7/j1 $H7/admin/api/hint"; }
api7() { pi "curl -s -m 20 $1"; }
st7() { pi "curl -s -o /dev/null -m 20 -w '%{http_code}' $1"; } # status code only
login7() { st7 "-c $T7/$2 --data-urlencode password=$1 $H7/admin/api/login"; }
chg7() { st7 "-b $T7/j1 -H 'Content-Type: application/json' -d '$1' $H7/admin/api/password"; }
echo "$PW7" | ssh "${SSH_OPTS[@]}" "$PI" "sudo -n /usr/local/bin/pihub passwd --file $T7/admin.hash" 2>&1 | grep -v '^$'
[ "$(pi "stat -c '%a %U' $T7/admin.hash")" = "600 joao" ] && report "pihub passwd writes mode 0600 owned by joao" 0 || report "pihub passwd writes mode 0600 owned by joao" 1 "$(pi "stat -c '%a %U' $T7/admin.hash")"
pi "setsid nohup /usr/local/bin/pihub --listen 127.0.0.1:18083 --hash-file $T7/admin.hash --state-dir $T7 --services-dir /etc/pihub/services.d >$T7/log 2>&1 </dev/null & echo \$! >$T7/pid"
wait_for 10 pi "curl -s -o /dev/null -m 2 $H7/"
if [ "$(login7 "$PW7" j1)" != 204 ]; then
  report "H7 throwaway hub starts and logs in" 1 "$(pi "tail -3 $T7/log")"
else
  login7 "$PW7" j2 >/dev/null
  [ "$(st7 "$H7/admin/api/hint")" = 401 ] && report "hint without a session gets 401" 0 || report "hint without a session gets 401" 1
  [ "$(hint7)" = '{"hint":""}' ] && report "hint is empty at first, readable with a session" 0 || report "hint is empty at first, readable with a session" 1
  [ "$(pi "curl -sI -m 5 -b $T7/j1 $H7/admin/api/hint | tr -d '\r' | grep -ci '^cache-control: no-store'")" = 1 ] && report "hint answers Cache-Control: no-store" 0 || report "hint answers Cache-Control: no-store" 1
  [ "$(chg7 "{\"current\":\"nope-$RANDOM\",\"new\":\"$NEW7\"}")" = 401 ] && report "password change with a wrong current gets 401" 0 || report "password change with a wrong current gets 401" 1
  [ "$(chg7 "{\"current\":\"$PW7\",\"new\":\"$NEW7\",\"hint\":\"x $NEW7 x\"}")" = 400 ] && report "hint containing the new password gets 400" 0 || report "hint containing the new password gets 400" 1
  c="$(chg7 "{\"current\":\"$PW7\",\"new\":\"$NEW7\",\"hint\":\"hint text\"}")"
  [ "$c" = 204 ] && report "password change with the right current gets 204" 0 || report "password change with the right current gets 204" 1 "got $c"
  [ "$(st7 "-b $T7/j1 $H7/admin/api/system")" = 200 ] && report "the caller's session survives the change" 0 || report "the caller's session survives the change" 1
  [ "$(st7 "-b $T7/j2 $H7/admin/api/system")" = 401 ] && report "another session is logged out by the change" 0 || report "another session is logged out by the change" 1
  sleep 61 # the rate limit (5 per minute) counted the attempts above
  [ "$(login7 "$PW7" j3)" = 401 ] && [ "$(login7 "$NEW7" j3)" = 204 ] && report "old password refused, new one logs in" 0 || report "old password refused, new one logs in" 1
  [ "$(hint7)" = '{"hint":"hint text"}' ] && report "hint round trip" 0 || report "hint round trip" 1 "$(hint7)"
  [ "$(pi "stat -c '%a' $T7/admin.hash $T7/password-hint.txt | tr '\n' ' '; ls -A $T7 | grep -c tmp")" = "600 600 0" ] && report "hash and hint are 0600, no temp files left" 0 || report "hash and hint are 0600, no temp files left" 1
  c="$(st7 "-b $T7/j1 -H 'Content-Type: application/json' -d '{\"current\":\"$NEW7\",\"new\":\"$NEW7-2\",\"hint\":\"\"}' $H7/admin/api/password")"
  [ "$c" = 204 ] && [ "$(hint7)" = '{"hint":""}' ] && report "empty hint clears it" 0 || report "empty hint clears it" 1 "$c"
  [ "$(pi "journalctl -u pihub --no-pager | grep -cF -e '$NEW7' -e '$PW7'; grep -cF -e '$NEW7' -e '$PW7' $T7/log")" = "$(printf '0\n0')" ] && report "no password text in journal or throwaway log" 0 || report "no password text in journal or throwaway log" 1

  # system info against what the Pi itself says
  sj="$(pi "curl -s -m 20 -c $T7/j4 --data-urlencode password=$NEW7-2 $H7/admin/api/login >/dev/null; curl -s -m 20 -b $T7/j4 $H7/admin/api/system")"
  ref="$(pi "echo \$(hostname) \$(uname -r) \$(ip -4 -o addr show scope global | awk '{print \$2\"=\"\$4}' | cut -d/ -f1 | tr '\n' ',')")"
  echo "INFO  system: $sj"
  if python3 - "$sj" "$ref" <<'PY'
import json, sys
s = json.loads(sys.argv[1]); host, kern, addrs = sys.argv[2].split()
mine = {f"{a['iface']}={a['ip']}" for a in s["addresses"]}
want = {x for x in addrs.split(",") if x}
ok = s["hostname"] == host and s["kernel"] == kern and want <= mine and "lo" not in {a["iface"] for a in s["addresses"]}
ok = ok and s["os"] and s["arch"] and "camera" in s and s["camera"]["total"] > 0 and s["boot_time"] < s["time"]
sys.exit(0 if ok else 1)
PY
  then report "system info matches hostname, uname -r, ip -4 addr" 0; else report "system info matches hostname, uname -r, ip -4 addr" 1 "$ref"; fi

  # cpu_percent: idle, then under 3 s of one busy core
  cj() { pi "curl -s -m 20 -b $T7/j4 $H7/admin/api/stats" | python3 -c 'import json,sys; print(json.load(sys.stdin)["cpu_percent"])'; }
  cj >/dev/null; sleep 2; idle="$(cj)"
  lp="$(pi "setsid sh -c 'while :; do :; done' >/dev/null 2>&1 </dev/null & echo \$!")"
  sleep 3; busy="$(cj)"; pi "kill $lp"
  echo "INFO  cpu_percent idle=$idle under one busy loop=$busy"
  python3 -c "import sys; i,b=map(float,sys.argv[1:]); sys.exit(0 if 0<=i<=100 and 0<=b<=100 and b>i else 1)" "$idle" "$busy" &&
    report "cpu_percent within 0-100, higher under load" 0 "$idle -> $busy" || report "cpu_percent within 0-100, higher under load" 1 "$idle / $busy"
  rss="$(pi "awk '/VmRSS/{print \$2}' /proc/\$(cat $T7/pid)/status")"
  echo "INFO  throwaway hub VmRSS after H7 checks: ${rss:-?} kB"
  [ -n "$rss" ] && [ "$rss" -lt 23000 ] && report "pihub under 23000 kB after H7" 0 "$rss kB" || report "pihub under 23000 kB after H7" 1 "${rss:-?} kB"
fi

# Production: the startup migration copied the operator's hash. Hashes are compared, never printed.
if pi 'test -e /etc/pihub/admin.hash'; then
  a="$(pi 'sha256sum </etc/pihub/admin.hash')"; b="$(pi 'sha256sum </var/lib/pihub/admin.hash 2>&1')"
  [ "$a" = "$b" ] && report "production hash migrated to /var/lib/pihub, content identical" 0 || report "production hash migrated to /var/lib/pihub, content identical" 1 "differs or missing"
  m="$(pi "stat -c '%a %U' /var/lib/pihub/admin.hash; stat -c '%a %U:%G' /etc/pihub/admin.hash" | tr '\n' ' ')"
  [ "$m" = "600 joao 640 root:joao " ] && report "new hash 0600 joao, old hash untouched (0640 root:joao)" 0 || report "new hash 0600 joao, old hash untouched (0640 root:joao)" 1 "$m"
  [ "$(code "http://$HOST/admin")" = 200 ] && report "production /admin still answers 200" 0 || report "production /admin still answers 200" 1
else
  report "production hash migration" skip "no /etc/pihub/admin.hash"
fi
