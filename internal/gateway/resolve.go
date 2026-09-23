package gateway

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// resolver puts a name to a client address.
//
// An IP tells you a client is connected; a name tells you it is the Homey. The
// lookup is worth doing and never worth waiting for: it goes to a DNS server
// that may be the very network being reconfigured, and a connection must not be
// held up by it. So it is always answered from cache, and a miss starts a
// lookup for the next time somebody asks.
//
// Failures are cached too, and for longer than successes. Most home networks
// have no PTR records at all, and without a negative cache every request for
// the system page would start a fresh round of doomed lookups.
type resolver struct {
	mu      sync.Mutex
	entries map[string]*nameEntry
	// lookup is net.DefaultResolver.LookupAddr, replaced in tests.
	lookup func(ctx context.Context, addr string) ([]string, error)
}

type nameEntry struct {
	name    string
	expires time.Time
	pending bool
}

const (
	nameTTL     = 30 * time.Minute
	nameMissTTL = 4 * time.Hour
)

func newResolver() *resolver {
	return &resolver{
		entries: make(map[string]*nameEntry),
		lookup: func(ctx context.Context, addr string) ([]string, error) {
			return net.DefaultResolver.LookupAddr(ctx, addr)
		},
	}
}

// Name returns the cached name for an IP, or the empty string, and starts a
// lookup when there is nothing fresh.
func (r *resolver) Name(ip string) string {
	if ip == "" {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.entries[ip]
	if ok && time.Now().Before(e.expires) {
		return e.name
	}
	if ok && e.pending {
		return e.name
	}
	if !ok {
		e = &nameEntry{}
		r.entries[ip] = e
	}
	e.pending = true
	go r.resolve(ip)
	return e.name
}

func (r *resolver) resolve(ip string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	names, err := r.lookup(ctx, ip)

	name, ttl := "", nameMissTTL
	if err == nil && len(names) > 0 {
		name = tidyName(names[0])
		ttl = nameTTL
	}

	r.mu.Lock()
	r.entries[ip] = &nameEntry{name: name, expires: time.Now().Add(ttl)}
	r.mu.Unlock()
}

// tidyName turns a PTR record into something worth putting on the overview.
//
// Two steps. The domain goes, because it is the same for everything on the
// network and only costs width on a phone. Then a trailing device id goes: a
// lot of consumer hardware registers itself as name-<long hex string>, and the
// hex is neither readable nor anybody's business — it identifies one specific
// unit, which is exactly what should not end up on a screen or in a log.
//
// Conservative on purpose: only a final segment that is long and entirely
// hexadecimal is dropped, so an ordinary hyphenated hostname survives intact.
func tidyName(ptr string) string {
	name := strings.TrimSuffix(ptr, ".")
	if host, _, found := strings.Cut(name, "."); found && host != "" {
		name = host
	}
	if i := strings.LastIndex(name, "-"); i > 0 {
		if isDeviceID(name[i+1:]) {
			name = name[:i]
		}
	}
	return name
}

func isDeviceID(s string) bool {
	if len(s) < 12 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// describe puts the friendliest available label on an address.
//
// The address is unmapped first. A client arriving on a dual-stack listener
// appears as ::ffff:192.0.2.41, which is the same host as 192.0.2.41 and would
// otherwise be a second cache entry — and one that reverse resolution is far
// less likely to answer.
func (r *resolver) describe(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return r.Name(unmap(host))
}

// unmap turns an IPv4-mapped IPv6 address back into the IPv4 address it is,
// and leaves anything it cannot parse alone.
func unmap(host string) string {
	a, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	return a.Unmap().String()
}
