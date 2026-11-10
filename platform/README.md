# platform

Everything about the Pi itself, as opposed to the camera app in `app/`.
Hardware specifics belong here, so moving to another board means changing
this folder.

- `rootfs/` — files mirrored onto the Pi's `/`: the network fallback, cloud-init off
- `provision/` — getting a fresh SD card to a reachable Pi, then `base.sh`
- `deploy.sh` — pushes `platform/rootfs/`, `app/rootfs/` and `hub/rootfs/` (run `make -C hub build` first)

## Deploying

```bash
platform/deploy.sh --list                # what each component ships
platform/deploy.sh --check               # what differs on the Pi, changes nothing
platform/deploy.sh                       # install everything
platform/deploy.sh app                   # install one component
PI=joao@10.42.0.1 platform/deploy.sh     # another address
```

Files are installed as `root:root`, mode `0755` if executable in the repo,
`0644` otherwise. Two components shipping the same path is an error.

Every directory above a shipped file must be owned by root and not writable
by group or others; `--check` reports `unsafe dir`, an install fixes it. (An
earlier deploy had left `/`, `/etc` and `/usr` owned by `joao`, mode 775.)

After installing, only what changed is reloaded:

| Changed | Action |
|---|---|
| `/etc/systemd/` | `daemon-reload` |
| `/etc/udev/` | reload rules, replay block `add` events |
| a running service's unit or executable | restart it |

Only long-running services are restarted. Oneshots (`camera-offload@`,
`camera-net-fallback`) run the new file next time, and are never interrupted
mid-offload. `camera-live` is skipped while it is recording, since a restart
would end the recording; the deploy says so, and it needs restarting later.

## provision/

`base.sh` — setup that is state rather than files. Run it after the first
deploy onto a new card; it is safe to re-run:

```bash
ssh -i ~/.ssh/pi_camera_drive joao@192.168.1.206 sudo bash -s < platform/provision/base.sh
```

It deletes cloud-init's netplan WiFi profile (and the yaml holding the WiFi
password in plain text), sets the hostname (`cameradrive`, or the first
argument), disables ModemManager and bluetooth, removes what the
pi-mobile-server detour installed (gateway, its user and polkit rule, the
nftables firewall, `cgroup_enable=memory`; see `docs/history.md`), removes the
recovery leftovers (`netreport.service`, which held boot for ~50 s), keeps one
of the three identical NOPASSWD sudo files, locks the admin's empty password
and lists anything in system paths not owned by root. Reboot afterwards if it
says so.

From the first bring-up of camera-drive, kept because they still work:

- `fix-pi-card.sh` — writes the user, SSH key, WiFi profile and
  `netreport.service` straight onto the root partition from a laptop,
  bypassing cloud-init. The recovery path for a Pi that will not come up;
  run `base.sh` once the Pi is back.
- `diagnose-pi-card.sh` — read-only version of the above.
- `fill-secrets.sh`, `boot-originals/` — the cloud-init route, which failed
  (see "cloud-init's NoCloud datasource" in `docs/camera-drive.md`).
- `setup-ap.sh` — creates the `camera-drive-ap` hotspot profile that
  `camera-net-fallback` switches to.
