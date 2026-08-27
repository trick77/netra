package collector

import (
	"testing"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
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

// CapContainerRowsForTest and MaxContainerRowsForTest expose the container row
// backstop, so a test can assert the bound without restating the literal --
// which would then agree with a wrong value as readily as a right one.
func CapContainerRowsForTest(rows []*netrav1.ContainerSample) []*netrav1.ContainerSample {
	return capContainerRows(rows)
}

const MaxContainerRowsForTest = maxContainerRows

// CountHumanSessionsForTest exposes the logind session-class allowlist, which
// is the only judgement LogindSessions makes and the one thing a machine
// without logind can still check.
func CountHumanSessionsForTest(classes []string) int { return countHumanSessions(classes) }
