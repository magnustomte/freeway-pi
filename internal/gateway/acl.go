// Package gateway serves Modbus TCP on behalf of the unit.
package gateway

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
)

// ACL decides which peers may speak Modbus to the unit.
//
// Freeway WEB allowed exactly one address, which meant a client on DHCP could
// silently lose access. This takes the firewall's shape instead: a list of
// entries, each a single address or a whole subnet, so a Homey that moves
// within its own network keeps working without anyone touching this box.
type ACL struct {
	mu      sync.RWMutex
	enabled bool
	allowed []netip.Prefix
}

// NewACL returns a disabled ACL, which allows nothing.
//
// Disabled means closed, not open: the Modbus gateway is a thing you turn on
// deliberately, and turning it on is where you say who may use it. An empty
// list that quietly admitted the world would be a poor way to learn that.
func NewACL() *ACL { return &ACL{} }

// Set replaces the whole configuration.
func (a *ACL) Set(enabled bool, entries []string) error {
	prefixes, err := ParseEntries(entries)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.enabled = enabled
	a.allowed = prefixes
	return nil
}

// ParseEntries turns configuration text into prefixes. An entry may be a bare
// address, which becomes a single-host prefix, or CIDR notation.
func ParseEntries(entries []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(entries))
	for _, raw := range entries {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		p, err := ParseEntry(e)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ParseEntry parses one entry.
func ParseEntry(e string) (netip.Prefix, error) {
	if strings.Contains(e, "/") {
		p, err := netip.ParsePrefix(e)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not a valid address range: %w", e, err)
		}
		// Masking makes 192.0.2.176/24 mean the same as 192.0.2.0/24
		// rather than being rejected, which is what someone typing their own
		// address with a mask expects.
		return p.Masked(), nil
	}
	addr, err := netip.ParseAddr(e)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not a valid address: %w", e, err)
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// Enabled reports whether the gateway is meant to serve anyone.
func (a *ACL) Enabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.enabled
}

// Entries returns the configured prefixes.
func (a *ACL) Entries() []netip.Prefix {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]netip.Prefix(nil), a.allowed...)
}

// Allows reports whether this peer may connect.
func (a *ACL) Allows(addr netip.Addr) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.enabled {
		return false
	}
	// A v4 address arriving over a dual-stack listener is written as
	// ::ffff:192.0.2.176, which matches no v4 prefix until it is unmapped.
	addr = addr.Unmap()
	for _, p := range a.allowed {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// SuggestLocalPrefixes returns the networks this machine is on, for offering as
// a starting point when someone first enables the gateway. Typing a subnet from
// memory is how a wrong one gets saved.
func SuggestLocalPrefixes() []netip.Prefix {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	// Deduplicated. A box with a cable in and wifi connected has two interfaces
	// on the same subnet, and offering it twice reads as a choice between two
	// things that are the same thing: "Freeway Pi is on 192.0.2.0/24,
	// 192.0.2.0/24." Seen the moment a second interface was configured.
	var out []netip.Prefix
	seen := map[netip.Prefix]bool{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipnet.IP)
			if !ok {
				continue
			}
			ones, _ := ipnet.Mask.Size()
			p := netip.PrefixFrom(addr.Unmap(), ones)
			if !p.IsValid() || !p.Addr().Is4() {
				continue
			}
			if m := p.Masked(); !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}
