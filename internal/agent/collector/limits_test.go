package collector_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
)

// limitsFixture writes only the files named, so a test can assert what
// happens when one source is absent without the others disappearing too.
func limitsFixture(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func collectLimits(t *testing.T, root string) *collector.Result {
	t.Helper()
	res, err := collector.NewLimits(root).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return res
}

// Every gauge here exists to be read against its ceiling. Exhaustion does not
// present as a resource problem -- accept() fails, conntrack drops flows --
// so the symptom looks like a broken network.
func TestLimitsReportsSocketFileAndConntrackUsage(t *testing.T) {
	root := limitsFixture(t, map[string]string{
		"net/sockstat": "sockets: used 231\n" +
			"TCP: inuse 12 orphan 3 tw 45 alloc 60 mem 2\n" +
			"UDP: inuse 4 mem 1\n",
		"sys/fs/file-nr":                       "1216\t0\t9223372036854775807\n",
		"sys/net/netfilter/nf_conntrack_count": "512\n",
		"sys/net/netfilter/nf_conntrack_max":   "262144\n",
	})

	h := collectLimits(t, root).Host

	if got := h.GetSocketsUsed(); got != 231 {
		t.Errorf("sockets_used = %d, want 231", got)
	}
	if got := h.GetTcpOrphan(); got != 3 {
		t.Errorf("tcp_orphan = %d, want 3", got)
	}
	if got := h.GetTcpTw(); got != 45 {
		t.Errorf("tcp_tw = %d, want 45", got)
	}
	if got := h.GetTcpAlloc(); got != 60 {
		t.Errorf("tcp_alloc = %d, want 60", got)
	}
	if got := h.GetFdUsed(); got != 1216 {
		t.Errorf("fd_used = %d, want 1216", got)
	}
	// The middle field of file-nr has been 0 since 2.6 and must not be read
	// as the ceiling.
	if got := h.GetFdLimit(); got != 9223372036854775807 {
		t.Errorf("fd_limit = %d, want the third field", got)
	}
	if got := h.GetConntrackCount(); got != 512 {
		t.Errorf("conntrack_count = %d, want 512", got)
	}
	if got := h.GetConntrackLimit(); got != 262144 {
		t.Errorf("conntrack_limit = %d, want 262144", got)
	}
}

// A host doing neither NAT nor filtering has no conntrack module, which is
// absent rather than zero connections tracked.
func TestLimitsLeavesConntrackUnsetWhenTheModuleIsAbsent(t *testing.T) {
	root := limitsFixture(t, map[string]string{
		"net/sockstat":   "sockets: used 10\n",
		"sys/fs/file-nr": "100\t0\t1000\n",
	})

	h := collectLimits(t, root).Host

	if h.ConntrackCount != nil {
		t.Errorf("conntrack_count = %d without the module; want unset", h.GetConntrackCount())
	}
	if h.ConntrackLimit != nil {
		t.Errorf("conntrack_limit = %d without the module; want unset", h.GetConntrackLimit())
	}
	// The sources that were present still report.
	if h.SocketsUsed == nil {
		t.Error("sockets_used unset; one absent source must not cost the others")
	}
}

// Each file is independently optional, and none of them is worth failing a
// scrape over: every other collector reading /proc reports its own failure.
func TestLimitsReportsNothingAndNoErrorWithNoProcAtAll(t *testing.T) {
	res, err := collector.NewLimits(filepath.Join(t.TempDir(), "absent")).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect returned an error for an unreadable /proc: %v", err)
	}
	h := res.Host
	if h.SocketsUsed != nil || h.FdUsed != nil || h.ConntrackCount != nil {
		t.Error("fields set with no /proc to read them from")
	}
}
