package conditions

import (
	"testing"
	"time"
)

func devKey(kind string) Key { return Key{HostID: 1, Kind: kind} }

func badScan(keys ...Key) Scan {
	scan := Scan{
		Evaluated: map[string]bool{},
		Seen:      map[Key]bool{},
		Unjudged:  map[Key]bool{},
		Bad:       map[Key]Finding{},
		Reporting: map[int32]bool{1: true},
	}
	for _, k := range keys {
		scan.Evaluated[k.Kind] = true
		scan.Seen[k] = true
		scan.Bad[k] = Finding{Key: k, Severity: SeverityWarning}
	}
	return scan
}

// A deviation opens only after OpenAfter consecutive passes, so a nightly cron
// burst that lifts load5 for one tick writes nothing.
func TestDeviationOpensOnlyAfterConsecutivePasses(t *testing.T) {
	d := NewDelay()
	key := devKey(KindLoad)

	for pass := 1; pass < OpenAfter; pass++ {
		scan := badScan(key)
		d.Apply(scan, nil)
		if _, still := scan.Bad[key]; still {
			t.Fatalf("pass %d: finding survived the delay, want withheld", pass)
		}
	}

	scan := badScan(key)
	d.Apply(scan, nil)
	if _, still := scan.Bad[key]; !still {
		t.Errorf("pass %d: finding was still withheld, want released", OpenAfter)
	}
}

// The count is CONSECUTIVE. A host that crosses the line for one tick a day
// must not accumulate crossings and open on the third day, describing nothing
// that happened at the time.
func TestDelayResetsWhenTheSubjectRecovers(t *testing.T) {
	d := NewDelay()
	key := devKey(KindProcesses)

	d.Apply(badScan(key), nil)

	// A pass where nothing is bad at all.
	quiet := badScan()
	quiet.Evaluated[KindProcesses] = true
	quiet.Seen[key] = true
	d.Apply(quiet, nil)

	// Back over the line: this must be counted as the first pass again.
	for pass := 1; pass < OpenAfter; pass++ {
		scan := badScan(key)
		d.Apply(scan, nil)
		if _, still := scan.Bad[key]; still {
			t.Fatalf("pass %d after recovery: released too early", pass)
		}
	}
}

// THE FAILURE THIS GUARDS. Withholding a finding whose condition is already
// open reads to Diff as a miss, and two misses close it -- so a drive genuinely
// over its limit would be opened, closed and reopened forever, losing its onset
// each time. The delay decides when to START looking, never whether to keep
// looking.
func TestDelayNeverWithholdsAnAlreadyOpenCondition(t *testing.T) {
	d := NewDelay()
	key := devKey(KindTemperature)
	open := []Open{{ID: 7, Key: key, Severity: SeverityWarning}}

	for pass := 1; pass <= OpenAfter+2; pass++ {
		scan := badScan(key)
		d.Apply(scan, open)
		if _, still := scan.Bad[key]; !still {
			t.Fatalf("pass %d: an open condition's finding was withheld", pass)
		}
	}
}

// The five older kinds are raised on the first observation: a host that has
// stopped reporting should not wait three minutes to say so.
func TestDelayLeavesNonDeviationKindsAlone(t *testing.T) {
	d := NewDelay()

	for _, kind := range []string{KindSilent, KindDisk, KindDrive, KindFailedUnits, KindSporadic} {
		key := devKey(kind)
		scan := badScan(key)
		d.Apply(scan, nil)
		if _, still := scan.Bad[key]; !still {
			t.Errorf("%s was delayed, want raised on the first pass", kind)
		}
	}
}

// A delayed finding must not reach Diff as an open action, and must not clear
// anything either -- there is nothing open to clear.
func TestDelayedFindingProducesNoAction(t *testing.T) {
	d := NewDelay()
	key := devKey(KindLoad)

	scan := badScan(key)
	d.Apply(scan, nil)

	if actions := Diff(nil, scan, time.Now()); len(actions) != 0 {
		t.Errorf("Diff produced %d actions for a delayed finding, want 0", len(actions))
	}
}
