# Admin page

`http://<pi>/admin` shows Pi stats and reboot / power-off buttons behind one password.

Set or change the password over SSH (it is read from stdin, 8 characters minimum):

    ssh joao@<pi> sudo pihub passwd

This writes `/etc/pihub/admin.hash` (`iterations$salt$hash`, PBKDF2-SHA256, mode 0640 root:joao). Until it exists `/admin` answers 503 with that command. The hub reads the file on every login, so no restart is needed.

Measured on the Pi Zero 2 W: Go's `crypto/pbkdf2` takes about 8 microseconds per iteration (100000 iterations = 0.8 s), so 25000 iterations is about 200 ms. The count is stored in each hash; raise `pbkdf2Iterations` in `hub/auth.go` and run `pihub passwd` again to change it.

Sessions live in memory: a hub restart logs everyone out. They expire after 30 idle minutes. Power actions need a confirm step (a one-time nonce valid for 60 s).

## Services

The Services section lists every service with its class, units and memory (`n/a` when the kernel has cgroup memory accounting off, as on this Pi). Exclusive services have a switch button: the dialog names what will stop and what will start, and a one-time nonce (bound to that exact action) confirms it. Background services are read-only. A banner shows overload warnings; it never blocks anything. See `docs/adding-a-service.md`.

## Wi-Fi

The Wi-Fi section shows the mode (client, hotspot turned on by you, hotspot raised automatically, or not connected), the SSID and address, the last working network, and lists saved networks (**Switch**, Forget), nearby networks (Scan, client mode only), and an add form. **Switch** names the current and the target network; it arms a revert first, and press **Keep this network** at the new address or the Pi goes back after `--wifi-revert` seconds (default 30). If the new network fails to connect, the Pi goes back to the previous one at once and the message says where it ended. **Turn on hotspot** warns that the Pi leaves the current network and gives the hotspot's SSID and `http://10.42.0.1/admin`; the fallback leaves a hotspot you turned on alone until **Leave hotspot** (back to the last working network) or a reboot. The fallback timer is shown read-only and never controlled. Details, values and the manual hotspot test: `docs/network.md`.
