package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
)

type conditionJSON struct {
	ID            int64           `json:"id"`
	HostID        int32           `json:"host_id"`
	Hostname      string          `json:"hostname"`
	Kind          string          `json:"kind"`
	Subject       string          `json:"subject"`
	Severity      string          `json:"severity"`
	OpenedTS      time.Time       `json:"opened_ts"`
	OpenedAtLeast bool            `json:"opened_at_least"`
	Detail        json.RawMessage `json:"detail"`
	MeasuredTS    *time.Time      `json:"measured_ts"`
	Stale         bool            `json:"stale"`
}

type conditionsBody struct {
	Conditions []conditionJSON `json:"conditions"`
	Kinds      []struct {
		Kind       string `json:"kind"`
		Label      string `json:"label"`
		Severity   string `json:"severity"`
		Thresholds *struct {
			WarnPct  float64 `json:"warn_pct"`
			CritPct  float64 `json:"crit_pct"`
			WarnFree int64   `json:"warn_free"`
			CritFree int64   `json:"crit_free"`
		} `json:"thresholds"`
	} `json:"kinds"`
}

// The endpoint answers what is open now, with the onset the page prints, and
// only the rows that are still open.
func TestIntegrationConditionsEndpointServesOpenRowsWithTheirOnset(t *testing.T) {
	srv, s := newAdminFixture(t)
	ctx := context.Background()
	id, _ := createHost(t, srv, "conditional")
	now := time.Now().UTC().Truncate(time.Second)
	onset := now.Add(-6 * time.Hour)

	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_conditions
		    (host_id, kind, subject, severity, opened_ts, opened_at_least, detail)
		VALUES ($1, 'disk', 'root', 'critical', $2, TRUE, '{"pct":97.5,"mount":"/"}')`,
		id, onset); err != nil {
		t.Fatalf("seed open: %v", err)
	}
	// A resolved row, which must not appear: the endpoint answers "firing now",
	// not the history beside it.
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_conditions
		    (host_id, kind, subject, severity, opened_ts, resolved_ts, resolved_reason, detail)
		VALUES ($1, 'disk', 'var', 'warning', $2, $3, 'cleared', '{}')`,
		id, onset, now); err != nil {
		t.Fatalf("seed resolved: %v", err)
	}

	resp := doAdmin(t, srv, http.MethodGet, "/api/v1/conditions", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body conditionsBody
	decodeJSON(t, resp, &body)

	if len(body.Conditions) != 1 {
		t.Fatalf("conditions = %+v, want only the open one", body.Conditions)
	}
	got := body.Conditions[0]
	if got.Kind != "disk" || got.Subject != "root" || got.Severity != "critical" {
		t.Errorf("condition = %+v", got)
	}
	if got.Hostname != "conditional" {
		t.Errorf("hostname = %q, want the host joined in", got.Hostname)
	}
	// The onset the row carries, not the moment netra concluded it. This is the
	// column that used to be empty for four kinds out of five.
	if !got.OpenedTS.Equal(onset) {
		t.Errorf("opened_ts = %v, want the onset %v", got.OpenedTS, onset)
	}
	if !got.OpenedAtLeast {
		t.Error("opened_at_least did not survive the wire; the page would name a moment it cannot vouch for")
	}
	var detail map[string]any
	if err := json.Unmarshal(got.Detail, &detail); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail["mount"] != "/" || detail["pct"] != 97.5 {
		t.Errorf("detail = %+v, want the numbers the sentence is written from", detail)
	}
}

// The catalogue rides along, and it lists every kind rather than the ones
// present.
//
// A filter for a kind nobody is carrying must still be able to name itself, or
// a reader who followed a link to a kind that has since cleared is left holding
// a filter the page cannot name.
func TestIntegrationConditionsEndpointServesTheWholeCatalogue(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodGet, "/api/v1/conditions", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body conditionsBody
	decodeJSON(t, resp, &body)

	// Nothing is open, and the vocabulary is still complete.
	if len(body.Conditions) != 0 {
		t.Errorf("conditions = %+v, want none", body.Conditions)
	}
	if len(body.Kinds) != len(conditions.Catalogue()) {
		t.Fatalf("kinds = %d, want every one (%d)", len(body.Kinds), len(conditions.Catalogue()))
	}

	var disk *struct {
		WarnPct  float64 `json:"warn_pct"`
		CritPct  float64 `json:"crit_pct"`
		WarnFree int64   `json:"warn_free"`
		CritFree int64   `json:"crit_free"`
	}
	for _, k := range body.Kinds {
		if k.Label == "" {
			t.Errorf("%s reached the browser with no label", k.Kind)
		}
		if k.Kind == conditions.KindDisk {
			disk = k.Thresholds
		}
	}
	// The thresholds the browser colours a HEALTHY meter with. Without them on
	// the wire the four constants grow back in TypeScript.
	if disk == nil {
		t.Fatal("the disk kind carried no thresholds")
	}
	if disk.WarnPct != conditions.DiskWarnPct || disk.CritFree != conditions.DiskCritFree {
		t.Errorf("thresholds = %+v, want the hub's own numbers", *disk)
	}
}

// A subject that is present and unmeasurable is SERVED and MARKED, never
// dropped.
//
// The hub deliberately never resolves such a condition -- a hung NFS export
// looks exactly like an unmounted one from here -- and the browser used to make
// it go away by dropping the mount after three minutes, which is the same lie
// told quietly.
func TestIntegrationConditionsEndpointMarksAnUnmeasuredSubjectStale(t *testing.T) {
	srv, s := newAdminFixture(t)
	ctx := context.Background()
	id, _ := createHost(t, srv, "wedged")
	now := time.Now().UTC().Truncate(time.Second)

	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_current (host_id, last_seen) VALUES ($1, $2)`, id, now); err != nil {
		t.Fatalf("host_current: %v", err)
	}
	var fsID int32
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO filesystems (host_id, label, mountpoint)
		VALUES ($1, 'backup', '/mnt/backup') RETURNING id`, id).Scan(&fsID); err != nil {
		t.Fatalf("filesystems: %v", err)
	}
	// The host is talking; this mount has not been re-read for an hour.
	measured := now.Add(-time.Hour)
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO filesystem_current (host_id, fs_id, ts, total, used, free)
		VALUES ($1, $2, $3, 100, 97, 3)`, id, fsID, measured); err != nil {
		t.Fatalf("filesystem_current: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_conditions
		    (host_id, kind, subject, severity, opened_ts, detail)
		VALUES ($1, 'disk', 'backup', 'critical', $2, '{"pct":97.0,"mount":"/mnt/backup"}')`,
		id, measured); err != nil {
		t.Fatalf("seed condition: %v", err)
	}

	resp := doAdmin(t, srv, http.MethodGet, "/api/v1/conditions", "")
	var body conditionsBody
	decodeJSON(t, resp, &body)

	if len(body.Conditions) != 1 {
		t.Fatalf("conditions = %+v, want the stale one still served", body.Conditions)
	}
	got := body.Conditions[0]
	if !got.Stale {
		t.Error("a mount not re-read for an hour was not marked stale")
	}
	if got.MeasuredTS == nil || !got.MeasuredTS.Equal(measured) {
		t.Errorf("measured_ts = %v, want the reading's own ts %v", got.MeasuredTS, measured)
	}
}

// A host-wide condition has no subject to measure, so it is never stale.
//
// Its measurement IS last_seen, and the silent condition already states it.
// Marking it would put "not measured since" on a row that never claimed a
// measurement.
func TestIntegrationConditionsEndpointNeverMarksAHostWideConditionStale(t *testing.T) {
	srv, s := newAdminFixture(t)
	ctx := context.Background()
	id, _ := createHost(t, srv, "quiet")
	now := time.Now().UTC().Truncate(time.Second)
	long := now.Add(-72 * time.Hour)

	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_current (host_id, last_seen) VALUES ($1, $2)`, id, long); err != nil {
		t.Fatalf("host_current: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_conditions (host_id, kind, subject, severity, opened_ts, detail)
		VALUES ($1, 'silent', '', 'critical', $2, '{}')`, id, long); err != nil {
		t.Fatalf("seed condition: %v", err)
	}

	resp := doAdmin(t, srv, http.MethodGet, "/api/v1/conditions", "")
	var body conditionsBody
	decodeJSON(t, resp, &body)

	if len(body.Conditions) != 1 {
		t.Fatalf("conditions = %+v, want one", body.Conditions)
	}
	if body.Conditions[0].Stale {
		t.Error("a silent condition was marked stale; its subject is the host and it says so itself")
	}
	if body.Conditions[0].MeasuredTS != nil {
		t.Errorf("measured_ts = %v, want null for a host-wide subject", body.Conditions[0].MeasuredTS)
	}
}
