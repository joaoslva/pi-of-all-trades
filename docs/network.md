# Network

The Pi has two network modes. Only these are supported.

| Mode | What it is | Address |
|---|---|---|
| Client | The Pi joins a Wi-Fi network (home, a phone hotspot, ...) through a saved NetworkManager profile. | the LAN's DHCP address, and `pi-of-all-trades.local` |
| Hotspot | The Pi raises its own network `pi-of-all-trades` (profile `camera-drive-ap`, the profile name is internal and kept). No internet relay: the radio (BCM43430) cannot be an access point and a client at once. | `http://10.42.0.1/` |

Two things manage this, and they are separate on purpose:

- `camera-net-fallback`, a shell script run by `camera-net-fallback.timer`. It decides client versus hotspot. It lives outside the hub, so a hub crash or a bad hub release cannot take the hotspot away.
- The hub (`/admin`, Wi-Fi section). It manages Wi-Fi **profiles** (list, add, forget, switch) and can ask for the hotspot on demand. It has no endpoint that stops, disables, masks or starts the fallback, and it only reads whether the timer is active.

## What `camera-net-fallback` does

Read from `platform/rootfs/usr/local/bin/camera-net-fallback` (H4 did this first). Profiles: `camera-drive-ap` (the hotspot, `AP_CON`) and every other `802-11-wireless` profile (the client profiles, e.g. `camera-drive-wifi`). Interface `wlan0`. Log `/srv/camera-drive/net.log`.

Schedule (`camera-net-fallback.timer`): 15 s after boot, then 30 s after each run finished (`OnUnitActiveSec=30s`, `AccuracySec=2s`). The service is a oneshot with `TimeoutStartSec=120`. The timer is `WantedBy=timers.target`, enabled by `setup-ap.sh`.

Each run, in order:

1. **Switch flag (added in H4).** If `/run/pihub/wifi-switching` exists and is under 90 s old, exit at once. Older than 90 s is ignored, so a dead hub can never disable the fallback.
2. No `nmcli`: exit.
3. Wait up to 60 s for the radio (device state at least 30; it appears about 21 s after power on). Still not there: exit.
4. Wait for NetworkManager's own join: up to 25 s, decide at once when connected (100), or after 5 quiet seconds disconnected (30); states 40 to 90 (connecting) reset the quiet count.
5. Active profile on `wlan0` that is not `camera-drive-ap`: **do nothing** (H4b: a leftover `/run/pihub/hotspot-hold` is removed first).
6. **Hosting** (active is `camera-drive-ap`): if `/run/pihub/hotspot-hold` exists (the operator turned the hotspot on from the admin page), **exit: no scan, no rejoin** (H4b). Otherwise look for a known network at most every 120 s (`AP_SCAN_EVERY`, stamp file `/run/camera-net-fallback.scan`, because scanning on the AP's radio disturbs phones). `nmcli device wifi list --rescan yes`; if a known profile's SSID is visible: `connection down camera-drive-ap`, 2 s, `nmcli -w 30 connection up <profile>`. If that fails, `connection up camera-drive-ap` again. Scan empty: stay a hotspot.
7. **Nothing active** (H4b: any hold is removed first; a hold never suppresses this branch): scan once, try (`nmcli -w 30 connection up`) every client profile whose SSID is visible or which is `hidden`; the first that works wins. If none, and the `camera-drive-ap` profile exists, `nmcli connection up camera-drive-ap`. No profile: log "run setup-ap.sh".

So the hotspot is raised only when nothing is connected after the settle wait and no known network is in range. It is not a one-way switch: while hosting by fallback, a returning known network brings the Pi back to client mode within about two minutes. A hotspot turned on from the admin page (hold present) stays.

## What the hub does

Everything goes through `nmcli -t` (parsed by pure functions in `hub/wifi.go`) as user `joao`, allowed by `50-pihub.rules` (NetworkManager `settings.modify.system`, `network-control`, `wifi.scan`; the transient revert unit). Every change is behind the admin session and a one-time confirm token bound to the exact action (`wifi-connect:<name>` cannot run `wifi-forget:<name>`).

- **Status** (read-only): mode, SSID, address, the saved list, the last working client network, whether the fallback timer is active. Mode is one small state type derived from `nmcli` plus the hold flag: `client`, `hotspot-on-demand` (hotspot active and `/run/pihub/hotspot-hold` present), `hotspot-fallback` (hotspot active, no hold: the fallback raised it), `none`. A hold with nothing or a client active does not make a hotspot.
- **Last working network** (`last_client`, `/var/lib/pihub/network.json`, atomic write, separate from `state.json`): the client profile that was Kept, or was active when the hub started or the status was read (no switch waiting for Keep). A profile that was forgotten is not offered.
- **Scan**: client mode only. In hotspot mode a scan drops the hotspot, so the hub refuses (409) and the page asks for the SSID by hand.
- **Add**: SSID 1 to 32 bytes, password 8 to 63 characters (WPA-PSK) or empty (open). The profile is named after the SSID; an existing name is refused. Hidden networks and enterprise Wi-Fi are not supported. The password is an argument of the `nmcli` process for a moment (single-user Pi).
- **Forget**: refused for the network in use and for `camera-drive-ap`.
- **Switch** (API op `connect`): see below. The hotspot profile is not a Switch target; the page has Turn on hotspot for that. Refused for the network in use.
- **Turn on hotspot** (`hotspot-on`): writes the switching flag and the hold, then `nmcli connection up camera-drive-ap`. The radio cannot serve both, so there is no revert timer: if the call fails the hold is removed and the previous client connection is brought back at once. The hotspot then stays until **Leave hotspot** or a reboot (the hold is under `/run`). The confirm dialog gives the hotspot SSID (read from the profile) and `http://10.42.0.1/admin`.
- **Leave hotspot** (`hotspot-off`): removes the hold and switches to `last_client` with the safe connect below. If that fails the Pi returns to the hotspot with the hold restored (the previous connection). Refused when no working client network is known.
- **Keep**: cancels the revert.

### Connect is safe

1. Write `/run/pihub/wifi-switching` (timestamp inside, mtime used by the script). The directory is created by `RuntimeDirectory=pihub` and preserved across hub restarts.
2. Arm a revert: `systemd-run --collect --unit=pihub-wifi-revert --on-active=<N> sh -c 'nmcli -w 30 connection up id "$1"; rm -f <flag>' sh <previous>` (when the previous connection is the hotspot it also touches the hold). It is a root transient unit, independent of the hub: killing the hub does not cancel it. If arming fails, nothing is switched.
3. `nmcli -w 20 connection up id <new>`. Answer: the new address (`pi-of-all-trades.local` and the IP if known) and the deadline.
4. The page shows **Keep this network** (also after a reload: the timer being armed is the truth). Keep stops `pihub-wifi-revert.timer`, removes the flag and records the network as `last_client`.
5. Without Keep, the timer fires, brings the previous profile back and removes the flag. If even that fails, the flag is gone and the fallback raises the hotspot as usual.

**A failed attempt falls back at once** (H4b), not after the timer. When `nmcli up <new>` returns failure the hub immediately: (1) re-activates the previous connection (a client profile, or the hotspot with the hold restored if that was the previous mode); if that works it cancels the revert timer and removes the flag; (2) if not, raises `camera-drive-ap` once, **without** a hold (the fallback still rejoins client profiles by itself), and leaves the timer armed as the safety net; (3) if that fails too the timer stays armed. The error returned to the page says which network the Pi ended on. The armed timer remains for the other case: connected but never pressed Keep.

With no active connection there is nothing to return to: no revert, a failed attempt tries the hotspot once, and the flag is removed when the call ends.

`--wifi-revert` (seconds, default 30, allowed 10 to 80). It must stay below the 90 s the fallback honours the flag.

### Values (first guesses, plan "Decisions")

| What | Value | Measured |
|---|---|---|
| Revert | 30 s (was 60 s, shortened by the operator) | Only the "connected but never kept" safety net now. |
| Failed switch, recovery | immediate | wrong password, client to client (H4b `verify.sh`): back on `camera-drive-wifi` 4.8 s after the connect call was issued (2 one-second samples without it); the timer was cancelled and the flag removed. |
| Switching flag honoured | 90 s | not tuned |
| Hotspot scan interval | 120 s | not tuned |

## Manual hotspot test (the operator's job, never run by `verify.sh`)

The checks never raise the real hotspot (unit tests with a fake `nmcli` and the fallback script with a fake `nmcli` cover the logic). To test it by hand, with a laptop on the same LAN, arm a revert first:

    ssh joao@pi-of-all-trades.local 'sudo systemd-run --unit=manual-revert --on-active=300 nmcli connection up id camera-drive-wifi'

On demand (the normal way): on the admin page press **Turn on hotspot**, read the dialog, confirm. The page stops answering; join `pi-of-all-trades` on the phone and open `http://10.42.0.1/admin`. The hotspot must stay up for several minutes (the fallback sees the hold and does not rejoin). Press **Leave hotspot**, confirm: the Pi joins the last working network (shown on the page); press **Keep this network** at its address. `sudo systemctl stop manual-revert.timer` at the end. A reboot also ends a hold. Turn VPN off on the phone first (see `docs/camera-drive.md`, "If the AP loads nothing on your phone").

By fallback (no hold): make the known SSID vanish (switch the router off, or `sudo nmcli connection modify camera-drive-wifi 802-11-wireless.ssid nonexistent-test && sudo nmcli connection down camera-drive-wifi`). Within about 40 s `camera-drive` appears; the page says "raised automatically". Then `kill -9` the hub (`sudo kill -9 $(systemctl show -p MainPID --value pihub)`) and confirm the hotspot stays up and the hub returns in a few seconds. Restore the SSID afterwards.

## Known gaps

- A fallback run that already passed the flag check (it can sit in its 25 s settle wait) is not stopped when a switch starts. The window is a few seconds per 30 s; the revert still brings the previous network back.
- Keep over a changed address needs the operator to open the new address; in hotspot to client switches the phone loses the page until it rejoins.
