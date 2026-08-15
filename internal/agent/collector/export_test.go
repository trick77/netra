package collector

import (
	"testing"
	"time"
)

// SetSysClassNetForTest points the per-interface sysfs reads at a fixture
// tree for the duration of one test.
//
// SystemIfaces is the one production path that cannot take its root as a
// parameter -- it is an IfaceLister, and the signature is the injection seam
// for every other test in this package. Without this hook the VRF and alias
// reads would only ever be exercised against whatever the machine running the
// suite happens to have configured, which is not a test.
func SetSysClassNetForTest(t *testing.T, root string) {
	t.Helper()

	prev := sysClassNet
	sysClassNet = root
	t.Cleanup(func() { sysClassNet = prev })
}

// IfaceAliasForTest exposes the alias lookup.
func IfaceAliasForTest(name string) string { return ifaceAlias(name) }

// SetStatfsTimeoutForTest shortens the per-mountpoint statfs deadline, so the
// wedged-mount path can be exercised without spending the production two
// seconds per blocked call.
func (f *Filesystems) SetStatfsTimeoutForTest(d time.Duration) { f.statfsTimeout = d }
