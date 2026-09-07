package conditions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
)

type fakeStore struct {
	scan     conditions.Scan
	scanErr  error
	open     []conditions.Open
	openErr  error
	applied  []conditions.Action
	applyErr error
	passes   int
}

func (f *fakeStore) ScanConditions(context.Context, time.Time) (conditions.Scan, error) {
	f.passes++
	return f.scan, f.scanErr
}

func (f *fakeStore) OpenConditions(context.Context) ([]conditions.Open, error) {
	return f.open, f.openErr
}

func (f *fakeStore) ApplyConditions(_ context.Context, a []conditions.Action, _ time.Time) error {
	f.applied = append(f.applied, a...)
	return f.applyErr
}

// silentScan is a pass in which one host has not been heard from.
func silentScan(hostID int32) conditions.Scan {
	k := conditions.Key{HostID: hostID, Kind: conditions.KindSilent}
	return conditions.Scan{
		Evaluated: map[string]bool{conditions.KindSilent: true},
		Seen:      map[conditions.Key]bool{k: true},
		Bad: map[conditions.Key]conditions.Finding{
			k: {Key: k, Severity: conditions.SeverityCritical},
		},
		Reporting: map[int32]bool{hostID: false},
	}
}

// The warm-up, and the bug it exists for.
//
// When the hub restarts, every host's last_seen is as old as the downtime, so
// the first pass would open `silent` for the whole fleet and close it again
// once the agents' buffered scrapes land -- recording one hub outage as N host
// outages, permanently, in the log alerting reads.
func TestSilenceIsNotJudgedDuringWarmUp(t *testing.T) {
	started := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{scan: silentScan(1)}

	e := conditions.New(store, started)
	// One second after start-up: the hub has not been up long enough to know
	// the difference between a silent host and its own downtime.
	e.SetClockForTest(func() time.Time { return started.Add(time.Second) })

	if err := e.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(store.applied) != 0 {
		t.Fatalf("opened a condition during warm-up: %+v", store.applied)
	}
}

// And the other half: during warm-up an already-open silent condition must not
// be RESOLVED either. Dropping the kind from Bad alone would leave Diff
// thinking every silent host had recovered.
func TestWarmUpDoesNotResolveOpenSilence(t *testing.T) {
	started := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	k := conditions.Key{HostID: 1, Kind: conditions.KindSilent}

	// A pass that says nothing is silent -- which during warm-up is not
	// something the hub is entitled to conclude.
	store := &fakeStore{
		scan: conditions.Scan{
			Evaluated: map[string]bool{conditions.KindSilent: true},
			Seen:      map[conditions.Key]bool{k: true},
			Bad:       map[conditions.Key]conditions.Finding{},
			Reporting: map[int32]bool{1: true},
		},
		open: []conditions.Open{{ID: 3, Key: k, Severity: conditions.SeverityCritical, MissingTicks: 1}},
	}

	e := conditions.New(store, started)
	e.SetClockForTest(func() time.Time { return started.Add(time.Second) })

	if err := e.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(store.applied) != 0 {
		t.Fatalf("resolved a silent condition during warm-up: %+v", store.applied)
	}
}

// Past the warm-up it judges normally, or the guard is a permanent mute.
func TestSilenceIsJudgedOnceWarmedUp(t *testing.T) {
	started := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{scan: silentScan(1)}

	e := conditions.New(store, started)
	e.SetClockForTest(func() time.Time { return started.Add(conditions.StaleAfter + time.Second) })

	if err := e.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(store.applied) != 1 || store.applied[0].Open == nil {
		t.Fatalf("want one open action, got %+v", store.applied)
	}
}

// The warm-up is about silence alone. A disk that is full is full whether or
// not the hub has been up long -- the reading came from the agent, not from
// the hub's sense of time.
func TestWarmUpDoesNotSuppressOtherKinds(t *testing.T) {
	started := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	k := conditions.Key{HostID: 1, Kind: conditions.KindDisk, Subject: "/var"}
	store := &fakeStore{scan: conditions.Scan{
		Evaluated: map[string]bool{conditions.KindDisk: true},
		Seen:      map[conditions.Key]bool{k: true},
		Bad: map[conditions.Key]conditions.Finding{
			k: {Key: k, Severity: conditions.SeverityCritical},
		},
		Reporting: map[int32]bool{1: true},
	}}

	e := conditions.New(store, started)
	e.SetClockForTest(func() time.Time { return started.Add(time.Second) })

	if err := e.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(store.applied) != 1 || store.applied[0].Open == nil {
		t.Fatalf("warm-up suppressed a disk condition: %+v", store.applied)
	}
}

// A scan that cannot run is not an empty scan. Returning the error rather than
// proceeding is what stops a database blip resolving the whole fleet.
func TestAFailedScanStopsThePass(t *testing.T) {
	store := &fakeStore{scanErr: errors.New("connection refused")}
	e := conditions.New(store, time.Now().Add(-time.Hour))

	if err := e.Once(context.Background()); err == nil {
		t.Fatal("a failed scan was treated as a successful pass")
	}
	if len(store.applied) != 0 {
		t.Fatalf("wrote something after a failed scan: %+v", store.applied)
	}
}

// Nothing to do means no transaction at all, which is what a healthy fleet
// costs: one scan, one read of the open set, and no write.
func TestAQuietPassWritesNothing(t *testing.T) {
	store := &fakeStore{scan: conditions.Scan{
		Evaluated: map[string]bool{conditions.KindDisk: true},
		Seen:      map[conditions.Key]bool{},
		Bad:       map[conditions.Key]conditions.Finding{},
		Reporting: map[int32]bool{},
	}}
	e := conditions.New(store, time.Now().Add(-time.Hour))

	if err := e.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(store.applied) != 0 {
		t.Fatalf("a quiet pass wrote %+v", store.applied)
	}
}
