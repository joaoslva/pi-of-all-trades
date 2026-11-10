#!/usr/bin/env bash
#
# Push files onto the Raspberry Pi. Each component owns a rootfs/ tree that
# mirrors the Pi's /:
#
#   platform/rootfs/usr/local/bin/foo  ->  /usr/local/bin/foo
#   app/rootfs/etc/udev/...            ->  /etc/udev/...
#
# Usage:
#   platform/deploy.sh [--list|--check] [--no-reload] [component ...]
#
#   component    "platform", "app" or "hub"; default: all three
#   --list       print what each component ships, touch nothing
#   --check      show what differs on the Pi, change nothing
#   --no-reload  install without reloading systemd or udev, or restarting
#                services
#
# Running services whose unit file or executable changed are restarted, so a
# deploy is live when this returns. Oneshots are not: they run the new file
# next time, and restarting one could cut an offload short.
#
#   PI=joao@10.42.0.1 platform/deploy.sh

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PI="${PI:-joao@192.168.1.206}"
KEY="${KEY:-$HOME/.ssh/pi_camera_drive}"
SSH_OPTS=(-i "$KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)

die() { printf 'error: %b\n' "$1" >&2; exit 1; }

MODE=install
RELOAD=1
components=()
for arg in "$@"; do
  case "$arg" in
    --list)      MODE=list ;;
    --check)     MODE=check ;;
    --no-reload) RELOAD=0 ;;
    -h|--help)   sed -n '2,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*)          die "unknown option $arg" ;;
    *)           components+=("$arg") ;;
  esac
done

rootfs_of() { echo "$REPO/$1/rootfs"; }

# Paths relative to a rootfs, as they will appear on the Pi.
files_in() {
  (cd "$1" && find . -name __pycache__ -prune -o -type f -print | sed 's|^\.||' | sort)
}

if [ ${#components[@]} -eq 0 ]; then
  components=(platform app hub)
fi

roots=()
for c in "${components[@]}"; do
  r="$(rootfs_of "$c")"
  [ -d "$r" ] || die "component '$c' has no rootfs ($r)"
  roots+=("$r")
done

# The hub binary is built, not committed.
for c in "${components[@]}"; do
  [ "$c" != hub ] || [ -f "$REPO/hub/rootfs/usr/local/bin/pihub" ] ||
    die "hub binary missing: run 'make -C hub build' first"
done

for c in "${components[@]}"; do
  [ "$c" != app ] || [ -f "$REPO/app/rootfs/usr/local/bin/camera-drive" ] ||
    die "app binary missing: run 'make -C app build' first"
done

# Only regular files: the install step below does not handle symlinks,
# sockets or anything else, so refuse them rather than half-deploy.
odd="$(find "${roots[@]}" -name __pycache__ -prune -o ! -type f ! -type d -print)"
[ -z "$odd" ] || die "only regular files can be deployed:\n$odd"

# A path shipped by two components would be silently won by whichever copied last.
dups="$(for r in "${roots[@]}"; do files_in "$r"; done | sort | uniq -d)"
[ -z "$dups" ] || die "paths shipped by more than one component:\n$dups"

if [ "$MODE" = list ]; then
  for i in "${!components[@]}"; do
    echo "== ${components[$i]}"
    files_in "${roots[$i]}" | sed 's/^/  /'
  done
  exit 0
fi

echo "==> target: $PI ($MODE: ${components[*]})"
STAGE="$(ssh "${SSH_OPTS[@]}" "$PI" mktemp -d /tmp/camera-deploy.XXXXXX)" || die "cannot reach $PI"
trap 'ssh "${SSH_OPTS[@]}" "$PI" rm -rf "$STAGE" || true' EXIT

srcs=()
for r in "${roots[@]}"; do srcs+=("$r/"); done
rsync -rlpt --exclude=__pycache__ -e "ssh $(printf '%q ' "${SSH_OPTS[@]}")" "${srcs[@]}" "$PI:$STAGE/"

ssh "${SSH_OPTS[@]}" "$PI" sudo -n bash -s -- "$STAGE" "$MODE" "$RELOAD" <<'REMOTE'
set -euo pipefail
S=$1 MODE=$2 RELOAD=$3

# An old deploy left /, /etc and /usr owned by joao and group-writable, which
# hands root to anything running as that user. Every directory above a
# deployed file must be owned by root and not writable by group or others.
declare -A seen=()
dirs=0
check_parents() {
  local d was
  d="$(dirname "$1")"
  while :; do
    if [ -z "${seen[$d]+x}" ]; then
      seen[$d]=1
      if [ -d "$d" ] && [ -n "$(find "$d" -maxdepth 0 \( ! -user root -o -perm /022 ! -perm -1000 \) -print)" ]; then
        was="$(stat -c '%a %U:%G' "$d")"
        if [ "$MODE" = check ]; then
          printf '  unsafe dir %-52s %s\n' "$d" "$was"
        else
          [ "$(stat -c %U "$d")" = root ] || chown root:root "$d"
          chmod go-w "$d"
          printf '  fixed dir  %-52s was %s\n' "$d" "$was"
        fi
        dirs=$((dirs + 1))
      fi
    fi
    [ "$d" = / ] && break
    d="$(dirname "$d")"
  done
}

changed=()
while IFS= read -r -d '' f; do
  dest="${f#"$S"}"
  check_parents "$dest"
  if [ -x "$f" ]; then want=755; else want=644; fi

  if [ ! -e "$dest" ]; then
    why=new
  elif ! cmp -s "$f" "$dest"; then
    why=content
  elif [ "$(stat -c '%a %U:%G' "$dest")" != "$want root:root" ]; then
    why="was $(stat -c '%a %U:%G' "$dest")"
  else
    continue
  fi

  if [ "$MODE" = check ]; then
    printf '  differs    %-52s %s\n' "$dest" "$why"
  else
    # install -D creates missing parents (root, 0755) and leaves existing
    # directories alone; it replaces the file rather than writing into it,
    # so a running binary is safe to update.
    install -D -o root -g root -m "$want" "$f" "$dest"
    printf '  installed  %-52s %s\n' "$dest" "$why"
  fi
  changed+=("$dest")
done < <(find "$S" -type f -print0 | sort -z)

[ ${#changed[@]} -gt 0 ] || [ "$dirs" -gt 0 ] || { echo "  everything up to date"; exit 0; }
[ "$MODE" = install ] && [ "$RELOAD" = 1 ] && [ ${#changed[@]} -gt 0 ] || exit 0

if printf '%s\n' "${changed[@]}" | grep -q '^/etc/systemd/'; then
  systemctl daemon-reload
  echo "==> systemd reloaded"
fi

for unit in /etc/systemd/system/*.service; do
  name="$(basename "$unit")"
  case "$name" in *@.service) continue ;; esac  # templates are udev-started oneshots
  [ "$(systemctl show -p Type --value "$name")" = oneshot ] && continue
  systemctl -q is-active "$name" || continue
  # ExecStart reads "{ path=/usr/local/bin/foo ; argv[]=... }".
  exe="$(systemctl show -p ExecStart --value "$name" | sed -n 's/.*path=\([^ ;]*\).*/\1/p')"
  hit=0
  for c in "${changed[@]}"; do
    if [ "$c" = "$unit" ] || [ "$c" = "$exe" ]; then hit=1; fi
  done
  [ "$hit" = 1 ] || continue
  # Restarting camera-live ends a recording. Leave it for later instead.
  if [ "$name" = camera-live.service ] &&
     grep -Eq '"recording": *true' /srv/camera-drive/recstatus.json 2>/dev/null; then
    echo "==> NOT restarting $name: a recording is in progress. Restart it afterwards."
    continue
  fi
  systemctl restart "$name"
  echo "==> restarted $name"
done
if printf '%s\n' "${changed[@]}" | grep -q '^/etc/udev/'; then
  udevadm control --reload
  # Re-run add rules for devices already plugged in, e.g. a camera in storage mode.
  udevadm trigger --subsystem-match=block --action=add >/dev/null 2>&1 || true
  echo "==> udev rules reloaded"
fi
REMOTE

echo "==> done"
