package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
	"github.com/trick77/netra/internal/hub/store"
)

func condCtx(t *testing.T) (context.Context, *store.Store) {
	t.Helper()
	s := store.OpenTest(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return ctx, s
}

func newHost(t *testing.T, ctx context.Context, s *store.Store, name string) int32 {
	t.Helper()
	var id int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	return id
}

func diskKey(host int32, label string) conditions.Key {
	return conditions.Key{HostID: host, Kind: conditions.KindDisk, Subject: label}
}

func openFinding(k conditions.Key, severity string, at time.Time) conditions.Finding {
	return conditions.Finding{
		Key:      k,
		Severity: severity,
		OpenedTS: at,
		Detail:   map[string]any{"pct": 97.0, "mount": "/var"},
	}
}

// An opened condition is a row AND an event, written together.
func TestIntegrationOpeningWritesBothHalves(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-open")
	at := time.Now().UTC().Truncate(time.Second)
	k := diskKey(host, "root")

	if err := s.ApplyConditions(ctx,
		[]conditions.Action{{Open: ptrFinding(openFinding(k, conditions.SeverityCritical, at))}},
		at); err != nil {
		t.Fatalf("apply: %v", err)
	}

	open, err := s.OpenConditions(ctx)
	if err != nil {
		t.Fatalf("open conditions: %v", err)
	}
	if len(open) != 1 || open[0].Key != k {
		t.Fatalf("open = %+v, want one %+v", open, k)
	}
	if open[0].Severity != conditions.SeverityCritical {
		t.Errorf("severity = %q", open[0].Severity)
	}

	// The event carries the OPENING timestamp, not the tick that noticed: a
	// filesystem walked back to 03:00 opened at 03:00.
	var evTS time.Time
	var severity string
	var detail []byte
	if err := s.Pool().QueryRow(ctx, `
		SELECT ts, severity, detail FROM events
		 WHERE host_id = $1 AND type = 'disk'`, host).Scan(&evTS, &severity, &detail); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if !evTS.UTC().Equal(at) {
		t.Errorf("event ts = %v, want the onset %v", evTS.UTC(), at)
	}
	if severity != conditions.SeverityCritical {
		t.Errorf("event severity = %q, want critical", severity)
	}
	var d map[string]any
	if err := json.Unmarshal(detail, &d); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d["transition"] != "opened" {
		t.Errorf("transition = %v, want opened", d["transition"])
	}
}

// The partial unique index is the invariant, so a second open of the same
// subject cannot create a second row -- and must not write a second event
// either, which would report an onset that never happened.
func TestIntegrationOpeningTwiceIsOnceOnly(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-twice")
	at := time.Now().UTC().Truncate(time.Second)
	k := diskKey(host, "root")

	action := []conditions.Action{{Open: ptrFinding(openFinding(k, conditions.SeverityWarning, at))}}
	if err := s.ApplyConditions(ctx, action, at); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// A second attempt, at a LATER timestamp so it would be a distinct event
	// row if it were written at all.
	later := at.Add(time.Minute)
	if err := s.ApplyConditions(ctx,
		[]conditions.Action{{Open: ptrFinding(openFinding(k, conditions.SeverityWarning, later))}},
		later); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	var rows, events int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_conditions WHERE host_id = $1`, host).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1 AND type = 'disk'`, host).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if rows != 1 {
		t.Errorf("host_conditions rows = %d, want 1", rows)
	}
	if events != 1 {
		t.Errorf("events = %d, want 1 -- a re-open must not invent an onset", events)
	}
}

// Resolving keeps the row and writes the closing event, which carries what it
// closed so a consumer can pair the two without scanning.
func TestIntegrationResolvingKeepsTheRow(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-resolve")
	at := time.Now().UTC().Truncate(time.Second)
	k := diskKey(host, "root")

	if err := s.ApplyConditions(ctx,
		[]conditions.Action{{Open: ptrFinding(openFinding(k, conditions.SeverityWarning, at))}},
		at); err != nil {
		t.Fatalf("apply open: %v", err)
	}
	open, _ := s.OpenConditions(ctx)

	closed := at.Add(2 * time.Hour)
	if err := s.ApplyConditions(ctx, []conditions.Action{{Resolve: &conditions.Resolution{
		ID: open[0].ID, Key: k, Reason: conditions.ReasonCleared,
	}}}, closed); err != nil {
		t.Fatalf("apply resolve: %v", err)
	}

	if remaining, err := s.OpenConditions(ctx); err != nil || len(remaining) != 0 {
		t.Fatalf("open = %+v (err %v), want none", remaining, err)
	}

	// The row survives: history is the reason cleared rows are kept.
	var reason string
	var resolvedTS *time.Time
	if err := s.Pool().QueryRow(ctx, `
		SELECT resolved_reason, resolved_ts FROM host_conditions
		 WHERE id = $1`, open[0].ID).Scan(&reason, &resolvedTS); err != nil {
		t.Fatalf("read resolved row: %v", err)
	}
	if reason != conditions.ReasonCleared || resolvedTS == nil {
		t.Errorf("reason = %q, resolved_ts = %v", reason, resolvedTS)
	}

	var detail []byte
	if err := s.Pool().QueryRow(ctx, `
		SELECT detail FROM events
		 WHERE host_id = $1 AND type = 'disk' AND detail ->> 'transition' = 'cleared'`,
		host).Scan(&detail); err != nil {
		t.Fatalf("read cleared event: %v", err)
	}
	var d map[string]any
	if err := json.Unmarshal(detail, &d); err != nil {
		t.Fatalf("detail: %v", err)
	}
	// info, whatever the condition's own severity was: the thing worth
	// attention is a condition opening, and paging on a recovery is how people
	// learn to mute a feed.
	if d["severity"] != "info" {
		t.Errorf("cleared severity = %v, want info", d["severity"])
	}
	if d["open_ms"] != float64((2 * time.Hour).Milliseconds()) {
		t.Errorf("open_ms = %v, want the interval it was open", d["open_ms"])
	}
}

// The same subject can go bad twice, which is why the primary key is an
// identity and the triple is unique only among open rows.
func TestIntegrationTheSameSubjectCanReopen(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-reopen")
	first := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)
	k := diskKey(host, "root")

	if err := s.ApplyConditions(ctx,
		[]conditions.Action{{Open: ptrFinding(openFinding(k, conditions.SeverityWarning, first))}},
		first); err != nil {
		t.Fatalf("open: %v", err)
	}
	open, _ := s.OpenConditions(ctx)
	cleared := first.Add(time.Hour)
	if err := s.ApplyConditions(ctx, []conditions.Action{{Resolve: &conditions.Resolution{
		ID: open[0].ID, Key: k, Reason: conditions.ReasonCleared,
	}}}, cleared); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	again := cleared.Add(time.Hour)
	if err := s.ApplyConditions(ctx,
		[]conditions.Action{{Open: ptrFinding(openFinding(k, conditions.SeverityCritical, again))}},
		again); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	var total int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_conditions WHERE host_id = $1`, host).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Errorf("rows = %d, want 2 -- the history and the new occurrence", total)
	}
	nowOpen, _ := s.OpenConditions(ctx)
	if len(nowOpen) != 1 || nowOpen[0].Severity != conditions.SeverityCritical {
		t.Errorf("open = %+v, want the new critical one", nowOpen)
	}
}

// A miss carries nil detail, which must leave the stored numbers alone rather
// than blank a NOT NULL column.
func TestIntegrationAMissKeepsTheStoredDetail(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-miss")
	at := time.Now().UTC().Truncate(time.Second)
	k := diskKey(host, "root")

	if err := s.ApplyConditions(ctx,
		[]conditions.Action{{Open: ptrFinding(openFinding(k, conditions.SeverityWarning, at))}},
		at); err != nil {
		t.Fatalf("open: %v", err)
	}
	open, _ := s.OpenConditions(ctx)

	if err := s.ApplyConditions(ctx, []conditions.Action{{Update: &conditions.Update{
		ID: open[0].ID, Key: k, Severity: conditions.SeverityWarning, MissingTicks: 1,
	}}}, at); err != nil {
		t.Fatalf("update: %v", err)
	}

	var detail []byte
	if err := s.Pool().QueryRow(ctx,
		`SELECT detail FROM host_conditions WHERE id = $1`, open[0].ID).Scan(&detail); err != nil {
		t.Fatalf("read: %v", err)
	}
	var d map[string]any
	if err := json.Unmarshal(detail, &d); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d["pct"] != 97.0 {
		t.Errorf("detail = %+v, want the numbers it opened with", d)
	}

	after, _ := s.OpenConditions(ctx)
	if len(after) != 1 || after[0].MissingTicks != 1 {
		t.Errorf("open = %+v, want the miss recorded", after)
	}
}

// Retention removes resolved rows and NEVER open ones. 0001_init.sql's scar:
// a pruned log let a unit failed for 90 days lose its only event and go
// silently NULL -- the hub forgetting a live problem and calling it resolved.
func TestIntegrationPruneKeepsOpenConditionsForever(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-prune")
	ancient := time.Now().UTC().Add(-365 * 24 * time.Hour)

	// One open and ancient, one resolved and ancient.
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_conditions (host_id, kind, subject, severity, opened_ts)
		VALUES ($1, 'disk', 'old-open', 'warning', $2)`, host, ancient); err != nil {
		t.Fatalf("insert open: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_conditions
		    (host_id, kind, subject, severity, opened_ts, resolved_ts, resolved_reason)
		VALUES ($1, 'disk', 'old-closed', 'warning', $2, $2, 'cleared')`, host, ancient); err != nil {
		t.Fatalf("insert resolved: %v", err)
	}

	if _, err := s.Pool().Exec(ctx,
		`CALL netra_prune_conditions(0, '{"retention": "90 days"}'::jsonb)`); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var subjects []string
	rows, err := s.Pool().Query(ctx,
		`SELECT subject FROM host_conditions WHERE host_id = $1`, host)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sub string
		if err := rows.Scan(&sub); err != nil {
			t.Fatalf("scan: %v", err)
		}
		subjects = append(subjects, sub)
	}

	if len(subjects) != 1 || subjects[0] != "old-open" {
		t.Fatalf("after prune: %v, want only the still-open one", subjects)
	}
}

// The scan reads real tables, so this is what proves the observers' SQL runs
// at all -- column names, joins and the nullable mountpoint included.
func TestIntegrationScanFindsAFullDisk(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-scan")
	now := time.Now().UTC()

	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_current (host_id, last_seen) VALUES ($1, $2)`, host, now); err != nil {
		t.Fatalf("host_current: %v", err)
	}
	var fsID int32
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO filesystems (host_id, label, mountpoint)
		VALUES ($1, 'root', '/') RETURNING id`, host).Scan(&fsID); err != nil {
		t.Fatalf("filesystems: %v", err)
	}
	// 97% full with 3 GB left: over both halves of the compound rule.
	used := int64(97) * 1024 * 1024 * 1024
	free := int64(3) * 1024 * 1024 * 1024
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO filesystem_current (host_id, fs_id, ts, total, used, free)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		host, fsID, now, used+free, used, free); err != nil {
		t.Fatalf("filesystem_current: %v", err)
	}

	scan, err := s.ScanConditions(ctx, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !scan.Evaluated[conditions.KindDisk] {
		t.Fatal("the disk kind was not evaluated; its query failed")
	}
	if !scan.Reporting[host] {
		t.Error("a host seen just now was not reporting")
	}

	k := diskKey(host, "root")
	if !scan.Seen[k] {
		t.Fatalf("the mount was not seen: %+v", scan.Seen)
	}
	f, bad := scan.Bad[k]
	if !bad {
		t.Fatalf("a 97%% disk with 3 GB left was not flagged: %+v", scan.Bad)
	}
	if f.Severity != conditions.SeverityCritical {
		t.Errorf("severity = %q, want critical", f.Severity)
	}
	// The subject is the label; the mountpoint is what the sentence prints.
	if f.Detail["mount"] != "/" {
		t.Errorf("detail mount = %v, want the mountpoint", f.Detail["mount"])
	}
}

// The other half of the compound rule, against the real query: a big array at
// the same percentage has room and must not be flagged.
func TestIntegrationScanLeavesABigArrayAlone(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-scan-big")
	now := time.Now().UTC()

	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_current (host_id, last_seen) VALUES ($1, $2)`, host, now); err != nil {
		t.Fatalf("host_current: %v", err)
	}
	var fsID int32
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO filesystems (host_id, label, mountpoint)
		VALUES ($1, 'ark', '/mnt/ark') RETURNING id`, host).Scan(&fsID); err != nil {
		t.Fatalf("filesystems: %v", err)
	}
	// 97% of a ~9.5 TB array: 287 GB left, which is a week of headroom.
	gb := int64(1024) * 1024 * 1024
	used := 9300 * gb
	free := 287 * gb
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO filesystem_current (host_id, fs_id, ts, total, used, free)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		host, fsID, now, used+free, used, free); err != nil {
		t.Fatalf("filesystem_current: %v", err)
	}

	scan, err := s.ScanConditions(ctx, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := diskKey(host, "ark")
	if !scan.Seen[k] {
		t.Error("the mount was not seen, so it would read as vanished")
	}
	if _, bad := scan.Bad[k]; bad {
		t.Error("a 9.5 TB array with 287 GB free was flagged")
	}
}

// A host nobody has heard from is silent, and its onset is last_seen itself.
func TestIntegrationScanFindsASilentHost(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-silent")
	now := time.Now().UTC()
	quiet := now.Add(-time.Hour)

	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_current (host_id, last_seen) VALUES ($1, $2)`, host, quiet); err != nil {
		t.Fatalf("host_current: %v", err)
	}

	scan, err := s.ScanConditions(ctx, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.Reporting[host] {
		t.Error("a host quiet for an hour was called reporting")
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindSilent}
	f, bad := scan.Bad[k]
	if !bad {
		t.Fatalf("a silent host was not flagged: %+v", scan.Bad)
	}
	if !f.OpenedTS.UTC().Equal(quiet.Truncate(time.Microsecond)) {
		t.Errorf("opened_ts = %v, want last_seen %v", f.OpenedTS.UTC(), quiet)
	}
}

// Failed units are one condition per host, and the onset is the OLDEST of
// them: five units failing at five times is one condition that began with the
// first.
func TestIntegrationScanCountsFailedUnitsOncePerHost(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-units")
	now := time.Now().UTC()
	oldest := now.Add(-6 * time.Hour)

	// The COUNT comes from the agent's own summary, which is what the UI
	// counts too -- see the comment on scanUnits. The unit rows supply the
	// names and the onset.
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_current (host_id, last_seen, services_failed)
		VALUES ($1, $2, 2)`, host, now); err != nil {
		t.Fatalf("host_current: %v", err)
	}
	for _, u := range []struct {
		name  string
		state string
		ts    time.Time
	}{
		{"exim4.service", "failed", oldest},
		{"nginx.service", "failed", now.Add(-time.Hour)},
		{"cron.service", "active", now},
	} {
		if _, err := s.Pool().Exec(ctx, `
			INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
			VALUES ($1, $2, $3, $3, $4)`, host, u.name, u.state, u.ts); err != nil {
			t.Fatalf("insert unit: %v", err)
		}
	}

	scan, err := s.ScanConditions(ctx, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindFailedUnits}
	f, bad := scan.Bad[k]
	if !bad {
		t.Fatalf("failed units were not flagged: %+v", scan.Bad)
	}
	if f.Detail["count"] != 2 {
		t.Errorf("count = %v, want 2 -- the active unit must not be counted", f.Detail["count"])
	}
	if !f.OpenedTS.UTC().Equal(oldest.Truncate(time.Microsecond)) {
		t.Errorf("opened_ts = %v, want the oldest failure %v", f.OpenedTS.UTC(), oldest)
	}
}

// A host whose units are all healthy is SEEN, which is what lets a condition
// on it clear rather than hang open forever.
func TestIntegrationAHealthyHostIsStillSeen(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-healthy")
	now := time.Now().UTC()

	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_current (host_id, last_seen) VALUES ($1, $2)`, host, now); err != nil {
		t.Fatalf("host_current: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
		VALUES ($1, 'cron.service', 'active', 'running', $2)`, host, now); err != nil {
		t.Fatalf("insert unit: %v", err)
	}

	scan, err := s.ScanConditions(ctx, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindFailedUnits}
	if !scan.Seen[k] {
		t.Error("a host with healthy units was not seen, so its condition could never clear")
	}
	if _, bad := scan.Bad[k]; bad {
		t.Error("a host with no failed units was flagged")
	}
}

// A condition that gets worse is the same condition. It updates in place and
// writes NO event: only opening and clearing are transitions, and a row per
// tick for an unchanged condition is the near-constant-series waste the whole
// event model exists to keep out of the log.
func TestIntegrationSeverityChangesInPlaceWithoutAnEvent(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-worse")
	at := time.Now().UTC().Truncate(time.Second)
	k := diskKey(host, "root")

	if err := s.ApplyConditions(ctx,
		[]conditions.Action{{Open: ptrFinding(openFinding(k, conditions.SeverityWarning, at))}},
		at); err != nil {
		t.Fatalf("open: %v", err)
	}
	open, _ := s.OpenConditions(ctx)

	if err := s.ApplyConditions(ctx, []conditions.Action{{Update: &conditions.Update{
		ID:       open[0].ID,
		Key:      k,
		Severity: conditions.SeverityCritical,
		Detail:   map[string]any{"pct": 99.0, "mount": "/var"},
	}}}, at.Add(time.Minute)); err != nil {
		t.Fatalf("update: %v", err)
	}

	after, err := s.OpenConditions(ctx)
	if err != nil {
		t.Fatalf("open conditions: %v", err)
	}
	if len(after) != 1 || after[0].Severity != conditions.SeverityCritical {
		t.Fatalf("open = %+v, want one critical", after)
	}

	var detail []byte
	if err := s.Pool().QueryRow(ctx,
		`SELECT detail FROM host_conditions WHERE id = $1`, open[0].ID).Scan(&detail); err != nil {
		t.Fatalf("read: %v", err)
	}
	var d map[string]any
	if err := json.Unmarshal(detail, &d); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d["pct"] != 99.0 {
		t.Errorf("detail = %+v, want the refreshed numbers", d)
	}

	var events int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1 AND type = 'disk'`, host).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Errorf("events = %d, want 1 -- getting worse is not a transition", events)
	}
}

// Resolving twice is not two events. The guard on resolved_ts makes the write
// idempotent, which matters because a retried transaction must not invent a
// second ending for one condition.
func TestIntegrationResolvingTwiceIsIdempotent(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-twice-resolve")
	at := time.Now().UTC().Truncate(time.Second)
	k := diskKey(host, "root")

	if err := s.ApplyConditions(ctx,
		[]conditions.Action{{Open: ptrFinding(openFinding(k, conditions.SeverityWarning, at))}},
		at); err != nil {
		t.Fatalf("open: %v", err)
	}
	open, _ := s.OpenConditions(ctx)
	resolve := []conditions.Action{{Resolve: &conditions.Resolution{
		ID: open[0].ID, Key: k, Reason: conditions.ReasonVanished,
	}}}

	if err := s.ApplyConditions(ctx, resolve, at.Add(time.Hour)); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if err := s.ApplyConditions(ctx, resolve, at.Add(2*time.Hour)); err != nil {
		t.Fatalf("second resolve: %v", err)
	}

	var cleared int
	if err := s.Pool().QueryRow(ctx, `
		SELECT count(*) FROM events
		 WHERE host_id = $1 AND detail ->> 'transition' = 'cleared'`, host).Scan(&cleared); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cleared != 1 {
		t.Errorf("cleared events = %d, want 1", cleared)
	}
}

// No actions, no transaction.
func TestIntegrationApplyingNothingDoesNothing(t *testing.T) {
	ctx, s := condCtx(t)
	if err := s.ApplyConditions(ctx, nil, time.Now()); err != nil {
		t.Fatalf("apply nothing: %v", err)
	}
}

// A host-wide condition stores its subject as ” and sends the event's subject
// as NULL, which is what every other producer sends and what the natural key's
// NULLS NOT DISTINCT expects.
func TestIntegrationAHostWideConditionHasANullEventSubject(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-hostwide")
	at := time.Now().UTC().Truncate(time.Second)
	k := conditions.Key{HostID: host, Kind: conditions.KindSilent}

	if err := s.ApplyConditions(ctx, []conditions.Action{{Open: ptrFinding(conditions.Finding{
		Key:      k,
		Severity: conditions.SeverityCritical,
		OpenedTS: at,
		Detail:   map[string]any{"last_seen": at.Format(time.RFC3339)},
	})}}, at); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var subject *string
	if err := s.Pool().QueryRow(ctx,
		`SELECT subject FROM events WHERE host_id = $1 AND type = 'silent'`, host).Scan(&subject); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if subject != nil {
		t.Errorf("event subject = %q, want NULL for a host-wide condition", *subject)
	}
}

// The summary and the unit rows are ALLOWED to disagree, and the count wins.
//
// fleet/conditions.ts states it: "the count leads and comes from
// services_failed, which is the agent's own summary; the names annotate it and
// come from the hub's unit rows. The two are allowed to disagree -- a host
// heard from once has a summary and no unit rows yet". Counting unit rows here
// instead would make the hub and the browser differ routinely, because the
// summary rides every 60s scrape while the snapshot filling those rows arrives
// every five minutes.
func TestIntegrationFailedUnitsTrustTheSummaryOverTheRows(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-units-summary")
	now := time.Now().UTC()

	// A host heard from, with a summary and no unit rows at all.
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_current (host_id, last_seen, services_failed)
		VALUES ($1, $2, 3)`, host, now); err != nil {
		t.Fatalf("host_current: %v", err)
	}

	scan, err := s.ScanConditions(ctx, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := conditions.Key{HostID: host, Kind: conditions.KindFailedUnits}
	f, bad := scan.Bad[k]
	if !bad {
		t.Fatalf("a host reporting 3 failed services raised nothing: %+v", scan.Bad)
	}
	if f.Detail["count"] != 3 {
		t.Errorf("count = %v, want the summary's 3", f.Detail["count"])
	}
	if _, named := f.Detail["units"]; named {
		t.Errorf("named units it has no rows for: %+v", f.Detail)
	}
}

// Never more names than the count claims: a row reading "1 failed unit" beside
// two names contradicts itself.
func TestIntegrationFailedUnitsNeverNameMoreThanTheCount(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-units-cap")
	now := time.Now().UTC()

	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_current (host_id, last_seen, services_failed)
		VALUES ($1, $2, 1)`, host, now); err != nil {
		t.Fatalf("host_current: %v", err)
	}
	for _, name := range []string{"a.service", "b.service"} {
		if _, err := s.Pool().Exec(ctx, `
			INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
			VALUES ($1, $2, 'failed', 'failed', $3)`, host, name, now); err != nil {
			t.Fatalf("insert unit: %v", err)
		}
	}

	scan, err := s.ScanConditions(ctx, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	f := scan.Bad[conditions.Key{HostID: host, Kind: conditions.KindFailedUnits}]
	names, _ := f.Detail["units"].([]string)
	if len(names) != 1 {
		t.Errorf("units = %v, want 1 to match the count", names)
	}
}

// A mount whose reading is stale is still MOUNTED.
//
// The agent's statfs backoff skips a wedged mountpoint for exponentially many
// scrapes while the host keeps posting, so a stale reading is routine. Dropping
// it from Seen would make Diff read it as vanished -- which skips the
// hysteresis, resolves on that one tick, and reopens later with a fresh onset,
// destroying the very thing this table exists to keep.
func TestIntegrationAWedgedMountIsStillSeen(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-wedged")
	now := time.Now().UTC()

	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_current (host_id, last_seen) VALUES ($1, $2)`, host, now); err != nil {
		t.Fatalf("host_current: %v", err)
	}
	var fsID int32
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO filesystems (host_id, label, mountpoint)
		VALUES ($1, 'wedged', '/mnt/wedged') RETURNING id`, host).Scan(&fsID); err != nil {
		t.Fatalf("filesystems: %v", err)
	}
	// The host posted a minute ago; this mount was last measured an hour ago.
	gb := int64(1024) * 1024 * 1024
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO filesystem_current (host_id, fs_id, ts, total, used, free)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		host, fsID, now.Add(-time.Hour), 100*gb, 97*gb, 3*gb); err != nil {
		t.Fatalf("filesystem_current: %v", err)
	}

	scan, err := s.ScanConditions(ctx, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	k := diskKey(host, "wedged")
	if !scan.Seen[k] {
		t.Error("a wedged mount fell out of Seen, so it would resolve as vanished")
	}
	// Present, but not judged on an hour-old reading.
	if _, bad := scan.Bad[k]; bad {
		t.Error("a stale reading was judged as if it were current")
	}
}

// The disk onset is the READING's timestamp, marked as a floor, rather than
// the tick that noticed. A host off for a month must not have its disk
// recorded as having filled up the moment the hub looked.
func TestIntegrationDiskOnsetIsTheReadingNotTheTick(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "cond-onset")
	now := time.Now().UTC()
	reading := now.Add(-2 * time.Minute)

	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_current (host_id, last_seen) VALUES ($1, $2)`, host, now); err != nil {
		t.Fatalf("host_current: %v", err)
	}
	var fsID int32
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO filesystems (host_id, label, mountpoint)
		VALUES ($1, 'root', '/') RETURNING id`, host).Scan(&fsID); err != nil {
		t.Fatalf("filesystems: %v", err)
	}
	gb := int64(1024) * 1024 * 1024
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO filesystem_current (host_id, fs_id, ts, total, used, free)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		host, fsID, reading, 100*gb, 97*gb, 3*gb); err != nil {
		t.Fatalf("filesystem_current: %v", err)
	}

	scan, err := s.ScanConditions(ctx, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	f, bad := scan.Bad[diskKey(host, "root")]
	if !bad {
		t.Fatal("the full disk was not flagged")
	}
	if !f.OpenedTS.UTC().Equal(reading.Truncate(time.Microsecond)) {
		t.Errorf("opened_ts = %v, want the reading's own %v", f.OpenedTS.UTC(), reading)
	}
	if !f.OpenedAtLeast {
		t.Error("the onset is a floor and must say so")
	}
}

func ptrFinding(f conditions.Finding) *conditions.Finding { return &f }
