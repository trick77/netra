package client_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/trick77/netra/internal/agent/client"
	"github.com/trick77/netra/internal/agent/collector"
)

// capture runs the startup inventory with the default logger pointed at a
// buffer, and hands back what an operator would have read.
func capture(t *testing.T, cols ...collector.Collector) string {
	t.Helper()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	client.LogStartupInventoryForTest(cols)
	return buf.String()
}

// The report that produced this: an agent reading the session count from
// logind -- the path where EVERYTHING worked -- logged
// `WARN collector limited capability=users_source reason=logind` at every
// start. A "_source" key names which of two sources answered, not something
// the operator failed to grant.
func TestStartupInventoryDoesNotWarnAboutASource(t *testing.T) {
	out := capture(t, &capabilityCollector{key: "users_source", value: "logind"})

	if strings.Contains(out, "collector limited") {
		t.Errorf("a source key was logged as a limitation:\n%s", out)
	}
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "collector source") {
		t.Errorf("the source was not reported at all:\n%s", out)
	}
	// Which source it was is the whole value of the line: it is the difference
	// between a zero because nobody is logged in and a zero from a file
	// nothing writes.
	if !strings.Contains(out, "logind") {
		t.Errorf("the line does not name the source:\n%s", out)
	}
}

// And the line this function exists for still warns: a capability that IS a
// limitation must not be quietened by the same change.
func TestStartupInventoryStillWarnsAboutALimitation(t *testing.T) {
	out := capture(t, &capabilityCollector{key: "users", value: "unavailable"})

	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "collector limited") {
		t.Errorf("a real limitation was not warned about:\n%s", out)
	}
}

// The healthy path stays silent, which is what makes the warnings worth
// reading.
func TestStartupInventorySaysNothingWhenEverythingIsOK(t *testing.T) {
	out := capture(t, &capabilityCollector{key: "users", value: "ok"})

	if strings.Contains(out, "collector limited") || strings.Contains(out, "collector source") {
		t.Errorf("an ok capability produced output:\n%s", out)
	}
}
