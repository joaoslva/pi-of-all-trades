# pi-of-all-trades

A Raspberry Pi Zero 2 W that hosts small services of all kinds. Open
`http://pi-of-all-trades.local/` (or the Pi's IP) and pick a service: each button
redirects to that service's own port. Heavy services (the camera app, a game)
run one at a time. Light background jobs, like the camera offload, run
alongside. A password-protected `/admin` page shows the Pi's stats, switches
the heavy service, manages Wi-Fi, changes the admin password and powers the
Pi off. With no known Wi-Fi around, the Pi raises its own hotspot
`pi-of-all-trades` and serves the same pages at `http://10.42.0.1/`.

Today's services:

- **camera-drive**: empties an Aiptek Cam 3200 by itself when it is plugged in
  (offload), and works as its camcorder back-end (live view, recording, a
  gallery with playback, export and zip). Port 8081. How it works and what it
  cost to learn: `docs/camera-drive.md`.

Everything is Go built on the laptop and shipped over SSH. No Python or web
server runtime on the Pi. A new service is a systemd unit, a port and one JSON
file: `docs/adding-a-service.md`.

## Layout

```
app/        the camera application, one Go binary `camera-drive` (offload, wipe, live, web), systemd units, udev rule (app/rootfs/ mirrors the Pi's /)
hub/        pihub, the Go landing page on port 80 (hub/rootfs/ mirrors the Pi's /; `make -C hub build` first)
platform/   the Pi itself: deploy.sh, network fallback, provisioning and recovery scripts
docs/       PLAN.md (the build plan), admin.md, network.md, adding-a-service.md, camera-drive.md (the camera service), history.md (working notes)
backups/    local data pulled from the Pi, never committed
```

## Deploying

```bash
platform/deploy.sh --check   # what differs on the Pi, changes nothing
platform/deploy.sh           # install platform/, app/ and hub/
```

Details in `platform/README.md`; how the app works in `docs/camera-drive.md`.
