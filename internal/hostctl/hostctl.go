// Package hostctl does the things to the box that need root: refresh apt's
// lists, install pending updates, restart it, and shut it down.
//
// None of it happens in this process. Everything goes through the root helper
// in package privhelper, which takes a fixed set of operations and no free text
// at all, so the daemon keeps no privileges it could be tricked into using.
package hostctl

import (
	"context"
	"fmt"
	"strings"

	"freewaypi/internal/i18n"

	"freewaypi/internal/privhelper"
)

// The two apt jobs. Refresh asks apt what is available; Upgrade installs it.
//
// They share one systemd unit in the helper, because apt takes a lock and a
// refresh started while an upgrade runs would simply fail. That makes them
// mutually exclusive by construction rather than by checking.
const (
	TaskRefresh = "refresh"
	TaskUpgrade = "upgrade"
)

// Available reports whether the helper is reachable, and why not when it is
// not.
//
// Checked rather than assumed, so the interface can say "this needs enabling"
// instead of offering a button that fails.
func Available(ctx context.Context) (bool, string) {
	return privhelper.Available(ctx)
}

// StartApt begins one of the two apt jobs. It returns as soon as the job has
// started, which is not the same as finished: either takes minutes, and Status
// reports on it afterwards.
func StartApt(ctx context.Context, task string) error {
	switch task {
	case TaskRefresh, TaskUpgrade:
	default:
		return i18n.Errf("error.apt.task", task)
	}
	if _, err := privhelper.Call(ctx, "apt", privhelper.Field{Key: "task", Value: task}); err != nil {
		return err
	}
	return nil
}

// Apt is how an apt job is going.
type Apt struct {
	// Task is refresh or upgrade, so the interface can say which. Empty when
	// none has run since the box booted.
	Task string `json:"task,omitempty"`
	// Running is true while apt is working.
	Running bool `json:"running"`
	// Finished is true once one has run in this boot, successfully or not.
	Finished bool `json:"finished"`
	Failed   bool `json:"failed"`
	ExitCode int  `json:"exit_code"`
	// Log is the tail of apt's output, which is the only honest answer to
	// "what happened" when one fails.
	Log string `json:"log,omitempty"`
}

// Status reports on the last or current apt job.
func Status(ctx context.Context) (*Apt, error) {
	out, err := privhelper.Call(ctx, "apt-status")
	if err != nil {
		return nil, fmt.Errorf("apt-status: %w", err)
	}
	return parseAptStatus(out), nil
}

// parseAptStatus reads the helper's answer.
//
// Separate from Status so the states can be tested without a helper: the
// distinction between "nothing has ever run", "running" and "finished and
// failed" is where this is easy to get wrong, and all three look similar in
// systemd's output.
func parseAptStatus(out string) *Apt {
	head, log, _ := strings.Cut(out, "\n---\n")

	a := &Apt{}
	for _, line := range strings.Split(head, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "task":
			a.Task = value
		case "state":
			// "activating" counts as running: systemd reports it while the
			// unit is still starting, and a job that showed as stopped for its
			// first second would flicker in the interface.
			a.Running = value == "active" || value == "activating"
			// An unknown unit means none has run since boot, and systemd
			// answers that with an empty string or "inactive"; "failed" is the
			// only state that means something went wrong.
			a.Failed = value == "failed"
			a.Finished = value == "failed" || value == "inactive"
		case "code":
			fmt.Sscanf(value, "%d", &a.ExitCode)
		}
	}
	if a.ExitCode != 0 {
		a.Failed = true
	}
	// No log at all means nothing has ever run, whatever systemd says about an
	// unknown unit.
	a.Log = strings.TrimSpace(log)
	if a.Log == "" {
		a.Finished, a.Failed = false, false
	}
	return a
}

// Reboot restarts the box. It returns before that happens: the helper delays by
// two seconds so this answer gets out first.
func Reboot(ctx context.Context) error { return power(ctx, "reboot") }

// Poweroff stops the box. Nothing here can start it again — that needs somebody
// at the plug — so the interface asks before calling it.
func Poweroff(ctx context.Context) error { return power(ctx, "poweroff") }

func power(ctx context.Context, what string) error {
	if _, err := privhelper.Call(ctx, what); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}
