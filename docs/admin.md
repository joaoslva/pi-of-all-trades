# Admin page

`http://<pi>/admin` shows Pi stats and reboot / power-off buttons behind one password.

Set or change the password over SSH (it is read from stdin, 8 characters minimum):

    ssh joao@<pi> sudo pihub passwd

This writes `/etc/pihub/admin.hash` (`iterations$salt$hash`, PBKDF2-SHA256, mode 0640 root:joao). Until it exists `/admin` answers 503 with that command. The hub reads the file on every login, so no restart is needed.

Measured on the Pi Zero 2 W: Go's `crypto/pbkdf2` takes about 8 microseconds per iteration (100000 iterations = 0.8 s), so 25000 iterations is about 200 ms. The count is stored in each hash; raise `pbkdf2Iterations` in `hub/auth.go` and run `pihub passwd` again to change it.

Sessions live in memory: a hub restart logs everyone out. They expire after 30 idle minutes. Power actions need a confirm step (a one-time nonce valid for 60 s).
