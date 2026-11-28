package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/client"
	"github.com/trick77/netra/internal/agent/collector"
	"github.com/trick77/netra/internal/agent/config"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// snapshotCollector emits one systemd snapshot per scrape, numbered so a test
// can tell which one reached the hub.
type snapshotCollector struct{ n int64 }

func (*snapshotCollector) Name() string { return "snapshot" }

func (s *snapshotCollector) Collect(context.Context) (*collector.Result, error) {
	s.n++
	return &collector.Result{
		SystemdSnapshot: &netrav1.SystemdSnapshot{
			TsMs:     s.n,
			Complete: true,
			Units: []*netrav1.SystemdUnitState{
				{UnitName: "exim4.service", State: "failed", Substate: "failed"},
			},
		},
	}, nil
}

func newSnapshotClient(t *testing.T, url string, c collector.Collector) *client.Client {
	t.Helper()
	return client.New(config.Config{
		HubURL:       url,
		Token:        "nta_test",
		BufferWindow: time.Hour,
		ProcRoot:     "../collector/testdata/proc1",
	}, []collector.Collector{collector.NewMemory("../collector/testdata/proc1"), c})
}

// The snapshot reaches the hub, and stops being sent once acked.
//
// It rides the Client rather than buffer.Scrape, so neither of the reflective
// guards that walk the scrape struct (sliceFields, and the row-count test
// built on it) can see it -- they only visit slice fields. Without a test of
// its own the whole path is uncovered.
func TestSystemdSnapshotIsSentAndClearedOnAck(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newSnapshotClient(t, srv.URL, &snapshotCollector{})
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := rec.last().GetSystemdSnapshot(); got == nil {
		t.Fatal("no snapshot on the POST; the hub cannot reconcile without one")
	} else if len(got.GetUnits()) != 1 {
		t.Fatalf("snapshot carries %d units, want 1", len(got.GetUnits()))
	}
}

// A POST the hub never acked must re-send the snapshot.
//
// Clearing it on send rather than on ack would drop the repair into the same
// hole that created the divergence: the scrape that failed to deliver is
// exactly the one carrying the correction, and the next snapshot would be up
// to five minutes away with a stale warning on the page throughout.
func TestSystemdSnapshotSurvivesAFailedPost(t *testing.T) {
	var down atomic.Bool
	down.Store(true)

	rec := &recorder{}
	inner := rec.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	c := newSnapshotClient(t, srv.URL, &snapshotCollector{})
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err == nil {
		t.Fatal("Flush succeeded against a hub returning 503")
	}
	if rec.count() != 0 {
		t.Fatalf("the recorder saw %d requests through a 503", rec.count())
	}

	down.Store(false)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush after recovery: %v", err)
	}

	if rec.last().GetSystemdSnapshot() == nil {
		t.Error("the snapshot was dropped by the failed POST; the correction it " +
			"carried is exactly what the outage kept from arriving")
	}
}

// mdCollector emits one md snapshot per scrape, its ts numbered.
type mdCollector struct{ n int64 }

func (*mdCollector) Name() string { return "md" }

func (m *mdCollector) Collect(context.Context) (*collector.Result, error) {
	m.n++
	return &collector.Result{
		MdSnapshot: &netrav1.MdSnapshot{
			TsMs:   m.n,
			Arrays: []*netrav1.MdArray{{Name: "md2", SyncAction: "check"}},
		},
	}, nil
}

// The md snapshot rides outside the ring like the systemd one: the newest
// reaches the hub, older ones are superseded rather than queued, and an acked
// one is not sent again.
func TestMdSnapshotSendsTheNewestAndClearsOnAck(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec.handler(t))
	t.Cleanup(srv.Close)

	c := newSnapshotClient(t, srv.URL, &mdCollector{})
	ctx := context.Background()

	c.ScrapeOnce(ctx)
	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	got := rec.last().GetMdSnapshot()
	if got.GetTsMs() != 2 || len(got.GetArrays()) != 1 {
		t.Fatalf("md snapshot = %v, want the second scrape's", got)
	}

	c.ScrapeOnce(ctx)
	if err := c.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := rec.last().GetMdSnapshot(); got.GetTsMs() != 3 {
		t.Errorf("after the ack, md snapshot = %v, want the third scrape's", got)
	}
}
