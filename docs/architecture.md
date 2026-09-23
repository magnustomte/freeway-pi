# How Freeway Pi is put together

This is the developer's half of the documentation. The [README](../README.md)
is for somebody who wants their ventilation to work.

## One process, one snapshot

`freewayd` is a single static Go binary with no cgo, no runtime and no build
step for the browser. The interface is hand-written ES5-compatible JavaScript
served from `go:embed`, so a deploy is one file.

The RS-485 bus is the scarce resource: 19200 bps, half duplex, one device
answering at a time. So exactly one thing owns the serial port. A poller reads
the mapped register space on a schedule and publishes a snapshot; the web
interface, the Modbus TCP gateway and the history database all read that
snapshot. Nothing else touches the bus except a write somebody asked for, which
is queued at a higher priority than polling.

That is why the gateway can answer a client in milliseconds while a poll is in
flight, and why two clients cannot make the unit stutter.

### When the bus goes away

A run of twelve consecutive failures reopens the serial port. Either the unit is
off, in which case it costs one syscall, or the adapter was unplugged and put
back — in which case this is the only thing that notices without somebody
logging in.

Gateway clients get Modbus exception 11 (gateway target device failed to
respond) rather than a hanging connection, so a client that polls recovers by
itself when the bus does.

A fault is three log lines at most: it broke, the port came back, the bus came
back. Repeats are suppressed until the state changes.

## The privilege boundary

`freewayd` runs as its own user with `NoNewPrivileges=yes` and a capability
bounding set of exactly `CAP_NET_BIND_SERVICE`. It cannot become root, and sudo
would not help if it tried: the bounding set is inherited and cannot be raised.

The handful of things that need root — changing the network, joining a wifi
network, installing updates, rebooting, the firewall — are done by
`freeway-helper`, a shell script systemd starts as a separate root service on a
unix socket. The socket is `root:freeway` mode 0660, so who may ask is a
question of group membership rather than of a sudoers rule.

The protocol is one request per connection, `key=value` per line, ended by a
lone dot. Every value arrives from the network before it gets there, so every
one is checked against a pattern before it goes near a command. A refusal is
reported in the payload and the helper exits zero — a non-zero exit makes
systemd tear the connection down before the reason reaches whoever asked.

A refusal is `feil: ` followed by a code, the reason in English and, for the
few that have one, a detail only the helper knows, separated by tabs. The
daemon looks the code up as `helper.<code>` in the message catalogue and shows
the reason in the reader's language; the card writes the English into
`freeway.txt.done`. A test reads the helper and fails if it can send a code the
catalogue has no words for.

## Undo that survives

Every change that can make the box unreachable arms its undo before it applies:
the address, the wifi network, the firewall. The browser has to come back and
confirm; if it does not, the old settings return by themselves.

That timer is transient, and a transient systemd unit does not survive a reboot
— so pulling the power, which is the natural thing to do when a box stops
answering, would leave the broken settings in place with nothing left to undo
them. `freeway-unwind` runs at boot and puts them back.

It reads the saved file, and that is not a guess: confirming deletes the file
and reverting deletes the file, so a file still there at boot means neither
happened, which means the box went down inside the window. The reboot is the
evidence.

The judgement call is deliberate. A power cut during those ninety seconds undoes
a change that might have been fine. Rolling back costs somebody pressing save
again; not rolling back costs a trip to the cupboard with a monitor.

## The way back in

`freeway-card` reads `freeway.txt` from the boot partition at boot, acts on it,
and deletes it — leaving `freeway.txt.done` with the result and no secrets. It
can join a wifi network, open ssh for a while, and set the PIN.

Keys go to uid 1000 — the login account, found the way Raspberry Pi OS's own
`userconf` finds it. The image creates one, `fwadmin`, with no password and no
keys: inert until somebody adds one, and carrying no credential that could ship
in an image thousands of people flash. The daemon's own user is not a login and
must not become one.

Setting the PIN needs no old PIN, deliberately: whoever took the card out of the
box has physical possession, which is a stronger claim than any PIN and the only
claim somebody locked out can still make. The derivation happens in the daemon,
not the script — PBKDF2 at 120 000 iterations is not a thing to reimplement in
`/bin/sh` — so the script carries the PIN from the card to a root-owned file in
`/run` and forgets it, and the daemon deletes that file once it has read it.

## Authentication

The PIN never crosses the network. The browser derives a key from it with PBKDF2
against a salt the server issues, and proves possession with `HMAC(key, nonce)`
against a single-use nonce. The server stores the key, which means the stored
verifier *is* the login — anyone who can read the configuration or a backup can
log in — but the server has to be able to check the proof, so that is inherent.

Wrong proofs back off rather than lock out, the delay is capped, and the count
decays: a mistyped PIN should not shut somebody out of their own house for an
hour, and ten thousand guesses should still take days.

Changing the PIN requires the old one even though the page already sits behind a
session, because a session is a page left open on a phone in a kitchen. It
rotates the signing secret, so every other session ends.

## Interface

One embedded JSON catalogue per language, read by both Go and the browser. The
browser gets it as a blocking script that sets `window.__i18n`, so `t()` is ready
before anything renders — an earlier version fetched it and leaked message ids
onto the page for a frame.

Errors carry an id and arguments and are rendered at read time
(`i18n.Errf`, `i18n.Wrap`), so the same error reads in whichever language the
reader asked for.

Tests enforce what review keeps missing: every `t('...')` the scripts ask for
exists in the catalogue, every function the scripts call is defined, no page
repeats an element id, and no Norwegian is left hard-coded in the scripts.

## Building and testing

```sh
make test               # unit tests, no hardware needed
make dist               # arm64 and armv7 binaries
make deploy HOST=...    # copy and install over ssh
```

`packaging/install.sh` does the whole installation and is the only description
of it — the image build runs the same script in a chroot rather than keeping a
second recipe that would drift.

`packaging/harden.sh` is separate and run by hand on an existing install:
firewall, masked services, armed revert. The image runs it at build time, where
there is no session to lose.

### fwprobe

`cmd/fwprobe` is the instrument, not part of the product. It talks to the bus
directly — with `freewayd` stopped, since only one thing may own the port — and
is what the register findings were made with:

```sh
fwprobe -port /dev/freeway-rs485 known     # compare against the old interface
fwprobe -port /dev/freeway-rs485 sweep     # which registers exist
fwprobe -port /dev/freeway-rs485 timing    # turnaround, safe inter-frame gap
fwprobe -port /dev/freeway-rs485 write     # test a hypothesis about one
```

## Keeping the repository clean

Configuration with addresses, credentials or keys never belongs here. Secrets
live in `/etc/freeway/config.json` on the device, written on first install.

Examples use the ranges RFC 5737 reserves for documentation — `192.0.2.0/24`,
`198.51.100.0/24`, `203.0.113.0/24` — and RFC 2606's `example.net`, never a real
one.

A pre-commit hook enforces it. Enable it once per clone:

```sh
git config core.hooksPath .githooks
```

It refuses staged content holding a private network address, an email address, a
private key, an ssh public key or something shaped like a credential. Only
generic patterns live in the hook, since the hook is itself committed; names
specific to one installation go in `notes/pii-patterns.txt`, which is ignored.

The hook runs before the commit because that is the only point at which it is
still cheap. Git history is permanent: a later commit that removes a value
leaves it in the earlier one, so the fix is rewriting history rather than
adding a commit.
