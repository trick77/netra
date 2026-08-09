package collector

import netrav1 "github.com/trick77/netra/internal/gen/netra/v1"

// Result is one collector's contribution to a scrape.
//
// A collector fills only what it owns and leaves everything else nil. The
// agent merges the results of the collectors that succeeded and discards the
// rest whole, so a collector that fails part-way through contributes nothing
// rather than leaving half its fields behind for the hub to store as though
// they had been measured.
//
// Returning a value rather than writing into a shared sample also keeps
// collectors independent of one another: two of them cannot interleave writes
// into the same object, and a test needs no shared fixture.
type Result struct {
	// Host is this collector's share of the wide per-host row. Fields it does
	// not own stay unset, and the agent merges only the fields that are set.
	Host *netrav1.HostSample

	// Cores is one row per CPU core. Later 1C PRs add a slice per family
	// alongside this one.
	Cores []*netrav1.CpuCoreSample
}
