// Package system reports on the box itself.
//
// The appliance has to look after itself for years without anybody watching it,
// so the interface has to be able to answer "is this thing still healthy" — and
// more importantly, "is it still being patched". A box that quietly stopped
// receiving security updates two years ago looks exactly like one that is fine.
//
// Everything here is read without privileges. Anything needing root belongs in
// the helper, not in the daemon.
package system

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Info is what the system page shows.
type Info struct {
	Hostname string `json:"hostname"`
	Model    string `json:"model"`
	OS       string `json:"os"`
	Kernel   string `json:"kernel"`
	Arch     string `json:"arch"`

	UptimeSeconds float64 `json:"uptime_seconds"`
	LoadAverage   float64 `json:"load_average"`

	MemoryTotalBytes int64 `json:"memory_total_bytes"`
	MemoryUsedBytes  int64 `json:"memory_used_bytes"`
	DiskTotalBytes   int64 `json:"disk_total_bytes"`
	DiskFreeBytes    int64 `json:"disk_free_bytes"`

	// TemperatureC is the SoC temperature, which on a Pi in a cupboard is
	// worth an eye.
	TemperatureC float64 `json:"temperature_c,omitempty"`
	// Throttled is the Raspberry Pi's own record of under-voltage and
	// throttling. Under-voltage corrupts SD cards, so it is not cosmetic.
	Throttled *Throttled `json:"throttled,omitempty"`

	Updates Updates `json:"updates"`
}

// Throttled decodes the Pi's throttling word.
type Throttled struct {
	Raw uint32 `json:"raw"`
	// Now is what is happening at this moment.
	UnderVoltageNow bool `json:"under_voltage_now"`
	ThrottledNow    bool `json:"throttled_now"`
	// Ever is what has happened since the box booted.
	UnderVoltageEver bool `json:"under_voltage_ever"`
	ThrottledEver    bool `json:"throttled_ever"`
}

// Read gathers everything, tolerating whatever is not available: a missing file
// means one blank field, not a failed page.
func Read(diskPath string) Info {
	var i Info
	i.Hostname, _ = os.Hostname()
	i.Model = strings.TrimRight(readFile("/proc/device-tree/model"), "\x00\n ")
	i.OS = osRelease("PRETTY_NAME")
	i.Kernel = uname()
	i.Arch = runtimeArch()

	i.UptimeSeconds = uptime()
	i.LoadAverage = loadAverage()
	i.MemoryTotalBytes, i.MemoryUsedBytes = memory()
	i.DiskTotalBytes, i.DiskFreeBytes = disk(diskPath)
	i.TemperatureC = temperature()
	i.Throttled = throttled()
	i.Updates = CachedUpdates()
	return i
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func osRelease(key string) string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		k, v, ok := strings.Cut(s.Text(), "=")
		if ok && k == key {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func uname() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return ""
	}
	return string(trimNul(u.Release[:]))
}

func runtimeArch() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return ""
	}
	return string(trimNul(u.Machine[:]))
}

func trimNul(b []byte) []byte {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		return b[:i]
	}
	return b
}

func uptime() float64 {
	fields := strings.Fields(readFile("/proc/uptime"))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return v
}

func loadAverage() float64 {
	fields := strings.Fields(readFile("/proc/loadavg"))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return v
}

func memory() (total, used int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	var available int64
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			available = v * 1024
		}
	}
	// Available rather than free: free excludes the cache, and a healthy Linux
	// box always looks alarmingly short of free memory.
	return total, total - available
}

func disk(path string) (total, free int64) {
	if path == "" {
		path = "/"
	}
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize)
}

func temperature() float64 {
	raw := strings.TrimSpace(readFile("/sys/class/thermal/thermal_zone0/temp"))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return v / 1000
}

// throttled reads the Raspberry Pi firmware's throttling word.
//
// Bits 0 to 3 are what is happening now, bits 16 to 19 what has happened since
// boot. Under-voltage is the one that matters: it corrupts SD cards, and the
// sticky bit is how a problem that only happens at boot gets noticed at all.
func throttled() *Throttled {
	raw := strings.TrimSpace(readFile("/sys/devices/platform/soc/soc:firmware/get_throttled"))
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(raw, "0x"), 16, 32)
	if err != nil {
		return nil
	}
	w := uint32(v)
	return &Throttled{
		Raw:              w,
		UnderVoltageNow:  w&(1<<0) != 0,
		ThrottledNow:     w&(1<<2) != 0,
		UnderVoltageEver: w&(1<<16) != 0,
		ThrottledEver:    w&(1<<18) != 0,
	}
}

// Format renders a byte count for people.
func Format(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGT"[exp])
}
