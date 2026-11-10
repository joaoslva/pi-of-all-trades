#!/usr/bin/env bash
#
# Retire the Python camera app on the Pi: remove the old script files and purge
# python3-flask, python3-waitress, python3-pil plus whatever only they pulled in.
# Debian's own python3 stays.
#
#   ssh -i ~/.ssh/pi_camera_drive joao@192.168.1.206 'bash -s -- --check' < platform/provision/retire-python.sh
#   ssh -i ~/.ssh/pi_camera_drive joao@192.168.1.206 'sudo bash -s -- --apply' < platform/provision/retire-python.sh
#
# --check (the default) only prints what --apply would do and needs no root.
# --apply does it, and is safe to repeat. Nothing is removed unless
# `apt-get -s purge` shows exactly the planned packages and no others.
set -euo pipefail

mode=${1:---check}
case "$mode" in --check | --apply) ;; *) echo "usage: $0 [--check|--apply]" >&2; exit 2 ;; esac
[ "$mode" = --apply ] && [ "$(id -u)" != 0 ] && { echo "--apply needs root" >&2; exit 2; }

say() { printf '%s\n' "$*"; }
rc=0

# ---- old files ----------------------------------------------------------------
say "== files ($mode)"
old=(/usr/local/bin/camera-drive-web /usr/local/bin/camera-live /usr/local/bin/camera-offload /usr/local/bin/camera-wipe)
# The Go binary and the units that replace them must be in place first.
[ -x /usr/local/bin/camera-drive ] || { say "REFUSE  /usr/local/bin/camera-drive is missing"; exit 1; }
for f in "${old[@]}"; do
  if grep -rlE "^(ExecStart|ExecStartPre)=.*${f}( |$)" /etc/systemd/system /usr/lib/systemd/system 2>/dev/null | grep -q .; then
    say "REFUSE  a unit still runs $f"; exit 1
  fi
done
if grep -rqE "$(IFS='|'; echo "${old[*]}")" /etc/udev/rules.d /etc/sudoers.d 2>/dev/null; then
  say "REFUSE  udev or sudoers still names an old script"; exit 1
fi
files=()
for f in "${old[@]}"; do [ -e "$f" ] && files+=("$f"); done
while IFS= read -r d; do files+=("$d"); done < <(find /usr/local/bin -name __pycache__ 2>/dev/null)
if [ ${#files[@]} = 0 ]; then say "nothing to remove (already clean)"; fi
for f in "${files[@]}"; do
  if [ "$mode" = --apply ]; then rm -rf -- "$f"; say "removed  $f"; else say "would remove  $f"; fi
done

# ---- packages -----------------------------------------------------------------
say "== packages ($mode)"
app=(python3-flask python3-waitress python3-pil)
have=()
for p in "${app[@]}"; do dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q 'ok installed' && have+=("$p"); done
if [ ${#have[@]} = 0 ]; then
  say "none of ${app[*]} is installed (already clean)"
  exit 0
fi

# Orphans after the purge, from apt's own "no longer required" list, kept only if they are
# in the dependency closure of the three: so this never takes anything that was already
# orphaned for another reason.
closure=$(apt-cache depends --recurse --no-recommends --no-suggests --no-conflicts --no-breaks --no-replaces --no-enhances --installed "${have[@]}" 2>/dev/null | grep -E '^[a-z0-9]' | sort -u)
orphans=$(apt-get -s purge "${have[@]}" 2>/dev/null | awk '/no longer required:/{f=1;next} f&&/^ /{for(i=1;i<=NF;i++)print $i;next} {f=0}' | sort -u)
mine=$(comm -12 <(echo "$closure") <(echo "$orphans"))
plan=$(printf '%s\n' "${have[@]}" $mine | sort -u)
left=$(comm -13 <(echo "$closure") <(echo "$orphans"))

say "will remove:"; sed 's/^/  /' <<<"$plan"
[ -n "$left" ] && { say "orphaned but not from these three, left alone (apt autoremove would take them):"; sed 's/^/  /' <<<"$left"; }

# What would depend on them: apt must plan to remove exactly the plan, nothing else.
sim=$(apt-get -s purge $plan 2>/dev/null | awk '/^Purg /{print $2}' | sed 's/:.*//' | sort -u)
if [ "$sim" != "$plan" ]; then
  say "REFUSE  apt-get -s purge would remove something else:"; diff <(echo "$plan") <(echo "$sim") | sed 's/^/  /' || true
  exit 1
fi
if grep -qxE 'python3|python3-minimal|python3-apt|libpython3.*|python3-dbus' <<<"$plan"; then
  say "REFUSE  the plan includes Debian's own python3"; exit 1
fi
say "apt-get -s purge agrees: exactly these $(wc -l <<<"$plan") packages, no dependents."
for p in $plan; do
  users=$(apt-cache rdepends --installed "$p" 2>/dev/null | sed '1,2d;s/^ *[|]\?//' | grep -vxF -f <(echo "$plan") || true)
  [ -n "$users" ] && say "  $p is also named by: $(echo $users)"
done

if [ "$mode" = --apply ]; then
  DEBIAN_FRONTEND=noninteractive apt-get purge -y $plan
  say "== after"; dpkg -l "${app[@]}" 2>&1 | tail -3 || true
else
  say "(--check: nothing was changed; run with --apply to do this)"
fi
exit $rc
