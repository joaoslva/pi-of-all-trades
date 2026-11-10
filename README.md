# camera-drive

A Raspberry Pi Zero 2 W that empties an Aiptek Cam 3200 by itself and works as
a camcorder back-end for it: plug the camera in, and a phone browses, plays
and downloads everything at `http://cameradrive.local/` (or the Pi's IP): the landing page on port 80 links
to the camera on port 8081. With
no known WiFi around, the Pi raises its own hotspot and serves the same page at
`http://10.42.0.1/`.

## Layout

```
app/        the camera application: scripts, systemd units, udev rule (app/rootfs/ mirrors the Pi's /)
hub/        pihub, the Go landing page on port 80 (hub/rootfs/ mirrors the Pi's /; `make -C hub build` first)
platform/   the Pi itself: deploy.sh, network fallback, provisioning and recovery scripts
docs/       camera-drive.md (how it works, what it cost to learn), history.md (working notes)
backups/    local data pulled from the Pi, never committed
```

## Deploying

```bash
platform/deploy.sh --check   # what differs on the Pi, changes nothing
platform/deploy.sh           # install platform/ and app/
```

Details in `platform/README.md`; how the app works in `docs/camera-drive.md`.
