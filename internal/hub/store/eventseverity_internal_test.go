package store

import (
	"testing"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

func ptrTo[T any](v T) *T { return &v }

// The field is the real channel, so it wins when both are present.
func TestEventSeverityPrefersTheField(t *testing.T) {
	got := eventSeverity(&netrav1.Event{
		Severity:   ptrTo("critical"),
		DetailJson: `{"severity":"info"}`,
	})
	if got != "critical" {
		t.Errorf("eventSeverity = %q, want critical", got)
	}
}

// The fallback that keeps an old agent working.
//
// Agents are deployed per host and version independently of the hub, so an
// agent predating the proto field must still classify. Without this every
// event it sends arrives as "info" -- a silent downgrade of exactly the rows
// that matter, since only the notable ones ever stated a severity at all.
func TestEventSeverityFallsBackToTheDetailKey(t *testing.T) {
	got := eventSeverity(&netrav1.Event{
		DetailJson: `{"state":"clean","degraded":1,"severity":"critical"}`,
	})
	if got != "critical" {
		t.Errorf("eventSeverity = %q, want critical from the detail key", got)
	}
}

// A producer that states nothing is info, which is the column's default and
// the right reading: most events are facts, not emergencies.
func TestEventSeverityDefaultsToInfo(t *testing.T) {
	for name, ev := range map[string]*netrav1.Event{
		"no detail at all":   {},
		"empty object":       {DetailJson: `{}`},
		"unrelated keys":     {DetailJson: `{"action":"upgrade"}`},
		"detail is not json": {DetailJson: `not json`},
		"detail is an array": {DetailJson: `[1,2,3]`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := eventSeverity(ev); got != "info" {
				t.Errorf("eventSeverity = %q, want info", got)
			}
		})
	}
}

// An unrecognised word must not reach the column.
//
// events.severity carries a CHECK constraint, so passing a value through
// unexamined would turn one malformed event into a rejected BATCH -- taking
// every other family in it down with a host's whole scrape.
func TestEventSeverityRefusesAnUnknownWord(t *testing.T) {
	for name, ev := range map[string]*netrav1.Event{
		"unknown field": {Severity: ptrTo("catastrophic")},
		"unknown key":   {DetailJson: `{"severity":"URGENT"}`},
		"empty field":   {Severity: ptrTo("")},
		// The field is unusable, so the detail key still gets its turn rather
		// than the whole row falling to info.
		"bad field, good key": {
			Severity:   ptrTo("catastrophic"),
			DetailJson: `{"severity":"warning"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := eventSeverity(ev)
			if !validEventSeverity(got) {
				t.Fatalf("eventSeverity = %q, which the CHECK constraint would reject", got)
			}
			if name == "bad field, good key" && got != "warning" {
				t.Errorf("eventSeverity = %q, want warning from the detail key", got)
			}
		})
	}
}
