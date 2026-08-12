package collector_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func fakeIfaces(ifaces ...collector.Iface) collector.IfaceLister {
	return func() ([]collector.Iface, error) { return ifaces, nil }
}

func addrRow(t *testing.T, rows []*netrav1.HostAddress, addr string) *netrav1.HostAddress {
	t.Helper()
	for _, r := range rows {
		if r.GetAddress() == addr {
			return r
		}
	}
	t.Fatalf("no row for %q in %d rows", addr, len(rows))
	return nil
}

// vrf and description were on the wire and in the schema from the start, and
// the hub wrote both columns, but the collector set neither -- so the Network
// inventory table had two columns that were empty on every host.
func TestAddressesReportsVRFAndDescription(t *testing.T) {
	testee := collector.NewAddresses(fakeIfaces(
		collector.Iface{
			Name: "eth0", Index: 2, Addrs: []string{"10.0.0.5/24"},
			VRF: "mgmt-vrf", Description: "uplink to core",
		},
	))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	row := addrRow(t, res.Addresses, "10.0.0.5")
	if got := row.GetVrf(); got != "mgmt-vrf" {
		t.Errorf("vrf = %q, want mgmt-vrf", got)
	}
	if got := row.GetDescription(); got != "uplink to core" {
		t.Errorf("description = %q, want \"uplink to core\"", got)
	}
}

// The alias is part of what is reported, so it is part of what counts as a
// change. Keying the comparison on iface and address alone left an operator's
// `ip link set ... alias` unreported until some unrelated address moved.
func TestAddressesReportsAnAliasChangeOnItsOwn(t *testing.T) {
	iface := collector.Iface{Name: "eth0", Index: 2, Addrs: []string{"10.0.0.5/24"}}

	lister := func() ([]collector.Iface, error) { return []collector.Iface{iface}, nil }
	testee := collector.NewAddresses(lister)

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	// Only the alias changes; the address set is identical.
	iface.Description = "now labelled"

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if len(res.Addresses) == 0 {
		t.Fatal("an alias change went unreported; the hub would serve the old text indefinitely")
	}
	if got := addrRow(t, res.Addresses, "10.0.0.5").GetDescription(); got != "now labelled" {
		t.Errorf("description = %q, want \"now labelled\"", got)
	}
}

// A bridge port has a master too. Reporting "br0" as an interface's VRF would
// be wrong on the far more common of the two setups, so the master's DEVTYPE
// is what decides.
func TestIfaceVRFIgnoresANonVRFMaster(t *testing.T) {
	root := t.TempDir()
	collector.SetSysClassNetForTest(t, root)

	mkIface := func(name, devtype string) {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if devtype != "" {
			body := "INTERFACE=" + name + "\nDEVTYPE=" + devtype + "\n"
			if err := os.WriteFile(filepath.Join(dir, "uevent"), []byte(body), 0o644); err != nil {
				t.Fatalf("write uevent: %v", err)
			}
		}
	}
	enslave := func(child, master string) {
		t.Helper()
		if err := os.Symlink(filepath.Join("..", master), filepath.Join(root, child, "master")); err != nil {
			t.Fatalf("symlink master: %v", err)
		}
	}

	mkIface("br0", "bridge")
	mkIface("mgmt-vrf", "vrf")
	mkIface("eth0", "")
	mkIface("eth1", "")
	mkIface("eth2", "")
	enslave("eth0", "br0")
	enslave("eth1", "mgmt-vrf")

	if got := collector.IfaceVRFForTest("eth0"); got != "" {
		t.Errorf("bridge port reported vrf = %q, want empty", got)
	}
	if got := collector.IfaceVRFForTest("eth1"); got != "mgmt-vrf" {
		t.Errorf("vrf slave reported vrf = %q, want mgmt-vrf", got)
	}
	if got := collector.IfaceVRFForTest("eth2"); got != "" {
		t.Errorf("unenslaved interface reported vrf = %q, want empty", got)
	}
}

// Most interfaces have no alias. Absent reads as empty, not as an error.
func TestIfaceAliasReadsTheInterfaceAlias(t *testing.T) {
	root := t.TempDir()
	collector.SetSysClassNetForTest(t, root)

	if err := os.MkdirAll(filepath.Join(root, "eth0"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "eth0", "ifalias"), []byte("uplink to core\n"), 0o644); err != nil {
		t.Fatalf("write ifalias: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "eth1"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if got := collector.IfaceAliasForTest("eth0"); got != "uplink to core" {
		t.Errorf("alias = %q, want \"uplink to core\"", got)
	}
	if got := collector.IfaceAliasForTest("eth1"); got != "" {
		t.Errorf("alias = %q for an interface with none, want empty", got)
	}
}

// IPv4 and IPv6 are treated identically throughout (spec §5.2).
func TestAddressesReportsBothFamilies(t *testing.T) {
	testee := collector.NewAddresses(fakeIfaces(
		collector.Iface{Name: "eth0", Index: 2, Addrs: []string{"10.0.0.5/24", "2001:db8::1/64"}},
	))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Addresses) != 2 {
		t.Fatalf("addresses = %d, want 2", len(res.Addresses))
	}

	v4 := addrRow(t, res.Addresses, "10.0.0.5")
	if v4.GetFamily() != 4 {
		t.Errorf("family = %d for an IPv4 address, want 4", v4.GetFamily())
	}
	if v4.GetIface() != "eth0" {
		t.Errorf("iface = %q, want eth0", v4.GetIface())
	}
	if v4.GetIfIndex() != 2 {
		t.Errorf("if_index = %d, want 2", v4.GetIfIndex())
	}

	if got := addrRow(t, res.Addresses, "2001:db8::1").GetFamily(); got != 6 {
		t.Errorf("family = %d for an IPv6 address, want 6", got)
	}
}

// The agent reports raw facts only. Classifying an address as
// loopback/private/public is the HUB's job, so the rules can be corrected
// without redeploying every agent in the fleet -- an agent that classified
// would freeze today's definition of "private" into every host.
func TestAddressesDoesNotClassifyScope(t *testing.T) {
	testee := collector.NewAddresses(fakeIfaces(
		collector.Iface{Name: "eth0", Index: 2, Addrs: []string{"192.168.1.10/24", "8.8.8.8/32"}},
	))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, r := range res.Addresses {
		if r.GetVrf() != "" {
			continue
		}
		// HostAddress carries no scope field at all -- the schema column is
		// filled hub-side. This asserts the agent sends nothing resembling a
		// classification in the fields it does have.
		if r.GetDescription() != "" {
			t.Errorf("address %s carries a description %q; the agent must not classify",
				r.GetAddress(), r.GetDescription())
		}
	}
}

// The same exclusions as the network collector: a veth's address is container
// plumbing rather than a fact about this host, and the two collectors must
// agree on which interfaces exist or an address and its traffic cannot be
// joined on iface.
func TestAddressesExcludesTheSameInterfacesAsNetwork(t *testing.T) {
	testee := collector.NewAddresses(fakeIfaces(
		collector.Iface{Name: "lo", Index: 1, Addrs: []string{"127.0.0.1/8"}},
		collector.Iface{Name: "veth99", Index: 5, Addrs: []string{"172.17.0.2/16"}},
		collector.Iface{Name: "docker0", Index: 3, Addrs: []string{"172.17.0.1/16"}},
		collector.Iface{Name: "eth0", Index: 2, Addrs: []string{"10.0.0.5/24"}},
	))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, r := range res.Addresses {
		switch r.GetIface() {
		case "lo", "veth99", "docker0":
			t.Errorf("%s reported; it is excluded from net_samples too", r.GetIface())
		}
	}
	if len(res.Addresses) != 1 {
		t.Errorf("addresses = %d, want 1 (eth0 only)", len(res.Addresses))
	}
}

// Addresses are inventory, not a measurement. An unchanged host reports
// nothing rather than resending an identical set every 60s.
func TestAddressesReportsNothingWhenUnchanged(t *testing.T) {
	lister := fakeIfaces(collector.Iface{Name: "eth0", Index: 2, Addrs: []string{"10.0.0.5/24"}})
	testee := collector.NewAddresses(lister)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	if len(res.Addresses) != 1 {
		t.Fatalf("first scrape reported %d addresses, want 1", len(res.Addresses))
	}

	res, err = testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if len(res.Addresses) != 0 {
		t.Errorf("unchanged scrape reported %d addresses, want 0", len(res.Addresses))
	}
}

// A change must be reported in full: the hub replaces the host's address set
// rather than merging, so a partial report would look like the other addresses
// were removed.
func TestAddressesReportsTheWholeSetWhenAnythingChanges(t *testing.T) {
	first := collector.Iface{Name: "eth0", Index: 2, Addrs: []string{"10.0.0.5/24"}}
	testee := collector.NewAddresses(fakeIfaces(first))

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}

	// A second address appears on the same interface.
	testee = collector.NewAddresses(fakeIfaces(
		collector.Iface{Name: "eth0", Index: 2, Addrs: []string{"10.0.0.5/24", "10.0.0.6/24"}},
	))
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Addresses) != 2 {
		t.Errorf("addresses = %d, want both reported, not just the new one", len(res.Addresses))
	}
}

// Reporting on change alone is only safe while every scrape reaches the hub.
// When the ring drops the scrape carrying a set, the agent re-arms this
// collector, and it must then report the current addresses again even though
// nothing about them changed -- otherwise a static host serves a stale list
// forever, since the hub replaces inventory and returns early on an empty set.
func TestResendInventoryReportsAnUnchangedSetAgain(t *testing.T) {
	// Given: a collector that has already reported its set.
	testee := collector.NewAddresses(fakeIfaces(
		collector.Iface{Name: "eth0", Index: 2, Addrs: []string{"10.0.0.5/24"}},
	))
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if len(res.Addresses) != 0 {
		t.Fatalf("addresses = %d on an unchanged scrape, want 0", len(res.Addresses))
	}

	// When: the agent says the reported set never reached the hub.
	testee.ResendInventory()

	// Then: the next scrape carries it again, unchanged.
	res, err = testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect after ResendInventory: %v", err)
	}
	if len(res.Addresses) != 1 {
		t.Fatalf("addresses = %d after a re-arm, want 1", len(res.Addresses))
	}
	if got := res.Addresses[0].GetAddress(); got != "10.0.0.5" {
		t.Errorf("address = %q, want 10.0.0.5", got)
	}
}

// This collector's first Collect is DATA, not a warm-up reading, so the
// agent's startup priming must skip it -- see TestPrimeDoesNotConsumeTheFirst
// AddressSet in the client package for the behaviour this flag drives.
func TestAddressesEmitsABaseline(t *testing.T) {
	testee := collector.NewAddresses(fakeIfaces(
		collector.Iface{Name: "eth0", Index: 2, Addrs: []string{"10.0.0.5/24"}},
	))

	if !testee.EmitsBaseline() {
		t.Error("EmitsBaseline() = false; priming would discard the only address set the agent reports")
	}
}
