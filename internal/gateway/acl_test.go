package gateway

import (
	"net/netip"
	"testing"
)

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("bad test address %q: %v", s, err)
	}
	return a
}

func TestACLDisabledAllowsNobody(t *testing.T) {
	// Turning the gateway on is where you say who may use it. A disabled
	// gateway that quietly admitted everyone would be a nasty surprise.
	a := NewACL()
	if err := a.Set(false, []string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	if a.Allows(mustAddr(t, "192.0.2.5")) {
		t.Fatal("a disabled gateway admitted a listed peer")
	}
}

func TestACLAllowsSingleAddressesAndSubnets(t *testing.T) {
	a := NewACL()
	if err := a.Set(true, []string{"192.0.2.10", "198.51.100.0/24"}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"192.0.2.10":   true,  // the listed host
		"192.0.2.11":   false, // its neighbour is not implied
		"198.51.100.1": true,  // anywhere in the listed subnet
		"198.51.100.9": true,
		"198.51.101.1": false,
		"203.0.113.1":  false,
	}
	for addr, want := range cases {
		if got := a.Allows(mustAddr(t, addr)); got != want {
			t.Errorf("Allows(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestACLSubnetSurvivesAClientChangingAddress(t *testing.T) {
	// The reason to support subnets at all: a client on DHCP must not lose
	// access because its lease moved.
	a := NewACL()
	if err := a.Set(true, []string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"192.0.2.176", "192.0.2.42", "192.0.2.254"} {
		if !a.Allows(mustAddr(t, addr)) {
			t.Errorf("a client that moved to %s lost access", addr)
		}
	}
}

func TestParseEntryMasksHostBits(t *testing.T) {
	// Someone typing their own address with a mask means the subnet it is in.
	p, err := ParseEntry("192.0.2.176/24")
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParsePrefix("192.0.2.0/24"); p != want {
		t.Fatalf("parsed to %v, want %v", p, want)
	}
}

func TestParseEntryRejectsNonsense(t *testing.T) {
	for _, e := range []string{"not-an-address", "192.0.2.1/33", "192.0.2.300", "192.0.2.0/", ""} {
		if _, err := ParseEntry(e); err == nil {
			t.Errorf("accepted %q", e)
		}
	}
}

func TestSetRejectsTheWholeListIfAnyEntryIsBad(t *testing.T) {
	// A half-applied access list is worse than a rejected one.
	a := NewACL()
	if err := a.Set(true, []string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Set(true, []string{"198.51.100.0/24", "rubbish"}); err == nil {
		t.Fatal("a list containing a bad entry was accepted")
	}
	if !a.Allows(mustAddr(t, "192.0.2.5")) {
		t.Fatal("a rejected update disturbed the working configuration")
	}
	if a.Allows(mustAddr(t, "198.51.100.5")) {
		t.Fatal("a rejected update was partly applied")
	}
}

func TestACLIgnoresBlankEntries(t *testing.T) {
	// Text areas in a web form produce these; they are not errors.
	a := NewACL()
	if err := a.Set(true, []string{"192.0.2.0/24", "", "   "}); err != nil {
		t.Fatal(err)
	}
	if n := len(a.Entries()); n != 1 {
		t.Fatalf("kept %d entries, want 1", n)
	}
}

func TestACLUnmapsIPv4OverDualStackListener(t *testing.T) {
	// A v4 peer on a dual-stack listener arrives as ::ffff:192.0.2.5 and would
	// match no v4 prefix at all. Getting this wrong locks out every client.
	a := NewACL()
	if err := a.Set(true, []string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	mapped := netip.AddrFrom16(mustAddr(t, "192.0.2.5").As16())
	if !a.Allows(mapped) {
		t.Fatalf("a v4-mapped peer %v was refused", mapped)
	}
}

func TestSuggestLocalPrefixesReturnsUsableSubnets(t *testing.T) {
	// Used to pre-fill the field when the gateway is first enabled, so nobody
	// has to type a subnet from memory.
	for _, p := range SuggestLocalPrefixes() {
		if !p.IsValid() {
			t.Errorf("suggested an invalid prefix %v", p)
		}
		if p.Addr() != p.Masked().Addr() {
			t.Errorf("suggested %v with host bits set", p)
		}
	}
}
