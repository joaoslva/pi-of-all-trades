#!/bin/sh
# Set what the sandbox's fake nmcli and systemctl report. Each part is independent.
#   hub/dev/scenario.sh reset
#   hub/dev/scenario.sh wifi     client|hotspot-demand|hotspot-fallback|keep|none|broken|fail-up
#   hub/dev/scenario.sh services normal|off|warnings
#   hub/dev/scenario.sh camera   ok|copying|none
set -eu
DEV="${PIHUB_DEV:-${TMPDIR:-/tmp}/pihub-dev}"
F="$DEV/fake"; HOLD="$DEV/run/pihub/hotspot-hold"
mkdir -p "$F" "$DEV/run/pihub" "$DEV/camera"; touch "$F/log" "$F/units"
unit() { # unit <name> <state> <memory>
  awk -v u="$1" '$1 != u' "$F/units" >"$F/units.tmp"; echo "$1 $2 $3" >>"$F/units.tmp"; mv "$F/units.tmp" "$F/units"
}
wifi() {
  rm -f "$F/broken" "$F/fail-up" "$HOLD"; unit pihub-wifi-revert.timer inactive 0; unit camera-net-fallback.timer active 0
  cat >"$F/saved" <<'S'
camera-drive-wifi|HomeNet|infrastructure
camera-drive-ap|camera-drive|ap
Cafe Free|Cafe: Free|infrastructure
Office 5G|Office-5G|infrastructure
Grandparents|Casa-Avos|infrastructure
S
  echo camera-drive-wifi >"$F/active"
  case "$1" in
    client) ;;
    hotspot-demand) echo camera-drive-ap >"$F/active"; touch "$HOLD" ;;
    hotspot-fallback) echo camera-drive-ap >"$F/active" ;;
    keep) echo "Cafe Free" >"$F/active"; unit pihub-wifi-revert.timer active 0 ;;
    none) : >"$F/active" ;;
    broken) touch "$F/broken" ;;
    fail-up) printf 'Office 5G\n' >"$F/fail-up" ;; # Switch to Office 5G fails and the hub falls back
    *) echo "unknown wifi scenario $1" >&2; exit 2 ;;
  esac
}
services() {
  for u in camera-drive-web camera-live navidrome.service backup.timer files.service notes.service; do unit $u inactive 0; done
  awk '$1 !~ /^camera-offload@/' "$F/units" >"$F/units.tmp"; mv "$F/units.tmp" "$F/units"
  case "$1" in
    normal) unit camera-drive-web active 21495808; unit camera-live active 9437184; unit backup.timer active 0 ;;
    off) ;;
    warnings) # two heavy services and four background ones at once
      unit camera-drive-web active 21495808; unit navidrome.service active 48234496
      unit camera-offload@sda1.service active 0; unit backup.timer active 0; unit files.service active 5242880; unit notes.service active 3145728 ;;
    *) echo "unknown services scenario $1" >&2; exit 2 ;;
  esac
}
camera() {
  now="$(date -u +%Y-%m-%dT%H:%M:%S+00:00)"
  case "$1" in
    ok) printf '{"state":"idle","last_result":"copied 14, skipped 212 already held","updated":"%s"}\n' "$(date -u -d '-2 hours' +%Y-%m-%dT%H:%M:%S+00:00)" >"$DEV/camera/status.json" ;;
    copying) printf '{"state":"copying","total":40,"done":12,"updated":"%s"}\n' "$now" >"$DEV/camera/status.json" ;;
    none) rm -f "$DEV/camera/status.json" ;;
    *) echo "unknown camera scenario $1" >&2; exit 2 ;;
  esac
}
case "${1:-}" in
  reset) wifi client; services normal; camera ok ;;
  wifi|services|camera) "$1" "${2:?scenario name}" ;;
  *) sed -n '2,6p' "$0" >&2; exit 2 ;;
esac
