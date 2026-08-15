package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/client"
	"github.com/trick77/netra/internal/agent/collector"
	"github.com/trick77/netra/internal/agent/config"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

func checkClient(t *testing.T, hubURL string) *client.Client {
	t.Helper()
	return client.NewWithInterval(
		config.Config{
			HubURL:       hubURL,
			Token:        "nta_test",
			BufferWindow: time.Hour,
			ProcRoot:     "../collector/testdata/proc1",
		},
		[]collector.Collector{},
		time.Minute,
	)
}

// The check is a real POST, to the real path, with the real token -- that is
// the whole point, since nothing cheaper exercises routing and authentication
// together. It carries no samples: every family insert on the hub returns early
// on an empty slice, so the request costs the hub nothing and still proves the
// path works end to end.
func TestCheckHubPostsASampleFreeBatchToTheIngestPath(t *testing.T) {
	var (
		gotPath string
		gotAuth string
	)
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		rec.handler(t).ServeHTTP(w, r)
	}))
	defer srv.Close()

	checkClient(t, srv.URL).CheckHub(context.Background())

	if gotPath != "/api/agent/v1/ingest" {
		t.Errorf("path = %q, want the ingest path", gotPath)
	}
	if gotAuth != "Bearer nta_test" {
		t.Errorf("authorization = %q, want the agent token", gotAuth)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(rec.requests))
	}
	if n := len(rec.requests[0].GetHostSamples()); n != 0 {
		t.Errorf("host samples = %d, want 0 -- the check must not invent a sample", n)
	}
	// The hash rides along even though the batch is empty. Without it the hub
	// compares its stored hash against nothing, which never matches, and every
	// agent is told to re-send metadata on every restart.
	if len(rec.requests[0].GetMetadataHash()) == 0 {
		t.Error("the check carried no metadata hash; the hub cannot tell it is unchanged")
	}
}

// A hub that has no metadata for this host says so on any post, and the answer
// is as valid on the check as on a scrape. Honouring it here means the FIRST
// real batch carries the block rather than the second.
func TestCheckHubAdoptsAMetadataRequestFromTheHub(t *testing.T) {
	rec := &recorder{respond: func(req *netrav1.IngestRequest) *netrav1.IngestResponse {
		return &netrav1.IngestResponse{AckSeq: req.GetSeq(), RequestMetadata: true}
	}}
	srv := httptest.NewServer(rec.handler(t))
	defer srv.Close()

	testee := checkClient(t, srv.URL)
	testee.CheckHub(context.Background())
	testee.ScrapeOnce(context.Background())
	if err := testee.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) < 2 {
		t.Fatalf("requests = %d, want the check plus the flush", len(rec.requests))
	}
	if rec.requests[1].GetMetadata() == nil {
		t.Error("the first real batch carried no metadata; the hub asked for it during the check")
	}
}

// A rejected token, an unreachable hub and a misrouted path all leave the agent
// RUNNING. A hub that is down when its agents boot is the ordinary case the
// ring buffer exists for, and refusing to start would turn a recoverable outage
// into a fleet that has to be restarted by hand once it ends.
func TestCheckHubSurvivesEveryRejection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"unauthorized", http.StatusUnauthorized},
		{"not found, as a misrouted proxy returns", http.StatusNotFound},
		{"unavailable", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			testee := checkClient(t, srv.URL)
			testee.CheckHub(context.Background())

			// Still usable afterwards: the check must not have consumed the
			// sequence counter, poisoned the ring, or left the client wedged.
			testee.ScrapeOnce(context.Background())
			if got := testee.BufferDepth(); got != 1 {
				t.Errorf("buffer depth = %d, want 1 -- the agent keeps collecting", got)
			}
		})
	}
}

// An unreachable hub is a transport error rather than a status, and it is the
// one an operator hits most: a typo in NETRA_HUB_URL.
func TestCheckHubSurvivesAnUnreachableHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	testee := checkClient(t, url)
	testee.CheckHub(context.Background())

	testee.ScrapeOnce(context.Background())
	if got := testee.BufferDepth(); got != 1 {
		t.Errorf("buffer depth = %d, want 1", got)
	}
}
