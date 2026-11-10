#!/usr/bin/env bash
#
# Build and deploy the app component, then check offload and wipe on the real Pi.
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
