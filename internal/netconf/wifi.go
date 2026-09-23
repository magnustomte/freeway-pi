package netconf

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"freewaypi/internal/i18n"
	"freewaypi/internal/privhelper"
)

// WiFiRevertAfter is how long the browser has to come back over the new network
// and confirm.
//
// Longer than the wired revert: joining a network means association, DHCP and,
// on a phone, noticing that the network it was on has gone away.
const WiFiRevertAfter = 120 * time.Second

// Network is one wifi network the radio can hear.
type Network struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal"`
	// Security is nmcli's word for it — "WPA2", "WPA3", or empty for an open
	// network, which is worth telling somebody before they join it.
	Security string `json:"security"`
	Open     bool   `json:"open"`
	Active   bool   `json:"active"`
	// Saved is true when this box already knows the passphrase.
	Saved bool `json:"saved"`
}

// WiFi is the state of the radio, as the System page shows it.
type WiFi struct {
	// Supported is false when there is no wifi interface at all. Freeway Pi
	// prefers a cable, and a box without a radio should not be shown a control
	// it can never use.
	Supported bool `json:"supported"`
	// Radio reports NetworkManager's own software switch, which is neither
	// rfkill nor the hardware. Off, the device reads "unavailable" with the
	// hardware perfectly healthy and every scan comes back empty — so this is
	// asked rather than assumed, and the interface offers to turn it on.
	Radio bool `json:"radio"`
	// Connected is true when the active connection is the wireless one, which
	// is what decides whether changing it can strand the box.
	Connected bool   `json:"connected"`
	SSID      string `json:"ssid,omitempty"`
	Signal    int    `json:"signal,omitempty"`
	Device    string `json:"device,omitempty"`
	// Address is what the radio holds right now, which with a cable also
	// plugged in appears nowhere else — and is exactly the address the box
	// moves to the moment the cable comes out.
	Address string `json:"address,omitempty"`
	// Country is the regulatory domain. Empty is a real answer and a common
	// cause of "the scan finds nothing": the radio stays blocked until it
	// knows which channels are legal where it is standing.
	Country string `json:"country,omitempty"`
	// Saved networks, so one can be forgotten without retyping its passphrase.
	Saved []string `json:"saved,omitempty"`

	Writable bool   `json:"writable"`
	Why      string `json:"why,omitempty"`
}

// ReadWiFi reports the state of the radio. Reading needs no privileges.
func ReadWiFi(ctx context.Context) *WiFi {
	w := &WiFi{}
	w.Writable, w.Why = writable(ctx)

	lines, err := nmcli(ctx, "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device", "status")
	if err != nil {
		return w
	}
	for _, line := range lines {
		f := unterse(line)
		if len(f) < 4 || f[1] != "wifi" {
			continue
		}
		w.Supported = true
		w.Device = f[0]
		if f[2] == "connected" {
			w.SSID = f[3]
		}
		break
	}
	if !w.Supported {
		return w
	}
	if out, err := nmcli(ctx, "-t", "-f", "WIFI", "radio"); err == nil && len(out) > 0 {
		w.Radio = out[0] == "enabled"
	}

	// Connected over wifi is not the same as having a wifi connection: with a
	// cable in as well, the wireless one is up but nothing depends on it.
	if active, _, err := activeConnection(ctx); err == nil && active != "" && active == w.SSID {
		w.Connected = true
	}
	if w.SSID != "" {
		if live, err := nmcli(ctx, "-t", "-f", "IP4.ADDRESS", "device", "show", w.Device); err == nil {
			for _, line := range live {
				if _, value, ok := strings.Cut(line, ":"); ok && value != "" {
					w.Address = value
					break
				}
			}
		}
		for _, n := range scanCache(ctx, w.Device) {
			if n.SSID == w.SSID {
				w.Signal = n.Signal
				break
			}
		}
	}

	if out, err := nmcli(ctx, "-t", "-f", "NAME,TYPE", "connection", "show"); err == nil {
		for _, line := range out {
			f := unterse(line)
			if len(f) >= 2 && f[1] == "802-11-wireless" {
				w.Saved = append(w.Saved, f[0])
			}
		}
		sort.Strings(w.Saved)
	}
	w.Country = regulatoryDomain(ctx)
	return w
}

// scanCache reads what NetworkManager already heard, without asking the radio
// to go looking. Used for the signal strength on a page load, where a few
// seconds of rescan would be felt.
func scanCache(ctx context.Context, device string) []Network {
	lines, err := nmcli(ctx, "-t", "-f", "IN-USE,SSID,SIGNAL,SECURITY",
		"device", "wifi", "list", "ifname", device)
	if err != nil {
		return nil
	}
	return parseNetworks(lines, nil)
}

func regulatoryDomain(ctx context.Context) string {
	// iw lives in /usr/sbin, which is on a systemd service's PATH but not on
	// every interactive one — so the name is tried first and the absolute path
	// second, rather than reporting "no country set" forever on a box that has
	// one.
	lines, err := run(ctx, "iw", "reg", "get")
	if err != nil {
		lines, err = run(ctx, "/usr/sbin/iw", "reg", "get")
	}
	if err != nil {
		return ""
	}
	return countryFrom(lines)
}

// countryFrom picks the country out of `iw reg get`.
//
// Only two capital letters are a country. The kernel says "00" for none, and
// the Broadcom driver on every Pi with built-in wifi reports its own domain as
// "99" — which the System page showed as though it were a country, on exactly
// the fresh box where no country has been chosen and the radio is switched off
// for that reason. The global line comes first and carries the real answer
// once one is set, so the first real code wins.
func countryFrom(lines []string) string {
	for _, l := range lines {
		rest, ok := strings.CutPrefix(strings.TrimSpace(l), "country ")
		if !ok {
			continue
		}
		code, _, _ := strings.Cut(rest, ":")
		code = strings.TrimSpace(code)
		if len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z' {
			return code
		}
	}
	return ""
}

// Scan asks the radio to look now. This takes a few seconds, which is why it is
// a button and not something a page load does.
func Scan(ctx context.Context) ([]Network, error) {
	out, err := privhelper.Call(ctx, "wifi-scan")
	if err != nil {
		return nil, err
	}
	saved := map[string]bool{}
	for _, s := range ReadWiFi(ctx).Saved {
		saved[s] = true
	}
	return parseNetworks(strings.Split(out, "\n"), saved), nil
}

// parseNetworks turns nmcli's terse output into a list, strongest first, with
// each network appearing once — the same SSID on two bands is one network as
// far as anybody choosing it is concerned.
func parseNetworks(lines []string, saved map[string]bool) []Network {
	best := map[string]Network{}
	var order []string
	for _, line := range lines {
		f := unterse(strings.TrimSpace(line))
		if len(f) < 4 || f[1] == "" {
			continue
		}
		signal, _ := strconv.Atoi(f[2])
		n := Network{
			SSID:     f[1],
			Signal:   signal,
			Security: f[3],
			Open:     f[3] == "" || f[3] == "--",
			Active:   f[0] == "*",
			Saved:    saved[f[1]],
		}
		if n.Open {
			n.Security = ""
		}
		prev, seen := best[n.SSID]
		if !seen {
			order = append(order, n.SSID)
			best[n.SSID] = n
			continue
		}
		if n.Signal > prev.Signal {
			n.Active = n.Active || prev.Active
			best[n.SSID] = n
		} else if n.Active {
			prev.Active = true
			best[n.SSID] = prev
		}
	}
	out := make([]Network, 0, len(order))
	for _, ssid := range order {
		out = append(out, best[ssid])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Signal > out[j].Signal })
	return out
}

// unterse splits one line of `nmcli -t` output.
//
// nmcli separates fields with a colon and escapes any colon or backslash
// inside a value with a backslash — so a plain Split eats SSIDs and, more
// quietly, MAC addresses. This undoes exactly that.
func unterse(line string) []string {
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			if i+1 < len(line) {
				i++
				cur.WriteByte(line[i])
			}
		case ':':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	return append(fields, cur.String())
}

// Join connects to a wifi network, arming a revert first when the box is
// already on wifi — which is the only case where getting it wrong takes the
// box away.
//
// The passphrase goes to the helper and is never stored, logged or read back:
// the API that shows the network settings has no field for it.
func Join(ctx context.Context, ssid, psk string, revertAfter time.Duration) error {
	if ssid == "" {
		return i18n.Errf("error.wifi.ssid")
	}
	if psk != "" && (len(psk) < 8 || len(psk) > 64) {
		return i18n.Errf("error.wifi.psk")
	}
	_, err := privhelper.Call(ctx, "wifi-connect",
		privhelper.Field{Key: "ssid", Value: ssid},
		privhelper.Field{Key: "psk", Value: psk},
		privhelper.Field{Key: "revert", Value: fmt.Sprint(int(revertAfter.Seconds()))})
	return joinError(err)
}

// joinError says what a failed join means.
//
// A wrong passphrase reaches NetworkManager, the radio associates, and the
// access point rejects the key in the handshake. NetworkManager then asks for
// another, finds nobody to ask, and reports "Secrets were required, but not
// provided" — which reads as though the passphrase never arrived, when it
// arrived and was wrong. That is by far the commonest failure, so it is the
// one worth saying plainly. Anything else keeps NetworkManager's own words,
// under a heading in the reader's language rather than the helper's.
func joinError(err error) error {
	var e *i18n.Error
	if !errors.As(err, &e) || e.ID != "helper.wifi.join" || len(e.Args) == 0 {
		return err
	}
	detail, _ := e.Args[0].(string)
	if strings.Contains(detail, "Secrets were required") || strings.Contains(detail, "no-secrets") {
		return i18n.Errf("error.wifi.rejected")
	}
	return i18n.Errf("helper.wifi.join", strings.TrimPrefix(detail, "Error: "))
}

// Forget deletes a saved network.
func Forget(ctx context.Context, ssid string) error {
	_, err := privhelper.Call(ctx, "wifi-forget", privhelper.Field{Key: "ssid", Value: ssid})
	return err
}

// SetCountry sets the regulatory domain.
//
// Not a detail: until it is set the radio is blocked, and a scan that finds
// nothing looks like broken hardware rather than like an unanswered question.
func SetCountry(ctx context.Context, code string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 {
		return i18n.Errf("error.wifi.country")
	}
	_, err := privhelper.Call(ctx, "wifi-country", privhelper.Field{Key: "country", Value: code})
	return err
}

// EnableRadio turns NetworkManager's software wifi switch on.
//
// Its own operation because it is its own failure: a box with healthy hardware,
// a country set and the switch off looks exactly like a box with a broken card,
// and no amount of scanning says otherwise.
func EnableRadio(ctx context.Context) error {
	_, err := privhelper.Call(ctx, "wifi-radio")
	return err
}
