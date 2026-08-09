package store_test

import (
	"testing"

	"github.com/trick77/netra/internal/hub/store"
)

// Scope classification lives in the hub so it can be corrected by upgrading
// one binary rather than redeploying every agent (spec §5.2). That only pays
// off if it is right, and "which of my hosts is publicly reachable" is the
// question an operator asks when something is on fire.
//
// IPv4 and IPv6 are treated identically throughout, so every case below has
// both families.
func TestAddressScope(t *testing.T) {
	for _, c := range []struct {
		addr, want, why string
	}{
		{"127.0.0.1", "loopback", "IPv4 loopback"},
		{"::1", "loopback", "IPv6 loopback"},

		{"10.0.0.5", "private", "RFC 1918 /8"},
		{"172.16.0.1", "private", "RFC 1918 /12"},
		{"192.168.1.1", "private", "RFC 1918 /16"},
		{"fd00::1", "private", "IPv6 unique-local"},

		{"169.254.1.1", "private", "IPv4 link-local: not routable off the link"},
		{"fe80::1", "private", "IPv6 link-local: not routable off the link"},

		{"224.0.0.1", "private", "IPv4 multicast identifies no host"},
		{"ff02::1", "private", "IPv6 multicast identifies no host"},
		{"0.0.0.0", "private", "unspecified is not a way to reach anything"},
		{"::", "private", "unspecified is not a way to reach anything"},

		{"8.8.8.8", "public", "routable IPv4"},
		{"2001:db8::1", "public", "routable IPv6"},

		{"", "", "unparseable input classifies as nothing rather than guessing"},
		{"not-an-address", "", "unparseable input classifies as nothing rather than guessing"},
	} {
		if got := store.AddressScope(c.addr); got != c.want {
			t.Errorf("AddressScope(%q) = %q, want %q (%s)", c.addr, got, c.want, c.why)
		}
	}
}

// The distinction that matters operationally: a host with only private
// addresses must never be reported as publicly reachable, and one with a
// public address must never be hidden.
func TestAddressScopeNeverCallsAPrivateAddressPublic(t *testing.T) {
	private := []string{
		"10.255.255.255", "172.31.255.255", "192.168.255.255",
		"127.0.0.1", "169.254.169.254", "fd00::ffff", "fe80::abcd", "::1",
	}
	for _, addr := range private {
		if got := store.AddressScope(addr); got == "public" {
			t.Errorf("AddressScope(%q) = public; it is not reachable from outside", addr)
		}
	}

	// 169.254.169.254 is the cloud metadata endpoint -- link-local, and
	// emphatically not a public address, though it is the one most likely to
	// be mistaken for something routable.
	if got := store.AddressScope("169.254.169.254"); got != "private" {
		t.Errorf("AddressScope(cloud metadata endpoint) = %q, want private", got)
	}
}
