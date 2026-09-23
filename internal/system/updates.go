package system

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Updates describes whether the box is still being kept current.
type Updates struct {
	// Pending and Security count what apt would install, and Packages names
	// them: "17 ventende" tells nobody whether this is a kernel or a font.
	Pending  int       `json:"pending"`
	Security int       `json:"security"`
	Packages []Package `json:"packages,omitempty"`
	// LastSuccess is when apt last updated its lists. A date long past is the
	// clearest sign that a box has stopped being maintained.
	LastSuccess time.Time `json:"last_success"`
	// RebootRequired is set by the packages that need one.
	RebootRequired bool `json:"reboot_required"`
	// RebootBecause names them, from the file the packages write.
	RebootBecause []string `json:"reboot_because,omitempty"`
	// Unattended reports whether automatic upgrades are configured at all.
	Unattended bool `json:"unattended_upgrades"`
	// DistroEOL is when this release stops receiving security updates, and
	// DistroSupported whether that date is still ahead.
	DistroEOL       time.Time `json:"distro_eol,omitempty"`
	DistroSupported bool      `json:"distro_supported"`
	Error           string    `json:"error,omitempty"`

	// CheckedAt is when the count below was taken, and Checking whether a
	// fresh one is being taken right now. The count takes about fifteen
	// seconds on this hardware, so the page shows the last one and says how
	// old it is rather than holding the whole page hostage to apt.
	CheckedAt time.Time `json:"checked_at"`
	Checking  bool      `json:"checking"`
}

// Package is one upgradable package.
type Package struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Security bool   `json:"security"`
}

// updateTTL is how long a count is served before a fresh one is taken.
//
// Nothing about the answer changes faster than unattended-upgrades runs, which
// is daily. Fifteen minutes is short enough that a manual upgrade shows up
// while somebody is still looking at the page.
const updateTTL = 15 * time.Minute

// updateCache holds the last count.
//
// The count shells out to apt, which takes about fifteen seconds on a Pi 3B+
// — long enough that doing it inside the request left the system page blank
// while it ran. So the cached value is returned at once and a refresh runs
// behind it. The first caller after a start gets an empty count marked
// Checking, which the interface says out loud rather than showing as zero.
var updateCache struct {
	mu       sync.Mutex
	value    Updates
	valid    bool
	checking bool
	// waiters lets a caller that wants a fresh count wait for the one already
	// running instead of starting a second apt.
	waiters []chan struct{}
}

// CachedUpdates returns the last count, starting a refresh when it is stale.
func CachedUpdates() Updates {
	updateCache.mu.Lock()
	u, valid, checking := updateCache.value, updateCache.valid, updateCache.checking
	stale := !valid || time.Since(u.CheckedAt) > updateTTL
	if stale && !checking {
		updateCache.checking = true
		checking = true
		go refreshUpdates()
	}
	updateCache.mu.Unlock()

	// The cheap parts are read fresh every time: they are three stat calls,
	// and a reboot-required flag that lags by fifteen minutes is worse than
	// useless on a page whose whole job is to say what needs attention.
	u = withCheapFacts(u)
	u.Checking = checking
	return u
}

// RefreshUpdates forces a fresh count and waits for it.
//
// Used after an upgrade, where showing the pre-upgrade count would say the work
// did not happen.
func RefreshUpdates() Updates {
	updateCache.mu.Lock()
	if updateCache.checking {
		// One already running: wait for it rather than starting a second apt.
		done := make(chan struct{})
		updateCache.waiters = append(updateCache.waiters, done)
		updateCache.mu.Unlock()
		<-done
	} else {
		updateCache.checking = true
		updateCache.mu.Unlock()
		refreshUpdates()
	}
	updateCache.mu.Lock()
	u := updateCache.value
	updateCache.mu.Unlock()
	return withCheapFacts(u)
}

func refreshUpdates() {
	u := countUpdates()
	u.CheckedAt = time.Now()

	updateCache.mu.Lock()
	updateCache.value = u
	updateCache.valid = true
	updateCache.checking = false
	waiters := updateCache.waiters
	updateCache.waiters = nil
	updateCache.mu.Unlock()

	for _, w := range waiters {
		close(w)
	}
}

// withCheapFacts fills in everything that costs nothing to read.
func withCheapFacts(u Updates) Updates {
	if id := osRelease("VERSION_ID"); id != "" {
		if eol, ok := distroEOL[id]; ok {
			u.DistroEOL = eol
			u.DistroSupported = time.Now().Before(eol)
		}
	}
	u.RebootRequired = false
	u.RebootBecause = nil
	if _, err := os.Stat("/var/run/reboot-required"); err == nil {
		u.RebootRequired = true
		for _, line := range strings.Split(readFile("/var/run/reboot-required.pkgs"), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				u.RebootBecause = append(u.RebootBecause, line)
			}
		}
	}
	// When apt last refreshed its lists.
	//
	// update-success-stamp is written by a Post-Invoke-Success hook that not
	// every installation configures — on this one it has never existed, so the
	// interface said "unknown" for ever and the "over a week old" warning
	// could never fire. The others are what is actually there: update-stamp is
	// written by the apt-daily timer, and the lists directory is touched by any
	// refresh at all, including one somebody ran by hand.
	u.LastSuccess = time.Time{}
	for _, path := range []string{
		"/var/lib/apt/periodic/update-success-stamp",
		"/var/lib/apt/periodic/update-stamp",
		"/var/lib/apt/lists",
	} {
		if st, err := os.Stat(path); err == nil && st.ModTime().After(u.LastSuccess) {
			u.LastSuccess = st.ModTime()
		}
	}
	u.Unattended = false
	for _, p := range []string{
		"/etc/apt/apt.conf.d/20auto-upgrades",
		"/etc/apt/apt.conf.d/50unattended-upgrades",
	} {
		if _, err := os.Stat(p); err == nil {
			u.Unattended = true
		}
	}
	return u
}

// distroEOL holds the end of security support for the releases this can run on.
// Hard-coded rather than fetched: the box must be able to warn about its own
// obsolescence without a working internet connection, which is exactly the
// situation where it has been forgotten.
var distroEOL = map[string]time.Time{
	"11": time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), // bullseye
	"12": time.Date(2028, 6, 30, 0, 0, 0, 0, time.UTC), // bookworm
	"13": time.Date(2030, 6, 30, 0, 0, 0, 0, time.UTC), // trixie
	"14": time.Date(2032, 6, 30, 0, 0, 0, 0, time.UTC), // forky, estimated
}

// countUpdates is the expensive part: it asks apt what it would install.
func countUpdates() Updates {
	var u Updates

	// apt-check is tried first because where it exists it is precise about
	// which updates are security ones. It ships with update-notifier on
	// Ubuntu and not on Debian, so this is usually a miss — and it gives a
	// count only, so the package list still comes from the simulation below.
	var securityCount = -1
	if out, err := exec.Command("/usr/lib/update-notifier/apt-check").CombinedOutput(); err == nil {
		parts := strings.Split(strings.TrimSpace(string(out)), ";")
		if len(parts) == 2 {
			if n, err := strconv.Atoi(parts[1]); err == nil {
				securityCount = n
			}
		}
	}

	// Otherwise ask apt to simulate an upgrade. No package, no privileges, and
	// on the boxes this was tried on it is the only route that works.
	out, err := exec.Command("apt-get", "-s", "-o", "Debug::NoLocking=true", "upgrade").Output()
	if err != nil {
		u.Error = err.Error()
		return u
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		p := parseInstLine(line)
		if p.Name == "" {
			continue
		}
		u.Pending++
		if p.Security {
			u.Security++
		}
		u.Packages = append(u.Packages, p)
	}
	if securityCount >= 0 {
		u.Security = securityCount
	}
	return u
}

// parseInstLine reads one of apt's simulation lines.
//
// The shape is:
//
//	Inst libfoo [1.2-3] (1.2-4 Debian:12/stable [arm64])
//
// The bracketed version after the name is the one installed now; the one in
// parentheses is what would replace it, which is the one worth showing.
func parseInstLine(line string) Package {
	fields := strings.Fields(strings.TrimPrefix(line, "Inst "))
	if len(fields) == 0 {
		return Package{}
	}
	p := Package{Name: fields[0]}
	// Debian marks security updates by the archive they come from.
	p.Security = strings.Contains(line, "-security") || strings.Contains(line, "Debian-Security")
	if i := strings.Index(line, "("); i >= 0 {
		rest := line[i+1:]
		if v := strings.Fields(rest); len(v) > 0 {
			p.Version = strings.TrimSuffix(v[0], ")")
		}
	}
	return p
}
