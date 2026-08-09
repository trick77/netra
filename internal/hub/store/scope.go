package store

import "net/netip"

// AddressScope classifies an address as loopback, private or public.
//
// This lives in the HUB, not the agent, and that is the point (spec §5.2). The
// agent reports raw addresses only, so the rules below are ONE implementation
// that can be corrected by upgrading the hub -- an agent that classified would
// freeze today's definition of "private" into every host in the fleet, and
// fixing it would mean redeploying all of them.
//
// IPv4 and IPv6 are treated identically throughout.
func AddressScope(addr string) string {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return ""
	}

	switch {
	case ip.IsLoopback():
		return "loopback"

	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		// 169.254/16 and fe80::/10. Not routable off the link, so they are
		// not a way to reach the host -- grouped with private rather than
		// given a category nothing queries.
		return "private"

	case ip.IsPrivate():
		// RFC 1918 for v4, fc00::/7 unique-local for v6.
		return "private"

	case ip.IsMulticast(), ip.IsUnspecified():
		// Neither identifies a host. Reporting them as public would put
		// 0.0.0.0 in the answer to "which hosts have a public address".
		return "private"

	default:
		return "public"
	}
}
