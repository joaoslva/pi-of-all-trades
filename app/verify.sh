#!/usr/bin/env bash
#
# Build and deploy the app component, then check offload, wipe, live and the web read side on the real Pi.
# One PASS/FAIL/SKIP line per check; exits non-zero on any FAIL. Never reboots.
#
# Offload and wipe are exercised on a loop-mounted FAT test image under a temp
# dir, side by side with the Python originals (read from git, paths rewritten
# in a copy). The real camera is only ever looked at read-only: nothing here
# mounts /dev/sd*, writes /srv/camera-drive, or runs wipe against it.
#
#   app/verify.sh                    # PI=joao@192.168.1.206 by default
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PI="${PI:-joao@192.168.1.206}"
KEY="${KEY:-$HOME/.ssh/pi_camera_drive}"
PYREF="${PYREF:-65c643e}" # last commit that still has the Python camera-offload and camera-wipe
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

# One run at a time, for the whole run: the lock lives while this pipe is open.
coproc LOCK { ssh "${SSH_OPTS[@]}" "$PI" 'sudo -n flock -n /run/pihub-verify.lock sh -c "echo locked; cat >/dev/null" || echo busy'; }
read -r -t 20 lock <&"${LOCK[0]}"
[ "$lock" = locked ] || { echo "FAIL  lock (another verify is running, or the Pi is unreachable)"; exit 1; }

echo "== unit tests"
make -C "$REPO/app" test >/dev/null && report "go vet and go test" 0 || { report "go vet and go test" 1; exit 1; }
make -C "$REPO/app" build || { echo "FAIL  build"; exit 1; }
echo "== deploy --check (before)"; "$REPO/platform/deploy.sh" --check app || { echo "FAIL  deploy --check"; exit 1; }
echo "== deploy"; "$REPO/platform/deploy.sh" app || { echo "FAIL  deploy"; exit 1; }
echo "== deploy --check (after)"; "$REPO/platform/deploy.sh" --check app
echo "== checks"

# ---- real camera: read-only -------------------------------------------------
unit="$(pi 'systemctl cat camera-offload@.service')"
grep -q '^ExecStart=/usr/local/bin/camera-drive offload /dev/%I$' <<<"$unit" &&
  report "camera-offload@.service runs camera-drive offload /dev/%I" 0 ||
  report "camera-offload@.service runs camera-drive offload /dev/%I" 1
if pi lsusb | grep -q '08ca:2023'; then
  n="$(pi 'journalctl -b -u "camera-offload@*" --no-pager | grep -c "Starting camera-offload@"')"
  [ "$n" = 1 ] && report "real camera (storage): camera-offload@ ran once this boot" 0 ||
    report "real camera (storage): camera-offload@ ran once this boot" 1 "ran $n times"
  pi 'findmnt -n /dev/sda1' | grep -q . &&
    report "real camera is not left mounted" 1 || report "real camera is not left mounted" 0
else
  report "real camera: camera-offload@ ran once this boot" skip "camera not in storage mode"
fi

# ---- camera-live (C2) -----------------------------------------------------------
unit="$(pi 'systemctl cat camera-live.service')"
grep -q '^ExecStart=/usr/local/bin/camera-drive live$' <<<"$unit" &&
  report "camera-live.service runs camera-drive live" 0 || report "camera-live.service runs camera-drive live" 1
grep -q '^KillMode=mixed$' <<<"$unit" && report "camera-live.service keeps KillMode=mixed" 0 ||
  report "camera-live.service keeps KillMode=mixed" 1

# No camera node: exit 1 with the log line the Python gave. An empty -dev-dir and temp dirs,
# so this passes whether or not the camera is plugged in and never touches /dev/video0.
nodev="$(pi 'T=$(mktemp -d /var/tmp/c2-nodev.XXXXXX); /usr/local/bin/camera-drive live -run-dir $T/run -root $T/srv -dev-dir $T/dev -detect-interval 100ms; echo "rc=$?"; rm -rf $T' 2>&1)"
grep -q 'no camera video device found' <<<"$nodev" && grep -q '^rc=1$' <<<"$nodev" &&
  report "live with no camera device exits 1 and logs 'no camera video device found'" 0 ||
  report "live with no camera device exits 1 and logs 'no camera video device found'" 1 "$nodev"

# Real camera in live mode. These record into /srv/camera-drive/video and snapshot into media/snapshots
# (each check removes only the files it made), and the SIGTERM check stops and restarts the production
# camera-live. So they only run when asked: REAL_CAMERA=1 app/verify.sh
if ! pi lsusb | grep -q '08ca:2022'; then
  for n in "preview reaches live.jpg" "snapshot appears" "three record cycles give three valid files" "SIGTERM mid-recording leaves a valid file"; do
    report "real camera (live): $n" skip "camera not in live mode (08ca:2022 absent)"
  done
elif [ "${REAL_CAMERA:-}" != 1 ]; then
  report "real camera (live): preview, snapshot, record cycles, SIGTERM" skip "camera is in live mode; set REAL_CAMERA=1 to record with it"
else
  out2="$(mktemp)"
  ssh "${SSH_OPTS[@]}" "$PI" bash -s <<'REMOTE' | tee "$out2"
set -u
R=/run/camera-drive; S=/srv/camera-drive
report() { case "$2" in 0) echo "PASS  $1${3:+ ($3)}";; *) echo "FAIL  $1${3:+ ($3)}";; esac; }
ctl() { python3 -c 'import json,os,sys;p="/run/camera-drive/control.json";d=json.load(open(p));d[sys.argv[1]]=json.loads(sys.argv[2]);open(p+".tmp","w").write(json.dumps(d));os.replace(p+".tmp",p)' "$1" "$2" </dev/null; }
rs() { python3 -c 'import json,sys;print(json.load(open("/srv/camera-drive/recstatus.json")).get(sys.argv[1]))' "$1" </dev/null; }
until_() { local n=$1; shift; for _ in $(seq "$n"); do "$@" && return 0; sleep 0.5; done; return 1; }
jpg_ok() { [ "$(stat -c %s $R/live.jpg 2>/dev/null || echo 0)" -ge 1024 ] && [ "$(tail -c2 $R/live.jpg | od -An -tx1 | tr -d ' \n')" = ffd9 ]; }
recording() { [ "$(rs recording)" = True ] && [ "$(rs file)" != None ]; }
stopped() { [ "$(rs recording)" = False ]; }
valid() { d=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$1" </dev/null 2>/dev/null); [ -n "$d" ] && awk "BEGIN{exit !($d > 0)}"; }
made=()
trap 'for f in "${made[@]}"; do rm -f "$f"; done; ctl preview false; ctl recording false' EXIT

until_ 20 pgrep -f 'camera-drive live' >/dev/null || { report "camera-drive live is running" 1; exit 0; }
ctl preview true
until_ 40 jpg_ok && report "real camera (live): preview reaches live.jpg" 0 || report "real camera (live): preview reaches live.jpg" 1

prev=$(rs last_snapshot); ctl snapshot_seq "$(python3 -c 'import json;print(json.load(open("/run/camera-drive/control.json"))["snapshot_seq"]+1)' </dev/null)"
snap_new() { [ "$(rs last_snapshot)" != "$prev" ] && [ "$(rs last_snapshot)" != None ]; }
if until_ 20 snap_new; then
  f=$S/media/snapshots/$(rs last_snapshot); made+=("$f")
  [ "$(head -c2 "$f" | od -An -tx1 | tr -d ' \n')" = ffd8 ] && report "real camera (live): snapshot appears" 0 "$(basename "$f")" || report "real camera (live): snapshot appears" 1 "not a JPEG"
else report "real camera (live): snapshot appears" 1; fi

ok=0
for i in 1 2 3; do
  ctl recording true
  until_ 60 recording || { echo "INFO  cycle $i never started recording"; continue; }
  f=$S/video/$(rs file); made+=("$f"); sleep 5
  ctl recording false
  until_ 80 stopped; sleep 1
  valid "$f" && ok=$((ok + 1)) || echo "INFO  cycle $i: $f not valid"
done
[ $ok = 3 ] && report "real camera (live): three record cycles give three valid files" 0 || report "real camera (live): three record cycles give three valid files" 1 "$ok of 3"

ctl recording true
if until_ 60 recording; then
  f=$S/video/$(rs file); made+=("$f"); sleep 4
  sudo -n systemctl kill --kill-whom=main -s TERM camera-live
  until_ 120 bash -c '! systemctl is-active --quiet camera-live'
  valid "$f" && report "real camera (live): SIGTERM mid-recording leaves a valid file" 0 || report "real camera (live): SIGTERM mid-recording leaves a valid file" 1
  [ "$(rs state)" = off ] && report "real camera (live): recstatus ends off after SIGTERM" 0 || report "real camera (live): recstatus ends off after SIGTERM" 1 "$(rs state)"
  sudo -n systemctl start camera-live
else report "real camera (live): SIGTERM mid-recording leaves a valid file" 1 "recording never started"; fi
REMOTE
  fails=$((fails + $(grep -c '^FAIL' "$out2")))
  rm -f "$out2"
fi

# ---- web read side (C3): Go on :8082 beside the production Python on :8081 -----------
# Read-only against /srv/camera-drive (thumbs are made in a scratch root). Never calls /api/wipe,
# /api/power or an export route except to see the 501 stub. The only state touched is
# control.json (preview on, then back to all-false) and only when the camera is in live mode.
out3="$(mktemp)"
ssh "${SSH_OPTS[@]}" "$PI" bash -s <<'REMOTE' | tee "$out3"
set -u
GO=/usr/local/bin/camera-drive
T=$(mktemp -d /var/tmp/c3-verify.XXXXXX)
G=http://127.0.0.1:8082; P=http://127.0.0.1:8081
GOPID=""
report() { case "$2" in 0) echo "PASS  $1${3:+ ($3)}";; skip) echo "SKIP  $1${3:+ ($3)}";; *) echo "FAIL  $1${3:+ ($3)}";; esac; }
check() { local n=$1; shift; if "$@"; then report "$n" 0; else report "$n" 1; fi; }
startgo() { # startgo [extra flags]: Go web on :8082 as joao, pid recorded
  nohup $GO web --listen 127.0.0.1:8082 "$@" >$T/go.log 2>&1 & GOPID=$!
  for _ in $(seq 50); do curl -s -o /dev/null $G/api/live.jpg && return 0; sleep 0.1; done; return 1
}
stopgo() { [ -n "$GOPID" ] && kill $GOPID 2>/dev/null; wait $GOPID 2>/dev/null; GOPID=""; }
ctl() { curl -s -X POST -H 'Content-Type: application/json' -d "$1" $G/api/control >/dev/null; }
cleanup() { [ -n "$GOPID" ] && { ctl '{"preview":false,"recording":false}'; kill $GOPID 2>/dev/null; }; rm -rf $T; }
trap cleanup EXIT
rss() { awk '/^VmRSS/{print $2}' /proc/$1/status; }
hwm() { awk '/^VmHWM/{print $2}' /proc/$1/status; }
ticks() { awk '{print $14+$15}' /proc/$1/stat; }
PYPID=$(systemctl show -p MainPID --value camera-drive-web)
frames() { grep -ac '^--frame' "$1"; }

cat > $T/a.py <<'PY'
import hashlib, http.client, io, json, struct, sys, zipfile, urllib.parse
G, P, TH = sys.argv[1:4]
def hp(base):
    u = urllib.parse.urlparse(base); return u.hostname, u.port
def req(base, method, path, body=None, headers=None):
    c = http.client.HTTPConnection(*hp(base), timeout=60)
    c.putrequest(method, path, skip_accept_encoding=True)  # path sent as-is, no client-side cleaning
    for k, v in (headers or {}).items(): c.putheader(k, v)
    if body is not None: c.putheader("Content-Length", str(len(body)))
    c.endheaders(body)
    r = c.getresponse(); data = r.read(); h = {k.lower(): v for k, v in r.getheaders()}
    c.close(); return r.status, h, data
def rep(name, ok, detail=""): print(("PASS  " if ok else "FAIL  ") + name + (f" ({detail})" if detail else ""), flush=True)

# state
VARY = {"disk"}   # used/free/pct move with every write; nothing else is allowed to differ
def state(b): return json.loads(req(b, "GET", "/api/state")[2])
a, b = state(G), state(P)
for _ in range(2):
    if {k: v for k, v in a.items() if k not in VARY} == {k: v for k, v in b.items() if k not in VARY}: break
    a, b = state(G), state(P)
same = json.dumps({k: v for k, v in a.items() if k not in VARY}, sort_keys=True) == json.dumps({k: v for k, v in b.items() if k not in VARY}, sort_keys=True)
rep("web: /api/state equal to Python (sorted keys, bar disk)", same,
    f"{len(a['dumps'])} dumps, {len(a['captures'])} days, held={a['held']}, camera={a['camera']}")
rep("web: disk keys and total agree", set(a["disk"]) == set(b["disk"]) and a["disk"]["total"] == b["disk"]["total"])
if not same:
    for k in a:
        if k not in VARY and a[k] != b[k]: print("INFO  differs:", k, str(a[k])[:300], "|", str(b[k])[:300])

imgs = [i["path"] for d in a["dumps"] for i in d["items"] if i["kind"] == "image"]
vids = [i["path"] for d in a["dumps"] for i in d["items"] if i["kind"] == "video"] + [i["path"] for g in a["captures"] for i in g["items"] if i["kind"] == "video"]
q = lambda p: "/media/" + urllib.parse.quote(p)

# media ranges, image and video
for label, rel in (("image", imgs[0]), ("video", vids[-1])):
    ra, rb = (req(x, "GET", q(rel), headers={"Range": "bytes=100-4195"}) for x in (G, P))
    keys = ["content-range", "content-length", "content-type", "accept-ranges", "content-disposition"]
    ha, hb = ({k: h.get(k) for k in keys} for h in (ra[1], rb[1]))
    rep(f"web: /media {label} Range: identical 206 status, headers and body", ra[0] == rb[0] == 206 and ha == hb and ra[2] == rb[2],
        f"{rel} {ha}" if ha != hb or ra[0] != 206 else f"{ra[0]} {ha['content-range']} sha {hashlib.sha256(ra[2]).hexdigest()[:12]}")
ra, rb = (req(x, "GET", q(vids[-1]) + "?dl") for x in (G, P))
rep("web: /media ?dl full body identical", ra[0] == rb[0] == 200 and ra[2] == rb[2] and ra[1].get("content-disposition") == rb[1].get("content-disposition"))

# zip of 10 files
form = "&".join("path=" + urllib.parse.quote(p) for p in imgs[:10]).encode()
zs = []
for x in (G, P):
    st, h, data = req(x, "POST", "/api/zip", form, {"Content-Type": "application/x-www-form-urlencoded"})
    zf = zipfile.ZipFile(io.BytesIO(data)); zs.append((st, h.get("content-disposition", "")[:34], sorted((i.filename, i.file_size, i.compress_type) for i in zf.infolist()), zf.testzip()))
rep("web: zip of 10 files lists identical names and sizes (stored, testzip ok)", len(zs[0][2]) == 10 and zs[0][2] == zs[1][2] and zs[0][0] == zs[1][0] == 200 and zs[0][3] is None and zs[0][1].startswith("attachment; filename=camera-drive_"),
    f"{len(zs[0][2])} entries")

# thumbnails (scratch root, so nothing is written under /srv/camera-drive)
def jpeg_width(d):
    if d[:2] != b"\xff\xd8": return None
    i = 2
    while i + 4 <= len(d):
        if d[i] != 0xff: return None
        m, ln = d[i+1], struct.unpack(">H", d[i+2:i+4])[0]
        if m in (0xc0, 0xc1, 0xc2): return struct.unpack(">H", d[i+7:i+9])[0]
        i += 2 + ln
for label, rel in (("image", imgs[0]), ("video", vids[-1])):
    st, h, data = req(TH, "GET", "/thumb/" + urllib.parse.quote(rel))
    w = jpeg_width(data)
    rep(f"web: thumbnail of one {label} is a JPEG of width 320", st == 200 and w == 320 and h.get("cache-control") == "max-age=86400", f"{rel} status={st} width={w}")

# 501 stubs, absent power route, page
for m, p in (("POST", "/api/export"), ("GET", "/api/mp4/" + vids[-1]), ("GET", "/api/export/x/file")):
    st, h, data = req(G, m, p, b"{}" if m == "POST" else None, {"Content-Type": "application/json"})
    rep(f"web: {m} {p.split('/')[1]}/{p.split('/')[2]} is the 501 stub", st == 501 and json.loads(data) == {"ok": False, "error": "export lands in C4"})
rep("web: no /api/power route (POST and GET 404)", req(G, "POST", "/api/power", b"")[0] == 404 and req(G, "GET", "/api/power")[0] == 404)
st, h, page = req(G, "GET", "/")
rep("web: / serves the page without a power button or __ICON__ placeholder", st == 200 and b'id="power"' not in page and b"/api/power" not in page and b"__ICON" not in page and h["content-type"].startswith("text/html"))

# traversal over HTTP
bad = ["/media/../../etc/passwd", "/media/%2e%2e/%2e%2e/etc/passwd", "/media/%2e%2e%2f%2e%2e%2fetc/passwd", "/media/%252e%252e/etc/passwd",
       "/media//etc/passwd", "/media/%2fetc/passwd", "/media/media/%00/x", "/media/media/..%00/x", "/thumb/../../etc/passwd", "/thumb/%2e%2e/etc/passwd",
       "/api/play/../../etc/passwd", "/api/play/%2e%2e/%2e%2e/etc/passwd", "/media/etc/passwd", "/media/media", "/media/media/", "/media/video/",
       "/media/media/..", "/media/media/snapshots/../../index.json", "/thumb/index.json", "/media/index.json", "/media/proxy/x.mp4", "/media/thumbs/x.jpg"]
codes = {p: req(G, "GET", p)[0] for p in bad}
codes["POST /api/zip ../index.json"] = req(G, "POST", "/api/zip", b"path=../index.json", {"Content-Type": "application/x-www-form-urlencoded"})[0]
codes["POST /api/zip media/../index.json"] = req(G, "POST", "/api/zip", b"path=media/../index.json", {"Content-Type": "application/x-www-form-urlencoded"})[0]
rep("web: all traversal attempts over HTTP are 404", all(c == 404 for c in codes.values()), f"{len(codes)} attempts" if all(c == 404 for c in codes.values()) else str({k: v for k, v in codes.items() if v != 404}))
PY

cat > $T/loop.py <<'PY'
# The same request mix against one server: state, ranges, zips, cached thumbnails.
import http.client, json, sys, urllib.parse
base = urllib.parse.urlparse(sys.argv[1]); n = int(sys.argv[2])
def req(m, p, body=None, h=None):
    c = http.client.HTTPConnection(base.hostname, base.port, timeout=120)
    c.request(m, p, body, h or {}); r = c.getresponse(); d = r.read(); c.close(); return r.status, d
st = json.loads(req("GET", "/api/state")[1])
imgs = [i["path"] for d in st["dumps"] for i in d["items"] if i["kind"] == "image"]
for i in range(n):
    req("GET", "/api/state")
    p = imgs[i % len(imgs)]
    req("GET", "/media/" + urllib.parse.quote(p), h={"Range": "bytes=0-65535"})
    req("GET", "/thumb/" + urllib.parse.quote(p))
    if i % 10 == 0:
        req("POST", "/api/zip", "&".join("path=" + urllib.parse.quote(x) for x in imgs[i % 20:i % 20 + 10]).encode(), {"Content-Type": "application/x-www-form-urlencoded"})
PY

# ---- group A: HTTP behaviour, no camera needed ----
mkdir -p $T/root; ln -s /srv/camera-drive/media $T/root/media; ln -s /srv/camera-drive/video $T/root/video
nohup $GO web --listen 127.0.0.1:8083 --root $T/root >$T/go3.log 2>&1 & TPID=$!
if startgo; then
  sleep 0.5
  python3 $T/a.py $G $P http://127.0.0.1:8083
else report "web: Go app starts on :8082" 1 "$(cat $T/go.log)"; fi
kill $TPID 2>/dev/null; wait $TPID 2>/dev/null
check "web: unit file untouched, Python still serves :8081" bash -c "curl -s -o /dev/null -w '%{http_code}' $P/api/state | grep -q 200 && systemctl cat camera-drive-web | grep -q camera-drive-web"

# /api/play: multipart frames, ffmpeg gone after the client leaves
vid=video/2026-08-17_175004.mkv
curl -s --max-time 3 -o $T/play.out $G/api/play/$vid; n=$(frames $T/play.out)
[ "$n" -ge 5 ] && report "web: /api/play streams multipart frames" 0 "$n frames in 3 s of $vid" || report "web: /api/play streams multipart frames" 1 "$n frames"
curl -s -o /dev/null $G/api/play/$vid & CP=$!; sleep 2
pgrep -f 'image2pipe' >/dev/null && had=1 || had=0
kill $CP; wait $CP 2>/dev/null; gone=0
for _ in $(seq 30); do pgrep -f 'image2pipe' >/dev/null || { gone=1; break; }; sleep 0.1; done
[ $had = 1 ] && [ $gone = 1 ] && report "web: ffmpeg for /api/play is gone within 3 s of the client leaving" 0 || report "web: ffmpeg for /api/play is gone within 3 s of the client leaving" 1 "was running=$had gone=$gone"
[ "$(curl -s -o /dev/null -w '%{http_code}' $G/api/play/media/snapshots/SNAP_2026-08-17_183539.jpg)" = 400 ] && report "web: /api/play of a photo is 400" 0 || report "web: /api/play of a photo is 400" 1
stopgo

# ---- group B: live preview. Needs the camera in live mode with camera-live running ----
if lsusb | grep -q '08ca:2022' && systemctl is-active --quiet camera-live; then
  startgo || report "web: Go app starts on :8082" 1
  ctl '{"preview":true}'
  for _ in $(seq 60); do [ "$(stat -c %s /run/camera-drive/live.jpg 2>/dev/null || echo 0)" -ge 1024 ] && break; sleep 0.5; done
  if [ "$(stat -c %s /run/camera-drive/live.jpg 2>/dev/null || echo 0)" -lt 1024 ]; then
    report "web: live stream checks" 1 "preview never produced live.jpg"
  else
    [ "$(curl -s -o $T/one.jpg -w '%{http_code} %{content_type}' $G/api/live.jpg)" = "200 image/jpeg" ] && report "web: /api/live.jpg serves a frame" 0 || report "web: /api/live.jpg serves a frame" 1
    curl -sN --max-time 10 -o $T/s1 $G/api/stream; n=$(frames $T/s1)
    [ "$n" -ge 30 ] && report "web: /api/stream delivers frames for 10 s" 0 "$n frames" || report "web: /api/stream delivers frames for 10 s" 1 "$n frames"
    curl -sN -o /dev/null $G/api/stream & CP=$!; sleep 2
    est=$(ss -Htn state established '( sport = :8082 )' | wc -l)
    kill $CP; wait $CP 2>/dev/null; t0=$SECONDS; left=1
    for _ in $(seq 50); do [ "$(ss -Htn state established '( sport = :8082 )' | wc -l)" = 0 ] && { left=0; break; }; sleep 0.1; done
    [ "$est" -ge 1 ] && [ $left = 0 ] && report "web: stream connection closes within 5 s of the client leaving" 0 "$((SECONDS - t0)) s" || report "web: stream connection closes within 5 s of the client leaving" 1 "established=$est still_open=$left"

    cps=(); for i in $(seq 10); do curl -sN --max-time 10 -o $T/m$i $G/api/stream & cps+=($!); done; wait "${cps[@]}" 2>/dev/null
    counts=$(for i in $(seq 10); do frames $T/m$i; done | sort -n | tr '\n' ' ')
    min=$(echo $counts | awk '{print $1}'); max=$(echo $counts | awk '{print $NF}'); h=$(hwm $GOPID)
    echo "INFO  10 streams, frames each: $counts; Go peak RSS ${h} kB"
    [ "$h" -lt 61440 ] && report "web: 10 simultaneous streams keep the Go process under 60 MB" 0 "${h} kB" || report "web: 10 simultaneous streams keep the Go process under 60 MB" 1 "${h} kB"
    awk "BEGIN{exit !($min >= 20 && $min >= 0.7 * $max)}" && report "web: no one of 10 streams stalled" 0 "min $min max $max" || report "web: no one of 10 streams stalled" 1 "min $min max $max"

    # CPU of one 10 s stream, Go then Python, median of 3 (utime+stime ticks of the server process, 100 per second)
    gts=(); pts=()
    for _ in 1 2 3; do
      g0=$(ticks $GOPID); curl -sN --max-time 10 -o /dev/null $G/api/stream; gts+=($(($(ticks $GOPID) - g0)))
      p0=$(ticks $PYPID); curl -sN --max-time 10 -o /dev/null $P/api/stream; pts+=($(($(ticks $PYPID) - p0)))
    done
    gt=$((${gts[0]} + ${gts[1]} + ${gts[2]})); pt=$((${pts[0]} + ${pts[1]} + ${pts[2]}))
    echo "INFO  one 10 s stream, ticks per trial: Go ${gts[*]}, Python ${pts[*]}"
    # Ticks are 10 ms and trials wander by +-3, so "no higher" is read as: within 10% plus 2 ticks of Python's total.
    awk "BEGIN{exit !($gt <= 1.1 * $pt + 2)}" && report "web: CPU during one stream no higher than Python's (sum of 3, 10% noise margin)" 0 "go $gt vs python $pt ticks" || report "web: CPU during one stream no higher than Python's (sum of 3, 10% noise margin)" 1 "go $gt vs python $pt ticks"
  fi
  ctl '{"preview":false,"recording":false}'
  sleep 1
  ctl_state=$(python3 -c 'import json;d=json.load(open("/run/camera-drive/control.json"));print(d["preview"],d["recording"])')
  [ "$ctl_state" = "False False" ] && report "web: control.json restored to preview=false recording=false" 0 || report "web: control.json restored to preview=false recording=false" 1 "$ctl_state"
  stopgo
else
  for n in "/api/live.jpg, /api/stream frames, close after leave, 10 streams, CPU"; do report "web: $n" skip "camera not in live mode or camera-live not running"; done
fi

# ---- group C: memory after the same request loop, Go then Python ----
startgo && {
  python3 $T/loop.py $G 150; gr=$(rss $GOPID)
  python3 $T/loop.py $P 150; pr=$(rss $PYPID)
  echo "INFO  after 150 rounds: Go RSS ${gr} kB, Python RSS ${pr} kB"
  [ $((gr * 2)) -le "$pr" ] && report "web: Go RSS at most half of Python's after the same request loop" 0 "go ${gr} kB, python ${pr} kB" || report "web: Go RSS at most half of Python's after the same request loop" 1 "go ${gr} kB, python ${pr} kB"
}
stopgo
REMOTE
fails=$((fails + $(grep -c '^FAIL' "$out3")))
rm -f "$out3"

# ---- loop-image tests, run as root on the Pi ----------------------------------
W="$(pi mktemp -d /var/tmp/c1-ref.XXXXXX)" || { echo "FAIL  cannot reach Pi"; exit 1; }
git -C "$REPO" show "$PYREF:app/rootfs/usr/local/bin/camera-offload" | ssh "${SSH_OPTS[@]}" "$PI" "cat > $W/offload.py"
git -C "$REPO" show "$PYREF:app/rootfs/usr/local/bin/camera-wipe" | ssh "${SSH_OPTS[@]}" "$PI" "cat > $W/wipe.py"

out="$(mktemp)"
ssh "${SSH_OPTS[@]}" "$PI" sudo -n bash -s -- "$W" <<'REMOTE' | tee "$out"
set -u
W=$1
GO=/usr/local/bin/camera-drive
T=$(mktemp -d /var/tmp/c1-verify.XXXXXX)
LOOPS=()
RULES=/run/udev/rules.d/99-c1-verify.rules
cleanup() {
  findmnt -rn -o TARGET | grep "^$T/" | sort -r | xargs -r -n1 umount 2>/dev/null
  for l in "${LOOPS[@]}"; do losetup -d "$l" 2>/dev/null; done
  if [ -e "$RULES" ]; then rm -f "$RULES"; udevadm control --reload; fi
  rm -rf "$T" "$W"
}
trap cleanup EXIT
report() { case "$2" in 0) echo "PASS  $1${3:+ ($3)}";; skip) echo "SKIP  $1${3:+ ($3)}";; *) echo "FAIL  $1${3:+ ($3)}";; esac; }
check() { # check <name> <command...>
  local n=$1; shift
  if "$@"; then report "$n" 0; else report "$n" 1; fi
}
eq() { [ "$1" = "$2" ]; }
median() { sort -n | sed -n 2p; }

export T
mkdir -p "$T"/{py,go,go2,pf,pg}/run
for d in py go go2 pf pg; do mkdir -p "$T/$d/srv"; done

# --- the Python originals, with every path rewritten into $T ----------------
mkpy() { # mkpy <src> <dst> <tree>
  sed -e "s#Path(\"/srv/camera-drive\")#Path(\"$T/$3/srv\")#" \
      -e "s#Path(\"/run/camera-drive.lock\")#Path(\"$T/$3/lock\")#" \
      -e "s#Path(\"/run/camera-drive/mnt\")#Path(\"$T/$3/run/mnt\")#" \
      -e "s#Path(\"/run/camera-drive/wipe-mnt\")#Path(\"$T/$3/run/wipe-mnt\")#" \
      -e "s#glob(\"sd\[a-z\]\[0-9\]\")#glob(\"$4\")#" "$1" > "$2"
  ! grep -q '/srv/camera-drive\|/run/camera-drive\|sd\[a-z\]' "$2"
}
check "python offload copy has no production path left" mkpy $W/offload.py $T/off-py.py py x
check "python wipe copy has no production path or sd glob left" mkpy $W/wipe.py $T/wipe-py.py py loopX

# --- test images --------------------------------------------------------------
mkimg() { # mkimg <file> <MB>
  truncate -s "$2M" "$1" && mkfs.vfat "$1" >/dev/null
}
attach() { REPLY=$(losetup -f --show "$1") || return 1; LOOPS+=("$REPLY"); }
contains() { [[ $1 == *"$2"* ]]; }
rematch() { [[ $1 =~ $2 ]]; }
rw() { # rw <img> <command...>: loop-mount the image read-write at $T/seed and run
  local img=$1; shift
  attach "$img" || return 1; local l=$REPLY
  mkdir -p "$T/seed"; mount "$l" "$T/seed" || return 1
  "$@"; local rc=$?
  umount "$T/seed"; losetup -d "$l"; return $rc
}
seed() {
  d="$T/seed"; mkdir -p "$d/DCIM/100A" "$d/DCIM/101B" "$d/DCIM/a b" "$d/DCIM/a" "$d/System Volume Information"
  head -c 200000 /dev/urandom > "$d/DCIM/100A/IMG_0001.JPG"
  head -c 150000 /dev/urandom > "$d/DCIM/101B/IMG_0001.JPG"   # same name, other content
  head -c 300000 /dev/urandom > "$d/DCIM/100A/MOV_0002.AVI"
  head -c 90000  /dev/urandom > "$d/DCIM/100A/IMG_0003.JPG"
  head -c 70000  /dev/urandom > "$d/DCIM/a b/clash.jpg"      # sort order decides who gets _<hash8>
  head -c 60000  /dev/urandom > "$d/DCIM/a/clash.jpg"
  echo "not media" > "$d/DCIM/100A/readme.txt"
  head -c 5000 /dev/urandom > "$d/System Volume Information/IMG_7777.JPG"
}
plant() { head -c 40000 /dev/urandom > "$T/seed/DCIM/100A/NEWFILE.JPG"; }

mkimg "$T/cam.img" 32 && rw "$T/cam.img" seed
report "test image built" $?

# --- differential offload: Python vs Go on the same image -------------------
attach "$T/cam.img"; DEV=$REPLY
mkpy $W/offload.py $T/off-py.py py x
python3 $T/off-py.py "$DEV" >"$T/py.out" 2>&1; pyrc=$?
$GO offload --root $T/go/srv --run-dir $T/go/run --lock $T/go/lock "$DEV" >"$T/go.out" 2>&1; gorc=$?
check "offload exit codes match (python $pyrc, go $gorc)" eq "$pyrc" "$gorc"
check "offload left no mount behind" bash -c "! findmnt -rn $T/py/run/mnt && ! findmnt -rn $T/go/run/mnt"

norm() { sed -E 's#[0-9]{4}-[0-9]{2}-[0-9]{2}_[0-9]{6}#STAMP#g'; }
tree() { (cd "$1/media" && find . -type f -printf '%P %s %m %u:%g %T@\n' | norm | sort); }
index() { python3 -c 'import json,sys; [print(k,v) for k,v in sorted(json.load(open(sys.argv[1])).items())]' "$1/index.json" | norm; }
keys() { python3 -c 'import json,sys; print(sorted(json.load(open(sys.argv[1]))))' "$1/status.json"; }
vals() { python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); [d.pop(k) for k in ("updated","last_folder")]; print(sorted(d.items()))' "$1/status.json"; }
check "index.json identical (python vs go, stamp folder normalised)" eq "$(index $T/py/srv)" "$(index $T/go/srv)"
check "copied trees identical (names, sizes, modes, owners, mtimes)" eq "$(tree $T/py/srv)" "$(tree $T/go/srv)"
check "status.json has the same keys" eq "$(keys $T/py/srv)" "$(keys $T/go/srv)"
check "status.json has the same values (bar updated, last_folder)" eq "$(vals $T/py/srv)" "$(vals $T/go/srv)"
check "index has 6 entries, ignored dirs and .txt skipped" eq "$(index $T/go/srv | wc -l)" 6
check "name clash got a _<hash8> suffix" rematch "$(index $T/go/srv)" '_[0-9a-f]{8}\.jpg'

# second run: everything skipped, nothing changes
snap="$(tree $T/go/srv)"
$GO offload --root $T/go/srv --run-dir $T/go/run --lock $T/go/lock "$DEV" >"$T/go.out2" 2>&1
check "second offload skips everything" bash -c "grep -q 'copied 0, skipped 6 already held' $T/go.out2"
check "second offload changed no files" eq "$(tree $T/go/srv)" "$snap"

# --- two offloads at once: the second exits 0 and copies nothing ------------
exec 9>"$T/go2/lock"; flock -n 9
$GO offload --root $T/go2/srv --run-dir $T/go2/run --lock $T/go2/lock "$DEV" >"$T/go2.out" 2>&1; rc=$?
exec 9>&-
check "second instance exits 0 while the lock is held" eq "$rc" 0
check "second instance logged it and copied nothing" bash -c "grep -q 'another offload is already running' $T/go2.out && [ -z \"\$(ls $T/go2/srv/media)\" ] && [ ! -e $T/go2/srv/index.json ]"

# --- kill -9 mid-run, then re-run releases the mount ------------------------
mkimg "$T/big.img" 128 && rw "$T/big.img" bash -c 'for i in 1 2 3 4 5 6 7 8; do head -c 12M /dev/urandom > "$T/seed/P_000$i.JPG"; done'
attach "$T/big.img"; BIG=$REPLY
$GO offload --root $T/pg/srv --run-dir $T/pg/run --lock $T/pg/lock "$BIG" >/dev/null 2>&1 &
pid=$!
for _ in $(seq 400); do findmnt -rn $T/pg/run/mnt >/dev/null && break; sleep 0.05; done
kill -9 $pid; wait $pid 2>/dev/null
findmnt -rn $T/pg/run/mnt >/dev/null && echo "INFO  after kill -9 the read-only mount was left behind, as expected" || echo "INFO  offload finished before kill -9 landed"
$GO offload --root $T/pg/srv --run-dir $T/pg/run --lock $T/pg/lock "$BIG" >"$T/pg.out" 2>&1; rc=$?
check "re-run after kill -9 exits 0" eq "$rc" 0
check "re-run after kill -9 released the mount" bash -c "! findmnt -rn $T/pg/run/mnt"
check "re-run after kill -9 indexed all 8 files" eq "$(index $T/pg/srv | wc -l)" 8

# --- wipe, only ever on loop images -----------------------------------------
rw "$T/cam.img" plant
cp "$T/cam.img" "$T/cam-py.img"; cp "$T/cam.img" "$T/cam-go.img"
attach "$T/cam-py.img"; DPY=$REPLY; attach "$T/cam-go.img"; DGO=$REPLY
case "$DPY$DGO" in */dev/sd*) echo "FAIL  refusing: wipe target looks like a real disk"; exit 1;; esac
mkdir -p /run/udev/rules.d
for l in "$DPY" "$DGO"; do echo "KERNEL==\"${l#/dev/}\", ENV{ID_VENDOR_ID}=\"08ca\", ENV{ID_MODEL_ID}=\"2023\"" >> "$RULES"; done
udevadm control --reload
for l in "$DPY" "$DGO"; do udevadm trigger --action=change --name-match="$l"; done
udevadm settle
check "udev reports the camera ids for the test loop devices" bash -c "udevadm info -q property --name=$DGO | grep -q ID_VENDOR_ID=08ca && udevadm info -q property --name=$DPY | grep -q ID_MODEL_ID=2023"
mkpy $W/wipe.py $T/wipe-py.py py "${DPY#/dev/}"
gowipe() { $GO wipe "$@" --root $T/go/srv --run-dir $T/go/run --dev-glob "$DGO"; }

$GO wipe >/dev/null 2>&1; check "wipe without --dry-run/--commit exits 2" eq "$?" 2
pyd=$(python3 $T/wipe-py.py --dry-run | tail -n1); god=$(gowipe --dry-run | tail -n1)
check "wipe --dry-run JSON identical to python" eq "$pyd" "$god"
echo "INFO  go dry-run: $god"
check "dry-run kept only the unknown file" contains "$god" '"kept": ["NEWFILE.JPG"]'
pyc=$(python3 $T/wipe-py.py --commit | tail -n1); goc=$(gowipe --commit | tail -n1)
check "wipe --commit JSON identical to python" eq "$pyc" "$goc"
echo "INFO  go commit:  $goc"
left() { local l=$1; mkdir -p "$T/chk"; mount -o ro "$l" "$T/chk" && (cd "$T/chk" && find . -type f | sort); umount "$T/chk"; }
check "camera contents after commit identical (python vs go)" eq "$(left $DPY)" "$(left $DGO)"
check "after commit only the unknown file and non-media are left" eq "$(left $DGO | tr '\n' ' ')" "./DCIM/100A/NEWFILE.JPG ./DCIM/100A/readme.txt ./System Volume Information/IMG_7777.JPG "

# --- time and memory: ~100 MB image, three runs each ------------------------
cat > $T/rss.py <<'PY'
import resource, subprocess, sys, time
t = time.monotonic()
r = subprocess.run(sys.argv[1:], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
print("%.2f %d %d" % (time.monotonic() - t, resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss, r.returncode))
PY
mkpy $W/offload.py $T/off-pf.py pf x
pyr=(); gor=()
for i in 1 2 3; do
  rm -rf $T/pf $T/pg2; mkdir -p $T/pf/srv $T/pf/run $T/pg2/srv $T/pg2/run
  sync; echo 3 > /proc/sys/vm/drop_caches
  pyr+=("$(python3 $T/rss.py python3 $T/off-pf.py "$BIG")")
  sync; echo 3 > /proc/sys/vm/drop_caches
  gor+=("$(python3 $T/rss.py $GO offload --root $T/pg2/srv --run-dir $T/pg2/run --lock $T/pg2/lock "$BIG")")
done
echo "INFO  python (seconds, peak RSS kB, rc): ${pyr[*]}"
echo "INFO  go     (seconds, peak RSS kB, rc): ${gor[*]}"
pt=$(printf '%s\n' "${pyr[@]}" | awk '{print $1}' | median); gt=$(printf '%s\n' "${gor[@]}" | awk '{print $1}' | median)
pm=$(printf '%s\n' "${pyr[@]}" | awk '{print $2}' | median); gm=$(printf '%s\n' "${gor[@]}" | awk '{print $2}' | median)
echo "INFO  median time python ${pt}s go ${gt}s; median peak RSS python ${pm} kB go ${gm} kB"
check "go offload time <= 110% of python" awk "BEGIN{exit !($gt <= 1.10 * $pt)}"
check "go offload peak RSS <= python" awk "BEGIN{exit !($gm <= $pm)}"
REMOTE
nfail="$(grep -c '^FAIL' "$out")"
rm -f "$out"
echo "$((fails + nfail)) check(s) failed"
[ $((fails + nfail)) = 0 ]
