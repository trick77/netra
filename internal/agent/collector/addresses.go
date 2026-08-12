package collector

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Iface is one interface and the addresses on it.
type Iface struct {
	Name  string
	Index int
	Addrs []string // CIDR form, as net.Addr renders it

	// VRF is the name of the VRF device this interface is enslaved to.
	//
	// ALWAYS EMPTY from SystemIfaces, and deliberately so -- see vrfUnknown
	// below. It stays on the struct because the field exists on the wire and
	// in the schema, and a caller that can determine it (a test, or a future
	// rtnetlink path) should be able to supply it without a signature change.
	VRF string

	// Description is the interface alias -- what SNMP calls ifAlias and what
	// `ip link set <if> alias <text>` writes. Empty unless an operator set one.
	Description string
}

// vrfUnknown records why HostAddress.vrf is left empty rather than filled.
//
// A VRF slave has a master link, but so does a bridge port and a bond slave,
// and sysfs offers nothing that identifies the master AS a VRF. The obvious
// discriminator does not work: drivers/net/vrf.c registers only
// rtnl_link_ops.kind = "vrf" and never calls SET_NETDEV_DEVTYPE, so
// /sys/class/net/<master>/uevent carries no DEVTYPE line at all. (Bridges
// DO -- net/bridge/br_device.c declares a device_type -- which is why a
// DEVTYPE test appears to work while only ever rejecting.)
//
// Deciding from the ABSENCE of a DEVTYPE would classify every master that is
// not a bridge or a bond as a VRF, which is wrong for team, macvlan and
// anything added later. The real answer is IFLA_INFO_KIND over rtnetlink,
// which this collector deliberately does not speak -- net.Interfaces is the
// whole reason there is no netlink dependency here.
//
// So the field is left unset, which in this codebase means "not measured"
// rather than "no VRF". That is the honest reading, and it is what the
// hub already stores.
const vrfUnknown = ""

// sysClassNet is where the per-interface attributes below are read from.
// A variable so the tests can point it at a fixture tree; there is no other
// reason to change it.
//
// Not derived from cfg.SysRoot, unlike Sensors and Mdraid: SystemIfaces is an
// IfaceLister, and that signature is the injection seam every other test in
// this package uses. Threading a root through it would change the seam for
// one attribute read.
var sysClassNet = "/sys/class/net"

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
		out = append(out, Iface{
			Name:        i.Name,
			Index:       i.Index,
			Addrs:       raw,
			VRF:         vrfUnknown,
			Description: ifaceAlias(i.Name),
		})
	}
	return out, nil
}

// ifaceAlias returns the interface alias, the Linux equivalent of SNMP's
// ifAlias. Absent on most interfaces, which reads as empty.
func ifaceAlias(name string) string {
	raw, err := os.ReadFile(filepath.Join(sysClassNet, name, "ifalias"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
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

// EmitsBaseline implements BaselineEmitter, keeping this collector out of the
// agent's startup priming.
//
// For the same reason Packages is kept out, and it was the more common failure
// of the two. Its first Collect IS the address set; priming consumed it into a
// discarded result and left prev populated, so the first real scrape reported
// nothing. Every later scrape reported nothing too, because from this
// collector's point of view nothing had changed -- and the hub cannot fill the
// gap either, since UpsertHostAddresses returns early on an empty set. A
// freshly enrolled host with a static address therefore had no addresses at
// the hub at all, indefinitely.
func (a *Addresses) EmitsBaseline() bool { return true }

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
				Iface:       i.Name,
				IfIndex:     ptrTo(uint32(i.Index)),
				Address:     ip.String(),
				Family:      family,
				Vrf:         i.VRF,
				Description: i.Description,
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

	// The VRF and the alias are part of what is being reported, so they are
	// part of what counts as a change. Keying on iface and address alone
	// would leave an operator's `ip link set ... alias` unreported until some
	// unrelated address moved, and the hub serving the old text meanwhile.
	fingerprint := make([]string, 0, len(rows))
	for _, r := range rows {
		fingerprint = append(fingerprint,
			r.GetIface()+" "+r.GetAddress()+" "+r.GetVrf()+" "+r.GetDescription())
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
