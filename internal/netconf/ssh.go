package netconf

import (
	"context"
	"strconv"
	"strings"
	"time"

	"freewaypi/internal/i18n"
	"freewaypi/internal/privhelper"
)

// SSH is whether the box can be reached over ssh, and on what terms.
//
// Closing it is the safer choice right up until freewayd will not start, and
// then it is the difference between clicking a button and carrying a monitor
// into a utility room. The card is that way back: it needs no network and no
// daemon — see packaging/freeway-card. So a fresh image ships with ssh closed,
// a box installed by hand keeps it open, and the interface says plainly what
// closing costs.
type SSH struct {
	// Firewall is false when no ruleset is in place. Then ssh is reachable and
	// nothing here can change that, which the interface has to say rather than
	// offering controls that quietly do nothing.
	Firewall bool `json:"firewall"`
	// Policy is the lasting setting: "open" or "closed".
	Policy string `json:"policy"`
	// Reachable is whether it is answering right now, which differs from the
	// policy exactly while a temporary opening is running.
	Reachable bool `json:"reachable"`
	// Until is when a temporary opening ends. Zero when none is running.
	Until time.Time `json:"until,omitzero"`

	// Keys are the public keys that may log in, as fingerprints. Never the
	// keys themselves: a public key is not a secret, but there is nothing the
	// interface could do with one.
	//
	// An open port with no key is a door with no handle, and the interface has
	// to say so — it is the state a box flashed with anything but Raspberry Pi
	// Imager starts in.
	Keys []SSHKey `json:"keys"`
	// User is the login account the keys belong to, empty when there is none.
	User string `json:"user,omitempty"`

	Writable bool   `json:"writable"`
	Why      string `json:"why,omitempty"`
}

// SSHKey is one entry in authorized_keys, as much of it as is worth showing.
type SSHKey struct {
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment,omitempty"`
	Type        string `json:"type,omitempty"`
}

// Temporary reports whether an opening is running against a closed policy.
func (s SSH) Temporary() bool { return !s.Until.IsZero() }

// ReadSSH asks the helper. Reading the ruleset needs privileges, so unlike the
// rest of this package it cannot be done directly.
func ReadSSH(ctx context.Context) *SSH {
	s := &SSH{Policy: "open", Reachable: true}
	out, err := privhelper.Call(ctx, "ssh-status")
	if err != nil {
		s.Why = err.Error()
		return s
	}
	s.Writable = true
	s.User, s.Keys = readSSHKeys(ctx)
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "firewall":
			s.Firewall = value == "yes"
		case "policy":
			s.Policy = value
		case "reachable":
			s.Reachable = value == "yes"
		case "until":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > 0 {
				s.Until = time.Unix(n, 0)
			}
		}
	}
	return s
}

// SetSSHPolicy makes the lasting decision.
func SetSSHPolicy(ctx context.Context, policy string) error {
	if policy != "open" && policy != "closed" {
		return i18n.Errf("error.ssh.policy")
	}
	_, err := privhelper.Call(ctx, "ssh-policy", privhelper.Field{Key: "policy", Value: policy})
	return err
}

// MaxSSHMinutes is a day. Longer than that is not a temporary opening, it is a
// decision, and there is a button for decisions.
const MaxSSHMinutes = 1440

// OpenSSH opens the door for a while. The opening is applied to the running
// ruleset and never written to disk, so a reboot returns to whatever was
// decided rather than to whatever somebody needed for ten minutes one evening.
func OpenSSH(ctx context.Context, minutes int) error {
	if minutes < 1 || minutes > MaxSSHMinutes {
		return i18n.Errf("error.ssh.minutes")
	}
	_, err := privhelper.Call(ctx, "ssh-open",
		privhelper.Field{Key: "minutes", Value: strconv.Itoa(minutes)})
	return err
}

// ShutSSH ends a temporary opening now, returning to the lasting setting.
func ShutSSH(ctx context.Context) error {
	_, err := privhelper.Call(ctx, "ssh-shut")
	return err
}

// readSSHKeys lists who may log in. A failure is reported as no keys, which is
// the safe direction: the interface then says nobody can get in, which is worse
// than the truth rather than better than it.
func readSSHKeys(ctx context.Context) (string, []SSHKey) {
	out, err := privhelper.Call(ctx, "ssh-keys")
	if err != nil {
		return "", nil
	}
	return parseSSHKeys(out)
}

// parseSSHKeys is the parsing half, kept apart so it can be tested without a
// helper, an account or a key.
func parseSSHKeys(out string) (string, []SSHKey) {
	var user string
	var keys []SSHKey
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "user":
			user = value
		case "key":
			// fingerprint, (TYPE), comment — the helper reorders ssh-keygen's
			// output into that, and a key nobody named has the two words "no
			// comment" where a comment would be.
			f := strings.SplitN(value, " ", 3)
			k := SSHKey{Fingerprint: f[0]}
			if len(f) > 1 {
				k.Type = strings.Trim(f[1], "()")
			}
			if len(f) > 2 && f[2] != "no comment" {
				k.Comment = f[2]
			}
			keys = append(keys, k)
		}
	}
	return user, keys
}

// AddSSHKey installs a public key for the login account.
//
// Over plain HTTP on a local network, which is fine: a public key is not a
// secret, and anybody who could substitute one already has the PIN this sits
// behind and could do anything else instead.
func AddSSHKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return i18n.Errf("error.ssh.key")
	}
	_, err := privhelper.Call(ctx, "ssh-key-add", privhelper.Field{Key: "key", Value: key})
	return err
}

// RemoveSSHKey takes one out, named by its fingerprint.
func RemoveSSHKey(ctx context.Context, fingerprint string) error {
	_, err := privhelper.Call(ctx, "ssh-key-remove",
		privhelper.Field{Key: "fingerprint", Value: fingerprint})
	return err
}
