#!/bin/sh
# Closes the box down to what it is for.
#
# Separate from install.sh, and run by hand, on purpose. install.sh runs on
# every deploy; this changes the shape of the machine, and a thing that shapes
# the machine should be something somebody chose rather than something that
# happened while they were updating.
#
# What it does:
#   - a firewall that answers on 22, 80 and 502 and nothing else
#   - stops and masks the services this appliance has no use for
#
# It leaves the network alone, wired or wireless. Wifi is a supported way to
# reach this box, so nothing here masks wpa_supplicant and the rules below
# allow DHCP and mDNS on whichever interface is carrying them.
#
# Everything here is reversible, and the firewall arms a revert before it
# applies: a rule that locks you out of a box in a cupboard is a drive to the
# cupboard. Confirm within the window or it rolls back by itself.
#
#   sudo ./harden.sh            apply, with a revert armed
#   sudo ./harden.sh build      apply for an image: no revert, nothing to lose
#   sudo ./harden.sh confirm    keep it
#   sudo ./harden.sh revert     undo it now
#   sudo ./harden.sh status     what is in force
set -eu

RULES=/etc/nftables.conf
BACKUP=/var/lib/freeway-helper/nftables.before
# Whether there was an /etc/nftables.conf before we wrote one. Reverting has to
# know the difference between "put the old file back" and "there was no file",
# and the backup's contents cannot say which.
MARKER=/var/lib/freeway-helper/nftables.had-config
# What each service was before it was masked. Recorded rather than assumed:
# unmasking a unit leaves it disabled, and on stock Raspberry Pi OS bluetooth
# and udisks2 are both enabled — so a revert that only unmasks quietly leaves
# the machine different from how it was found.
SERVICES=/var/lib/freeway-helper/services-before
# "Applied but nobody has said they can still reach the box." Its own file,
# because the backups cannot also carry that meaning: deleting them on confirm
# is what left a later revert with nothing to restore from, and keeping them
# would make the boot-time unwind undo a firewall that was confirmed weeks ago.
UNCONFIRMED=/var/lib/freeway-helper/harden-unconfirmed
REVERT_UNIT=freeway-firewall-revert
REVERT_AFTER=${REVERT_AFTER:-300}

# The armed revert fires minutes from now. By then the copy this was run from
# may well be gone — install.sh unpacks into /tmp — so the timer is pointed at
# the installed one when there is one.
INSTALLED=${INSTALLED:-/opt/freeway/bin/freeway-harden}
SELF=$(readlink -f "$0")
[ -x "$INSTALLED" ] && SELF=$INSTALLED

# The ports this box exists to answer on. SSH is here because the alternative
# is a keyboard and a screen in a utility room.
WEB_PORT=${WEB_PORT:-80}
MODBUS_PORT=${MODBUS_PORT:-502}

# Whether ssh is reachable when nobody has opened it on purpose.
#
# A fresh image writes "closed" here explicitly, because most owners never use
# ssh and the card — ssh=30 — opens it at boot without needing freewayd. The
# fallback when there is no file stays open: a box somebody installed by hand
# was reached over ssh to do it, and nobody should have the door closed behind
# them by upgrading.
SSH_POLICY_FILE=/var/lib/freeway-helper/ssh-policy
SSH_POLICY=$(cat "$SSH_POLICY_FILE" 2>/dev/null || echo open)
case "$SSH_POLICY" in
open) SSH_RULE="accept" ;;
closed) SSH_RULE="" ;;
*) SSH_POLICY=open; SSH_RULE="accept" ;;
esac

# Services this appliance has no use for.
#
# wpa_supplicant is deliberately NOT here. Freeway Pi prefers a cable, but it
# supports wifi — the System page configures it — and NetworkManager drives the
# radio through wpa_supplicant. Masking it would quietly remove a feature, and
# on a box that is on wifi it would remove the network.
#
# Bluetooth goes: nothing here speaks it, and on a Pi 3B+ it shares the chip
# with the wifi radio only in the sense that they are on the same die — killing
# the bluetooth service does not touch wlan0.
USELESS="bluetooth.service udisks2.service"

say() { printf '==> %s\n' "$1"; }
fail_build() { echo "harden.sh: $1" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || { echo "run this as root" >&2; exit 1; }

case "${1:-apply}" in

status)
	say "listening"
	ss -tulpn 2>/dev/null | awk 'NR==1 || /LISTEN|UNCONN/'
	say "firewall"
	nft list ruleset 2>/dev/null | head -40 || echo "  nftables is not in use"
	say "masked"
	systemctl list-unit-files --state=masked --no-legend --no-pager | awk '{print "  " $1}'
	exit 0
	;;

confirm)
	systemctl stop "$REVERT_UNIT.timer" 2>/dev/null || true
	# Only the "nobody has confirmed this" flag goes. The backups stay, so
	# `harden.sh revert` still works months later — which is the whole point of
	# taking them.
	rm -f "$UNCONFIRMED"
	say "kept"
	exit 0
	;;

revert)
	# Flush first and unconditionally: loading a saved ruleset on top of the
	# live one merges the two, and a "revert" that leaves our own drop policy
	# standing is the exact failure this timer exists to prevent.
	nft flush ruleset 2>/dev/null || true
	[ -s "$BACKUP" ] && { nft -f "$BACKUP" 2>/dev/null || true; }

	# The config file is only ours to touch if we have a backup proving we
	# wrote it. Without one this used to delete /etc/nftables.conf on the
	# reasoning that "no marker means there was no file" — but no marker AND no
	# backup means we never applied anything, and it took out the one Debian
	# ships. Verified by doing exactly that to a live box.
	if [ -f "$BACKUP" ]; then
		if [ -f "$MARKER" ]; then
			cp "$BACKUP" "$RULES"
		else
			rm -f "$RULES"
		fi
	fi
	systemctl stop "$REVERT_UNIT.timer" 2>/dev/null || true
	# The ssh setting goes too. Revert means the box as it was, and before any
	# of this ran ssh was reachable — leaving "closed" behind would mean a
	# later apply closed the door before anybody asked it to.
	rm -f "$BACKUP" "$MARKER" "$UNCONFIRMED" "$SSH_POLICY_FILE"

	# The services too, not only the firewall. The header promises this is
	# reversible, and a revert that puts half of it back makes that a lie —
	# and leaves no way to the other half but doing it by hand.
	#
	# It means a revert nobody confirmed also unmasks. That is the right
	# outcome: revert means the box as it was, not the parts of it that
	# happened to be about the network.
	if [ -f "$SERVICES" ]; then
		while IFS= read -r line; do
			unit=${line%%=*}
			was=${line#*=}
			case "$unit" in *.service) ;; *) continue ;; esac
			systemctl unmask "$unit" >/dev/null 2>&1 || true
			if [ "$was" = enabled ]; then
				systemctl enable --now "$unit" >/dev/null 2>&1 || true
				say "put $unit back the way it was ($was)"
			else
				say "unmasked $unit; it was $was before and stays that way"
			fi
		done < "$SERVICES"
		rm -f "$SERVICES"
	else
		# An older run left nothing to read. Unmask, and say plainly that the
		# enabled/disabled state is a guess nobody recorded.
		for unit in $USELESS; do
			if [ "$(systemctl is-enabled "$unit" 2>/dev/null)" = masked ]; then
				systemctl unmask "$unit" >/dev/null 2>&1 || true
				say "unmasked $unit; nothing recorded whether it was enabled"
			fi
		done
	fi

	logger -t freeway-harden "reverted the firewall and unmasked the services"
	say "reverted"
	exit 0
	;;

apply) ;;

build)
	# For the image build, inside a chroot. The armed revert exists because a
	# firewall can take away the session that applied it — and in a chroot
	# there is no session, no network and nothing listening. Arming one here
	# would mean the image shipped with a timer that undid it on first boot.
	#
	# Nothing else differs: the same ruleset, the same services, the same
	# single description of what closed down means.
	BUILD=1
	;;

*) echo "usage: harden.sh [apply|build|confirm|revert|status]" >&2; exit 2 ;;
esac

# --- services --------------------------------------------------------------

install -d -m 0700 -o root -g root "$(dirname "$SERVICES")"
if [ -f "$SERVICES" ]; then
	# Same reasoning as the firewall backup above. Running this a second time
	# would record "masked" — the state this script put them in — and a revert
	# would then faithfully restore the hardening instead of what was there
	# before it. Caught by doing exactly that.
	say "keeping the service states recorded the first time this ran"
else
	: > "$SERVICES"
	SERVICES_FRESH=1
fi
for unit in $USELESS; do
	if systemctl list-unit-files "$unit" --no-legend --no-pager | grep -q .; then
		# Written before anything changes, so revert has something true to
		# read rather than a default to assume.
		[ "${SERVICES_FRESH:-0}" = 1 ] &&
			printf '%s=%s\n' "$unit" "$(systemctl is-enabled "$unit" 2>/dev/null || echo unknown)" >> "$SERVICES"
		systemctl disable --now "$unit" 2>/dev/null || true
		systemctl mask "$unit" 2>/dev/null || true
		say "masked $unit"
	fi
done

# --- firewall --------------------------------------------------------------

command -v nft >/dev/null || { echo "nftables is not installed: apt install nftables" >&2; exit 1; }

install -d -m 0700 -o root -g root "$(dirname "$BACKUP")"
if [ -f "$BACKUP" ]; then
	# Already applied once. Keeping the first backup is the point: taking a
	# fresh one here would copy our own ruleset over it, and then "revert"
	# would restore the firewall instead of the state before there was one.
	# Running this twice is normal — a new version, a changed port — and it
	# must not quietly destroy the only way back.
	say "keeping the backup taken the first time this ran"
elif [ -f "$RULES" ]; then
	cp "$RULES" "$BACKUP"
	: > "$MARKER"
else
	rm -f "$MARKER"
	{ echo "flush ruleset"; nft list ruleset 2>/dev/null; } > "$BACKUP"
fi

# Armed before anything changes, exactly as a network change is. A rule that
# locks everybody out still has to come back on its own.
if [ "${BUILD:-0}" = 1 ]; then
	# Confirmed by construction: whoever built the image chose this.
	say "image build; no revert armed"
else
	# Armed before the change, not after: a box that becomes unreachable the
	# instant the new settings apply still has to come back.
	: > "$UNCONFIRMED"
	systemctl stop "$REVERT_UNIT.timer" 2>/dev/null || true
	systemd-run --unit="$REVERT_UNIT" --on-active="$REVERT_AFTER" --collect \
		--description="Freeway Pi: revert the firewall unless confirmed" \
		"$SELF" revert >/dev/null
fi

cat > "$RULES" <<NFTEOF
#!/usr/sbin/nft -f
# Written by freeway-pi's harden.sh. Edit and reload with: nft -f $RULES
flush ruleset

table inet filter {
	chain input {
		type filter hook input priority filter; policy drop;

		# Anything already agreed, and the machine talking to itself.
		ct state established,related accept
		iif lo accept
		ct state invalid drop

		# Enough ICMP to be a good neighbour: path MTU discovery breaks
		# without it, and a box that will not answer a ping is a box nobody
		# can tell apart from a dead one.
		ip protocol icmp accept
		ip6 nexthdr ipv6-icmp accept

		# What this appliance is for.
		tcp dport { $WEB_PORT, $MODBUS_PORT } accept

		# The way back in when something is wrong. In its own chain so that
		# opening and closing it is one atomic operation on that chain, rather
		# than rewriting the ruleset and hoping nothing else moved — a timed
		# open must not be able to take the web interface down with it.
		tcp dport 22 jump ssh
		# mDNS, so it can still be found by name rather than by address.
		udp dport 5353 accept

		# DHCP replies, for a box that does not hold a static address.
		udp sport 67 udp dport 68 accept
	}

	# Empty means closed: input's policy is drop, so falling off the end of
	# this chain drops. $SSH_RULE is one line or none.
	chain ssh {
		$SSH_RULE
	}

	chain forward {
		type filter hook forward priority filter; policy drop;
	}

	chain output {
		type filter hook output priority filter; policy accept;
	}
}
NFTEOF

if [ "${BUILD:-0}" = 1 ]; then
	# The rules are written and the service enabled; loading them needs a
	# kernel this chroot does not have, and nftables.service does it at boot.
	#
	# Not even checked here: "nft -c" opens a netlink socket before it will
	# parse anything, and a chroot has none — it fails with "Unable to
	# initialize Netlink socket". build-image.sh checks the file afterwards,
	# from the host, which has one.
	say "ruleset written; it is checked from outside and loads at boot"
else
	nft -f "$RULES"
fi
systemctl enable nftables.service >/dev/null 2>&1 || true

if [ "${BUILD:-0}" = 1 ]; then
	logger -t freeway-harden "firewall written into an image"
	say "firewall written into the image"
else
	logger -t freeway-harden "firewall applied; reverting in ${REVERT_AFTER}s unless confirmed"
	say "firewall applied"
fi
echo
if [ "$SSH_POLICY" = open ]; then
	echo "    Open: 22 (ssh), $WEB_PORT (web), $MODBUS_PORT (modbus), 5353/udp (mDNS), ICMP"
else
	echo "    Open: $WEB_PORT (web), $MODBUS_PORT (modbus), 5353/udp (mDNS), ICMP"
	echo "    ssh is closed; open it from the System page or from the card"
fi
echo
# Only the live path has anything to confirm. Saying "it rolls back in 300s"
# after an image build is a plain untruth — nothing was armed, and whoever read
# it would either wait for something that never happens or go looking for a
# timer that was never there.
if [ "${BUILD:-0}" = 1 ]; then
	echo "    Nothing to confirm: no revert was armed, because a chroot has no"
	echo "    session to lose. The rules load when the image first boots."
else
	echo "    It rolls back by itself in ${REVERT_AFTER}s. Check you can still reach the box"
	echo "    — open the web interface, and open a SECOND ssh session — then run:"
	echo
	echo "        sudo $SELF confirm"
fi
echo
