package read_test

import (
	"context"
	"errors"
	"testing"

	"github.com/trick77/netra/internal/hub/read"
)

// An array mid-check comes back with its progress, speed and time left worked
// out; an idle one with none of them, and its members either way.
func TestIntegrationArraysDeriveProgressAndTimeLeft(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "mirrored")

	// md2 at 50% of 2,000,000 sectors, 1000 KiB/s: 1,000,000 sectors of 512
	// bytes left at 1,024,000 B/s is 500 s.
	exec(t, pool, `
		INSERT INTO md_arrays (host_id, name, ts, level, raid_disks, degraded, state,
		                       sync_action, sync_done, sync_total, sync_speed_kib,
		                       mismatch_cnt, members)
		VALUES ($1, 'md2', now(), 'raid1', 2, 0, 'clean', 'check', 1000000, 2000000, 1000, 0,
		        '[{"device":"sdc1","state":"in_sync","slot":0},{"device":"sdd1","state":"in_sync","slot":1}]'),
		       ($1, 'md0', now() - interval '1 hour', 'raid1', 2, 1, 'clean', 'idle', NULL, NULL, NULL, NULL, '[]')`, id)

	got, err := svc.Arrays(ctx, id)
	if err != nil {
		t.Fatalf("Arrays: %v", err)
	}
	if len(got) != 2 || got[0].Name != "md0" || got[1].Name != "md2" {
		t.Fatalf("arrays = %+v, want md0 then md2", got)
	}

	idle, busy := got[0], got[1]
	if idle.Progress != nil || idle.SpeedBps != nil || idle.EtaS != nil {
		t.Errorf("idle array has sync figures: %+v", idle)
	}
	if !idle.Stale || busy.Stale {
		t.Errorf("stale: md0 (an hour old) = %v, md2 (now) = %v", idle.Stale, busy.Stale)
	}
	if idle.Members == nil || idle.Degraded != 1 {
		t.Errorf("idle array = %+v, want empty members (not null) and 1 degraded", idle)
	}
	if busy.Progress == nil || *busy.Progress != 0.5 {
		t.Errorf("progress = %v, want 0.5", busy.Progress)
	}
	if busy.SpeedBps == nil || *busy.SpeedBps != 1_024_000 {
		t.Errorf("speed = %v, want 1024000 B/s", busy.SpeedBps)
	}
	if busy.EtaS == nil || *busy.EtaS != 500 {
		t.Errorf("eta = %v, want 500 s", busy.EtaS)
	}
	if len(busy.Members) != 2 || busy.Members[1].Device != "sdd1" || *busy.Members[1].Slot != 1 {
		t.Errorf("members = %+v", busy.Members)
	}
}

func TestIntegrationArraysOfAnUnknownHost(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.Arrays(context.Background(), 987654); !errors.Is(err, read.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
