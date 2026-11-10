#!/usr/bin/env bash
#
# Pi setup that is state rather than files: services to disable, the
# hostname, leftovers from the SD card recovery and from pi-mobile-server.
# The files come from platform/deploy.sh, which must run first.
#
# Runs on the Pi as root, reading the script from stdin:
#   ssh -i ~/.ssh/pi_camera_drive joao@192.168.1.206 sudo bash -s < platform/provision/base.sh
#   ... sudo bash -s -- <hostname> < platform/provision/base.sh
#
# Safe to re-run: every step looks before it changes anything.

set -euo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

NEW_HOSTNAME="${1:-pi-of-all-trades}"
ADMIN_USER="${ADMIN_USER:-joao}"

step() { printf '\n==> %s\n' "$1"; }
did()  { printf '  %s\n' "$1"; }
die()  { printf 'error: %s\n' "$1" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run as root"
[ -e /etc/cloud/cloud-init.disabled ] \
  || die "platform files are missing: run platform/deploy.sh first"
[[ "$NEW_HOSTNAME" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]] || die "invalid hostname '$NEW_HOSTNAME'"

step "cloud-init leftovers"
# cloud-init.disabled (from deploy.sh) stops it running. The WiFi it rendered
# lives on as a netplan profile duplicating camera-drive-wifi, and it would
# autoconnect on any spare WiFi interface.
kept=0
while IFS=: read -r uuid file active; do
  case "$file" in /run/NetworkManager/system-connections/netplan-*) ;; *) continue ;; esac
  if [ "$active" = yes ]; then
    did "!! keeping netplan profile $uuid: it is the active connection"
    kept=1
    continue
  fi
  nmcli connection delete "$uuid" >/dev/null
  did "deleted netplan profile $uuid"
done < <(nmcli -t -f UUID,FILENAME,ACTIVE connection show)
shopt -s nullglob
yamls=(/etc/netplan/*.yaml)
if [ ${#yamls[@]} -gt 0 ] && [ "$kept" = 0 ]; then
  # These hold the WiFi password in plain text.
  rm -f "${yamls[@]}"
  nmcli connection reload
  did "removed ${yamls[*]}"
fi

step "hostname"
hosts="127.0.0.1	localhost
127.0.1.1	$NEW_HOSTNAME
::1		localhost ip6-localhost ip6-loopback
ff02::1		ip6-allnodes
ff02::2		ip6-allrouters"
if [ "$(cat /etc/hosts)" != "$hosts" ]; then
  printf '%s\n' "$hosts" > /etc/hosts
  did "/etc/hosts rewritten"
fi
old="$(hostnamectl hostname)"
if [ "$old" != "$NEW_HOSTNAME" ]; then
  hostnamectl hostname "$NEW_HOSTNAME"
  systemctl try-restart avahi-daemon.service
  did "renamed $old -> $NEW_HOSTNAME"
fi
did "hostname is $(hostnamectl hostname)"

step "services this box does not need"
for u in ModemManager.service bluetooth.service; do
  if systemctl is-enabled -q "$u" 2>/dev/null || systemctl is-active -q "$u"; then
    systemctl disable --now -q "$u"
    did "disabled $u"
  fi
done

step "pi-mobile-server leftovers"
# The multi-service detour (Sept 2026, tag pi-mobile-server-final) added a
# login gateway, its user and polkit rules, a firewall and the memory cgroup.
# The camera app is reached directly again, on port 80.
removed=0
if [ -e /etc/systemd/system/pms-gateway.service ]; then
  systemctl disable --now -q pms-gateway.service || true
  rm -f /etc/systemd/system/pms-gateway.service
  removed=1
fi
for f in /usr/local/bin/pms-gateway /etc/pms /var/lib/pms-gateway /var/lib/private/pms-gateway \
         /etc/polkit-1/rules.d/50-pms-gateway.rules /etc/sysusers.d/pms-gateway.conf; do
  if [ -e "$f" ]; then rm -rf "$f"; did "removed $f"; removed=1; fi
done
if id pms-gateway >/dev/null 2>&1; then
  userdel pms-gateway
  did "removed user pms-gateway"
fi
if [ -e /etc/systemd/system/nftables.service.d/pms.conf ]; then
  # Stop while the drop-in still limits the stop to our own table, so
  # NetworkManager's hotspot tables survive.
  systemctl disable --now -q nftables.service || true
  rm -rf /etc/systemd/system/nftables.service.d
  removed=1
  did "firewall stopped and disabled"
fi
nft destroy table inet pms 2>/dev/null || true
if grep -q 'table inet pms' /etc/nftables.conf 2>/dev/null; then
  # Debian's stock file, as shipped by the nftables package.
  cat > /etc/nftables.conf <<'NFT'
#!/usr/sbin/nft -f

flush ruleset

table inet filter {
	chain input {
		type filter hook input priority filter;
	}
	chain forward {
		type filter hook forward priority filter;
	}
	chain output {
		type filter hook output priority filter;
	}
}
NFT
  chmod 755 /etc/nftables.conf
  did "/etc/nftables.conf back to Debian's stock file"
fi
CMDLINE=/boot/firmware/cmdline.txt
if grep -qw 'cgroup_enable=memory' "$CMDLINE"; then
  sed -i '1s/[[:space:]]*cgroup_enable=memory//' "$CMDLINE"
  did "removed cgroup_enable=memory from $CMDLINE (reboot to apply)"
fi
if [ -e "$CMDLINE.pre-pms" ] && cmp -s "$CMDLINE" "$CMDLINE.pre-pms"; then
  rm -f "$CMDLINE.pre-pms"
fi
if [ "$removed" = 1 ]; then
  systemctl daemon-reload
  # Port 80 was the gateway's; the camera app takes it back.
  systemctl try-restart camera-drive-web.service
  did "camera-drive-web restarted"
fi
did "nothing of it left"

step "leftovers from the SD card recovery"
# netreport sleeps 45 s inside a oneshot, holding boot open for ~50 s.
if [ -e /etc/systemd/system/netreport.service ]; then
  systemctl disable -q netreport.service || true
  rm -f /etc/systemd/system/netreport.service /etc/systemd/system/multi-user.target.wants/netreport.service
  systemctl daemon-reload
  did "removed netreport.service"
fi
for f in /usr/local/sbin/netreport.sh /usr/local/sbin/ap-diag.sh \
         /boot/firmware/netreport.txt /srv/camera-drive/apdiag.log; do
  if [ -e "$f" ]; then rm -f "$f"; did "removed $f"; fi
done
if [ -d /usr/local/bin/__pycache__ ]; then
  rm -rf /usr/local/bin/__pycache__
  did "removed /usr/local/bin/__pycache__"
fi

step "sudo rules"
# Three files granted the same NOPASSWD rule. Keep one, so dropping it later
# happens in one place. Only when that one is really there.
if grep -qE "^$ADMIN_USER ALL=\(ALL\) NOPASSWD: ?ALL$" "/etc/sudoers.d/010-$ADMIN_USER" 2>/dev/null; then
  for f in /etc/sudoers.d/010_pi-nopasswd /etc/sudoers.d/90-cloud-init-users; do
    if [ -e "$f" ]; then rm -f "$f"; did "removed duplicate $f"; fi
  done
fi
visudo -cq || die "sudoers does not parse"
did "$(sudo -l -U "$ADMIN_USER" | grep -c NOPASSWD) NOPASSWD rule(s) for $ADMIN_USER"

step "local password"
# An empty password lets anyone at the serial console or tty1 log in, and
# lets any local user `su` to the admin. SSH is key-only, so lock it.
if [ "$(passwd -S "$ADMIN_USER" | awk '{print $2}')" = NP ]; then
  usermod -p '!' "$ADMIN_USER"
  did "$ADMIN_USER had an empty password; now locked"
fi
did "$(passwd -S "$ADMIN_USER")"

step "anything in system paths not owned by root"
# deploy.sh fixes the directories above the files it ships; this catches the rest.
find / -xdev \( -path /home -o -path /srv -o -path /tmp -o -path /var/tmp -o -path /var/lib \
  -o -path /var/cache -o -path /var/log -o -path /var/spool -o -path /var/mail \) -prune \
  -o \( ! -user root -o -type d -perm /022 ! -perm -1000 \) -print | sed 's/^/  !! /'

step "done"
if grep -qw 'cgroup_enable=memory' /proc/cmdline; then
  did "reboot to finish: sudo systemctl reboot"
else
  did "no reboot needed"
fi
