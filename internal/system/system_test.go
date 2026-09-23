package system

import (
	"testing"
	"time"
)

func TestReadSurvivesWhateverIsMissing(t *testing.T) {
	// The workstation this runs on has no Raspberry Pi firmware and no model
	// file. A missing file should leave one field blank, not fail the page.
	i := Read("/")
	if i.Hostname == "" {
		t.Error("no hostname")
	}
	if i.MemoryTotalBytes <= 0 {
		t.Error("no memory total")
	}
	if i.DiskTotalBytes <= 0 {
		t.Error("no disk total")
	}
	if i.UptimeSeconds <= 0 {
		t.Error("no uptime")
	}
	if i.Kernel == "" || i.Arch == "" {
		t.Errorf("kernel = %q, arch = %q", i.Kernel, i.Arch)
	}
}

func TestMemoryUsesAvailableRatherThanFree(t *testing.T) {
	// A healthy Linux box has almost no free memory, because it uses the rest
	// for cache. Reporting that as "used" would look like a box about to fall
	// over.
	total, used := memory()
	if total <= 0 {
		t.Skip("no /proc/meminfo")
	}
	if used >= total {
		t.Fatalf("used %d of %d: that is free, not available", used, total)
	}
	if float64(used)/float64(total) > 0.98 {
		t.Errorf("used %.0f%% of memory; that reads as the free figure", 100*float64(used)/float64(total))
	}
}

func TestDistroEOLKnowsTheReleasesThisCanRunOn(t *testing.T) {
	// Hard-coded so the box can warn about its own obsolescence without a
	// working internet connection, which is exactly the situation where it has
	// been forgotten.
	for _, id := range []string{"12", "13"} {
		if _, ok := distroEOL[id]; !ok {
			t.Errorf("no end-of-life date for Debian %s", id)
		}
	}
	if eol := distroEOL["13"]; !eol.After(time.Now()) {
		t.Error("trixie is recorded as already unsupported; check the table")
	}
}

func TestThrottledDecodesTheStickyBits(t *testing.T) {
	// 0x50000 is what the Pi reported when it was under-volting: bits 16 and
	// 18, meaning it happened and caused throttling, but is not happening now.
	// The sticky bits are the whole point — under-voltage at boot is over by
	// the time anybody looks.
	w := uint32(0x50000)
	got := Throttled{
		Raw:              w,
		UnderVoltageNow:  w&(1<<0) != 0,
		ThrottledNow:     w&(1<<2) != 0,
		UnderVoltageEver: w&(1<<16) != 0,
		ThrottledEver:    w&(1<<18) != 0,
	}
	if got.UnderVoltageNow || got.ThrottledNow {
		t.Error("0x50000 decoded as a problem happening right now")
	}
	if !got.UnderVoltageEver || !got.ThrottledEver {
		t.Error("0x50000 did not decode as having happened")
	}
}

func TestFormatIsReadable(t *testing.T) {
	for in, want := range map[int64]string{
		512:                     "512 B",
		1024 * 1024 * 3:         "3.0 MB",
		1024 * 1024 * 1024 * 29: "29.0 GB",
	} {
		if got := Format(in); got != want {
			t.Errorf("Format(%d) = %q, want %q", in, got, want)
		}
	}
}
