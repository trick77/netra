package conditions_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
)

// fakeStore is guarded because Run drives it from its own goroutine while the
// test reads the counters, and CI runs with -race.
type fakeStore struct {
	mu       sync.Mutex
	scan     conditions.Scan
	scanErr  error
	open     []conditions.Open
	openErr  error
	applied  []conditions.Action
	applyErr error
	passes   int
	folds    int
	foldErr  error
	// scannedOpen is what the last pass handed the scan, so a test can assert
	// the onset walk is told which conditions already have one.
	scannedOpen map[conditions.Key]bool
	// scannedSince is the uptime bound the pass handed down, which is what
	// stops a rate being counted over the hub's own downtime.
	scannedSince time.Time
}

func (f *fakeStore) ScanConditions(_ context.Context, _ time.Time,
	open map[conditions.Key]bool, since time.Time) (conditions.Scan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passes++
	f.scannedOpen = open
	f.scannedSince = since
	return f.scan, f.scanErr
}

// FoldSamples records that it was called and, when foldErr is set, that a fold
// failure does not cost the pass: the five kinds that owe the moving average
// nothing must still be evaluated.
func (f *fakeStore) FoldSamples(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.folds++
	return f.foldErr
}

func (f *fakeStore) foldCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.folds
}

func (f *fakeStore) OpenConditions(context.Context) ([]conditions.Open, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open, f.openErr
}

func (f *fakeStore) ApplyConditions(_ context.Context, a []conditions.Action, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, a...)
	return f.applyErr
}

func (f *fakeStore) passCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.passes
}

func (f *fakeStore) appliedActions() []conditions.Action {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.applied)
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
	if len(store.appliedActions()) != 0 {
		t.Fatalf("opened a condition during warm-up: %+v", store.appliedActions())
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
	if len(store.appliedActions()) != 0 {
		t.Fatalf("resolved a silent condition during warm-up: %+v", store.appliedActions())
	}
}

// Past the warm-up it judges normally, or the guard is a permanent mute.
func TestSilenceIsJudgedOnceWarmedUp(t *testing.T) {
	started := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{scan: silentScan(1)}

	e := conditions.New(store, started)
	e.SetClockForTest(func() time.Time { return started.Add(conditions.WarmUp + time.Second) })

	if err := e.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	if len(store.appliedActions()) != 1 || store.appliedActions()[0].Open == nil {
		t.Fatalf("want one open action, got %+v", store.appliedActions())
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
	if len(store.appliedActions()) != 1 || store.appliedActions()[0].Open == nil {
		t.Fatalf("warm-up suppressed a disk condition: %+v", store.appliedActions())
	}
}

// The evaluator tells the scan how far back this PROCESS can vouch for.
//
// It is what stops `sporadic` -- a rate over missing sample buckets -- being
// counted across the hub's own downtime and reported as a fleet of flaky
// machines. The warm-up could not do this job: its window is minutes and the
// rate's is hours, so the hole a two-hour outage leaves outlives any warm-up.
func TestTheScanIsToldHowFarBackTheHubCanVouchFor(t *testing.T) {
	started := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{scan: conditions.Scan{
		Evaluated: map[string]bool{},
		Seen:      map[conditions.Key]bool{},
		Bad:       map[conditions.Key]conditions.Finding{},
		Reporting: map[int32]bool{},
	}}

	e := conditions.New(store, started)
	e.SetClockForTest(func() time.Time { return started.Add(4 * time.Hour) })

	if err := e.Once(context.Background()); err != nil {
		t.Fatalf("Once: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.scannedSince.Equal(started) {
		t.Errorf("since = %v, want the process start %v", store.scannedSince, started)
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
	if len(store.appliedActions()) != 0 {
		t.Fatalf("wrote something after a failed scan: %+v", store.appliedActions())
	}
}

// The loop ticks, and keeps ticking through a failure.
//
// A pass that errors is one missed evaluation and the next is a tick away.
// Ending the loop instead would mean a transient database error silently
// stops condition tracking for the life of the process -- the kind of failure
// that is only noticed weeks later, when someone asks why nothing has opened.
func TestRunKeepsTickingAfterAFailedPass(t *testing.T) {
	store := &fakeStore{scanErr: errors.New("connection refused")}
	e := conditions.New(store, time.Now().Add(-time.Hour))
	e.SetIntervalForTest(time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.Run(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for {
		if store.passCount() >= 3 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("the loop stopped after %d passes", store.passCount())
		case <-time.After(time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return when its context ended")
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
	if len(store.appliedActions()) != 0 {
		t.Fatalf("a quiet pass wrote %+v", store.appliedActions())
	}
}
