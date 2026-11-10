#!/usr/bin/env bash
#
# Create the Pi's own access point profile, so it stays reachable with no
# router anywhere in sight.
#
# Prompts for the AP password locally with echo disabled and passes it to the
# Pi over SSH, so it never lands in your shell history or the transcript.
#
# The profile is created with autoconnect=no on purpose: it is activated only
# by camera-net-fallback when no known WiFi is reachable. That keeps the Pi a
# normal client whenever a real network exists.
#
# Usage:  ./setup-ap.sh

set -euo pipefail

PI="${PI:-joao@192.168.1.206}"
KEY="${KEY:-$HOME/.ssh/pi_camera_drive}"
SSH_OPTS=(-i "$KEY" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)

AP_SSID="${AP_SSID:-pi-of-all-trades}"
AP_CON=camera-drive-ap

die() { printf '\nerror: %s\n' "$1" >&2; exit 1; }

echo "Setting up the access point profile on $PI"
echo "  SSID will be: $AP_SSID"
echo

read -rsp "Choose a WiFi password for the Pi's own network (min 8 chars): " AP_PW; echo
[ ${#AP_PW} -ge 8 ] || die "WPA2 requires at least 8 characters"
read -rsp "confirm: " AP_PW2; echo
[ "$AP_PW" = "$AP_PW2" ] || die "passwords did not match"
unset AP_PW2

ssh "${SSH_OPTS[@]}" "$PI" true || die "cannot reach $PI"

# The password goes over the SSH channel via stdin, never as an argument --
# arguments are visible in `ps` to any other user on the box.
printf '%s' "$AP_PW" | ssh "${SSH_OPTS[@]}" "$PI" "
set -euo pipefail
AP_PW=\$(cat)
AP_CON='$AP_CON'
AP_SSID='$AP_SSID'

# Recreate cleanly so re-running this is safe.
sudo nmcli connection delete \"\$AP_CON\" >/dev/null 2>&1 || true

sudo nmcli connection add type wifi ifname wlan0 con-name \"\$AP_CON\" \
     autoconnect no ssid \"\$AP_SSID\" >/dev/null

sudo nmcli connection modify \"\$AP_CON\" \
     802-11-wireless.mode ap \
     802-11-wireless.band bg \
     802-11-wireless.channel 6 \
     wifi-sec.key-mgmt wpa-psk \
     wifi-sec.proto rsn \
     wifi-sec.pairwise ccmp \
     wifi-sec.group ccmp \
     wifi-sec.psk \"\$AP_PW\" \
     ipv4.method shared \
     ipv6.method ignore \
     connection.autoconnect-priority 0

# ipv4.method shared makes NetworkManager run its own DHCP server for us, so
# no hostapd and no dnsmasq package are needed.
echo '  profile created:'
sudo nmcli -t -f connection.id,802-11-wireless.mode,802-11-wireless.ssid,ipv4.method \
     connection show \"\$AP_CON\" | sed 's/^/    /'
"

unset AP_PW

echo
echo "Enabling the fallback timer"
ssh "${SSH_OPTS[@]}" "$PI" '
  sudo systemctl daemon-reload
  sudo systemctl enable --now camera-net-fallback.timer
  systemctl status camera-net-fallback.timer --no-pager | head -4
'

cat <<EOF

Done.

  The Pi stays a normal WiFi client whenever a known network is in range.
  When there is none, within about a minute it raises its own network:

      SSID:  $AP_SSID
      URL:   http://10.42.0.1/     (NetworkManager's shared-mode address)

  It switches back automatically when a known network reappears.

  To add another network later (a phone hotspot, a friend's WiFi), run on the Pi:
      sudo nmcli device wifi connect "<SSID>" password "<password>"
  and the fallback logic will pick it up with no further configuration.
EOF
