package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The read API mounts on the same /api/v1 the admin API does, so it is behind
// the same credential. An unauthenticated caller gets 401 rather than a host
// list -- and RequireAdmin is the ONLY thing standing between the internet and
// this data, now that Traefik routes the whole host and no PathPrefix narrows
// what reaches the hub.
func TestIntegrationReadEndpointsRequireTheAdminToken(t *testing.T) {
	srv, _ := newAdminFixture(t)
	id, _ := createHost(t, srv, "guarded")

	for _, path := range []string{
		"/api/v1/hosts",
		"/api/v1/hosts/1",
		"/api/v1/hosts/1/containers",
		"/api/v1/hosts/1/metrics?family=host",
		"/api/v1/events",
	} {
		resp, err := noRedirectClient(srv).Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s unauthenticated: status = %d, want 401", path, resp.StatusCode)
		}
	}

	// And the same paths answer once authenticated, so the 401s above are the
	// middleware rather than a missing route.
	resp := doAdmin(t, srv, http.MethodGet, "/api/v1/hosts/"+itoa(id), "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("authenticated GET: status = %d, want 200", resp.StatusCode)
	}
}

// GET /api/v1/hosts carries the host_current gauges alongside the identity
// fields the admin list already had.
func TestIntegrationHostListCarriesCurrentGauges(t *testing.T) {
	srv, s := newAdminFixture(t)
	id, _ := createHost(t, srv, "gauged")

	if _, err := s.Pool().Exec(context.Background(), `
		INSERT INTO host_current (host_id, last_seen, cpu_total, mem_used, mem_total, uptime_s)
		VALUES ($1, now(), 33.5, 500, 1000, 7200)`, id); err != nil {
		t.Fatalf("seed host_current: %v", err)
	}

	resp := doAdmin(t, srv, http.MethodGet, "/api/v1/hosts", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var hosts []struct {
		ID       int32    `json:"id"`
		Hostname string   `json:"hostname"`
		CPUTotal *float64 `json:"cpu_total"`
		MemTotal *int64   `json:"mem_total"`
	}
	decodeJSON(t, resp, &hosts)

	if len(hosts) != 1 {
		t.Fatalf("got %d hosts, want 1", len(hosts))
	}
	if hosts[0].CPUTotal == nil || *hosts[0].CPUTotal != 33.5 {
		t.Errorf("cpu_total = %v, want 33.5", hosts[0].CPUTotal)
	}
	if hosts[0].Hostname != "gauged" {
		t.Errorf("hostname = %q, want gauged", hosts[0].Hostname)
	}
}

// capabilities must reach the wire as an object, including for a host that has
// never reported any: null there would mean "we do not know what this agent
// can collect", which is the ambiguity the field exists to remove.
func TestIntegrationHostDetailSerialisesCapabilities(t *testing.T) {
	srv, s := newAdminFixture(t)
	id, _ := createHost(t, srv, "detailed")

	resp := doAdmin(t, srv, http.MethodGet, "/api/v1/hosts/"+itoa(id), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, `"capabilities":{}`) {
		t.Errorf("body = %s, want an empty capabilities object rather than null", body)
	}

	if _, err := s.Pool().Exec(context.Background(),
		`UPDATE hosts SET capabilities = '{"smart":"permission_denied"}'::jsonb WHERE id = $1`,
		id); err != nil {
		t.Fatalf("seed capabilities: %v", err)
	}

	resp = doAdmin(t, srv, http.MethodGet, "/api/v1/hosts/"+itoa(id), "")
	var detail struct {
		Capabilities map[string]string `json:"capabilities"`
	}
	decodeJSON(t, resp, &detail)
	if detail.Capabilities["smart"] != "permission_denied" {
		t.Errorf("capabilities = %v, want smart: permission_denied", detail.Capabilities)
	}
}

func TestIntegrationReadEndpointsAnswer404ForAnUnknownHost(t *testing.T) {
	srv, _ := newAdminFixture(t)

	for _, path := range []string{
		"/api/v1/hosts/4242",
		"/api/v1/hosts/4242/containers",
		"/api/v1/hosts/4242/filesystems",
		"/api/v1/hosts/4242/addresses",
		"/api/v1/hosts/4242/packages",
		"/api/v1/hosts/4242/units",
		"/api/v1/hosts/4242/metrics?family=host",
		"/api/v1/events?host=4242",
	} {
		resp := doAdmin(t, srv, http.MethodGet, path, "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestIntegrationReadEndpointsRejectANonNumericID(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodGet, "/api/v1/hosts/abc/containers", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// The dimension listings render as [] rather than null for a host with none,
// so a client can iterate the response without a null check that only some
// hosts need.
func TestIntegrationDimensionListingsRenderAsEmptyArrays(t *testing.T) {
	srv, _ := newAdminFixture(t)
	id, _ := createHost(t, srv, "empty")

	for _, suffix := range []string{"containers", "filesystems", "addresses", "packages", "units"} {
		resp := doAdmin(t, srv, http.MethodGet, "/api/v1/hosts/"+itoa(id)+"/"+suffix, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", suffix, resp.StatusCode)
		}
		if body := strings.TrimSpace(readBody(t, resp)); body != "[]" {
			t.Errorf("GET %s body = %s, want []", suffix, body)
		}
	}
}

// The whole /metrics envelope on the wire: the tier that answered, the step it
// implies, both windows, and the column names that make the tier impossible to
// confuse.
func TestIntegrationMetricsEnvelopeNamesItsTier(t *testing.T) {
	srv, s := newAdminFixture(t)
	id, _ := createHost(t, srv, "charted")

	if _, err := s.Pool().Exec(context.Background(), `
		INSERT INTO host_samples (host_id, ts, cpu_total)
		VALUES ($1, now() - INTERVAL '5 minutes', 21.0)`, id); err != nil {
		t.Fatalf("seed host_samples: %v", err)
	}

	resp := doAdmin(t, srv, http.MethodGet,
		"/api/v1/hosts/"+itoa(id)+"/metrics?family=host&columns=cpu_total", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, readBody(t, resp))
	}

	var res struct {
		Family     string   `json:"family"`
		Tier       string   `json:"tier"`
		StepS      int      `json:"step_s"`
		Columns    []string `json:"columns"`
		KeyColumns []string `json:"key_columns"`
		Warnings   []string `json:"warnings"`
		Window     struct {
			From time.Time `json:"from"`
			To   time.Time `json:"to"`
		} `json:"window"`
		Requested struct {
			From time.Time `json:"from"`
			To   time.Time `json:"to"`
		} `json:"requested_window"`
		Series []struct {
			Key    map[string]string `json:"key"`
			Points [][]json.RawMessage
		} `json:"series"`
	}
	decodeJSON(t, resp, &res)

	if res.Family != "host" || res.Tier != "raw" || res.StepS != 60 {
		t.Errorf("envelope = %s/%s/%ds, want host/raw/60s", res.Family, res.Tier, res.StepS)
	}
	if len(res.Columns) != 1 || res.Columns[0] != "cpu_total" {
		t.Errorf("columns = %v, want [cpu_total]", res.Columns)
	}
	if res.Window.To.IsZero() || res.Requested.To.IsZero() {
		t.Error("both windows must be present so every clamp is visible rather than inferred")
	}
	if res.Warnings == nil {
		t.Error("warnings = null, want [] -- an absent list is a null check a client should not need")
	}
	if len(res.Series) != 1 || len(res.Series[0].Points) != 1 {
		t.Fatalf("series = %+v, want one point", res.Series)
	}
}

// Tier selection reaches the wire: the same request over a longer range comes
// back naming a coarser tier and a different set of column names.
func TestIntegrationMetricsPicksACoarserTierForALongerRange(t *testing.T) {
	srv, _ := newAdminFixture(t)
	id, _ := createHost(t, srv, "historic")

	for _, tc := range []struct {
		query    string
		wantTier string
		wantStep int
		wantCol  string
	}{
		{"from=" + rfc(-6*24*time.Hour), "raw", 60, "cpu_total"},
		{"from=" + rfc(-10*24*time.Hour), "5m", 300, "cpu_total_avg"},
		{"from=" + rfc(-60*24*time.Hour), "1h", 3600, "cpu_total_avg"},
	} {
		t.Run(tc.wantTier, func(t *testing.T) {
			resp := doAdmin(t, srv, http.MethodGet,
				"/api/v1/hosts/"+itoa(id)+"/metrics?family=host&"+tc.query, "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d: %s", resp.StatusCode, readBody(t, resp))
			}

			var res struct {
				Tier    string   `json:"tier"`
				StepS   int      `json:"step_s"`
				Columns []string `json:"columns"`
			}
			decodeJSON(t, resp, &res)

			if res.Tier != tc.wantTier || res.StepS != tc.wantStep {
				t.Errorf("tier = %s/%ds, want %s/%ds", res.Tier, res.StepS, tc.wantTier, tc.wantStep)
			}
			if !slices.Contains(res.Columns, tc.wantCol) {
				t.Errorf("columns = %v, want %q", res.Columns, tc.wantCol)
			}
		})
	}
}

func TestIntegrationMetricsRejectsBadQueryParameters(t *testing.T) {
	srv, _ := newAdminFixture(t)
	id, _ := createHost(t, srv, "strict")
	base := "/api/v1/hosts/" + itoa(id) + "/metrics?"

	for name, query := range map[string]string{
		"an unknown family":  "family=cpu",
		"a missing family":   "",
		"an unparsable from": "family=host&from=yesterday",
		"an unparsable step": "family=host&step=fivemins",
		"a negative step":    "family=host&step=-5m",
		"an unknown column":  "family=host&columns=cpu_totl",
		"an inverted window": "family=host&from=" + rfc(0) + "&to=" + rfc(-time.Hour),
	} {
		t.Run(name, func(t *testing.T) {
			resp := doAdmin(t, srv, http.MethodGet, base+query, "")
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", resp.StatusCode, readBody(t, resp))
			}
		})
	}
}

// from and to accept unix milliseconds as well as RFC 3339, because
// milliseconds are what the response's own points carry: narrowing a window
// around a spike should not need the client to reformat a timestamp it was
// just handed.
func TestIntegrationMetricsAcceptsUnixMillisecondBounds(t *testing.T) {
	srv, _ := newAdminFixture(t)
	id, _ := createHost(t, srv, "millis")

	from := time.Now().Add(-time.Hour).UnixMilli()
	resp := doAdmin(t, srv, http.MethodGet,
		"/api/v1/hosts/"+itoa(id)+"/metrics?family=host&from="+itoa64(from), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readBody(t, resp))
	}
}

func TestIntegrationEventsEndpointFiltersAndOrders(t *testing.T) {
	srv, s := newAdminFixture(t)
	id, _ := createHost(t, srv, "eventful")

	if _, err := s.Pool().Exec(context.Background(), `
		INSERT INTO events (host_id, ts, type, subject, detail)
		VALUES ($1, now() - INTERVAL '10 minutes', 'mdraid_degraded', 'md0', '{"state":"degraded"}'),
		       ($1, now() - INTERVAL '20 minutes', 'agent_upgrade', NULL, '{}')`, id); err != nil {
		t.Fatalf("seed events: %v", err)
	}

	resp := doAdmin(t, srv, http.MethodGet, "/api/v1/events?host="+itoa(id), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var events []struct {
		Type     string          `json:"type"`
		Subject  *string         `json:"subject"`
		Hostname string          `json:"hostname"`
		Detail   json.RawMessage `json:"detail"`
	}
	decodeJSON(t, resp, &events)

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Type != "mdraid_degraded" {
		t.Errorf("first event = %q, want the newest", events[0].Type)
	}
	if events[0].Hostname != "eventful" {
		t.Errorf("hostname = %q, want eventful", events[0].Hostname)
	}
	if string(events[0].Detail) != `{"state": "degraded"}` &&
		string(events[0].Detail) != `{"state":"degraded"}` {
		t.Errorf("detail = %s, want the stored payload passed through", events[0].Detail)
	}
	if events[1].Subject != nil {
		t.Errorf("agent_upgrade subject = %v, want null", events[1].Subject)
	}

	filtered := doAdmin(t, srv, http.MethodGet,
		"/api/v1/events?host="+itoa(id)+"&type=agent_upgrade", "")
	var narrowed []struct {
		Type string `json:"type"`
	}
	decodeJSON(t, filtered, &narrowed)
	if len(narrowed) != 1 || narrowed[0].Type != "agent_upgrade" {
		t.Errorf("filtered = %+v, want the one agent_upgrade", narrowed)
	}
}

func TestIntegrationEventsEndpointRejectsBadQueryParameters(t *testing.T) {
	srv, _ := newAdminFixture(t)

	for name, query := range map[string]string{
		"a non-numeric host":  "host=abc",
		"an unparsable since": "since=lastweek",
		"a zero limit":        "limit=0",
		"a non-numeric limit": "limit=lots",
	} {
		t.Run(name, func(t *testing.T) {
			resp := doAdmin(t, srv, http.MethodGet, "/api/v1/events?"+query, "")
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func itoa(v int32) string   { return strconv.FormatInt(int64(v), 10) }
func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

// rfc renders a time offset from now in the format the endpoint parses.
func rfc(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(time.RFC3339)
}
