# History and working notes

## The rename (2026-11-10)

The Pi stopped being only the camera box: it hosts several services behind a landing page and an admin page. It is now `pi-of-all-trades`: the hostname (`pi-of-all-trades.local`), the Wi-Fi hotspot name, and the GitHub repo (it was `pi-apitek-web-interface`, before that `pi-apitek-dashboard`). The camera service keeps its own names (`camera-drive`, its units and folders), and the NetworkManager profile names `camera-drive-wifi` and `camera-drive-ap` are internal and unchanged.

## The pi-mobile-server detour (2026-09-11 to 2026-09-18)

For a week this repo was `pi-mobile-server`: the Pi as a pocket server running
Etherpad for meetings and the camera app, both behind a Go login gateway
(TLS, users and grants, per-port reverse proxy), with a service manager and
network modes planned. Phases 0–2 were built and deployed. On 2026-09-18 the
plan changed: Etherpad and the gateway go to a home server, and the Pi goes
back to being only camera-drive.

The work is kept, not deleted:

- git tag `pi-mobile-server-final` — the full repo at the end, with `PLAN.md`,
  `gateway/` (Go, tests, README) and `services/` (`service.toml` format,
  Etherpad notes)
- `backups/pi-mobile-server-2026-09-18/` on the laptop (gitignored) — the same
  tree plus the built binary, and a copy of the Pi's gateway state
  (`/var/lib/pms-gateway`, `/etc/pms`)

`platform/provision/base.sh` removed it from the Pi: the gateway, its user and
polkit rule, the nftables firewall (back to Debian's stock file, disabled) and
`cgroup_enable=memory`. The camera app listens on `0.0.0.0:80` again and has
its own power-off button; the hostname is `cameradrive` again.

Kept from the detour, because they fixed real problems with the Pi itself:

- cloud-init off; its netplan WiFi profile and `/etc/netplan/*.yaml` (the WiFi
  password in plain text) deleted
- ModemManager and bluetooth disabled; `netreport.service` removed. Boot went
  from 1 min 24 s to about 21 s.
- `/`, `/etc`, `/etc/systemd`, `/etc/udev`, `/usr`, `/usr/local`,
  `/usr/local/bin` were `joao:joao 775` — root for anything running as `joao`,
  such as the web app. Now root-owned; `deploy.sh` checks and fixes the
  directories above everything it ships.
- `joao` had an empty password (console login and `su` with none); now locked,
  SSH key only. Three identical NOPASSWD sudo files reduced to
  `/etc/sudoers.d/010-joao`.
- The hotspot fallback, made faster on 2026-09-18 (see `docs/camera-drive.md`).

## Working on the Pi

- `ssh -i ~/.ssh/pi_camera_drive joao@192.168.1.206`. The laptop cannot
  resolve `.local` names. The Pi's WiFi can miss the first ARP; retry.
- `platform/deploy.sh --check` shows drift between the repo and the Pi
  without changing anything; run it before and after deploying.
- Non-login SSH PATH lacks `/usr/sbin` (`iw`, `NetworkManager`, `swapon`).
- `joao` has passwordless sudo from one file and a locked password.
  `deploy.sh` needs `sudo -n`. Give `joao` a password before ever removing
  NOPASSWD, or sudo is gone.
- New card or recovered Pi: `platform/deploy.sh`, then `base.sh`, then reboot
  (see `platform/README.md`).
- Before anything that can break networking, arm a revert with
  `systemd-run --on-active=…` first. For a revert that must survive a reboot,
  use a real unit with `OnBootSec=` that removes itself.
- Long jobs on the Pi run as `systemd-run` units so a dropped SSH cannot
  interrupt them.

## Measured (2026-09-11)

| | |
|---|---|
| RAM visible | 416 MB (gpu_mem 64 MB); 256 MB CMA pool from `vc4-kms-v3d` |
| Idle baseline | ~270 MB available |
| camera-drive-web | ~37 MB RSS |
| Swap | 416 MB zstd zram (rpi-swap) |
| WiFi | BCM43430/1 family, 2.4 GHz only; TP-Link on channel 1. The onboard firmware cannot run an AP and a WPA client at once. |
| Storage | SanDisk 64 GB |
| Software | Raspberry Pi OS on Debian 13, arm64, kernel 6.18; NM 1.52, systemd 257, Python 3.13, ffmpeg 7.1 |

## Pitfalls already paid for

Camera-specific ones are in `docs/camera-drive.md`. Beyond those:

- Debian's `nftables.conf` starts with `flush ruleset` and the unit's
  `ExecStop` flushes too — both wipe NetworkManager's shared-mode (hotspot)
  tables.
- A deploy that copies a rootfs tree onto `/` with ownership preserved hands
  the system directories to the laptop user.
- `passwd -S` showing `NP` means an empty password.
- A bash `until` loop returns its body's last status, not the condition's.
- NM keyfiles must be mode 600; cloud-init's duplicate netplan profile will
  autoconnect on any free WiFi interface.
- The camera's hardware H.264 encoder draws from the CMA pool, so test
  `/api/mp4` before shrinking it.
- The WiFi radio only appears ~21 s after power on; anything network-related
  that runs earlier must wait for it, not give up.
