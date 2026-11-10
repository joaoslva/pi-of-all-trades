#!/usr/bin/env bash
#
# Deterministic provisioning, bypassing cloud-init entirely.
#
# Diagnoses what the last boot actually achieved, then writes -- by hand --
# the NetworkManager wifi profile, the user, the SSH key, and the service
# symlinks. Nothing here depends on datasources, instance identity, or
# once-per-instance semantics. It either works or fails loudly.
#
# Reads the wifi password out of the boot partition's network-config so the
# secret never has to be retyped and is never printed.
#
# Run with: sudo platform/provision/fix-pi-card.sh

set -uo pipefail

ROOT_DEV="${ROOT_DEV:-/dev/sda2}"
BOOT="${BOOT:-/run/media/joao/bootfs}"
MP=/mnt/pi-fix
USER_NAME=joao
PUBKEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFaDNJnT5Cj77YMdKNzDkNwMk554EeP1EREnlhtjbyLM camera-drive-laptop"

hdr() { printf '\n########## %s\n' "$1"; }
die() { printf '\nERROR: %s\n' "$1" >&2; mountpoint -q "$MP" && umount "$MP"; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run me with sudo"
[ -d "$BOOT" ] || die "boot partition not mounted at $BOOT -- is the card in the laptop?"

mkdir -p "$MP"
mountpoint -q "$MP" && umount "$MP"
mount "$ROOT_DEV" "$MP" || die "could not mount $ROOT_DEV read-write"

############################################################
# PART 1 -- what did the last boot actually do?
############################################################

hdr "did cloud-init create the user?"
if grep -qE "^${USER_NAME}:" "$MP/etc/passwd"; then
  echo "  YES: $(grep -E "^${USER_NAME}:" "$MP/etc/passwd")"
else
  echo "  NO -- user $USER_NAME does not exist"
fi

hdr "network config actually rendered anywhere?"
echo "--- /etc/NetworkManager/system-connections/ ---"
ls -la "$MP/etc/NetworkManager/system-connections/" 2>&1 | tail -n +2
echo "--- /etc/netplan/ ---"
ls -la "$MP/etc/netplan/" 2>&1 | tail -n +2
echo "--- is netplan even installed? ---"
ls "$MP/usr/sbin/netplan" "$MP/usr/bin/netplan" 2>/dev/null || echo "  netplan binary NOT present (this is the likely root cause)"

hdr "why did cloud-final fail?"
grep -iE 'cloud-final|runcmd|Traceback|CRITICAL|ERROR' "$MP/var/log/cloud-init.log" 2>/dev/null | tail -15
echo "--- cloud-init-output tail ---"
tail -25 "$MP/var/log/cloud-init-output.log" 2>/dev/null || echo "  no output log"

hdr "were the packages installed?"
for p in ffmpeg v4l-utils rsync avahi-daemon; do
  if chroot "$MP" dpkg -s "$p" >/dev/null 2>&1; then echo "  ok      $p"; else echo "  MISSING $p"; fi
done 2>/dev/null || echo "  (chroot unavailable on this arch, checking dpkg list instead)"
grep -cE '^Package: (ffmpeg|rsync|avahi-daemon)$' "$MP/var/lib/dpkg/status" 2>/dev/null | xargs -I{} echo "  {} of 3 key packages present in dpkg status"

############################################################
# PART 2 -- fix it, deterministically
############################################################

hdr "FIX: user account"
if grep -qE "^${USER_NAME}:" "$MP/etc/passwd"; then
  UID_N=$(grep -E "^${USER_NAME}:" "$MP/etc/passwd" | cut -d: -f3)
  GID_N=$(grep -E "^${USER_NAME}:" "$MP/etc/passwd" | cut -d: -f4)
  echo "  user exists (uid=$UID_N gid=$GID_N), leaving alone"
else
  UID_N=1000; GID_N=1000
  while cut -d: -f3 "$MP/etc/passwd" | grep -qx "$UID_N"; do UID_N=$((UID_N+1)); done
  while cut -d: -f3 "$MP/etc/group"  | grep -qx "$GID_N"; do GID_N=$((GID_N+1)); done
  echo "  creating $USER_NAME uid=$UID_N gid=$GID_N"
  printf '%s:x:%s:\n' "$USER_NAME" "$GID_N" >> "$MP/etc/group"
  printf '%s:x:%s:%s:Joao:/home/%s:/bin/bash\n' "$USER_NAME" "$UID_N" "$GID_N" "$USER_NAME" >> "$MP/etc/passwd"
  # '!' = locked password, key-only login. 20000 = arbitrary recent "last changed" day.
  printf '%s:!:20000:0:99999:7:::\n' "$USER_NAME" >> "$MP/etc/shadow"
fi

echo "  adding to supplementary groups"
for g in adm sudo users video audio plugdev netdev dialout gpio; do
  grep -qE "^${g}:" "$MP/etc/group" || continue
  if grep -E "^${g}:" "$MP/etc/group" | cut -d: -f4 | tr ',' '\n' | grep -qx "$USER_NAME"; then
    continue
  fi
  sed -i -E "s/^(${g}:[^:]*:[^:]*:)(.*)$/\1\2,${USER_NAME}/; s/^(${g}:[^:]*:[^:]*:),/\1/" "$MP/etc/group"
  echo "    + $g"
done

printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$USER_NAME" > "$MP/etc/sudoers.d/010-${USER_NAME}"
chmod 0440 "$MP/etc/sudoers.d/010-${USER_NAME}"

hdr "FIX: ssh key"
install -d -m 0700 -o "$UID_N" -g "$GID_N" "$MP/home/$USER_NAME"
install -d -m 0700 -o "$UID_N" -g "$GID_N" "$MP/home/$USER_NAME/.ssh"
printf '%s\n' "$PUBKEY" > "$MP/home/$USER_NAME/.ssh/authorized_keys"
chmod 0600 "$MP/home/$USER_NAME/.ssh/authorized_keys"
chown "$UID_N:$GID_N" "$MP/home/$USER_NAME/.ssh/authorized_keys"
echo "  authorized_keys written"

hdr "FIX: NetworkManager wifi profile"
SSID=$(grep -oP '^\s+"\K[^"]+(?=":\s*$)' "$BOOT/network-config" | head -1)
PSK=$(grep -oP '^\s+password:\s*"\K[^"]*' "$BOOT/network-config" | head -1)
[ -n "$SSID" ] || die "could not parse SSID out of $BOOT/network-config"
[ -n "$PSK" ] || die "could not parse wifi password out of $BOOT/network-config"
echo "  ssid: $SSID   (password read from boot partition, not printed)"

NMDIR="$MP/etc/NetworkManager/system-connections"
mkdir -p "$NMDIR"
cat > "$NMDIR/camera-drive-wifi.nmconnection" <<EOF
[connection]
id=camera-drive-wifi
type=wifi
interface-name=wlan0
autoconnect=true
autoconnect-priority=100

[wifi]
mode=infrastructure
ssid=${SSID}

[wifi-security]
key-mgmt=wpa-psk
psk=${PSK}

[ipv4]
method=auto

[ipv6]
method=auto
addr-gen-mode=default
EOF
# NetworkManager REFUSES to load keyfiles that are group/world readable.
chmod 600 "$NMDIR/camera-drive-wifi.nmconnection"
chown 0:0 "$NMDIR/camera-drive-wifi.nmconnection"
echo "  profile written, mode 600 root:root"

# rfkill can leave wlan0 soft-blocked; make sure it is not.
rm -f "$MP/var/lib/systemd/rfkill/"*wlan* 2>/dev/null

hdr "FIX: services"
SSH_UNIT=""
for c in "usr/lib/systemd/system/ssh.service" "lib/systemd/system/ssh.service"; do
  [ -f "$MP/$c" ] && SSH_UNIT="/$c"
done
if [ -n "$SSH_UNIT" ]; then
  mkdir -p "$MP/etc/systemd/system/multi-user.target.wants"
  ln -sf "$SSH_UNIT" "$MP/etc/systemd/system/multi-user.target.wants/ssh.service"
  echo "  ssh.service enabled -> $SSH_UNIT"
else
  echo "  !! ssh.service unit not found"
fi
rm -f "$MP/etc/ssh/sshd_not_to_be_run"

if [ -e "$MP/etc/systemd/system/multi-user.target.wants/userconfig.service" ]; then
  rm -f "$MP/etc/systemd/system/multi-user.target.wants/userconfig.service"
  echo "  userconfig wizard disabled"
else
  echo "  userconfig wizard already absent"
fi
# The wizard also hijacks tty1 via an autologin drop-in on some images.
rm -rf "$MP/etc/systemd/system/getty@tty1.service.d/autologin.conf" 2>/dev/null

hdr "FIX: media directory"
install -d -m 0755 -o "$UID_N" -g "$GID_N" "$MP/srv/camera-drive/media"

############################################################
# PART 3 -- a debug channel that survives a headless failure
############################################################

hdr "installing boot-time network report onto the BOOT partition"
# vfat, so we can read it from the laptop with no sudo and no mounting games.
cat > "$MP/usr/local/sbin/netreport.sh" <<'EOF'
#!/bin/bash
# Dumps network state to the boot partition 45s after boot so a headless
# failure can be diagnosed by simply reading the SD card.
sleep 45
OUT=/boot/firmware/netreport.txt
{
  echo "=== $(date -Is) uptime $(cut -d. -f1 /proc/uptime)s ==="
  echo "--- hostname ---"; hostname
  echo "--- ip addr ---"; ip -brief addr
  echo "--- wifi ---"; nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device 2>&1
  echo "--- active connections ---"; nmcli -t -f NAME,DEVICE,STATE connection show --active 2>&1
  echo "--- visible SSIDs ---"; nmcli -t -f SSID,SIGNAL,FREQ device wifi list 2>&1 | head -20
  echo "--- rfkill ---"; rfkill list 2>&1
  echo "--- regdom ---"; iw reg get 2>&1 | head -5
  echo "--- ssh listening? ---"; ss -lntp 2>/dev/null | grep -E ':22\b' || echo "sshd NOT listening"
  echo "--- users with uid>=1000 ---"; awk -F: '$3>=1000 && $3<65534 {print $1, $3}' /etc/passwd
  echo "--- NetworkManager log ---"; journalctl -u NetworkManager -b --no-pager 2>&1 | tail -40
  echo "--- cloud-init status ---"; cloud-init status --long 2>&1
} > "$OUT" 2>&1
sync
EOF
chmod 0755 "$MP/usr/local/sbin/netreport.sh"

cat > "$MP/etc/systemd/system/netreport.service" <<'EOF'
[Unit]
Description=Write a network diagnostic report to the boot partition
After=network.target NetworkManager.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/netreport.sh

[Install]
WantedBy=multi-user.target
EOF
ln -sf /etc/systemd/system/netreport.service \
       "$MP/etc/systemd/system/multi-user.target.wants/netreport.service"
echo "  netreport.service enabled -- writes /boot/firmware/netreport.txt each boot"

hdr "verification"
echo "  passwd entry : $(grep -E "^${USER_NAME}:" "$MP/etc/passwd")"
echo "  shadow field : $(grep -E "^${USER_NAME}:" "$MP/etc/shadow" | cut -d: -f2) (! means key-only)"
echo "  authorized_keys: $(wc -l < "$MP/home/$USER_NAME/.ssh/authorized_keys") line(s), mode $(stat -c%a "$MP/home/$USER_NAME/.ssh/authorized_keys")"
echo "  wifi profile : mode $(stat -c%a "$NMDIR/camera-drive-wifi.nmconnection") owner $(stat -c%U:%G "$NMDIR/camera-drive-wifi.nmconnection")"
echo "  ssh enabled  : $([ -L "$MP/etc/systemd/system/multi-user.target.wants/ssh.service" ] && echo yes || echo NO)"
echo "  wizard gone  : $([ -e "$MP/etc/systemd/system/multi-user.target.wants/userconfig.service" ] && echo NO || echo yes)"
echo "  groups       : $(grep -E "^(sudo|video|audio|netdev):" "$MP/etc/group" | grep "$USER_NAME" | cut -d: -f1 | tr '\n' ' ')"

sync
umount "$MP" && echo && echo "root partition unmounted cleanly."
echo
echo "Now: sync && udisksctl unmount -b /dev/sda1, then boot the Pi."
