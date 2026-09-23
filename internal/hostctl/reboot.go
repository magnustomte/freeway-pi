package hostctl

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"freewaypi/internal/i18n"
	"freewaypi/internal/notify"
)

// RebootWanted reports whether a package asked for a restart, and which.
//
// Debian writes the flag; nothing here decides it. The file appears when a
// kernel, a firmware or a library that everything links against has been
// replaced, and it stays until the box restarts.
func RebootWanted() (bool, []string) {
	if _, err := os.Stat("/var/run/reboot-required"); err != nil {
		return false, nil
	}
	var because []string
	if b, err := os.ReadFile("/var/run/reboot-required.pkgs"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				because = append(because, line)
			}
		}
	}
	return true, because
}

// RebootPolicy is what to do about it.
type RebootPolicy struct {
	// Auto restarts the box; otherwise it only says so.
	Auto bool
	// At is the hour and minute to do it, in the box's own timezone.
	At time.Time
	// Unit is what this installation is called, for the message.
	Unit string
}

// ParseRebootAt reads "HH:MM", falling back to four in the morning — late
// enough that nobody is up, early enough that the house is warm again by
// breakfast.
func ParseRebootAt(s string) (hour, minute int) {
	h, m := 4, 0
	if parsed, err := time.Parse("15:04", strings.TrimSpace(s)); err == nil {
		h, m = parsed.Hour(), parsed.Minute()
	}
	return h, m
}

// DueAt reports whether now is inside the window that starts at hour:minute.
//
// A window rather than an instant: this is checked on a timer, and an exact
// comparison would miss the minute whenever a check landed either side of it.
func DueAt(now time.Time, hour, minute int, window time.Duration) bool {
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	return !now.Before(target) && now.Sub(target) < window
}

// RebootWindow is how wide the window is that opens at the chosen time.
//
// It has to be at least as wide as the gap between checks, or a check landing
// either side of the minute would step over it and the box would wait a whole
// day. Since the clock is checked every minute, just over a minute is enough —
// and it keeps "restart at 14:40" a statement about 14:40.
const RebootWindow = 90 * time.Second

// WatchReboot raises an event when a restart is wanted, and performs one if
// that is what was asked for.
//
// The event is raised whichever the policy is. Under "auto" it is the warning
// that the box is about to go, which somebody watching a house at four in the
// morning would rather have than not.
//
// Two rhythms, because there are two questions. Whether a package has asked for
// a restart is news that arrives a few times a year, so it is worth looking at
// every few minutes and no more. Whether it is 14:40 yet is a promise about a
// clock, and a promise kept on a ticker anchored to whenever the daemon last
// started is not a promise: set 14:40 on a daemon that came up at 13:32 and the
// box would go down at 14:47. So the clock gets its own ticker, at the
// resolution the person was allowed to type.
func WatchReboot(ctx context.Context, n *notify.Notifier, policy func() RebootPolicy,
	every time.Duration, log *slog.Logger) {
	const key = "reboot-required"
	poll := time.NewTicker(every)
	defer poll.Stop()
	clock := time.NewTicker(time.Minute)
	defer clock.Stop()

	// raise reports the condition, and is called from both rhythms: a box that
	// is about to restart has to have said so first, even when the chosen time
	// arrives before the first poll.
	raise := func(p RebootPolicy, because []string) {
		n.Raise(ctx, notify.Event{
			Key:      key,
			Kind:     "reboot_required",
			Severity: notify.Warning,
			// Written out in the notifier's language: the title travels to
			// a webhook, where a catalogue id is not a message.
			Title:   i18n.T(n.Lang(), "notify.reboot.title"),
			Message: strings.Join(because, ", "),
			Unit:    p.Unit,
			Time:    time.Now(),
		})
	}

	for {
		var onClock bool
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		case <-clock.C:
			onClock = true
		}

		wanted, because := RebootWanted()
		if !wanted {
			// Only the poll clears it. The flag is gone for good once the box
			// has restarted, and an all-clear is not worth a check a minute.
			if !onClock {
				n.Clear(ctx, key)
			}
			continue
		}
		p := policy()

		if !onClock {
			raise(p, because)
			continue
		}
		if !p.Auto {
			continue
		}
		if !DueAt(time.Now(), p.At.Hour(), p.At.Minute(), RebootWindow) {
			continue
		}
		raise(p, because)
		log.Warn("an update asked for a restart and the policy is automatic; restarting",
			"because", because, "at", p.At.Format("15:04"))
		if err := Reboot(ctx); err != nil {
			log.Error("could not restart", "err", err)
		}
		return
	}
}
