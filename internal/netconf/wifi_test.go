package netconf

import (
	"freewaypi/internal/i18n"
	"reflect"
	"strings"
	"testing"
)

// TestUnterseUndoesNmclisEscaping: `nmcli -t` separates fields with a colon and
// escapes any colon or backslash inside a value. A plain Split on ":" reads the
// fields off by one for every SSID that contains one — and then the signal
// strength is a security type and nothing says anything is wrong.
func TestUnterseUndoesNmclisEscaping(t *testing.T) {
	for _, c := range []struct {
		line string
		want []string
	}{
		{`*:Stue:72:WPA2`, []string{"*", "Stue", "72", "WPA2"}},
		{`:Kaffe\: og vaffel:61:WPA2`, []string{"", "Kaffe: og vaffel", "61", "WPA2"}},
		{`:back\\slash:10:WPA3`, []string{"", `back\slash`, "10", "WPA3"}},
		{`::0:`, []string{"", "", "0", ""}},
	} {
		if got := unterse(c.line); !reflect.DeepEqual(got, c.want) {
			t.Errorf("unterse(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// TestTheSameNetworkOnTwoBandsIsOneNetwork: a dual-band router answers on 2.4
// and 5 GHz under one name. Listing it twice asks somebody to choose between
// two things that are the same thing.
func TestTheSameNetworkOnTwoBandsIsOneNetwork(t *testing.T) {
	got := parseNetworks([]string{
		`:Alpha:44:WPA2`,
		`*:Alpha:88:WPA2`,
		`:Nabo:20:WPA2`,
	}, nil)
	if len(got) != 2 {
		t.Fatalf("%d networks, want 2: %+v", len(got), got)
	}
	if got[0].SSID != "Alpha" || got[0].Signal != 88 {
		t.Errorf("first = %+v, want Alpha at its stronger reading", got[0])
	}
	if !got[0].Active {
		t.Error("the band we are actually on was folded away, losing the tick")
	}
}

// TestTheStrongestComesFirst: the one somebody wants is almost always the one
// with the best signal, and scrolling a list of neighbours to find it is the
// difference between a control and a chore.
func TestTheStrongestComesFirst(t *testing.T) {
	got := parseNetworks([]string{
		`:Svak:12:WPA2`,
		`:Sterk:91:WPA2`,
		`:Midt:50:WPA2`,
	}, nil)
	for i, want := range []string{"Sterk", "Midt", "Svak"} {
		if got[i].SSID != want {
			t.Errorf("position %d = %s, want %s", i, got[i].SSID, want)
		}
	}
}

// TestAnOpenNetworkSaysSo: joining one is a decision, so it has to be visible
// before the button is pressed rather than after.
func TestAnOpenNetworkSaysSo(t *testing.T) {
	got := parseNetworks([]string{`:Open Cafe::30:`, `:Alpha:30:WPA2`}, nil)
	var open, closed *Network
	for i := range got {
		if got[i].SSID == "Open Cafe:" {
			open = &got[i]
		}
		if got[i].SSID == "Alpha" {
			closed = &got[i]
		}
	}
	// nmcli escapes a colon in an SSID; this one is not escaped, so the field
	// after IN-USE really is "Open Cafe" and the SSID really is empty — which is a
	// hidden network and is dropped rather than shown as a blank row.
	if open != nil {
		t.Fatalf("read a malformed line as a network: %+v", open)
	}
	if closed == nil || closed.Open {
		t.Fatalf("a WPA2 network was read as open: %+v", closed)
	}
}

func TestOpenAndSavedAreReported(t *testing.T) {
	got := parseNetworks([]string{
		`:Open Cafe:30:`,
		`:Alpha:80:WPA2`,
	}, map[string]bool{"Alpha": true})

	if got[0].SSID != "Alpha" || !got[0].Saved || got[0].Open {
		t.Errorf("Alpha = %+v, want saved and not open", got[0])
	}
	if got[1].SSID != "Open Cafe" || got[1].Open != true || got[1].Security != "" {
		t.Errorf("Open Cafe = %+v, want open with no security named", got[1])
	}
	if got[1].Saved {
		t.Error("a network this box has never joined was reported as saved")
	}
}

// TestAHiddenNetworkIsNotOffered: an access point that does not broadcast its
// name shows up with an empty SSID, and a blank row somebody can click is worse
// than no row at all.
func TestAHiddenNetworkIsNotOffered(t *testing.T) {
	got := parseNetworks([]string{`::62:WPA2`, `:Alpha:40:WPA2`}, nil)
	if len(got) != 1 || got[0].SSID != "Alpha" {
		t.Fatalf("got %+v, want only the named network", got)
	}
}

// TestThePrimaryDeviceIsTheOneWithTheBestRoute: a box with a cable in and wifi
// configured has both connections up at once, and which one nmcli names first
// is an accident of activation order. Reading that order as "the network this
// box is on" reports a cable on a box running entirely over the air.
func TestThePrimaryDeviceIsTheOneWithTheBestRoute(t *testing.T) {
	both := []string{
		"default via 192.0.2.1 dev eth0 proto dhcp src 192.0.2.10 metric 100",
		"default via 192.0.2.1 dev wlan0 proto dhcp src 192.0.2.11 metric 600",
	}
	if got := pickDefaultDevice(both); got != "eth0" {
		t.Errorf("with a cable in, primary = %q, want eth0", got)
	}

	// The cable comes out. The wifi route is the only one left, and the answer
	// has to change with it.
	if got := pickDefaultDevice(both[1:]); got != "wlan0" {
		t.Errorf("after the cable is pulled, primary = %q, want wlan0", got)
	}
}

// TestARouteWithNoMetricWins: metric is optional in ip's output and absent
// means zero, which is the best there is. Reading it as "no metric, skip" would
// ignore the only route a statically configured box has.
func TestARouteWithNoMetricWins(t *testing.T) {
	got := pickDefaultDevice([]string{
		"default via 192.0.2.1 dev wlan0 proto dhcp src 192.0.2.11 metric 600",
		"default via 192.0.2.1 dev eth0",
	})
	if got != "eth0" {
		t.Errorf("primary = %q, want eth0", got)
	}
}

// TestNoDefaultRouteIsAnAnswer: a box that has just lost its only link still
// has an active connection and no way out. Claiming a device there would make
// the page report a network that cannot carry anything.
func TestNoDefaultRouteIsAnAnswer(t *testing.T) {
	if got := pickDefaultDevice(nil); got != "" {
		t.Errorf("primary = %q, want nothing claimed", got)
	}
	if got := pickDefaultDevice([]string{"192.0.2.0/24 dev eth0 scope link"}); got != "" {
		t.Errorf("a link-local route was read as a way out: %q", got)
	}
}

// TestAKeyWithNoCommentIsStillReadRight: ssh-keygen prints "no comment" as two
// words where a comment would be, so fixed field positions shift under exactly
// the keys nobody bothered to name — and the type ends up in the comment.
func TestAKeyWithNoCommentIsStillReadRight(t *testing.T) {
	for _, c := range []struct {
		line        string
		wantType    string
		wantComment string
	}{
		{"key=SHA256:abc (ED25519) freeway-pi", "ED25519", "freeway-pi"},
		{"key=SHA256:abc (ED25519) no comment", "ED25519", ""},
		{"key=SHA256:abc (RSA) a comment with spaces", "RSA", "a comment with spaces"},
	} {
		_, keys := parseSSHKeys(c.line)
		if len(keys) != 1 {
			t.Fatalf("%q gave %d keys", c.line, len(keys))
		}
		if keys[0].Type != c.wantType || keys[0].Comment != c.wantComment {
			t.Errorf("%q = type %q comment %q, want %q and %q",
				c.line, keys[0].Type, keys[0].Comment, c.wantType, c.wantComment)
		}
	}
}

// TestCountryIsNeverADriversPlaceholder: on a Pi with built-in wifi and no
// country chosen, `iw reg get` answers "00" globally and "99" for the phy. The
// System page showed "99" as the country — on the one box where the answer
// that mattered was that none had been set.
func TestCountryIsNeverADriversPlaceholder(t *testing.T) {
	for _, c := range []struct {
		why   string
		lines []string
		want  string
	}{
		{"fresh Pi, nothing chosen", []string{"global", "country 00: DFS-UNSET",
			"	(2402 - 2472 @ 40), (N/A, 20), (N/A)", "", "phy#0 (self-managed)",
			"country 99: DFS-UNSET"}, ""},
		{"country chosen", []string{"global", "country NO: DFS-ETSI",
			"	(2400 - 2483 @ 40), (N/A, 20), (N/A)", "", "phy#0 (self-managed)",
			"country 99: DFS-UNSET"}, "NO"},
		{"an intersection is not a country", []string{"country 98: DFS-UNSET"}, ""},
		{"nothing at all", nil, ""},
	} {
		if got := countryFrom(c.lines); got != c.want {
			t.Errorf("%s: got %q, want %q", c.why, got, c.want)
		}
	}
}

// TestAWrongPassphraseIsCalledThat: NetworkManager reports a rejected key as
// "Secrets were required, but not provided", which reads as though nothing
// was sent. It was sent and it was wrong, and the page has to say so — the
// real message from a real wrong passphrase is the one below.
func TestAWrongPassphraseIsCalledThat(t *testing.T) {
	raw := i18n.Errf("helper.wifi.join", "Error: Connection activation failed: Secrets were required, but not provided.")
	for _, l := range i18n.Languages() {
		if got := i18n.Translate(l, joinError(raw)); strings.Contains(got, "Secrets") {
			t.Errorf("%s: a wrong passphrase was reported as %q", l, got)
		}
	}

	// Anything else keeps NetworkManager's reason, under a heading in the
	// reader's language rather than always the helper's Norwegian.
	other := i18n.Errf("helper.wifi.join", "Error: No network with SSID 'x' found.")
	got := i18n.Translate(i18n.EN, joinError(other))
	if strings.Contains(got, "kom ikke") {
		t.Errorf("the helper's Norwegian heading reached an English reader: %q", got)
	}
	if !strings.Contains(got, "No network with SSID") {
		t.Errorf("NetworkManager's own reason was lost: %q", got)
	}
	if joinError(nil) != nil {
		t.Error("success became an error")
	}
}
