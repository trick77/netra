package collector

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Iface is one interface and the addresses on it.
type Iface struct {
	Name  string
	Index int
	Addrs []string // CIDR form, as net.Addr renders it
}

// IfaceLister enumerates the host's interfaces.
//
// Injected so the collector is testable: the real one reports whatever the
// machine running the test happens to have configured, which is not a test.
// The production implementation is SystemIfaces, which uses net.Interfaces --
// no netlink library, because the standard library already asks the kernel the
// same question.
type IfaceLister func() ([]Iface, error)

// SystemIfaces is the production IfaceLister.
func SystemIfaces() ([]Iface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}

	out := make([]Iface, 0, len(ifaces))
	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			// One interface refusing to answer must not cost the others.
			continue
		}
		raw := make([]string, 0, len(addrs))
		for _, a := range addrs {
			raw = append(raw, a.String())
		}
		out = append(out, Iface{Name: i.Name, Index: i.Index, Addrs: raw})
	}
	return out, nil
}

// Addresses reports the host's IP addresses.
//
// It delivers no samples: addresses are inventory, not a measurement, so they
// ride the metadata rather than a hypertable and are reported when they
// change.
//
// The agent reports RAW ADDRESSES ONLY. Deriving scope -- loopback, private,
// public -- is the hub's job (spec §5.2), so the classification is one
// implementation that can be corrected without redeploying every agent in the
// fleet. An agent that classified would freeze today's rules into every host.
//
// Interface names are the join key with net_samples.iface, so this collector
// and Network must name an interface identically or an address and its traffic
// cannot be related.
type Addresses struct {
	lister IfaceLister

	// prev is the last reported set, so an unchanged host reports nothing.
	prev []string
}

// NewAddresses builds an Addresses collector.
func NewAddresses(lister IfaceLister) *Addresses {
	return &Addresses{lister: lister}
}

// Name implements Collector.
func (a *Addresses) Name() string { return "addresses" }

// ResendInventory implements InventoryResender.
//
// Forgetting the last reported set is the whole re-arm: the comparison below
// then finds nothing to compare against and emits the current addresses in
// full. Without it, a scrape carrying an address change that the ring dropped
// left the hub serving the previous set forever -- this collector only speaks
// when something changes, and from its point of view nothing had.
func (a *Addresses) ResendInventory() { a.prev = nil }

// Collect implements Collector.
func (a *Addresses) Collect(_ context.Context) (*Result, error) {
	ifaces, err := a.lister()
	if err != nil {
		return nil, err
	}

	var rows []*netrav1.HostAddress
	for _, i := range ifaces {
		if !reportableIface(i.Name) {
			// The same exclusions as the network collector, and for the same
			// reason: a veth's address is container plumbing rather than a
			// fact about this host.
			continue
		}
		for _, raw := range i.Addrs {
			ip, _, err := net.ParseCIDR(raw)
			if err != nil {
				// Some addresses arrive bare rather than as CIDR.
				ip = net.ParseIP(raw)
				if ip == nil {
					continue
				}
			}

			family := uint32(6)
			if ip.To4() != nil {
				family = 4
			}

			rows = append(rows, &netrav1.HostAddress{
				Iface:   i.Name,
				IfIndex: ptrTo(uint32(i.Index)),
				Address: ip.String(),
				Family:  family,
			})
		}
	}

	// Deterministic order so the change comparison below is about the
	// addresses rather than about map iteration.
	slices.SortFunc(rows, func(x, y *netrav1.HostAddress) int {
		if c := strings.Compare(x.GetIface(), y.GetIface()); c != 0 {
			return c
		}
		return strings.Compare(x.GetAddress(), y.GetAddress())
	})

	fingerprint := make([]string, 0, len(rows))
	for _, r := range rows {
		fingerprint = append(fingerprint, r.GetIface()+" "+r.GetAddress())
	}

	if slices.Equal(fingerprint, a.prev) {
		// Unchanged. Addresses are inventory: resending an identical set every
		// 60s would be the near-constant series the design keeps out of the
		// sample tables.
		return &Result{}, nil
	}
	a.prev = fingerprint

	return &Result{Addresses: rows}, nil
}
