# camera-drive Python -> Go inventory
Paths: W=app/rootfs/usr/local/bin/camera-drive-web, L=.../camera-live, O=.../camera-offload, X=.../camera-wipe, U=app/rootfs/etc/systemd/system/*.service, UD=app/rootfs/etc/udev/rules.d/99-camera-drive.rules, D=docs/camera-drive.md. G=git tag pi-mobile-server-final:gateway/.
Sizes: W 1484 lines (Python ~770 + HTML/CSS/JS PAGE string W:783-1457, can be go:embed unchanged), L 364, O 343, X 212, fallback 144 (bash, stays).

## 1. HTTP routes (W, waitress :80, 12 threads, W:1481; no auth anywhere)
- GET / W:755: serves PAGE html (icon placeholders replaced W:1457). 
- GET /api/state W:521: JSON {camera,disk,status,recstatus,control,live,held,dumps,captures}. camera = `lsusb` grep 08ca:2023 storage / 08ca:2022 live (W:106-116); disk = statvfs of /srv/camera-drive; status/recstatus/index/control = JSON files; dumps = media/<folder>/* listing (W:134); captures = video/*+media/snapshots/* grouped per day by filename stamp (W:168).
- GET /api/stream W:536: 503 if live.jpg missing; multipart/x-mixed-replace boundary=frame; polls live.jpg every 80ms, sends only changed frames that end in FFD9 and >=1024 B; ends after 60 idle polls (~5s) (W:278-291).
- GET /api/live.jpg W:553: one frame, no-store, 503 if none.
- GET /api/play/<rel> W:544: video only (400 else). ffmpeg `-v error -re -i F -c:v copy -f image2pipe -` ; splits stdout on FFD9/FFD8FF, emits multipart (W:294-324); kills ffmpeg when client leaves.
- GET /api/mp4/<rel> W:597: ensure_mp4 then file with Range support. Cached proxy at /srv/camera-drive/proxy.
- POST /api/control W:562: JSON {preview|recording|snapshot}; recording=true also sets preview=true; snapshot bumps snapshot_seq; 400 if nothing. Writes control.json (see 2).
- POST /api/power W:583: `sudo -n systemctl poweroff`, fire and forget.
- POST /api/export W:617: {format in mp4|mov|webm|mp3, paths[<=500]}; starts goroutine job, returns {ok,id}. GET /api/export/<id> W:644 status {state,done,total,current,failed,name,size,error}; DELETE W:652 cancels (running) or removes; GET /api/export/<id>/file W:664 (409 unless done; attachment; Range; sets fetched).
- GET /media/<rel> W:679: Range file; video and ?dl => attachment.
- GET /thumb/<rel> W:692: cached jpg, max-age 86400, 404 if none.
- POST /api/zip W:701: form field "path" (repeat, <=500); ZIP_STORED to tmp file, attachment camera-drive_<stamp>.zip, tmp deleted after.
- POST /api/wipe W:737: {commit:bool} -> `sudo -n /usr/local/bin/camera-wipe --commit|--dry-run`, timeout 900 s, returns last stdout line as JSON.
- Path guard safe_media_path W:200: resolve under /srv/camera-drive/{media,video} only, must be a file, else 404.
ffmpeg args (all prefixed `-v error ... -y out`, run_ffmpeg W:331, then ffprobe `-show_entries format=duration -of csv=p=0` must succeed and size>1024):
- mp4 W:375-378: try `-c:v h264_v4l2m2m -b:v 1500k`, fall back `-c:v libx264 -preset veryfast -crf 26`; common `-pix_fmt yuv420p -c:a aac -b:a 96k -movflags +faststart -f mp4`, to <name>.part then rename.
- mov W:400: from cached mp4 `-c copy -movflags +faststart -f mov`.
- webm W:404: `-c:v libvpx-vp9 -deadline realtime -cpu-used 8 -row-mt 1 -b:v 1000k -pix_fmt yuv420p -c:a libopus -b:a 96k -f webm`.
- mp3 W:409: `-vn -c:a libmp3lame -q:a 3 -f mp3`.
- video thumb W:503: `ffmpeg -hide_banner -loglevel error -ss 1 -i F -frames:v 1 -vf scale=320:-1 -y out`, retry without -ss; timeout 90 s.
Global encode mutex W:90. Jobs in memory; TTL 1800 s after finish, 300 s after first fetch (W:84-85, 427); at startup rm TMP/export-* and prune_proxies (W:1470-1474). Multiple outputs => stored zip; clash names get dump-folder prefix (W:239).

## 2. IPC web <-> camera-live (no socket)
- /run/camera-drive/control.json (W:63, L:48), written atomically (tmp + rename) by web (W:262-270): {"preview":bool,"recording":bool,"snapshot_seq":int}. camera-live polls every 0.4 s (L:60, 307). Resets it to all-false/0 at start (L:277); also writes recording:false itself when disk <512 MiB (L:320-324) -> writers race; read-modify-write on both sides.
- /run/camera-drive/live.jpg (tmpfs), rewritten in place by ffmpeg (L:177); web must check FFD9 tail.
- /srv/camera-drive/recstatus.json (L:53,89): {state: off|idle|live|error, recording, file, audio, last_error, last_snapshot, updated}. Read by web as "recstatus".
- status.json = written by camera-offload (O:148): {state idle|copying|error, total, done, last_result, last_copied/skipped/failed/bytes/last_folder, updated}. index.json = {sha256: path relative to media/} (O:274).
- Snapshot = copy live.jpg -> media/snapshots/SNAP_%Y-%m-%d_%H%M%S.jpg via .part+rename (L:223-237); ignored unless capturing. Recording file video/<%Y-%m-%d_%H%M%S>.mkv.
- Log lines to /srv/camera-drive/record.log, offload.log (shared by offload and wipe).

## 3. Privilege boundaries
- camera-drive-web runs as joao, NoNewPrivileges=no, ProtectSystem=strict, RW=/srv/camera-drive, ProtectHome, PrivateTmp, Nice=10, CAP_NET_BIND_SERVICE ambient, RuntimeDirectory=camera-drive Preserve=yes, ExecStartPre=+install thumbs dir (U:web 1-60).
- sudo: only `sudo -n camera-wipe` and `sudo -n systemctl poweroff` (W:590,742). Real sudoers is NOPASSWD:ALL for joao (platform/provision/base.sh:161-169, fix-pi-card.sh:93); no scoped rule exists. Go can drop sudo: run hub as root-less + polkit (see 8) or keep sudo.
- camera-live: User=joao, groups video audio, KillMode=mixed, KillSignal=SIGTERM, TimeoutStopSec=60, Restart=no, ExecStartPre=+install dirs video and media/snapshots (U:live).
- camera-offload@.service: oneshot root, TimeoutStartSec=3600, Nice=5, ionice 6, started by udev SYSTEMD_WANTS (U:offload, UD:132-134). Writes as root: index.json, status.json, offload.log, media/<stamp>/ (chown 1000:1000 at O:285). Web reads these, never writes them.
- udev: storage partition 08ca:2023 -> camera-offload@%k + UDISKS_IGNORE (UD:132-140); usb 08ca:2022 -> camera-live.service (UD:152-154); no remove rule on purpose (UD:148-151).
- camera-wipe runs root via sudo; locates camera by `udevadm info --query=property --name=/dev/sdXN` ID_VENDOR_ID/ID_MODEL_ID (X:409-436), never by name.
- Lock: /run/camera-drive.lock flock (O:42,319). Mounts: /run/camera-drive/mnt ro (O:43), /run/camera-drive/wipe-mnt rw (X:379). LED /sys/class/leds/{ACT,led0} (O:57).
- Network: platform camera-net-fallback (bash/nmcli, root timer, 15 s after boot then 30 s) stays as is.

## 4. camera-offload (trigger: udev add of 08ca:2023 partition -> camera-offload@sdX1.service)
1. flock non-blocking; second instance exits 0 (O:319-326).
2. Wait up to 15 s for the device node (O:159).
3. Mount `-o ro,noatime,nosuid,nodev,noexec,uid=0,gid=0` at /run/camera-drive/mnt (O:174); never writes to camera.
4. Walk tree, skip System Volume Information/.fseventsd/.Spotlight-V100/.Trashes/LOST.DIR/FOUND.000; media suffixes jpg jpeg avi mov mp4 wav png (O:46-48,201).
5. sha256 each file; if hash in index.json -> skip (identity is content, names repeat).
6. New files go to media/<ingest %Y-%m-%d_%H%M%S>/ (camera clock is dead); name clash -> <stem>_<hash8><ext>.
7. copy2, re-hash the DESTINATION, mismatch => delete copy, count failed; os.sync then add to index (O:259-274).
8. index.json written atomically + dir fsync once at end (O:136-146, 281); status.json updated per file. (Crash mid-run loses index entries for copied files; they just re-copy next time.)
9. Never deletes anything; always umount (retry 5x) and sync in finally.
10. LED: blink 3 ok, 2 empty, 10 on failure; saves/restores trigger. Exit 1 if any failure.
## camera-wipe (trigger: POST /api/wipe, UI does dry-run first then confirm dialog; never automatic)
1. Needs --dry-run or --commit, else usage exit 2 (X:477).
2. Reads index.json, error out if unreadable.
3. Finds camera partition via udev vendor/product among /dev/sd[a-z][0-9].
4. Mounts rw at /run/camera-drive/wipe-mnt (not the offload mount).
5. For each media file: sha256; unreadable => keep+error.
6. Hash not in index => KEEP (no --force exists).
7. Index points to a file missing under media/ => KEEP + error (X:522).
8. Only then unlink (commit) or just list (dry-run).
9. os.sync, umount in finally; prints ONE JSON line {ok,deleted,kept,errors,committed} as last stdout line; ok = no errors. Exit 0/1.
10. Log to offload.log. Web trusts only that JSON; keep wipe a separate root program.

## 5. Pillow / Flask use -> Go
- Pillow ONLY in thumb_for W:496-499: open, convert RGB, thumbnail(320 box), JPEG q80. Go: shell out to the same ffmpeg already used for videos (`-vf scale=320:-1 -frames:v 1`, W:506) = 0 extra lines; or image/jpeg+image/png decode and a ~25-line box downscale, encode jpeg.Options{Quality:80}. Cache key sha256(rel:mtime_ns)[:32] (W:488) -> crypto/sha256.
- Flask: route patterns -> net/http ServeMux (Go 1.22 "GET /media/{rel...}"); jsonify/request.json -> encoding/json; request.form.getlist -> r.ParseForm; send_file conditional/Range -> http.ServeContent; as_attachment -> set Content-Disposition; guess_type -> mime.TypeByExtension (register .mkv .avi if missing, Pi has no /etc/mime.types guarantee); abort -> http.Error; after_this_request -> defer os.Remove; send_from_directory max_age -> Cache-Control header.
- waitress threads -> goroutines; http.Server WriteTimeout must be 0 for streams. zipfile ZIP_STORED -> archive/zip CreateHeader{Method: zip.Store}. secrets.token_hex -> crypto/rand. shutil.disk_usage -> syscall.Statfs. lsusb -> read /sys/bus/usb/devices/*/{idVendor,idProduct}. fcntl.flock -> syscall.Flock. os.sync -> syscall.Sync. Signals -> os/signal. v4l2-ctl --info and arecord -l stay external.

## 6. Traps (D = docs/camera-drive.md)
- Only one process may open /dev/video0; web never does; one ffmpeg, two outputs (D:194, L:8-15).
- Pick video node by v4l2-ctl card name (cam 3200/slim 3200/aiptek) + "capture", not number (L:103-119); ALSA by card name, fallback USB (L:122-137).
- ffmpeg: no -input_format (D:226); `-channels 1` not -ac (D:230, L:160); no -use_wallclock_as_timestamps, use `-af aresample=async=1:first_pts=0` (D:288, L:169); never re-encode live (D:234); h264_v4l2m2m invalid in capture pipeline/fragmented mp4 (D:243) but OK file->+faststart mp4; keep libx264 fallback.
- Matroska not AVI for recordings (D:247). Stop ffmpeg with SIGINT, wait up to 15 s, then TERM, KILL (L:203-216). KillMode=mixed (D:252, U:live 86-90).
- ffmpeg `-f mp4` mandatory with .part output (D:158, W:371). Start retries 4x with 4 s settle check (L:188-199).
- camera-live starts idle: no ffmpeg until preview/recording (D:117, L:27). Recording toggle = ffmpeg restart, ~4 s gap. Refuse record when <512 MiB free.
- RuntimeDirectory=camera-drive + Preserve=yes; do NOT add /run/camera-drive to ReadWritePaths (226/NAMESPACE); do NOT set CapabilityBoundingSet if sudo is used (D:257-275, U:web 11-37).
- Torn live.jpg: require FFD9 end (D:203). Snapshot and control files via tmp+rename.
- Camera clock dead: ingest time only; filenames repeat: hash identity; proxy cache and zip names must include dump folder (D:208-213, W:215,239).
- Offload mounts ro; wipe is a separate privileged program with hash rule, no override (D:95-106). Device found by USB ids, not /dev/sda.
- udev: no remove rule (systemctl in udev can deadlock); UDISKS_IGNORE; match partition not disk (UD).
- One encode at a time (512 MB RAM); cancel kills ffmpeg; export TTLs; 500 path cap; delete orphaned export-* at start.
- Streams: flush every frame, no gzip/timeouts; end idle stream after ~5 s. Reverse-proxied stream needs FlushInterval -1 (G proxy.go:30).
- UI: `[hidden]{display:none !important}` before any display rule (D:277); keep PAGE verbatim.
- Do not scan/disturb: net-fallback AP scan every 120 s only (fallback:28).

## 7. Rewrite steps (single static binary `camera-drive` with subcommands web|live|offload|wipe; keep script paths as symlinks so units/udev/sudo stay valid)
1. Shared pkg + camera-wipe + camera-offload (~450 Go): sha256 walk, mount/umount via exec, flock, index/status JSON, LED. Verify: loop-mounted FAT image, run offload twice (2nd = all skipped), plant unknown file, wipe dry-run/commit output byte-compatible with Python JSON; compare index.json.
2. camera-live (~330): device discovery, ffmpeg arg builder (diff argv against Python's), supervisor, control.json poll, snapshot, signals. Verify: argv golden test, unplug, SIGTERM leaves valid mkv (ffprobe).
3. web read side (~350): /, state, media, thumb, stream, live.jpg, play, control, power, wipe, zip + safe_media_path tests. Verify: curl against same /srv tree, Range, traversal tests (../, symlink), JSON diff vs Flask /api/state.
4. transcode + export jobs (~300): ensure_mp4, export formats, job TTL, cancel, mp4 Range. Verify: all 4 formats ffprobe-valid, cancel kills ffmpeg, restart cleans tmp.
5. Cutover: replace Pillow, go:embed PAGE, edit unit ExecStart, static build GOOS=linux GOARCH=arm64 CGO_ENABLED=0, delete Python + waitress/Flask/Pillow from provision. (~50 Go, mostly packaging)
Total ~1500 Go lines + 675 embedded HTML lines, plus ~300 test lines.

## 8. Old gateway reuse (hub: one admin password, systemd start/stop, poweroff). Deps there: toml, godbus, x/crypto, modernc sqlite (not stdlib; sqlite and users/grants/audit unnecessary here). Gateway has NO systemctl start/stop code: Run.Units/IdleStop only parsed (G manifest.go:33-40); only polkit rule exists.
- power.go:12-44 (44 lines): logind PowerOff/Reboot over D-Bus, `CanPowerOff`. Needs godbus; stdlib alternative `exec systemctl poweroff` with polkit rule. Keep as is if godbus acceptable.
- platform/rootfs/etc/polkit-1/rules.d/50-pms-gateway.rules:5-39 (tag path platform/...): allowlist units+verbs start/stop/restart via org.freedesktop.systemd1.manage-units, login1 power-off/reboot, NM actions; adapt user name and unit list. Lets Go call `systemctl start|stop X` (exec) with no sudo, works with NoNewPrivileges.
- auth.go:19-33,52-91 argon2id hash/verify (x/crypto); 93-103 newToken/tokenHash (stdlib, reuse as is); 105-152 limiter (stdlib, ~50 lines, reuse). For one admin password: store one hash in a file instead of store.go (309 lines sqlite, drop).
- app.go:23-27 cookie "__Host-pms" Secure HttpOnly (needs HTTPS: tls.go:26-115 self-signed cert, stdlib, reusable); app.go:90-133 session cache/idle+max expiry (simplify to in-memory map); app.go:252-264 originOK Sec-Fetch-Site CSRF check (stdlib, reuse verbatim); app.go:180-186 clientIP.
- web.go:52-81 withSession/withOwner, 120-176 handleLogin (limiter + dummy hash for timing + fresh session token), 238-264 handlePower (confirm=yes, audit, a.power injectable for tests) - reuse shape.
- proxy.go:17-80 httputil.ReverseProxy with FlushInterval:-1 (essential for MJPEG), strips session cookie, 502 page on upstream down. Reusable if hub proxies camera app on a port; manifest.go:111+ port range 8443-8450 validation only if keeping multi-service ports.
- config.go:28-70 TOML config + Duration: replace by a handful of flags/env to stay stdlib.
- Drop: access.go, store.go, user CLI (main.go:212-290), manifest levels/rules.
Estimated reusable: ~450 of 2500 gateway lines.
