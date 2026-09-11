package conditions_test

import (
	"testing"

	"github.com/trick77/netra/internal/hub/conditions"
)

// The catalogue lists EVERY kind, present or not.
//
// That is the whole reason it exists rather than being derived from the open
// rows: a filter for a kind nobody is carrying must still be able to name
// itself, or a reader who followed a link to a kind that has since cleared is
// left holding a filter the page cannot name.
func TestCatalogueNamesEveryKind(t *testing.T) {
	want := map[string]bool{
		conditions.KindSilent:      false,
		conditions.KindSporadic:    false,
		conditions.KindFailedUnits: false,
		conditions.KindDisk:        false,
		conditions.KindDrive:       false,
		conditions.KindTemperature: false,
		conditions.KindProcesses:   false,
		conditions.KindLoad:        false,
	}
	for _, k := range conditions.Catalogue() {
		if _, known := want[k.Kind]; !known {
			t.Errorf("catalogue has %q, which is not a Kind constant", k.Kind)
			continue
		}
		want[k.Kind] = true
		if k.Label == "" {
			t.Errorf("%s has no label -- an unnameable filter is the bug this prevents", k.Kind)
		}
		if k.Severity != conditions.SeverityWarning && k.Severity != conditions.SeverityCritical {
			t.Errorf("%s entry severity = %q", k.Kind, k.Severity)
		}
	}
	for kind, listed := range want {
		if !listed {
			t.Errorf("%s is missing from the catalogue", kind)
		}
	}
}

// Only disk carries thresholds, and they are the ones the rule judges by.
//
// The browser needs them to colour a meter for a HEALTHY mount, which no
// condition covers -- so serving them is what stops the four numbers growing
// back as a second copy in TypeScript.
func TestCatalogueServesTheDiskThresholdsItJudgesBy(t *testing.T) {
	for _, k := range conditions.Catalogue() {
		if k.Kind != conditions.KindDisk {
			if k.Thresholds != nil {
				t.Errorf("%s carries thresholds, and nothing reads them", k.Kind)
			}
			continue
		}
		if k.Thresholds == nil {
			t.Fatal("disk has no thresholds; the browser cannot colour a meter without them")
		}
		if k.Thresholds.WarnPct != conditions.DiskWarnPct ||
			k.Thresholds.CritPct != conditions.DiskCritPct ||
			k.Thresholds.WarnFree != conditions.DiskWarnFree ||
			k.Thresholds.CritFree != conditions.DiskCritFree {
			t.Errorf("thresholds = %+v, want the constants DiskSeverity actually uses", *k.Thresholds)
		}
	}
}
