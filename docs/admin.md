# Admin page

`http://<pi>/admin` shows Pi stats and reboot / power-off buttons behind one password.

Set or change the password over SSH (it is read from stdin, 8 characters minimum):

    ssh joao@<pi> sudo pihub passwd

This writes `/var/lib/pihub/admin.hash` (`iterations$salt$hash`, PBKDF2-SHA256, mode 0600, owned by `--user`, default `joao`), by temp file and rename. It lives in the state dir because the hub runs with `ProtectSystem=strict` and must be able to write it for the password change below. Until it exists `/admin` answers 503 with that command. The hub reads the file on every login, so no restart is needed. `--hash-file` overrides the path (the verify checks use it with throwaway hashes). At startup, when the new file is missing and the old `/etc/pihub/admin.hash` exists, the hub copies it over once (0600) and logs one line; the old file is left alone.

## Account

- `POST /admin/api/password` with `{"current","new","hint"?}`: needs a session and counts against the login rate limit (5 attempts a minute per client). Wrong `current` is 401 and changes nothing; `new` must be 8 or more characters and differ from `current` (400). On success every other session is logged out, the caller's stays, and the answer is 204.
- Password hint: `<state-dir>/password-hint.txt`, at most 120 characters, no control characters. `GET /admin/api/hint` needs a session (the login screen never shows it; only the password card does) and answers `{"hint":""}` when there is none, with `Cache-Control: no-store`. It is changed only through the password endpoint: `hint` absent keeps it, `""` clears it. A hint containing the current or new password (any case) is refused.
- `GET /admin/api/system`: hostname, IPv4 addresses per interface, OS, kernel, architecture, hub revision (first 8 of the git commit, plus `modified`), boot time, time and zone, and `camera` (`state`, `last_result`, `updated` from `<--camera-dir>/status.json`, null when missing, plus `free` and `total` bytes).
- `GET /admin/api/stats` also has `cpu_percent`: busy share since the previous call (the first call averages since boot).

Measured on the Pi Zero 2 W: Go's `crypto/pbkdf2` takes about 8 microseconds per iteration (100000 iterations = 0.8 s), so 25000 iterations is about 200 ms. The count is stored in each hash; raise `pbkdf2Iterations` in `hub/auth.go` and run `pihub passwd` again to change it.

Sessions live in memory: a hub restart logs everyone out. They expire after 30 idle minutes. Power actions need a confirm step (a one-time nonce valid for 60 s).

## Services

The Services section lists every service with its class, units and memory (`n/a` when the kernel has cgroup memory accounting off, as on this Pi). Exclusive services have a switch button: the dialog names what will stop and what will start, and a one-time nonce (bound to that exact action) confirms it. Background services are read-only. A banner shows overload warnings; it never blocks anything. See `docs/adding-a-service.md`.

## Wi-Fi

The Wi-Fi section shows the mode (hotspot or client), the SSID and the address, lists saved networks (Connect, Forget), nearby networks (Scan, client mode only), and an add form. Switching networks arms a revert first; press **Keep this network** at the new address or the Pi goes back to the previous one after `--wifi-revert` seconds (default 30). The hotspot is raised only by the fallback timer, which the page shows read-only and never controls. Details, values and the manual hotspot test: `docs/network.md`.
