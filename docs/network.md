# Network

The Pi has two network modes. Only these are supported.

| Mode | What it is | Address |
|---|---|---|
| Client | The Pi joins a Wi-Fi network (home, a phone hotspot, ...) through a saved NetworkManager profile. | the LAN's DHCP address, and `cameradrive.local` |
| Hotspot | The Pi raises its own network `camera-drive` (profile `camera-drive-ap`). No internet relay: the radio (BCM43430) cannot be an access point and a client at once. | `http://10.42.0.1/` |

Two things manage this, and they are separate on purpose:

- `camera-net-fallback`, a shell script run by `camera-net-fallback.timer`. It decides client versus hotspot. It lives outside the hub, so a hub crash or a bad hub release cannot take the hotspot away.
- The hub (`/admin`, Wi-Fi section). It only manages Wi-Fi **profiles**: list, add, forget, connect. It has no endpoint that stops, disables, masks or starts the fallback, and it only reads whether the timer is active.

## What `camera-net-fallback` does

Read from `platform/rootfs/usr/local/bin/camera-net-fallback` (H4 did this first). Profiles: `camera-drive-ap` (the hotspot, `AP_CON`) and every other `802-11-wireless` profile (the client profiles, e.g. `camera-drive-wifi`). Interface `wlan0`. Log `/srv/camera-drive/net.log`.

Schedule (`camera-net-fallback.timer`): 15 s after boot, then 30 s after each run finished (`OnUnitActiveSec=30s`, `AccuracySec=2s`). The service is a oneshot with `TimeoutStartSec=120`. The timer is `WantedBy=timers.target`, enabled by `setup-ap.sh`.

Each run, in order:

1. **Switch flag (added in H4).** If `/run/pihub/wifi-switching` exists and is under 90 s old, exit at once. Older than 90 s is ignored, so a dead hub can never disable the fallback.
2. No `nmcli`: exit.
3. Wait up to 60 s for the radio (device state at least 30; it appears about 21 s after power on). Still not there: exit.
4. Wait for NetworkManager's own join: up to 25 s, decide at once when connected (100), or after 5 quiet seconds disconnected (30); states 40 to 90 (connecting) reset the quiet count.
5. Active profile on `wlan0` that is not `camera-drive-ap`: **do nothing**.
6. **Hosting** (active is `camera-drive-ap`): look for a known network at most every 120 s (`AP_SCAN_EVERY`, stamp file `/run/camera-net-fallback.scan`, because scanning on the AP's radio disturbs phones). `nmcli device wifi list --rescan yes`; if a known profile's SSID is visible: `connection down camera-drive-ap`, 2 s, `nmcli -w 30 connection up <profile>`. If that fails, `connection up camera-drive-ap` again. Scan empty: stay a hotspot.
7. **Nothing active**: scan once, try (`nmcli -w 30 connection up`) every client profile whose SSID is visible or which is `hidden`; the first that works wins. If none, and the `camera-drive-ap` profile exists, `nmcli connection up camera-drive-ap`. No profile: log "run setup-ap.sh".

So the hotspot is raised only when nothing is connected after the settle wait and no known network is in range. It is not a one-way switch: while hosting, a returning known network brings the Pi back to client mode within about two minutes.

## What the hub does

Everything goes through `nmcli -t` (parsed by pure functions in `hub/wifi.go`) as user `joao`, allowed by `50-pihub.rules` (NetworkManager `settings.modify.system`, `network-control`, `wifi.scan`; the transient revert unit). Every change is behind the admin session and a one-time confirm token bound to the exact action (`wifi-connect:<name>` cannot run `wifi-forget:<name>`).

- **Status** (read-only): mode (client, hotspot, none), SSID, address, the saved list, whether the fallback timer is active.
- **Scan**: client mode only. In hotspot mode a scan drops the hotspot, so the hub refuses (409) and the page asks for the SSID by hand.
- **Add**: SSID 1 to 32 bytes, password 8 to 63 characters (WPA-PSK) or empty (open). The profile is named after the SSID; an existing name is refused. Hidden networks and enterprise Wi-Fi are not supported. The password is an argument of the `nmcli` process for a moment (single-user Pi).
- **Forget**: refused for the network in use and for `camera-drive-ap`.
- **Connect**: see below. The hotspot profile cannot be connected from here; only the fallback raises it.
- **Keep**: cancels the revert.

### Connect is safe

1. Write `/run/pihub/wifi-switching` (timestamp inside, mtime used by the script). The directory is created by `RuntimeDirectory=pihub` and preserved across hub restarts.
2. Arm a revert: `systemd-run --collect --unit=pihub-wifi-revert --on-active=<N> sh -c 'nmcli -w 30 connection up id "$1"; rm -f <flag>' sh <previous>`. It is a root transient unit, independent of the hub: killing the hub does not cancel it. If arming fails, nothing is switched.
3. `nmcli -w 20 connection up id <new>`. Answer: the new address (`cameradrive.local` and the IP if known) and the deadline.
4. The page shows **Keep this network** (also after a reload: the timer being armed is the truth). Keep stops `pihub-wifi-revert.timer` and removes the flag.
5. Without Keep, the timer fires, brings the previous profile back and removes the flag. If even that fails, the flag is gone and the fallback raises the hotspot as usual.

If `nmcli up` fails (wrong password), the revert stays armed and restores the previous network when it fires; the hub does not shortcut it, so there is one recovery path, not two.

With no active connection there is nothing to return to: no revert, and the flag is removed when the call ends.

`--wifi-revert` (seconds, default 60, allowed 10 to 80). It must stay below the 90 s the fallback honours the flag.

### Values (first guesses, plan "Decisions")

| What | Value | Measured |
|---|---|---|
| Revert | 60 s | wrong password, client to client: NetworkManager itself fell back to `camera-drive-wifi` 3.7 s after the connect call (outage about 2 s), so ssh never stayed down. The revert unit finished and removed the flag 66 s after the call (60 s timer plus about 6 s). The revert only matters when NM does not fall back by itself, which this check cannot force. |
| Switching flag honoured | 90 s | not tuned |
| Hotspot scan interval | 120 s | not tuned |

## Manual hotspot test (the operator's job, never run by `verify.sh`)

The checks never raise the real hotspot. To test it by hand, with a laptop that is on the same LAN, arm a revert first:

    ssh joao@cameradrive.local 'sudo systemd-run --unit=manual-revert --on-active=300 nmcli connection up id camera-drive-wifi'
    # then make the known SSID vanish: switch the router off, or
    ssh joao@cameradrive.local 'sudo nmcli connection modify camera-drive-wifi 802-11-wireless.ssid nonexistent-test && sudo nmcli connection down camera-drive-wifi'

Within about 40 s the `camera-drive` network appears; join it and open `http://10.42.0.1/admin`. Then `kill -9` the hub (`sudo kill -9 $(systemctl show -p MainPID --value pihub)`) and confirm the hotspot stays up and the hub returns in a few seconds. Restore the SSID (`nmcli connection modify camera-drive-wifi 802-11-wireless.ssid <real>`) and `sudo systemctl stop manual-revert.timer` (or wait: it restores the profile, not the SSID). Turn VPN off on the phone first (see `docs/camera-drive.md`, "If the AP loads nothing on your phone").

## Known gaps

- A fallback run that already passed the flag check (it can sit in its 25 s settle wait) is not stopped when a switch starts. The window is a few seconds per 30 s; the revert still brings the previous network back.
- Keep over a changed address needs the operator to open the new address; in hotspot to client switches the phone loses the page until it rejoins.
