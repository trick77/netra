package store

import (
	"testing"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

func ptrTo[T any](v T) *T { return &v }

// The field is the only channel, so a detail key cannot contradict it.
//
// The hub used to read both, so that an agent predating the field still
// classified. Every supported agent states it in the field now (the ingest
// gate in httpapi refuses the ones that do not), and 0015_event_severity.sql
// backfilled the column from the key -- so a row still carrying one is history
// the column already agrees with, never a second opinion.
func TestEventSeverityReadsTheFieldAndNothingElse(t *testing.T) {
	got := eventSeverity(&netrav1.Event{
		Severity:   ptrTo("critical"),
		DetailJson: `{"severity":"info"}`,
	})
	if got != "critical" {
		t.Errorf("eventSeverity = %q, want critical", got)
	}
}

// A detail key on its own no longer classifies anything.
func TestEventSeverityIgnoresTheDetailKey(t *testing.T) {
	got := eventSeverity(&netrav1.Event{
		DetailJson: `{"state":"clean","degraded":1,"severity":"critical"}`,
	})
	if got != "info" {
		t.Errorf("eventSeverity = %q, want info: the detail key is not a channel", got)
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
		// An unusable field falls to info. It does NOT hand the decision to
		// the detail key -- that is the fallback this change removed, and the
		// fallthrough here exists only to protect the CHECK constraint.
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
			if name == "bad field, good key" && got != "info" {
				t.Errorf("eventSeverity = %q, want info: the detail key gets no turn", got)
			}
		})
	}
}
