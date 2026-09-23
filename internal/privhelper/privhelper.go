// Package privhelper talks to the root helper.
//
// A handful of things this appliance has to do — change its own network,
// install pending updates, restart, shut down — need privileges the daemon
// deliberately does not have. They are done by a separate root service, reached
// over a unix socket that only the daemon's group may open.
//
// Not sudo. The daemon runs with NoNewPrivileges=yes and a capability bounding
// set of exactly CAP_NET_BIND_SERVICE; the bounding set is inherited and cannot
// be raised, so even a successful escalation would hand root none of the
// capabilities apt needs. Socket activation avoids the question: systemd starts
// the helper as its own root service, and the daemon's only privilege is being
// allowed to ask.
//
// One request per connection, key=value per line, ended by a lone dot. The
// helper checks every value against a pattern before it goes near a command,
// because all of it arrived over the network first.
package privhelper

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"freewaypi/internal/i18n"
)

// SocketPath is where the helper listens. Set by the socket unit.
const SocketPath = "/run/freeway-helper.sock"

// socketPath is the one the tests can move.
var socketPath = SocketPath

// Field is one line of a request. Ordered rather than a map so a request reads
// the same way every time, which matters when it turns up in a log.
type Field struct {
	Key   string
	Value string
}

// Timeout bounds one exchange.
//
// Everything the helper does either returns at once or hands the work to a
// transient unit, so one that has not answered in fifteen seconds is a failure
// rather than something to keep waiting for.
const Timeout = 15 * time.Second

// Call sends one request and returns what the helper said.
//
// The error is non-nil when the helper could not be reached or answered with a
// failure; the text is meant to be shown to somebody, so it is in the same
// language as the rest of the interface.
func Call(ctx context.Context, op string, fields ...Field) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return "", i18n.Wrap(err, "error.helper.unreachable", err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}

	var req strings.Builder
	write := func(k, v string) error {
		// A newline in a value would be a second line of the request, which is
		// the one way a caller could smuggle a field past the helper. None of
		// the values this sends can contain one, so this is a guard against a
		// future caller rather than against the network.
		if strings.ContainsAny(v, "\n\r") {
			return fmt.Errorf("privhelper: %s inneholder linjeskift", k)
		}
		fmt.Fprintf(&req, "%s=%s\n", k, v)
		return nil
	}
	if err := write("op", op); err != nil {
		return "", err
	}
	for _, f := range fields {
		if err := write(f.Key, f.Value); err != nil {
			return "", err
		}
	}
	req.WriteString(".\n")

	if _, err := io.WriteString(conn, req.String()); err != nil {
		return "", fmt.Errorf("hjelpetjenesten: %w", err)
	}
	// Half-close, so a helper that reads to end of input rather than to the
	// dot still sees the request finish.
	if c, ok := conn.(*net.UnixConn); ok {
		c.CloseWrite()
	}

	// Bounded: the upgrade log is four kilobytes, and nothing else answers with
	// more than a line.
	out, err := io.ReadAll(io.LimitReader(conn, 64*1024))
	if err != nil {
		return "", fmt.Errorf("hjelpetjenesten: %w", err)
	}
	answer := strings.TrimSpace(string(out))
	if rest, found := strings.CutPrefix(answer, "feil: "); found {
		return "", refusal(rest)
	}
	return answer, nil
}

// refusal turns the helper's no into an error the page can show in the
// reader's own language.
//
// The helper answers with a code, the reason in English and sometimes a detail
// only it knows, separated by tabs. The code is looked up as helper.<code>; a
// code the catalogue has never heard of falls back to the English, and an
// answer in the older form — a sentence and no tabs — is passed through as it
// is, so a daemon and a helper from different versions still say something.
func refusal(rest string) error {
	parts := strings.SplitN(rest, "\t", 3)
	if len(parts) < 2 {
		return fmt.Errorf("%s", rest)
	}
	id := "helper." + parts[0]
	if !i18n.Has(i18n.Default, id) {
		return fmt.Errorf("%s", parts[1])
	}
	if len(parts) == 3 {
		return i18n.Errf(id, parts[2])
	}
	return i18n.Errf(id)
}

// Available reports whether the helper is reachable, and why not when it is
// not.
//
// Checked rather than assumed, so the interface can say "this is not enabled"
// instead of offering a control that fails at the worst possible moment.
func Available(ctx context.Context) (bool, string) {
	if _, err := Call(ctx, "check"); err != nil {
		return false, err.Error()
	}
	return true, ""
}
