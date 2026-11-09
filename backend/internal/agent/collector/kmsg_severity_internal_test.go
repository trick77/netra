package collector

import "testing"

// Every allowlist entry states a severity the hub accepts.
//
// See kmsgClass.Severity for why a typo like "Critical" is silent. The
// real-message cases in kmsg_test.go only cover entries someone wrote a fixture
// for; this covers the table.
func TestKmsgClassesStateAValidSeverity(t *testing.T) {
	for i, c := range kmsgClasses {
		switch c.Severity {
		case "info", "warning", "critical":
		default:
			t.Errorf("kmsgClasses[%d] (%s, %s): severity %q, want info, warning or critical",
				i, c.Type, c.re, c.Severity)
		}
	}
}
