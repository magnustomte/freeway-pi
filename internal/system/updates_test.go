package system

import "testing"

// TestParseInstLine pins the shape of apt's simulation output.
//
// The whole package list depends on reading these, and apt's format is not a
// documented interface — so it is written down here, with a real line from the
// box this runs on.
func TestParseInstLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want Package
	}{{
		name: "ordinary upgrade",
		line: "Inst libc6 [2.36-9+deb12u7] (2.36-9+deb12u10 Debian:12.9/stable [arm64])",
		want: Package{Name: "libc6", Version: "2.36-9+deb12u10"},
	}, {
		name: "security archive is noticed",
		line: "Inst openssl [3.0.14-1~deb12u2] (3.0.15-1~deb12u1 Debian-Security:12/stable-security [arm64])",
		want: Package{Name: "openssl", Version: "3.0.15-1~deb12u1", Security: true},
	}, {
		name: "newly installed dependency has no bracketed version",
		line: "Inst linux-image-6.1.0-31-arm64 (6.1.128-1 Debian:12.9/stable [arm64])",
		want: Package{Name: "linux-image-6.1.0-31-arm64", Version: "6.1.128-1"},
	}, {
		name: "nothing useful",
		line: "Inst ",
		want: Package{},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseInstLine(c.line)
			if got != c.want {
				t.Errorf("parseInstLine(%q)\n got %+v\nwant %+v", c.line, got, c.want)
			}
		})
	}
}

// TestCachedUpdatesDoesNotBlock is the whole point of the cache: the count
// shells out to apt and takes about fifteen seconds on this hardware, so the
// system page must never wait for it.
func TestCachedUpdatesDoesNotBlock(t *testing.T) {
	// Whatever the machine running the tests has, the first call returns
	// immediately with Checking set rather than waiting for apt.
	u := CachedUpdates()
	if !u.Checking && u.CheckedAt.IsZero() && u.Pending != 0 {
		t.Errorf("a first call that neither counted nor said it was counting: %+v", u)
	}
}
