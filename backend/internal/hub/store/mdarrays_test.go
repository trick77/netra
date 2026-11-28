package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/hub/conditions"
	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

func mdSnap(at time.Time, arrays ...*netrav1.MdArray) *netrav1.MdSnapshot {
	return &netrav1.MdSnapshot{TsMs: at.UnixMilli(), Arrays: arrays}
}

func mdArray(name, sync string) *netrav1.MdArray {
	return &netrav1.MdArray{
		Name: name, Level: "raid1", RaidDisks: 2, State: "clean", SyncAction: sync,
		Members: []*netrav1.MdMember{
			{Device: "sdc1", State: "in_sync", Slot: proto.Uint32(0)},
			{Device: "sdd1", State: "in_sync", Slot: proto.Uint32(1)},
		},
	}
}

// mdRows is md_arrays for one host: name → sync_action.
func mdRows(t *testing.T, s *store.Store, hostID int32) map[string]string {
	t.Helper()
	rows, err := s.Pool().Query(context.Background(),
		`SELECT name, sync_action FROM md_arrays WHERE host_id = $1`, hostID)
	if err != nil {
		t.Fatalf("read md_arrays: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, sync string
		if err := rows.Scan(&name, &sync); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[name] = sync
	}
	return out
}

// A snapshot is the whole set: what it omits is gone, an empty one clears the
// host, and one older than what is stored changes nothing.
func TestIntegrationApplyMdSnapshotReplacesTheSet(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "md-host")
	t0 := time.Now().Add(-5 * time.Minute).Truncate(time.Millisecond)

	// Given: two arrays, one of them checking.
	if err := s.ApplyMdSnapshot(ctx, id, mdSnap(t0, mdArray("md1", "check"), mdArray("md2", "idle"))); err != nil {
		t.Fatalf("ApplyMdSnapshot: %v", err)
	}
	if got := mdRows(t, s, id); got["md1"] != "check" || got["md2"] != "idle" {
		t.Fatalf("rows = %v", got)
	}

	// When: md2 is gone and md1 finished.
	if err := s.ApplyMdSnapshot(ctx, id, mdSnap(t0.Add(time.Minute), mdArray("md1", "idle"))); err != nil {
		t.Fatalf("ApplyMdSnapshot: %v", err)
	}
	if got := mdRows(t, s, id); len(got) != 1 || got["md1"] != "idle" {
		t.Fatalf("rows after md2 left = %v, want md1 idle alone", got)
	}

	// When: a replayed, older snapshot arrives.
	if err := s.ApplyMdSnapshot(ctx, id, mdSnap(t0, mdArray("md1", "check"), mdArray("md2", "idle"))); err != nil {
		t.Fatalf("ApplyMdSnapshot: %v", err)
	}
	if got := mdRows(t, s, id); len(got) != 1 || got["md1"] != "idle" {
		t.Fatalf("an older snapshot moved the rows: %v", got)
	}

	// When: the host has no arrays any more.
	if err := s.ApplyMdSnapshot(ctx, id, mdSnap(t0.Add(2*time.Minute))); err != nil {
		t.Fatalf("ApplyMdSnapshot: %v", err)
	}
	if got := mdRows(t, s, id); len(got) != 0 {
		t.Errorf("rows after an empty snapshot = %v, want none", got)
	}
}

// A load condition says which md arrays were at work: a scrub parks threads
// in I/O wait, which Linux counts as load on a quiet CPU.
func TestIntegrationLoadConditionNamesTheArraysAtWork(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(ctx, t, s, "md-load")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(ctx, t, s, host, now)

	// Eight days at a load of 1.5, then a quarter of an hour at 9, with md1
	// checking and md2 idle.
	seedHostSeries(ctx, t, s, host, now.Add(-15*time.Minute), 8*24*60, 300, 1.5)
	seedHostSeries(ctx, t, s, host, now, 15, 300, 9)
	if err := s.ApplyMdSnapshot(ctx, host, mdSnap(now, mdArray("md1", "check"), mdArray("md2", "idle"))); err != nil {
		t.Fatalf("ApplyMdSnapshot: %v", err)
	}

	if err := s.FoldSamples(ctx); err != nil {
		t.Fatalf("FoldSamples: %v", err)
	}
	// The fold reads a week back at most, and the host kinds want more than a
	// week of watching: backdate the state's start as a host running for a
	// month would have it.
	if _, err := s.Pool().Exec(ctx,
		`UPDATE metric_ewma SET first_ts = first_ts - interval '30 days'
		  WHERE host_id = $1 AND kind = $2`, host, conditions.KindLoad); err != nil {
		t.Fatalf("backdate ewma: %v", err)
	}
	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("ScanConditions: %v", err)
	}

	finding, bad := scan.Bad[conditions.Key{HostID: host, Kind: conditions.KindLoad}]
	if !bad {
		key := conditions.Key{HostID: host, Kind: conditions.KindLoad}
		t.Fatalf("a load of 9 against a normal of 1.5 was not raised; unjudged=%v seen=%v",
			scan.Unjudged[key], scan.Seen[key])
	}
	got, _ := finding.Detail["md_sync"].([]string)
	if len(got) != 1 || got[0] != "md1 check" {
		t.Errorf("md_sync = %v, want [md1 check]", finding.Detail["md_sync"])
	}

	// And the log's opening row says so too, not only the condition row.
	if err := s.ApplyConditions(ctx, []conditions.Action{{Open: &finding}}, now); err != nil {
		t.Fatalf("ApplyConditions: %v", err)
	}
	var logged string
	if err := s.Pool().QueryRow(ctx, `
		SELECT detail->'md_sync'->>0 FROM events
		 WHERE host_id = $1 AND type = $2`, host, conditions.KindLoad).Scan(&logged); err != nil {
		t.Fatalf("read the opening event: %v", err)
	}
	if logged != "md1 check" {
		t.Errorf("opening event md_sync[0] = %q, want md1 check", logged)
	}
}
