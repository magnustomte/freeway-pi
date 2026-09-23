// Package netconf reads and changes the box's network settings.
//
// Reading needs no privileges. Changing does, and that is done by the root
// helper in package privhelper — not by running the daemon as root, and not by
// letting it run arbitrary commands.
//
// A change to the address of a box reached only over the network can lock
// everybody out of it, so every change is applied with a revert armed behind
// it. The browser has to come back on the new address and confirm; if it does
// not, the old settings return by themselves.
package netconf

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"freewaypi/internal/i18n"

	"freewaypi/internal/privhelper"
)

// Config is the network settings as they are or as they should be.
type Config struct {
	// Connection is NetworkManager's name for it, e.g. "Wired connection 1".
	Connection string `json:"connection"`
	Device     string `json:"device"`
	// DHCP is true when the address comes from the network.
	DHCP bool `json:"dhcp"`
	// Address is CIDR, e.g. 192.0.2.10/24. Empty on DHCP.
	Address string   `json:"address"`
	Gateway string   `json:"gateway"`
	DNS     []string `json:"dns"`

	// Current holds what the box actually has right now, which on DHCP is the
	// only place the address appears at all.
	CurrentAddress string   `json:"current_address"`
	CurrentGateway string   `json:"current_gateway"`
	CurrentDNS     []string `json:"current_dns"`

	// Writable reports whether the helper is installed and permitted. Without
	// it the interface shows the settings and says how to enable changing
	// them, rather than offering a control that fails.
	Writable bool   `json:"writable"`
	Why      string `json:"why,omitempty"`

	// Links are the connections that could be configured instead of this one.
	//
	// A box with a cable in and wifi connected has two, and only one of them
	// can be the one being shown. Without this the interface could only ever
	// edit whichever carried traffic at that moment — so giving the wifi a
	// static address meant physically unplugging the cable first.
	Links []Link `json:"links,omitempty"`
}

// Link is one connection that can be configured.
type Link struct {
	Connection string `json:"connection"`
	Device     string `json:"device"`
	Wireless   bool   `json:"wireless"`
	// Primary is the one carrying traffic now, which is the one shown unless
	// somebody asks for another.
	Primary bool `json:"primary"`
}

// Read returns the settings of the connection carrying traffic.
func Read(ctx context.Context) (*Config, error) { return ReadConnection(ctx, "") }

// ReadConnection returns one connection's settings. An empty name means
// whichever is carrying traffic.
//
// The name is checked against the active connections rather than passed
// through: everything here arrives from a browser, and "which of these" is a
// question with a short list of right answers.
func ReadConnection(ctx context.Context, want string) (*Config, error) {
	links, err := Links(ctx)
	if err != nil {
		return nil, err
	}
	name, device := "", ""
	for _, l := range links {
		if (want == "" && l.Primary) || (want != "" && l.Connection == want) {
			name, device = l.Connection, l.Device
			break
		}
	}
	if name == "" {
		if want != "" {
			return nil, i18n.Errf("error.network.unknown", want)
		}
		return nil, i18n.Errf("error.network.none")
	}

	c := &Config{Connection: name, Device: device, Links: links}

	fields, err := nmcli(ctx, "-t", "-f", "ipv4.method,ipv4.addresses,ipv4.gateway,ipv4.dns",
		"connection", "show", name)
	if err != nil {
		return nil, err
	}
	for _, line := range fields {
		key, value, _ := strings.Cut(line, ":")
		switch key {
		case "ipv4.method":
			c.DHCP = value == "auto"
		case "ipv4.addresses":
			c.Address = value
		case "ipv4.gateway":
			c.Gateway = value
		case "ipv4.dns":
			c.DNS = splitList(value)
		}
	}

	// What it actually has, which on DHCP is the only place it appears.
	live, err := nmcli(ctx, "-t", "-f", "IP4.ADDRESS,IP4.GATEWAY,IP4.DNS", "device", "show", device)
	if err == nil {
		for _, line := range live {
			key, value, _ := strings.Cut(line, ":")
			switch {
			case strings.HasPrefix(key, "IP4.ADDRESS") && c.CurrentAddress == "":
				c.CurrentAddress = value
			case key == "IP4.GATEWAY":
				c.CurrentGateway = value
			case strings.HasPrefix(key, "IP4.DNS"):
				c.CurrentDNS = append(c.CurrentDNS, value)
			}
		}
	}

	c.Writable, c.Why = writable(ctx)
	return c, nil
}

// writable reports whether a change could actually be applied.
//
// Checked rather than assumed, so the interface can say "this needs enabling"
// instead of offering a button that fails at the worst possible moment.
func writable(ctx context.Context) (bool, string) {
	return privhelper.Available(ctx)
}

// Links lists the connections that are up, marking the one carrying traffic.
func Links(ctx context.Context) ([]Link, error) {
	lines, err := nmcli(ctx, "-t", "-f", "NAME,DEVICE,TYPE", "connection", "show", "--active")
	if err != nil {
		return nil, err
	}
	primary := primaryDevice(ctx)
	var out []Link
	for _, line := range lines {
		// unterse, not Split: a wifi connection is named after its SSID, and
		// an SSID may contain a colon. nmcli escapes it; splitting naively
		// would cut the name in half and then read the fields off by one.
		parts := unterse(line)
		if len(parts) < 3 || parts[2] == "loopback" {
			continue
		}
		out = append(out, Link{
			Connection: parts[0],
			Device:     parts[1],
			Wireless:   strings.Contains(parts[2], "wireless"),
			Primary:    parts[1] == primary,
		})
	}
	// With no default route at all nothing is primary, and the interface still
	// has to show something rather than an empty card.
	if len(out) > 0 {
		found := false
		for _, l := range out {
			found = found || l.Primary
		}
		if !found {
			out[0].Primary = true
		}
	}
	return out, nil
}

// activeConnection returns the connection that actually carries traffic.
//
// Not simply the first one nmcli lists. A box with a cable in and wifi
// configured has both up at once, and which of them nmcli names first is an
// accident of activation order — so the page could report "Wired connection 1"
// on a box running entirely over the air, or the reverse. The honest answer is
// whichever device owns the best default route, and only when there is no
// default route at all does order have to decide anything.
func activeConnection(ctx context.Context) (name, device string, err error) {
	lines, err := nmcli(ctx, "-t", "-f", "NAME,DEVICE,TYPE", "connection", "show", "--active")
	if err != nil {
		return "", "", err
	}

	primary := primaryDevice(ctx)
	var firstName, firstDevice string
	for _, line := range lines {
		// unterse, not Split: a wifi connection is named after its SSID, and
		// an SSID may contain a colon. nmcli escapes it; splitting naively
		// would cut the name in half and then read the fields off by one.
		parts := unterse(line)
		if len(parts) < 3 || parts[2] == "loopback" {
			continue
		}
		if parts[1] == primary {
			return parts[0], parts[1], nil
		}
		if firstName == "" {
			firstName, firstDevice = parts[0], parts[1]
		}
	}
	if firstName != "" {
		return firstName, firstDevice, nil
	}
	return "", "", i18n.Errf("error.network.none")
}

// primaryDevice is the interface holding the default route with the lowest
// metric — the one a packet to the rest of the network actually leaves by.
//
// Empty when there is no default route, which is a real state: a box that has
// just lost its only link still has an active connection and no way out.
func primaryDevice(ctx context.Context) string {
	// ip lives in /sbin, which is on a systemd service's PATH but not on every
	// interactive one.
	lines, err := run(ctx, "ip", "-4", "route", "show", "default")
	if err != nil {
		lines, err = run(ctx, "/sbin/ip", "-4", "route", "show", "default")
	}
	if err != nil {
		return ""
	}
	return pickDefaultDevice(lines)
}

// pickDefaultDevice is the parsing half, kept apart from the command so it can
// be tested against output nobody has to have a second interface to produce.
func pickDefaultDevice(lines []string) string {
	best, bestMetric := "", math.MaxInt
	for _, line := range lines {
		// Only default routes. The command above asks for those alone, but a
		// route to the local subnet is not a way off it, and a function that
		// depends on its caller having filtered is one refactor from claiming
		// the box has a gateway it does not.
		if !strings.HasPrefix(line, "default ") {
			continue
		}
		fields := strings.Fields(line)
		dev, metric := "", 0
		for i, f := range fields {
			switch f {
			case "dev":
				if i+1 < len(fields) {
					dev = fields[i+1]
				}
			case "metric":
				if i+1 < len(fields) {
					metric, _ = strconv.Atoi(fields[i+1])
				}
			}
		}
		// A route with no metric is metric 0, which is the best there is.
		if dev != "" && metric < bestMetric {
			best, bestMetric = dev, metric
		}
	}
	return best
}

func nmcli(ctx context.Context, args ...string) ([]string, error) {
	return run(ctx, "nmcli", args...)
}

// run executes a read-only command and returns its non-empty lines.
func run(ctx context.Context, name string, args ...string) ([]string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("netconf: %s %s: %w", name, strings.Join(args, " "), err)
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Apply changes the settings, arming a revert first.
//
// The order matters: the revert is armed before anything changes, so a box that
// becomes unreachable the instant the new settings apply still comes back. The
// browser confirms from the new address, which is the only proof that anybody
// can still reach it.
func Apply(ctx context.Context, c Config, revertAfter time.Duration) error {
	if !c.DHCP {
		if c.Address == "" {
			return i18n.Errf("error.network.address")
		}
		if !strings.Contains(c.Address, "/") {
			return i18n.Errf("error.network.netmask")
		}
	}

	fields := []privhelper.Field{
		{Key: "connection", Value: c.Connection},
		{Key: "revert", Value: fmt.Sprint(int(revertAfter.Seconds()))},
	}
	if c.DHCP {
		fields = append(fields, privhelper.Field{Key: "dhcp", Value: "1"})
	} else {
		fields = append(fields,
			privhelper.Field{Key: "dhcp", Value: "0"},
			privhelper.Field{Key: "address", Value: c.Address},
			privhelper.Field{Key: "gateway", Value: c.Gateway},
			privhelper.Field{Key: "dns", Value: strings.Join(c.DNS, ",")})
	}

	if _, err := privhelper.Call(ctx, "apply", fields...); err != nil {
		return fmt.Errorf("netconf: %w", err)
	}
	return nil
}

// Confirm cancels the armed revert. Called once the browser has reached the box
// on its new settings, which is the only evidence that they work.
func Confirm(ctx context.Context) error {
	if _, err := privhelper.Call(ctx, "confirm"); err != nil {
		return fmt.Errorf("netconf: %w", err)
	}
	return nil
}

// Pending reports whether a change is waiting to be confirmed.
func Pending(ctx context.Context) bool {
	out, err := privhelper.Call(ctx, "pending")
	return err == nil && out == "pending"
}
