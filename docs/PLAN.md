# Pi hub plan

The Pi becomes a small host for services. A Go binary called `pihub` owns port 80, lists the services on a landing page, and links each button to the service's own port. A password-protected `/admin` page shows Pi stats, switches the heavy service (one at a time, with confirmation), warns when too much runs at once, and manages Wi-Fi. The camera app is rewritten from Python into one static Go binary, so none of our code needs a Python runtime. Everything is built on the laptop and shipped with `platform/deploy.sh` over SSH.

Rule for the whole program. Every service is a systemd unit plus a JSON file, and the hub is the only thing that starts or stops them.

Branches in order are H1, H2, H3, H4 (hub) and C1, C2, C3, C4 (camera).

## How we work

- Each id is a local branch, for example `h1-hub-skeleton`. No pull requests.
- A branch is done when its unit tests pass, its `verify.sh` checks pass on the real Pi, and (for the gated ones) the operator has looked at it. Then merge into `main` with `git merge --no-ff`, run `go test ./...`, run `git push origin main`, delete the branch.
- Run it with `/batch-orchestrator`, implementers are `pstack:poteto-agent`. H1 goes first and alone, because it edits three files in `app/`. After that two lanes run in parallel. The hub lane is H2, H3, H4 and touches only `hub/`, `platform/`, `docs/`. The camera lane is C1, C2, C3, C4 and touches only `app/`, `platform/provision/base.sh`, `docs/`. C4 also needs H1 on `main`. A branch that needs a file outside its lane stops and asks the root.
- Verification is lean on purpose. Each branch adds its checks to `hub/verify.sh` or `app/verify.sh`. The script deploys with `platform/deploy.sh`, checks the real behaviour over SSH, and prints PASS or FAIL per check. It is a rerunnable script, not extra agents.
- Gated branches (H1, H2, H3, H4, C4) change what the operator sees. The root posts a screenshot in chat and waits for the operator's word before the merge.
- Real-Pi checks take a lock (`flock /run/pihub-verify.lock`) so two never run at once. Run `platform/deploy.sh --check` before and after each deploy. Any Wi-Fi check arms a revert first with `systemd-run --on-active=300`, as `docs/history.md` says.
- Camera checks need the camera plugged in. If it is not, the check is reported as skipped, never as passed.
- Each `verify.sh` deploys only its own component (`hub` for H, `app` for C), so the two lanes do not overwrite each other on the Pi. Tests never write to the real `/srv/camera-drive` or touch the camera's partition. Reboot and power checks are off unless the operator sets `PIHUB_TEST_POWER=1`.
- Memory check. Each branch records one number, the resident memory in kB of the process it changes, read from `/proc/<pid>/status` on the Pi.

## H1. Hub skeleton, camera moves to port 8081

Files.
- Create `hub/go.mod`, `hub/main.go`, `hub/services.go`, `hub/services_test.go`, `hub/web/index.html`, `hub/Makefile`, `hub/verify.sh`.
- Create `hub/rootfs/etc/systemd/system/pihub.service`, `hub/rootfs/etc/pihub/services.d/camera.json`, `hub/testdata/services.d/` (three fixtures).
- Edit `platform/deploy.sh` so the default components are `platform app hub`.
- Edit `app/rootfs/usr/local/bin/camera-drive-web` to listen on `0.0.0.0:8081`.
- Edit `app/rootfs/etc/systemd/system/camera-drive-web.service` to add `Wants=camera-live.service`, and `camera-live.service` to add `PartOf=camera-drive-web.service`.
- Edit `app/rootfs/etc/udev/rules.d/99-camera-drive.rules` to drop the line that starts `camera-live.service`.
- Edit `.gitignore` to ignore `hub/rootfs/usr/local/bin/pihub`.

Build.
- `type Service struct { ID, Name, Description string; Class Class; Units []string; Port int }` with `Class` as `Exclusive` or `Background`. `LoadServices(dir)` is the only parser and rejects missing fields, duplicate ids, and port clashes.
- `GET /` renders one button per service, linking to `http://<request host without port>:<Port>/`.
- `hub/Makefile` target `build` runs `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags='-s -w'` into `hub/rootfs/usr/local/bin/pihub`. Every later branch reuses it.
- `pihub.service` runs as `joao` with `CAP_NET_BIND_SERVICE` and `Restart=always`.

Checks.
- Unit. Valid file, duplicate id, port clash, missing units, unknown class. The handler renders the literal href `http://pi.test:8081/` for `Host: pi.test:80`.
- `verify.sh`. `http://192.168.1.206/` returns a page with a Camera button to port 8081, and that port answers. `kill -9 pihub` and it is back in 5 s. After a reboot both units are active. Stopping `camera-drive-web` stops `camera-live`. Camera plugged in starts `camera-offload@` and `camera-live` once.
- Memory. `pihub` under 20 MB after 400 requests (the spike measured 12.7 MB; the Python app is about 38 MB).

## H2. Admin login, Pi stats, power controls

Files.
- Create `hub/auth.go`, `hub/stats.go`, `hub/web/admin.html`, `hub/auth_test.go`, `hub/stats_test.go`, `hub/rootfs/etc/polkit-1/rules.d/50-pihub.rules`.
- Edit `hub/main.go` (routes and a `passwd` subcommand) and `hub/verify.sh`.

Build.
- Password hashing with `crypto/pbkdf2` from the Go 1.24 standard library. Pick the iteration count so one hash takes about 200 ms on the Pi. One hash in `/etc/pihub/admin.hash`, written by `pihub passwd` over SSH. Without the file `/admin` shows the setup command.
- Session token in an `HttpOnly`, `SameSite=Strict` cookie, held in memory with an idle timeout. Cross-site POSTs are rejected with the `Sec-Fetch-Site` check from the old gateway `app.go:252-264`. Five logins per minute per client.
- `ReadStats()` is a pure parse over `/proc/loadavg`, `/proc/meminfo`, `/sys/class/thermal/thermal_zone0/temp`, `statfs("/")`, `/proc/uptime`.
- `POST /admin/api/power` is two steps. The first call returns a one-time nonce and the second runs `systemctl reboot` or `poweroff`. The polkit rule allows only those two for `joao`.

Checks.
- Unit. Right and wrong password, sixth login blocked, cross-site POST rejected, stats 401 without a cookie, fixture `/proc` text parses to literal expected values.
- `verify.sh`. `pihub passwd` then login works. Stats match `uptime` and `free -m` within 5 percent. A reused nonce is rejected. The hash file is not world-readable and the journal has no password text. Reboot from the page brings the Pi back.
- Memory. Under 23 MB.

## H3. Service switching, one heavy at a time, overload warning

Files.
- Create `hub/control.go`, `hub/guard.go`, `hub/control_test.go`, `hub/guard_test.go`, `docs/adding-a-service.md`, `hub/rootfs/etc/pihub/services.d/offload.json`.
- Edit `hub/services.go`, `hub/web/admin.html`, the polkit rule (start, stop, restart for units named in `services.d` only), and `hub/verify.sh`.

Build.
- `Switch(target)` converges. It reads `/var/lib/pihub/state.json` (`{"active":"camera"}`), stops every other `Exclusive` service, starts the target, writes the state. Repeating it, or crashing between stop and start, ends in the same state. At startup the hub starts the saved service if it is not running.
- The same two-step nonce as power. The dialog names the service being stopped and the one being started.
- `Assess(usages, memAvailable) []Warning` is a pure function. It warns when more than one `Exclusive` runs, when more than 3 `Background` run, and when `MemAvailable` is under 120 MB. The admin page shows a banner. Warnings never block an action.
- Unit state goes through a small `Systemd` interface (`Show`, `Start`, `Stop`) so tests fake it.
- `docs/adding-a-service.md` is the contract for a new service such as the game. A unit, a port, one JSON file.

Checks.
- Unit with the fake `Systemd`. A to B stops A before starting B. The same switch twice makes no second call. A crash after the stop converges on reconcile. `Assess` returns the literal warning list for six input tables.
- `verify.sh`. Switching Camera off stops `camera-drive-web` and `camera-live` together, and back on starts both. With Camera off, plugging in the camera still copies files. Camera stays off after a reboot. A 300 MB `stress` run shows the low-memory banner and it clears afterwards. Starting a second heavy unit by hand shows the banner.
- Memory. Under 23 MB. Also record the seconds from confirm to the target answering.

## H4. Wi-Fi profiles in the admin page, fallback script stays separate

The Pi has two network modes and the plan supports only those. In hotspot mode the Pi raises its own network with no internet relay, because the radio cannot be an access point and a client at once. In client mode the Pi joins a LAN. The hotspot fallback stays the existing `camera-net-fallback` timer, outside the hub, so a hub crash or a bad hub release can never take the hotspot away.

Files.
- Create `hub/wifi.go`, `hub/wifi_test.go`, `docs/network.md`.
- Edit `hub/web/admin.html`, `hub/main.go`, the polkit rule (NetworkManager add, modify, delete, and activate for connections), `hub/verify.sh`, and `platform/README.md`.
- Edit `platform/rootfs/usr/local/bin/camera-net-fallback` so it does nothing while `/run/pihub/wifi-switching` exists and is under 90 s old. Edit its `.service` and `.timer` only if the read below shows they lack `Restart` or `WantedBy=timers.target`.
- Do not add the fallback to `services.d`. The hub has no endpoint that stops, disables, or masks it.

Build.
- Read `camera-net-fallback` in full first and write its rules into `docs/network.md`. This is the one thing not yet read.
- List, add, connect, forget over `nmcli -t`, with a pure parser for each command. Scanning is offered only in client mode. In hotspot mode a scan drops the hotspot, so the page asks for the SSID by hand and says so.
- Connect is safe. Before switching, write `/run/pihub/wifi-switching`, arm a revert (`systemd-run --on-active=60 nmcli connection up <previous>`), switch, and show a Keep this network button at the new address. Keep stops the timer and removes the flag. Doing nothing restores the previous connection, and if that also fails the fallback script raises the hotspot as before.
- The admin page shows the current mode (hotspot or client) and the SSID, read-only.
- NetworkManager has two writers now, the hub and the fallback. The flag file is how they avoid fighting. The hub writes, the script only reads.

Checks.
- Unit. Parser tests over captured `nmcli -t` text. Revert test with a fake clock (no Keep restores the previous network, Keep cancels).
- `verify.sh`. A deliberately wrong password returns to the TP-Link network within 60 s and `ssh` still works. While a switch is in flight the fallback does not raise the hotspot. With no known network the hotspot comes up and `http://10.42.0.1/admin` loads. `kill -9 pihub` while in hotspot mode leaves the hotspot up. The fallback timer is active after a reboot.
- Memory. Under 23 MB.

## C1. Offload and wipe in Go

Files.
- Create `app/go.mod`, `app/main.go`, `app/shared.go`, `app/offload.go`, `app/wipe.go`, `app/offload_test.go`, `app/wipe_test.go`, `app/Makefile`, `app/verify.sh`.
- Edit `app/rootfs/etc/systemd/system/camera-offload@.service` and the udev rule to run `/usr/local/bin/camera-drive offload %I`. Edit `camera-drive-web` so `sudo -n` calls `/usr/local/bin/camera-drive wipe`.
- Delete `app/rootfs/usr/local/bin/camera-offload` and `camera-wipe`. Edit `.gitignore`.

Build.
- One static binary `camera-drive` with subcommands. `offload` and `wipe` land here. `live` and `web` follow.
- Port offload rule for rule. Read-only mount, identity by content hash, copy then re-hash the destination and delete a bad copy, index written atomically, never deletes from the camera, always unmounts, LED pattern, `flock` single instance.
- Port wipe rule for rule. A file whose hash is not in the index is kept and there is no override flag. An index entry with a missing file is kept with an error. The last stdout line is the JSON `{ok,deleted,kept,errors,committed}`.
- Each one has a pure core that takes directory paths and a thin shell that mounts, so tests need no root.

Checks.
- Unit over temp directories. Second offload skips everything. A corrupted destination is removed and counted failed. A file not in the index is kept by wipe. The wipe JSON equals a literal string.
- Differential `verify.sh`. Offload a loop-mounted FAT image with the Python tool at trunk and the Go tool at head into separate directories. The `index.json` files and copied trees must be identical. Wipe dry run and commit output must match the Python JSON. Two offloads at once, the second exits 0. Kill mid-run, re-run, and the mount is released. Wipe never runs against the real camera in any check. The operator tests that himself.
- Memory and time. Go offload of a 500 MB image is no slower than 110 percent of Python and no heavier.

## C2. camera-live in Go

Files.
- Create `app/live.go`, `app/live_test.go`, `app/testdata/ffmpeg-argv-python.golden`.
- Edit `app/main.go` and `camera-live.service` to run `camera-drive live`. Delete `app/rootfs/usr/local/bin/camera-live`.

Build.
- Keep the files-as-IPC contract unchanged. `control.json` is read every 0.4 s. `recstatus.json` and `live.jpg` are written by temp file and rename.
- The recorder is a state machine over `Off`, `Idle`, `Live`, `Error`, with recording as a flag on `Live`.
- The ffmpeg arguments come from one pure function, diffed against a golden file captured from the Python tool.
- Stop ffmpeg with SIGINT, wait up to 15 s, then TERM, then KILL. Keep `KillMode=mixed`. Refuse to record under 512 MiB free. Keep the traps in `docs/python-app-inventory.md` section 6.

Checks.
- Unit. Golden argv for preview and record modes. State table. A race test where web and live both write `control.json`.
- `verify.sh` with the camera. Record 20 s with Python at trunk and Go at head, and both `ffprobe` with the same streams and a duration of 20 s plus or minus 1. Preview reaches 5 fps. A snapshot appears. Three start and stop cycles leave three valid files. SIGTERM mid-recording still leaves a valid file. Unplugging mid-recording gives `error` and no hang. Low disk refuses to record.
- CPU and memory of `live` plus ffmpeg during a 60 s preview are no worse than Python by more than 5 percent.

## C3. Web read side in Go

Files.
- Create `app/web.go`, `app/media.go`, `app/web_test.go`, `app/media_test.go`, `app/web/page.html` (the `PAGE` string from `camera-drive-web` lines 783 to 1457, verbatim, embedded).
- Edit `app/main.go` to add `web --listen`.

Build.
- Routes `/`, `/api/state`, `/api/stream`, `/api/live.jpg`, `/api/play/`, `/api/control`, `/api/power`, `/api/wipe`, `/media/`, `/thumb/`, `/api/zip` on `net/http`.
- One function `safeMediaPath(rel)` resolves under `media/` and `video/` only and refuses symlinks that escape. Every file route calls it.
- Thumbnails use the existing ffmpeg command for both videos and images, so no image library.
- `WriteTimeout` is 0 and streams flush every frame.
- The unit still points at the Python app. The Go app runs beside it on port 8082 for the checks only.

Checks.
- Unit. `safeMediaPath` with `../`, an absolute path, an encoded traversal, and a symlink out, each giving 404. A `/api/state` test over a fixture tree with literal expected JSON.
- `verify.sh`. Over the same `/srv/camera-drive`, `/api/state` from port 8081 (Python) and 8082 (Go) are equal after sorting keys. A Range request gives the same 206 headers. ZIP of ten files lists the same names and sizes. The live stream ends within 5 s of closing the tab. Ten simultaneous streams stay under 60 MB. The two pages look the same side by side.
- Memory. Go app at most half the Python app's, and no more CPU during one stream.

## C4. Export, cutover, delete Python

Files.
- Create `app/export.go`, `app/export_test.go`.
- Edit `app/web.go`, `camera-drive-web.service` to run `camera-drive web --listen 0.0.0.0:8081`, `platform/provision/base.sh` to remove `python3-flask`, `python3-waitress`, `python3-pil`, and the docs (`README.md`, `docs/camera-drive.md`, `platform/README.md`).
- Delete `app/rootfs/usr/local/bin/camera-drive-web` and `__pycache__`.

Build.
- Port `ensure_mp4` and the four export formats with the exact ffmpeg arguments from `docs/python-app-inventory.md` section 1. That includes the `h264_v4l2m2m` first try, the `libx264` fallback, the `.part` rename, and the `ffprobe` duration check.
- One global encode lock, so a second job waits. Jobs live in memory with the 1800 s and 300 s TTLs, a 500-path cap, cancel by killing ffmpeg, and cleanup of `export-*` at startup.
- The unit switches to the Go binary and the Python files and packages are removed in the same branch.

Checks.
- Unit. ffmpeg argument builder per format against literal slices. Job state machine (queued, running, done, cancelled). TTL with a fake clock. The 500-path cap.
- `verify.sh`. The same 30 s recording exported as MP4 at trunk and head has the same codec and resolution and a duration within 1 s. MOV, WebM, and MP3 each `ffprobe`. Two exports run one ffmpeg at a time. Cancel leaves no process or partial file. A service restart mid-export cleans `export-*`. `dpkg -l` shows none of the three Python packages and no `python3` process for our units. After a reboot, landing page, camera, record, export, and wipe all work from the phone.
- Time and memory. Export no slower than 105 percent of Python. Total resident memory of our processes at idle at most 60 percent of before.

## Decisions

- Timeouts are first guesses and probably too long. They are the 60 s Wi-Fi revert, the 90 s `wifi-switching` flag, the 120 s hotspot scan interval, the 5 s stream idle end, and the 5 s restart check. Tune them on the real Pi during H4 and C2 and record the final values in `docs/network.md`.

- Redirect to the service port. The hub carries no service traffic.
- Two classes. `Exclusive` runs one at a time, switched behind a confirm. `Background` can run together. The guard warns and never blocks.
- Offload stays a udev-started job, not a daemon. It uses no memory when idle and the hub lists it as a background service.
- Plain HTTP on trusted networks. One admin password. No users, no TLS.
- No dependencies beyond the Go standard library. Service files are JSON because the standard library has no TOML. The password hash is `crypto/pbkdf2`.
- ffmpeg and other binaries on the Pi are fine. The goal is no Python runtime for our code.
- The hotspot fallback stays a separate timer outside the hub. The hub manages Wi-Fi profiles only and cannot stop the fallback. Only two modes are supported, Pi hotspot with no internet relay and Pi client on a LAN.

- Protecting a service with a password (a host-scoped signed cookie issued by the hub and checked by Go services, toggled per service in the admin page) is benched as H5. The one-at-a-time switch already hides the camera while the game runs. Revisit after C3 if it is still wanted.

## Rejected

| Option | Why it lost |
|---|---|
| Reverse proxy under `/camera/` | Services would need to know a URL prefix. A redirect costs nothing. |
| nginx or Caddy | More memory and a config language for what one Go handler does. |
| TLS, users, grants, sqlite from the old gateway | One admin password is the need. The old attempt built these and was scrapped. |
| TOML manifests, argon2id | Each needs a dependency. JSON and `crypto/pbkdf2` are in the standard library. |
| Offload as a long-running daemon | The udev oneshot is free when idle. |
| Rust | Slower to write, no gain at this scale. |
| Symlinks for the old script names | `deploy.sh` refuses symlinks. Units and udev call the subcommand directly. |

## Risks

- Switching Wi-Fi from the hub in hotspot mode drops the hotspot, so the phone loses the page until the new network or the revert settles. The revert and the fallback script bring it back.
- A Wi-Fi change over Wi-Fi can lock the operator out. The revert is armed before every switch, and the fallback runs independently of the hub as a second safety net.
- `joao` has `NOPASSWD:ALL` sudo, so the hub's polkit allowlist is not the only door. Tightening sudo is outside this plan.
- Camera checks need the physical camera. They are skipped, not passed, without it.
- Two writers share `control.json`. The Go port keeps atomic rename and read-modify-write, and C2 has a race test.
- During C3 both web apps run on a 415 MB Pi. Stop one before streaming from the other.
- The page's JavaScript was only grepped for `fetch` calls. C3's side-by-side comparison catches an unlisted route.
- Debian keeps `python3` for its own tools. The goal is that our code needs none and the three app packages are gone.
- The Go spike installed Go into the global `~/.config/mise/config.toml`.

## Evidence so far

A throwaway Go spike was built on the laptop and run on the Pi. The code is in the session scratchpad and has no branch.

- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags='-s -w'` on Go 1.24.13 gave a 7.7 MB static ARM64 binary.
- On the Pi it answered 400 requests using `html/template`, a `/proc/meminfo` read, and a `systemctl is-active` call. `VmRSS` was 12,748 kB with 7 threads. The Python camera app is about 38 MB (measured 2026-09-11).
- SSH to `joao@192.168.1.206` works with `~/.ssh/pi_camera_drive`. The Pi runs Debian 13 on arm64 with 415 MB of RAM.

Not yet proven.
- `crypto/pbkdf2` speed on the Pi Zero 2 W. H2 measures it.
- What `camera-net-fallback` does in every case. H4 reads it first.
- That `camera-live` starts idle with no camera plugged in. C2 checks.
- That `/api/state` JSON is equal between Flask and Go. C3 checks.

## Reading list

- `docs/camera-drive.md`, especially "Things that were not obvious, and cost time". Every C branch reads it.
- `docs/history.md` for the pitfalls already paid for.
- `platform/README.md` for how `deploy.sh` ships and restarts.
- Git tag `pi-mobile-server-final` for reusable pieces. `gateway/app.go` lines 252 to 264 (`originOK`) and `platform/rootfs/etc/polkit-1/rules.d/50-pms-gateway.rules`.
- `docs/python-app-inventory.md` lists the Python app's routes, IPC, privileges, and traps, with `file:line` pointers. The C branches use it as their spec.
