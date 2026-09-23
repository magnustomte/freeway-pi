package gateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestResolverNeverBlocks is the property that matters: a name is nice to have
// and a connection must never wait for DNS, which on this box may be the very
// network being reconfigured.
func TestResolverNeverBlocks(t *testing.T) {
	release := make(chan struct{})
	r := newResolver()
	r.lookup = func(ctx context.Context, addr string) ([]string, error) {
		<-release
		return []string{"homey.lan."}, nil
	}

	done := make(chan string, 1)
	go func() { done <- r.Name("192.0.2.41") }()
	select {
	case got := <-done:
		if got != "" {
			t.Errorf("first call returned %q; it should answer empty, not wait", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Name blocked on the lookup")
	}
	close(release)

	// And the name turns up on a later call.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.Name("192.0.2.41") == "homey" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("the name never arrived; got %q", r.Name("192.0.2.41"))
}

// TestResolverCachesMisses: most home networks have no PTR records at all, and
// without this every request for the system page would start a fresh round of
// doomed lookups.
func TestResolverCachesMisses(t *testing.T) {
	var calls atomic.Int64
	r := newResolver()
	r.lookup = func(ctx context.Context, addr string) ([]string, error) {
		calls.Add(1)
		return nil, errors.New("no such host")
	}

	r.Name("192.0.2.9")
	waitFor(t, func() bool { return calls.Load() == 1 })
	for i := 0; i < 20; i++ {
		if got := r.Name("192.0.2.9"); got != "" {
			t.Fatalf("got a name from a failed lookup: %q", got)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d lookups for one address that has no name; want 1", n)
	}
}

// TestDescribeTakesTheHostFromAnAddress: ClientInfo.Addr carries a port, and
// the resolver is given an IP.
func TestDescribeTakesTheHostFromAnAddress(t *testing.T) {
	asked := make(chan string, 1)
	r := newResolver()
	r.lookup = func(ctx context.Context, addr string) ([]string, error) {
		asked <- addr
		return []string{"homey.example."}, nil
	}
	r.describe("192.0.2.41:51923")
	select {
	case got := <-asked:
		if got != "192.0.2.41" {
			t.Errorf("looked up %q, want the address without the port", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no lookup was started")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}

// TestTidyName covers what actually comes back from a home network's DNS.
//
// The device-id case is not hypothetical: a lot of consumer hardware registers
// itself as name-<long hex string>, and both halves of the problem matter —
// the hex is unreadable, and it identifies one specific unit.
func TestTidyName(t *testing.T) {
	cases := []struct{ ptr, want string }{
		{"homey.lan.", "homey"},
		{"nas.example.net.", "nas"},
		{"homey-0123456789abcdef0123456789abcdef.localdomain.", "homey"},
		// Short suffixes and non-hex ones are part of the name, not an id.
		{"printer.lan.", "printer"},
		{"switch-1.lan.", "switch-1"},
		{"server-abc123.lan.", "server-abc123"},
		// Nothing but an id: better a puzzle than an empty label.
		{"0123456789abcdef.lan.", "0123456789abcdef"},
		{"bare", "bare"},
	}
	for _, c := range cases {
		if got := tidyName(c.ptr); got != c.want {
			t.Errorf("tidyName(%q) = %q, want %q", c.ptr, got, c.want)
		}
	}
}

// TestDescribeUnmapsV4MappedAddresses: a client arriving on a dual-stack
// listener appears as ::ffff:192.0.2.41. Looking that up as written would be a
// second cache entry for the same host, and one reverse resolution is far less
// likely to answer.
func TestDescribeUnmapsV4MappedAddresses(t *testing.T) {
	asked := make(chan string, 2)
	r := newResolver()
	r.lookup = func(ctx context.Context, addr string) ([]string, error) {
		asked <- addr
		return []string{"homey.lan."}, nil
	}

	r.describe("[::ffff:192.0.2.41]:51923")
	select {
	case got := <-asked:
		if got != "192.0.2.41" {
			t.Fatalf("looked up %q, want the unmapped address", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no lookup was started")
	}
	waitFor(t, func() bool { return r.Name("192.0.2.41") == "homey" })

	// And the plain form now answers from the same entry rather than starting
	// a second lookup.
	if got := r.describe("192.0.2.41:1502"); got != "homey" {
		t.Errorf("describe of the plain address = %q, want the cached name", got)
	}
	select {
	case extra := <-asked:
		t.Errorf("a second lookup was started for %q", extra)
	default:
	}
}
