#!/bin/bash
# Builds a flashable Freeway Pi image from stock Raspberry Pi OS Lite.
#
# It does not describe an installation. install.sh already does that, it runs
# on the real box every deploy, and a second description in a different format
# would drift from it — so this mounts a stock image, chroots in, and runs the
# same script. Everything about *what* gets installed stays in one place.
#
#   sudo ./build-image.sh arm64     Pi 3 and up
#   sudo ./build-image.sh armhf     Pi 2 Model B and up, and a fallback
#
# Needs a static qemu and binfmt so arm binaries run under the chroot:
#
#   Debian: sudo apt install qemu-user-static binfmt-support
#   Ubuntu: sudo apt install qemu-user qemu-user-binfmt
#
# The result is dist/freeway-pi-<arch>-<version>.img.xz, and nothing in it is
# personal: no keys, no passwords, no network settings, no machine id. The
# login account exists and is inert until somebody adds a key.
set -euo pipefail

ARCH=${1:-arm64}
HOSTNAME_=${HOSTNAME_:-freeway}
LOGIN_USER=${LOGIN_USER:-fwadmin}
GROW_MB=${GROW_MB:-900}

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
cache=${CACHE:-$repo/.image-cache}
out=$repo/dist

case "$ARCH" in
arm64) BINARY=freewayd-linux-arm64; QEMU_NAMES="qemu-aarch64-static qemu-aarch64"
       URL=https://downloads.raspberrypi.com/raspios_lite_arm64_latest ;;
armhf) BINARY=freewayd-linux-armv7; QEMU_NAMES="qemu-arm-static qemu-arm"
       URL=https://downloads.raspberrypi.com/raspios_lite_armhf_latest ;;
*) echo "usage: build-image.sh [arm64|armhf]" >&2; exit 2 ;;
esac

# Debian ships qemu-user-static with the interpreters as qemu-<arch>-static;
# Ubuntu calls the package qemu-user and drops the suffix. Either is fine as
# long as it is statically linked, which is the whole requirement: the chroot
# has no x86 libraries to lend it.
QEMU=""
for n in $QEMU_NAMES; do
	if p=$(command -v "$n" 2>/dev/null); then QEMU=$p; break; fi
done

[ "$(id -u)" -eq 0 ] || { echo "run this as root" >&2; exit 1; }
if [ -z "$QEMU" ]; then
	echo "no qemu interpreter for $ARCH (looked for: $QEMU_NAMES)" >&2
	echo "  Debian: apt install qemu-user-static binfmt-support" >&2
	echo "  Ubuntu: apt install qemu-user qemu-user-binfmt" >&2
	exit 1
fi
# "statically linked" on Debian, "static-pie linked" on Ubuntu — both mean the
# interpreter carries everything it needs, which is the whole requirement: the
# chroot has no x86 libraries to lend it.
if ! file -L "$QEMU" 2>/dev/null | grep -qE "static(ally| -pie|-pie) linked"; then
	echo "$QEMU is not statically linked; the chroot has no libraries to lend it" >&2
	exit 1
fi
[ -f "$out/$BINARY" ] || { echo "missing $out/$BINARY — run make dist first" >&2; exit 1; }

say() { printf '\n==> %s\n' "$1"; }

# The size in sectors of partition N, read back from the table rather than
# remembered from what we asked for.
part_sectors() {
	sfdisk -J "$1" | python3 -c "import json,sys; print(json.load(sys.stdin)['partitiontable']['partitions'][$2-1]['size'])"
}
part_end() {
	sfdisk -J "$1" | python3 -c "import json,sys; p=json.load(sys.stdin)['partitiontable']['partitions'][$2-1]; print(p['start']+p['size'])"
}

# losetup -d returns before the kernel has finished with the device, and
# sfdisk then cannot open the file exclusively. Waiting is the difference
# between a partition table that was rewritten and one that quietly was not.
detach_loop() {
	losetup -d "$1" 2>/dev/null || true
	for _ in $(seq 1 50); do
		losetup -j "$img" 2>/dev/null | grep -q . || return 0
		sleep 0.1
	done
	echo "loop device for $img is still attached" >&2
	return 1
}

# Every partition has to end inside the file. A table that claims more than
# the file holds produces an image that flashes and does not boot, and neither
# xz nor dd will say a word about it.
check_fits() {
	local img=$1 size
	size=$(stat -c%s "$img")
	sfdisk -J "$img" | python3 -c "
import json, sys
size = $size
bad = False
for p in json.load(sys.stdin)['partitiontable']['partitions']:
    end = (p['start'] + p['size']) * 512
    if end > size:
        print('  %s ends %d bytes past the end of the image' % (p['node'], end - size))
        bad = True
sys.exit(1 if bad else 0)
" || { echo "the partition table does not fit the image" >&2; return 1; }
}

# --- the stock image -------------------------------------------------------

mkdir -p "$cache" "$out"
base=$cache/raspios-$ARCH.img
if [ ! -f "$base" ]; then
	say "downloading Raspberry Pi OS Lite ($ARCH)"
	curl -fL --progress-bar "$URL" -o "$base.xz"
	say "unpacking"
	xz -dk --stdout "$base.xz" > "$base.tmp"
	mv "$base.tmp" "$base"
fi

version=$(git -C "$repo" describe --tags --always --dirty 2>/dev/null || echo unknown)
img=$out/freeway-pi-$ARCH-$version.img
say "copying the stock image"
cp --reflink=auto "$base" "$img"

# Room for what we are about to add, plus enough for the first update.
say "growing the root filesystem by ${GROW_MB} MB"
before=$(part_sectors "$img" 2)
truncate -s "+${GROW_MB}M" "$img"
# No "|| true" anywhere near this. A grow that silently did not happen leaves
# the install to run out of room later, for reasons nothing explains.
echo ",+" | sfdisk --no-reread -N 2 "$img" >/dev/null
after=$(part_sectors "$img" 2)
[ "$after" -gt "$before" ] || { echo "the root partition did not grow" >&2; exit 1; }
check_fits "$img"

# --- mount -----------------------------------------------------------------

loop=$(losetup --show -fP "$img")
cleanup() {
	set +e
	umount -R "$mnt/dev/pts" "$mnt/dev" "$mnt/proc" "$mnt/sys" "$mnt/boot/firmware" "$mnt" 2>/dev/null
	rmdir "$mnt" 2>/dev/null
	losetup -d "$loop" 2>/dev/null
}
trap cleanup EXIT

e2fsck -pf "${loop}p2" >/dev/null 2>&1 || true
resize2fs "${loop}p2" >/dev/null

mnt=$(mktemp -d)
mount "${loop}p2" "$mnt"
mount "${loop}p1" "$mnt/boot/firmware"
mount -t proc none "$mnt/proc"
mount -t sysfs none "$mnt/sys"
mount -o bind /dev "$mnt/dev"
# debconf wants a pty. Without this every apt call in the chroot prints
# "Can not write log (Is /dev/pts mounted?)" and configures packages blind.
mount -t devpts none "$mnt/dev/pts" 2>/dev/null || mount -o bind /dev/pts "$mnt/dev/pts"
# Copied in as a fallback. With binfmt's F flag the kernel already holds the
# interpreter open and the chroot never needs its own copy, but not every host
# registers it that way, and a stray binary is cheaper than a build that dies
# halfway with "exec format error".
cp "$QEMU" "$mnt/usr/bin/" 2>/dev/null || true

# --- what goes in ----------------------------------------------------------

say "copying Freeway Pi in"
install -d -m 0755 "$mnt/tmp/freeway-install"
cp "$out/$BINARY" "$here"/* "$mnt/tmp/freeway-install/"
chmod +x "$mnt/tmp/freeway-install/"*.sh "$mnt/tmp/freeway-install/freeway-helper" \
	"$mnt/tmp/freeway-install/freeway-unwind" "$mnt/tmp/freeway-install/freeway-card"

cat > "$mnt/tmp/freeway-build.sh" <<INNER
#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "==> hostname"
echo "$HOSTNAME_" > /etc/hostname
sed -i "s/127.0.1.1.*/127.0.1.1\t$HOSTNAME_/" /etc/hosts

echo "==> login account"
# One account, inert. No password, no keys, and locked — so the image carries
# no credential at all and stays useless to anybody who flashes it until they
# add a key of their own. Passwordless sudo, because an account reachable only
# by a key somebody deliberately installed has nothing to gain from a prompt.
if ! getent passwd $LOGIN_USER >/dev/null; then
	existing=\$(getent passwd 1000 | cut -d: -f1 || true)
	if [ -n "\$existing" ]; then
		usermod -l $LOGIN_USER "\$existing"
		usermod -m -d /home/$LOGIN_USER $LOGIN_USER || true
		groupmod -n $LOGIN_USER "\$existing" || true
	else
		adduser --disabled-password --gecos "" --uid 1000 $LOGIN_USER
	fi
fi
# Raspberry Pi OS ships a placeholder account on uid 1000 with
# /usr/sbin/nologin, and its own userconf sets a shell when somebody
# configures it. Renaming it is not configuring it — without this, ssh
# connects and the session closes again immediately, which looks like a key
# problem and is not.
usermod -s /bin/bash $LOGIN_USER
passwd -l $LOGIN_USER >/dev/null
usermod -aG sudo,dialout,adm,plugdev $LOGIN_USER 2>/dev/null || true
echo "$LOGIN_USER ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/010-$LOGIN_USER
chmod 0440 /etc/sudoers.d/010-$LOGIN_USER

# The first-boot wizard would ask for an account on a console nobody is
# watching. We have made one.
systemctl disable userconfig.service 2>/dev/null || true
rm -f /etc/systemd/system/multi-user.target.wants/userconfig.service

echo "==> packages"
apt-get update -qq
apt-get install -y -qq nftables network-manager avahi-daemon
systemctl enable ssh

echo "==> freeway pi"
cd /tmp/freeway-install
./install.sh $BINARY

echo "==> ssh: closed, and key-only when it is opened"
# Closed on a fresh image. Most owners will never use ssh, and a port that
# answers on every box for a feature nobody asked for is exposure with nothing
# on the other side of the scale. The way back in when the interface is not
# answering is the card — ssh=30 in freeway.txt — which is read before
# freewayd starts and does not need it to.
#
# Only the image. A box that had install.sh run on it was almost certainly
# reached over ssh to do it, and closing the port under the person who just
# used it is not hardening; the helper's own default stays open for them.
install -d -m 0700 -o root -g root /var/lib/freeway-helper
printf 'closed\n' > /var/lib/freeway-helper/ssh-policy

# Key-only, which the README promises and stock Raspberry Pi OS does not
# deliver: it ships with password authentication on. The login account's
# password is locked, so today nobody could walk through that door — but a
# closed-down box should not advertise it, and "key-only" should be true
# rather than true by accident.
#
# And no banner. Raspberry Pi OS's userconf leaves one saying ssh may not work
# until a user has been set up, which on this image is false: the account
# exists, it simply has no key yet.
#
# The file sorts before the distribution's own drop-ins on purpose: sshd takes
# the first value it finds for each keyword, not the last.
install -d -m 0755 /etc/ssh/sshd_config.d
cat > /etc/ssh/sshd_config.d/10-freeway.conf <<'SSHD'
# Freeway Pi: public keys only. See packaging/build-image.sh.
PasswordAuthentication no
KbdInteractiveAuthentication no
Banner none
SSHD
first=\$(ls /etc/ssh/sshd_config.d/ | head -1)
[ "\$first" = 10-freeway.conf ] || { echo "sshd would read \$first before ours" >&2; exit 1; }

echo "==> trimming what a ventilation controller has no use for"
# Raspberry Pi OS Lite is built for a Pi somebody sits in front of: a camera
# stack, bluetooth, a C compiler, GPIO libraries for Python, a remote-access
# agent. Every package here is one more that needs patching and one more line
# on the System page asking to be updated — and an owner has no way to tell
# that mkvtoolnix is harmless and irrelevant rather than something to worry
# about. About three hundred megabytes and 180 packages go.
#
# Only listed packages are named; apt removes what then has nothing left to
# need it. The list is filtered to what is installed, because the 32- and
# 64-bit images do not carry the same kernels.
#
# Kept on purpose, because something here depends on them: rsync (the boot
# firmware copies itself into place with it), kms++-utils and net-tools
# (raspi-utils, which the bootloader updater needs), rpi-loop-utils (swap).
# The USB wifi firmware stays too: a Pi 2 has no wifi of its own, so wifi on
# one means a dongle.
#
# Last, after install.sh and not before it. Trimmed earlier, anything the
# stock image lacks but install.sh adds — unattended-upgrades — is not there to
# hold its dependencies, so they are removed as orphans and then fetched
# straight back; and the check below would blame the trim for a package that
# had simply not been installed yet. Here the box is complete, so autoremove
# can only take what nothing needs.
#
# cloud-init goes. It is what Raspberry Pi Imager's settings dialog writes to,
# and keeping it would mean two ways of configuring a box that can disagree;
# freeway.txt is the one that also works from Etcher.
TRIM="rpicam-apps-lite mkvtoolnix v4l-utils fbset
      bluez bluez-firmware
      build-essential gdb strace pkg-config manpages-dev python3-venv
      linux-headers-rpi-v6 linux-headers-rpi-v7 linux-headers-rpi-v8
      python3-gpiozero python3-libgpiod python3-rpi-lgpio python3-smbus2 python3-spidev gpiod
      rpi-connect-lite ssh-import-id
      cifs-utils ntfs-3g
      p7zip-full zip unzip lua5.1 luajit ncdu traceroute wireless-tools dmidecode
      libmtp-runtime usb-modeswitch rpi-usb-gadget rpi-update rpi-keyboard-config
      rpi-keyboard-fw-update paxctld udisks2 apt-listchanges man-db
      linux-image-rpi-v6
      cloud-init rpi-cloud-init-mods netcat-openbsd isc-dhcp-client isc-dhcp-common dhcpcd-base"
# dpkg-query fails on a name it does not know, and this script runs under
# pipefail — so the failure is swallowed here rather than ending the build on
# the first package one of the two images never had.
present=\$( { dpkg-query -W -f='\${db:Status-Abbrev} \${Package}\n' \$TRIM 2>/dev/null || true; } | awk '/^ii/ {print \$2}' | sort -u)
apt-get purge -y -qq --autoremove \$present
# Its seed files on the boot partition would be read by nothing now. Left
# there, they are an invitation to edit a file that does nothing.
rm -f /boot/firmware/user-data /boot/firmware/meta-data /boot/firmware/network-config

# The Pi 1 kernel's package is gone but its files are not: the initramfs
# trigger regenerated one for it moments before it was removed, and nothing
# takes kernel.img off the boot partition. Only a Pi 1 or a Zero reads them,
# and neither is supported.
if ! dpkg -s linux-image-rpi-v6 >/dev/null 2>&1; then
	rm -f /boot/firmware/kernel.img /boot/firmware/initramfs /boot/initrd.img-*-rpi-v6
fi

# A cascade that reached something the box needs must stop the build, not
# ship an image that fails to boot at its next kernel update. The trim was
# worked out against the 32-bit image; this is what catches the 64-bit one
# behaving differently.
for need in raspi-firmware raspi-utils rpi-eeprom initramfs-tools systemd udev \\
            network-manager wpasupplicant firmware-brcm80211 nftables avahi-daemon \\
            openssh-server sudo unattended-upgrades python3 raspi-config iw rsync; do
	dpkg -s "\$need" >/dev/null 2>&1 || { echo "trimming removed \$need, which the box needs" >&2; exit 1; }
done
kernels=\$( { dpkg-query -W -f='\${db:Status-Abbrev} \${Package}\n' 'linux-image-rpi-*' 2>/dev/null || true; } | grep -cE '^ii .*rpi-(v7|v8|2712)' || true)
[ "\$kernels" -gt 0 ] || { echo "trimming left no kernel for a Pi 2 or later" >&2; exit 1; }

echo "==> closing it down"
./harden.sh build

echo "==> tidying"
apt-get clean
rm -rf /var/lib/apt/lists/* /tmp/freeway-install /tmp/freeway-build.sh
INNER
chmod +x "$mnt/tmp/freeway-build.sh"

say "installing inside the image"
chroot "$mnt" /tmp/freeway-build.sh

# The firewall is written inside the chroot and cannot be checked there: nft
# opens a netlink socket before it parses anything. Check it here, where there
# is one. A ruleset that does not parse means nftables.service fails at boot
# and the box comes up with no firewall at all — quietly, which is the worst
# way for this particular thing to fail.
if command -v nft >/dev/null; then
	if nft -c -f "$mnt/etc/nftables.conf"; then
		say "firewall ruleset parses"
	else
		echo "the generated ruleset does not parse; the image would boot with no firewall" >&2
		exit 1
	fi
else
	say "nft is not installed here, so the ruleset went unchecked (apt install nftables)"
fi

# --- nothing personal ------------------------------------------------------

say "checking the login account is usable"
shell=$(chroot "$mnt" getent passwd "$LOGIN_USER" | cut -d: -f7)
case "$shell" in
*/nologin|*/false|"") echo "$LOGIN_USER has shell '$shell'; ssh would connect and close" >&2; exit 1 ;;
esac
chroot "$mnt" getent passwd 1000 | grep -q "^$LOGIN_USER:" ||
	{ echo "$LOGIN_USER is not uid 1000; the helper finds keys by uid" >&2; exit 1; }
say "$LOGIN_USER, uid 1000, shell $shell"

say "removing anything identifying"
# Host keys are regenerated on first boot; an image that shipped them would
# hand every box that flashed it the same identity.
rm -f "$mnt"/etc/ssh/ssh_host_*
chroot "$mnt" systemctl enable regenerate_ssh_host_keys.service 2>/dev/null || true
: > "$mnt/etc/machine-id"
rm -f "$mnt/var/lib/dbus/machine-id"
rm -rf "$mnt"/var/log/* "$mnt"/root/.bash_history "$mnt/home/$LOGIN_USER/.bash_history"
rm -f "$mnt"/etc/NetworkManager/system-connections/*
rm -f "$mnt/usr/bin/$(basename "$QEMU")"

sync
cleanup
trap - EXIT

# --- shrink and compress ---------------------------------------------------

say "shrinking"
loop=$(losetup --show -fP "$img")
e2fsck -pf "${loop}p2" >/dev/null 2>&1 || true
minimal=$(resize2fs -P "${loop}p2" 2>/dev/null | grep -o '[0-9]*$')
# Room to breathe: a filesystem resized to its exact minimum has nowhere to
# put the first update, and the boot resize only runs when the card is bigger
# than the image.
resize2fs "${loop}p2" "$((minimal + 40000))" >/dev/null
# Deleting a file frees its blocks and leaves what was in them. Everything the
# trim removed — the compiler, the camera stack, three hundred megabytes of it —
# is still sitting in those blocks, and xz compresses it as faithfully as the
# files that matter. Discarding the free blocks turns them into holes in the
# image file, which read back as zeros and compress to nothing.
e2fsck -fy -E discard "${loop}p2" >/dev/null 2>&1 || true
block=$(tune2fs -l "${loop}p2" | awk '/Block size/{print $3}')
count=$(tune2fs -l "${loop}p2" | awk '/Block count/{print $3}')
detach_loop "$loop"

sectors=$((count * block / 512))
echo ",$sectors" | sfdisk --no-reread -N 2 "$img" >/dev/null
# Read back, because this is the step that produced an unbootable image when
# it failed silently: the filesystem shrank, the table did not, and truncate
# cut the file to a size the table said was still occupied.
got=$(part_sectors "$img" 2)
[ "$got" = "$sectors" ] || {
	echo "the partition table says $got sectors, the filesystem is $sectors" >&2
	exit 1
}
truncate -s "$(( ($(part_end "$img" 2) + 1) * 512 ))" "$img"
check_fits "$img"

say "compressing"
rm -f "$img.xz"
xz -T0 -9 "$img"
ls -lh "$img.xz" | awk '{print "\n==> " $9 "  " $5}'

# Explicit, so chaining two builds with && works. The last command in a script
# decides its exit status, and a pipeline ending in awk is a poor thing to have
# that depend on.
exit 0
