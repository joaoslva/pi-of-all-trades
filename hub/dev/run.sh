#!/bin/sh
# Laptop sandbox for the hub pages: http://127.0.0.1:18090/ , admin password sandbox-pass.
# The hub runs inside bwrap with the fakes in hub/dev/bin bind-mounted over the real nmcli,
# systemctl and systemd-run, so nothing reaches this machine's Wi-Fi, services or power.
#   hub/dev/run.sh          build, reset the scenario, start in the background
#   hub/dev/run.sh stop
#   PIHUB_DEV_NO_SERVICES=1 hub/dev/run.sh   no services at all
#   hub/dev/scenario.sh ... change what the fakes report (see that file)
set -eu
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
DEV="${PIHUB_DEV:-${TMPDIR:-/tmp}/pihub-dev}"
ADDR=127.0.0.1:18090
FAKES="$REPO/hub/dev/bin"

stop() { pkill -f "$DEV/pihub-dev --listen" || true; rm -f "$DEV/pid"; }
[ "${1:-}" = stop ] && { stop; exit 0; }
stop
command -v bwrap >/dev/null || { echo "run.sh: needs bwrap" >&2; exit 1; }
command -v go >/dev/null || PATH="$(mise where go@1.24)/bin:$PATH"

rm -rf "$DEV"; mkdir -p "$DEV/state" "$DEV/services.d" "$DEV/run/pihub" "$DEV/thermal/thermal_zone0"
(cd "$REPO/hub" && go build -o "$DEV/pihub-dev" .)
cp "$REPO/hub/rootfs/etc/pihub/services.d/"*.json "$REPO/hub/testdata/services.d/files.json" \
   "$REPO/hub/testdata/services.d/notes.json" "$REPO/hub/dev/services.d/"*.json "$DEV/services.d/"
[ -n "${PIHUB_DEV_NO_SERVICES:-}" ] && rm "$DEV/services.d/"*.json # the landing page's empty state
echo 47236 >"$DEV/thermal/thermal_zone0/temp"
echo sandbox-pass | "$DEV/pihub-dev" passwd --file "$DEV/state/admin.hash" --user "$(id -un)" 2>/dev/null
PIHUB_DEV="$DEV" "$REPO/hub/dev/scenario.sh" reset

export PIHUB_FAKE="$DEV/fake" PATH="$FAKES:$PATH"
bwrap --dev-bind / / --bind "$DEV/run" /run --bind "$DEV/thermal" /sys/class/thermal \
  --ro-bind "$FAKES/nmcli" /usr/bin/nmcli --ro-bind "$FAKES/systemctl" /usr/bin/systemctl \
  --ro-bind "$FAKES/systemd-run" /usr/bin/systemd-run \
  --unshare-user --unshare-uts --hostname pi-sandbox \
  sh -c '
    for c in nmcli systemctl systemd-run; do
      [ "$(command -v $c)" = "$0/$c" ] && [ "$(/usr/bin/$c --fake-check)" = fake ] ||
        { echo "run.sh: $c is not the fake, refusing to start" >&2; exit 1; }
    done
    exec "$1" --listen "$2" --state-dir "$3/state" --hash-file "$3/state/admin.hash" \
      --services-dir "$3/services.d" --camera-dir "$3/camera"' \
  "$FAKES" "$DEV/pihub-dev" "$ADDR" "$DEV" >"$DEV/log" 2>&1 &
echo $! >"$DEV/pid"
for _ in $(seq 50); do
  curl -s -o /dev/null "http://$ADDR/" && { echo "sandbox: http://$ADDR/ (password sandbox-pass), state $DEV"; exit 0; }
  kill -0 "$(cat "$DEV/pid")" 2>/dev/null || break
  sleep 0.1
done
echo "run.sh: hub did not start" >&2; cat "$DEV/log" >&2; exit 1
