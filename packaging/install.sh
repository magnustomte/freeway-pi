#!/bin/sh
# Install or update freewayd.
#
# Run on the Pi with the binary beside it:
#   sudo ./install.sh freewayd-linux-arm64
#
# Safe to run repeatedly. An existing configuration is never overwritten.
set -eu

BINARY="${1:-}"
PREFIX=/opt/freeway
CONFDIR=/etc/freeway
USER=freeway

if [ -z "$BINARY" ]; then
	echo "usage: $0 <path-to-freewayd-binary>" >&2
	exit 2
fi
if [ ! -f "$BINARY" ]; then
	echo "$0: no such file: $BINARY" >&2
	exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
	echo "$0: must run as root" >&2
	exit 1
fi

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

echo "==> service account"
if ! id "$USER" >/dev/null 2>&1; then
	useradd --system --no-create-home --shell /usr/sbin/nologin "$USER"
	echo "    created $USER"
fi
# dialout owns the serial device node; without this the daemon cannot open it.
usermod -aG dialout "$USER"

echo "==> directories"
install -d -m 0755 "$PREFIX/bin"
install -d -m 0750 -o "$USER" -g "$USER" "$CONFDIR"

echo "==> binary"
# Replaced rather than written in place, so a running daemon is not modified
# underneath itself.
install -m 0755 "$BINARY" "$PREFIX/bin/freewayd.new"
mv "$PREFIX/bin/freewayd.new" "$PREFIX/bin/freewayd"
"$PREFIX/bin/freewayd" -version | sed 's/^/    version /'

echo "==> privileged helper"
# The daemon cannot do this itself and deliberately cannot become root: it runs
# with NoNewPrivileges=yes and a capability bounding set of exactly
# CAP_NET_BIND_SERVICE. The bounding set is inherited and cannot be raised, so
# sudo would not help even if it worked — root without CAP_CHOWN cannot run
# dpkg. So the helper is a separate root service, started by systemd on a unix
# socket that only this daemon's group may open. No sudoers rule anywhere.
install -m 0755 -o root -g root "$here/freeway-helper" "$PREFIX/bin/freeway-helper"
install -m 0644 "$here/freeway-helper.socket" /etc/systemd/system/freeway-helper.socket
install -m 0644 "$here/freeway-helper@.service" /etc/systemd/system/freeway-helper@.service

# Installed but never run from here. Closing the box down is a decision, not a
# step in an update — see harden.sh's own header. It is put in place so that
# the revert it arms has a path that still exists in five minutes' time, which
# the /tmp copy this script runs from does not.
install -m 0755 -o root -g root "$here/harden.sh" "$PREFIX/bin/freeway-harden"

# The revert a risky change arms is a transient systemd timer, and a transient
# unit does not survive a reboot. This is what reads the saved file on the way
# back up and puts things back when nobody ever confirmed.
install -m 0755 -o root -g root "$here/freeway-unwind" "$PREFIX/bin/freeway-unwind"
install -m 0644 "$here/freeway-unwind.service" /etc/systemd/system/freeway-unwind.service

# The lasting ssh setting lives in one file and is applied after nftables has
# loaded, so changing it survives a reboot without anything editing the rule
# file with sed.
install -m 0644 "$here/freeway-ssh-policy.service" /etc/systemd/system/freeway-ssh-policy.service

# Host keys, if this box has none. See the unit for why Raspberry Pi OS's own
# mechanism is not enough for an image that ships without them.
install -m 0644 "$here/freeway-sshkeys.service" /etc/systemd/system/freeway-sshkeys.service

# An installed timer, not a transient one from systemd-run. The transient
# version lost its own service file out of /run before it could fire, leaving
# ssh open with nothing left to close it.
install -m 0644 "$here/freeway-ssh-expire.timer" /etc/systemd/system/freeway-ssh-expire.timer
install -m 0644 "$here/freeway-ssh-expire.service" /etc/systemd/system/freeway-ssh-expire.service

# The way back in when there is no way in: instructions left on the boot
# partition, which needs nothing but the card and any laptop.
install -m 0755 -o root -g root "$here/freeway-card" "$PREFIX/bin/freeway-card"
install -m 0644 "$here/freeway-card.service" /etc/systemd/system/freeway-card.service

# The upgrade writes here. Created now so it exists with the right mode before
# anything reads it.
touch /var/log/freeway-upgrade.log
chmod 644 /var/log/freeway-upgrade.log

# Any older sudo-based helpers are removed rather than left lying around: they
# never worked under this unit's hardening, and a stale sudoers rule is a
# standing grant nobody is using.
rm -f "$PREFIX/bin/freeway-netconf" "$PREFIX/bin/freeway-admin"
rm -f /etc/sudoers.d/freeway-netconf /etc/sudoers.d/freeway-admin

echo "==> persistent system journal"
# Without this the box comes back from a restart with no record of why it
# went. See the file itself for the reasoning and the size cap.
install -d -m 0755 /etc/systemd/journald.conf.d
install -m 0644 "$here/50-freeway-journal.conf" /etc/systemd/journald.conf.d/50-freeway-journal.conf
if [ -d /run/systemd/system ]; then
	systemctl restart systemd-journald 2>/dev/null || true
	# Restarting is not enough. A journald that has just come up writes to the
	# runtime journal and only moves to disk when it is told to flush, which at
	# boot is systemd-journal-flush's job and here is ours. Without this the
	# setting takes effect at the next restart — the one whose reason it was
	# installed to preserve.
	journalctl --flush 2>/dev/null || true
fi

echo "==> udev rule"
install -m 0644 "$here/99-freeway-rs485.rules" /etc/udev/rules.d/99-freeway-rs485.rules
udevadm control --reload
# Both subsystems: the latency timer lives on the usb-serial device and the
# symlink on the tty. Triggering one leaves the other silently unapplied.
udevadm trigger --action=add --subsystem-match=usb-serial --subsystem-match=tty

echo "==> automatic security updates"
# The box has to look after itself for years with nobody watching it. Without
# this, "it patches itself" is simply untrue, and a machine that stopped
# receiving security updates looks exactly like one that is fine.
# update-notifier-common is deliberately absent: it ships apt-check on Ubuntu
# and not on Debian, and the daemon counts updates by asking apt to simulate an
# upgrade, which needs no package and no privileges. Fewer things to patch is
# the point of the exercise.
missing=""
for pkg in unattended-upgrades; do
	dpkg -s "$pkg" >/dev/null 2>&1 || missing="$missing $pkg"
done
if [ -n "$missing" ]; then
	echo "   installing$missing"
	DEBIAN_FRONTEND=noninteractive apt-get update -qq || true
	DEBIAN_FRONTEND=noninteractive apt-get install -y -qq $missing || {
		echo "    could not install$missing; automatic updates are NOT configured" >&2
	}
fi

if [ ! -f /etc/apt/apt.conf.d/20auto-upgrades ]; then
	cat > /etc/apt/apt.conf.d/20auto-upgrades <<'CONF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
CONF
	echo "    security updates will install automatically"
fi

# Rebooting is deliberately left off. A kernel update needs one, so a box that
# never reboots is only half patched — but restarting somebody's ventilation
# controller unasked is their decision, not the installer's. The system page
# says so and offers the switch.
if [ ! -f /etc/apt/apt.conf.d/51freeway-unattended ]; then
	cat > /etc/apt/apt.conf.d/51freeway-unattended <<'CONF'
// Written by the Freeway Pi installer.
Unattended-Upgrade::Automatic-Reboot "false";
Unattended-Upgrade::Automatic-Reboot-Time "04:00";
Unattended-Upgrade::Remove-Unused-Dependencies "true";
// Mail is not configured here; the daemon reports update state in its own
// interface and through its own notification channels.
CONF
fi

echo "==> configuration"
if [ -f "$CONFDIR/config.json" ]; then
	echo "    keeping the existing $CONFDIR/config.json"
else
	"$PREFIX/bin/freewayd" -config "$CONFDIR/config.json" -write-config
	chown "$USER:$USER" "$CONFDIR/config.json"
	echo "    wrote defaults to $CONFDIR/config.json"
	echo "    the Modbus gateway is off until you enable it and say who may use it"
fi

echo "==> service"
install -m 0644 "$here/freewayd.service" /etc/systemd/system/freewayd.service

# Inside an image build there is no systemd running, so nothing can be started
# — only enabled, which is all an image needs: the first boot starts them.
# "--now" refuses outright in that case rather than degrading, which is how
# this script first died halfway through building an image.
if [ -d /run/systemd/system ]; then
	NOW=--now
else
	NOW=""
	echo "    no systemd here; enabling only, to be started on first boot"
fi

systemctl daemon-reload 2>/dev/null || true
# The socket first: it is what the daemon asks when it reports whether the
# network can be changed from the interface.
systemctl enable $NOW freeway-helper.socket
systemctl enable freeway-unwind.service >/dev/null
systemctl enable freeway-sshkeys.service >/dev/null
systemctl enable freeway-ssh-policy.service >/dev/null
systemctl enable $NOW freeway-ssh-expire.timer >/dev/null
systemctl enable freeway-card.service >/dev/null
systemctl enable freewayd

if [ -z "$NOW" ]; then
	echo "==> installed; it starts when the image boots"
	exit 0
fi

systemctl restart freewayd

sleep 2
if systemctl is-active --quiet freewayd; then
	echo "==> running"
	systemctl --no-pager --lines=15 status freewayd || true
else
	echo "==> freewayd did not start" >&2
	journalctl -u freewayd --no-pager --lines=30 >&2
	exit 1
fi
