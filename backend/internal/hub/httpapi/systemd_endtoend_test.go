package httpapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/read"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// The reported bug, end to end: POST what an agent sends, read what the page
// reads.
//
// Every seam in between is exercised on purpose. The store tests prove the
// reconcile and the read tests prove the filter, but the complaint was never
// about either in isolation -- it was that a warning on the host page never
// went away. This is the test that fails if anything between the wire and the
// page brings that back.
func TestIntegrationAFailedUnitClearsItselfEndToEnd(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()
	svc := read.NewService(s.Pool())

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`SELECT id FROM hosts WHERE hostname = 'h1'`).Scan(&hostID); err != nil {
		t.Fatalf("read host: %v", err)
	}

	send := func(seq uint64, at time.Time, units ...*netrav1.SystemdUnitState) {
		t.Helper()
		req := fullRequest(seq, at.UnixMilli())
		req.SystemdSnapshot = &netrav1.SystemdSnapshot{
			TsMs: at.UnixMilli(), Complete: true, Units: units,
		}
		if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	}
	listed := func() []read.Unit {
		t.Helper()
		got, err := svc.Units(ctx, hostID)
		if err != nil {
			t.Fatalf("Units: %v", err)
		}
		return got
	}

	now := time.Now().Add(-time.Hour)
	ssh := &netrav1.SystemdUnitState{UnitName: "ssh.service", State: "active", Substate: "running"}
	eximOK := &netrav1.SystemdUnitState{UnitName: "exim4.service", State: "active", Substate: "running"}
	eximBad := &netrav1.SystemdUnitState{UnitName: "exim4.service", State: "failed", Substate: "failed"}

	// A healthy host lists nothing. On a real host that is 300-400 loaded
	// services, none of them anything an operator needs to act on.
	send(1, now, ssh, eximOK)
	if got := listed(); len(got) != 0 {
		t.Fatalf("a healthy host lists %d units, want none", len(got))
	}

	// exim4 falls over.
	send(2, now.Add(time.Minute), ssh, eximBad)
	got := listed()
	if len(got) != 1 || got[0].Name != "exim4.service" {
		t.Fatalf("units = %+v, want exim4.service alone", got)
	}

	// It is fixed. THIS is the step that used to be impossible: with only
	// events on the wire, a hub that missed the recovery had no way of being
	// told otherwise, and the warning stayed on the page forever.
	send(3, now.Add(2*time.Minute), ssh, eximOK)
	if got := listed(); len(got) != 0 {
		t.Fatalf("exim4.service is still listed as %+v after the host reported it healthy", got)
	}

	// It fails again, and this time it is purged rather than repaired -- so it
	// has to leave by vanishing from the snapshot rather than by changing
	// state. An agent can only iterate units that still exist, so no event
	// will ever mention it again.
	send(4, now.Add(3*time.Minute), ssh, eximBad)
	if len(listed()) != 1 {
		t.Fatal("exim4.service did not come back after failing a second time")
	}
	send(5, now.Add(4*time.Minute), ssh)
	if got := listed(); len(got) != 0 {
		t.Fatalf("units = %+v, want none -- the unit file is gone from the host", got)
	}
}
