package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
	"github.com/trick77/netra/internal/hub/notify"
	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

func pending(t *testing.T, s *store.Store, now time.Time, repeat time.Duration) []notify.Condition {
	t.Helper()
	got, err := s.PendingConditions(t.Context(), now, repeat)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	return got
}

func mark(t *testing.T, s *store.Store, items []notify.Condition, now time.Time) {
	t.Helper()
	if err := s.MarkNotified(t.Context(), items, now); err != nil {
		t.Fatalf("mark: %v", err)
	}
}

// One condition through its whole life: announced, escalated, reminded,
// cleared, and then nothing more.
func TestIntegrationNotifyConditionLifecycle(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(ctx, t, s, "web1")
	key := diskKey(host, "/var")
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.ApplyConditions(ctx, []conditions.Action{
		{Open: ptr(openFinding(key, conditions.SeverityWarning, now.Add(-time.Hour)))},
	}, now); err != nil {
		t.Fatal(err)
	}

	got := pending(t, s, now, 24*time.Hour)
	if len(got) != 1 || got[0].What != notify.Opened || got[0].Hostname != "web1" ||
		got[0].Severity != "warning" || got[0].Muted {
		t.Fatalf("after open = %+v", got)
	}
	if pct, _ := got[0].Detail["pct"].(float64); pct != 97 {
		t.Errorf("detail = %v", got[0].Detail)
	}
	mark(t, s, got, now)
	if got := pending(t, s, now, 24*time.Hour); len(got) != 0 {
		t.Fatalf("after mark = %+v", got)
	}

	// Worse: owed once.
	open, err := s.OpenConditions(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("open = %+v, %v", open, err)
	}
	escalate := func(sev, prev string) {
		t.Helper()
		if err := s.ApplyConditions(ctx, []conditions.Action{{Update: &conditions.Update{
			ID: open[0].ID, Key: key, Severity: sev, PrevSeverity: prev,
		}}}, now); err != nil {
			t.Fatal(err)
		}
	}
	escalate(conditions.SeverityCritical, conditions.SeverityWarning)
	got = pending(t, s, now, 24*time.Hour)
	if len(got) != 1 || got[0].What != notify.Escalated || got[0].Severity != "critical" {
		t.Fatalf("after escalate = %+v", got)
	}
	mark(t, s, got, now)

	// Easing off is not news, and rising back to what was told is not either.
	escalate(conditions.SeverityWarning, conditions.SeverityCritical)
	escalate(conditions.SeverityCritical, conditions.SeverityWarning)
	if got := pending(t, s, now, 24*time.Hour); len(got) != 0 {
		t.Fatalf("after ease and rise = %+v", got)
	}

	// A day on, a reminder; with reminders off, none.
	later := now.Add(25 * time.Hour)
	if got := pending(t, s, later, 0); len(got) != 0 {
		t.Fatalf("reminders off = %+v", got)
	}
	got = pending(t, s, later, 24*time.Hour)
	if len(got) != 1 || got[0].What != notify.Reminder {
		t.Fatalf("a day later = %+v", got)
	}
	mark(t, s, got, later)
	if got := pending(t, s, later.Add(time.Hour), 24*time.Hour); len(got) != 0 {
		t.Fatalf("an hour after the reminder = %+v", got)
	}

	// Cleared: one all-clear, then silence.
	if err := s.ApplyConditions(ctx, []conditions.Action{{Resolve: &conditions.Resolution{
		ID: open[0].ID, Key: key, Reason: conditions.ReasonCleared,
	}}}, later); err != nil {
		t.Fatal(err)
	}
	got = pending(t, s, later, 24*time.Hour)
	if len(got) != 1 || got[0].What != notify.Cleared || got[0].ResolvedTS == nil ||
		got[0].ResolvedReason != "cleared" {
		t.Fatalf("after clear = %+v", got)
	}
	mark(t, s, got, later)
	if got := pending(t, s, later.Add(48*time.Hour), 24*time.Hour); len(got) != 0 {
		t.Fatalf("after the all-clear = %+v", got)
	}
}

// A condition nobody was told about clears without an all-clear.
func TestIntegrationNotifyNoAllClearForTheUnannounced(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(ctx, t, s, "web1")
	key := diskKey(host, "/var")
	now := time.Now().UTC()

	if err := s.ApplyConditions(ctx, []conditions.Action{
		{Open: ptr(openFinding(key, conditions.SeverityCritical, now))},
	}, now); err != nil {
		t.Fatal(err)
	}
	open, _ := s.OpenConditions(ctx)
	if err := s.ApplyConditions(ctx, []conditions.Action{{Resolve: &conditions.Resolution{
		ID: open[0].ID, Key: key, Reason: conditions.ReasonVanished,
	}}}, now); err != nil {
		t.Fatal(err)
	}
	if got := pending(t, s, now, 24*time.Hour); len(got) != 0 {
		t.Fatalf("pending = %+v", got)
	}
}

func TestIntegrationNotifyMutes(t *testing.T) {
	ctx, s := condCtx(t)
	web1 := newHost(ctx, t, s, "web1")
	web2 := newHost(ctx, t, s, "web2")
	now := time.Now().UTC()

	if err := s.ApplyConditions(ctx, []conditions.Action{
		{Open: ptr(openFinding(diskKey(web1, "/var"), conditions.SeverityCritical, now))},
		{Open: ptr(openFinding(diskKey(web2, "/var"), conditions.SeverityCritical, now))},
	}, now); err != nil {
		t.Fatal(err)
	}

	mute := func(host int32, kind string, until *time.Time) {
		t.Helper()
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO alert_mutes (host_id, kind, until_ts) VALUES ($1, $2, $3)`,
			host, kind, until); err != nil {
			t.Fatal(err)
		}
	}
	expired := now.Add(-time.Minute)
	live := now.Add(time.Hour)
	mute(web1, "disk", &live)
	mute(web2, "", &expired)
	mute(web2, "load", nil)

	muted := map[int32]bool{}
	for _, c := range pending(t, s, now, 24*time.Hour) {
		muted[c.HostID] = c.Muted
	}
	if !muted[web1] || muted[web2] {
		t.Errorf("muted = %v, want web1 by kind and web2 not (expired, other kind)", muted)
	}

	// An event of a muted kind is muted too.
	mute(web2, "disk_error", nil)
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO events (host_id, ts, type, subject, detail, severity)
		VALUES ($1, $2, 'disk_error', 'sda', '{"message":"I/O error"}', 'critical')`,
		web2, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	events, err := s.CriticalEventsSince(ctx, now, now.Add(-notify.Freshness))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || !events[0].Muted {
		t.Errorf("events = %+v, want one muted", events)
	}

	if err := s.PruneMutes(ctx, now); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alert_mutes`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 3 {
		t.Errorf("%d mutes left after pruning, want 3 (the expired one gone)", left)
	}
}

func TestIntegrationCriticalEventsSince(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(ctx, t, s, "web1")
	now := time.Now().UTC().Truncate(time.Second)

	insert := func(ts time.Time, typ, severity, detail string) {
		t.Helper()
		if _, err := s.Pool().Exec(ctx, `
			INSERT INTO events (host_id, ts, type, subject, detail, severity)
			VALUES ($1, $2, $3, 'sda', $4::jsonb, $5)`, host, ts, typ, detail, severity); err != nil {
			t.Fatal(err)
		}
	}
	insert(now.Add(-time.Minute), "nvme_error", "critical", `{"message":"controller reset"}`)
	insert(now.Add(-2*time.Minute), "link_change", "warning", `{}`)
	insert(now.Add(-time.Hour), "disk_error", "critical", `{}`)
	// A condition transition is announced from the condition, not here.
	insert(now.Add(-time.Minute), "disk", "critical", `{"transition":"opened"}`)

	got, err := s.CriticalEventsSince(ctx, now, now.Add(-notify.Freshness))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != "nvme_error" || got[0].Subject != "sda" ||
		got[0].Hostname != "web1" || got[0].Detail["message"] != "controller reset" {
		t.Fatalf("got %+v", got)
	}
}

// A message's record lands in the events log and never reads back as a
// critical event to notify about, whatever its severity.
func TestIntegrationRecordNotification(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(ctx, t, s, "web1")
	now := time.Now().UTC().Truncate(time.Microsecond)

	rec := notify.Notification{
		Outcome: notify.Partial,
		Failed:  []string{"channel 2 (ntfy)"},
		Alerts: []notify.Alert{
			{Kind: "disk", Subject: "/var", What: notify.Opened, Severity: "critical"},
		},
	}
	if err := s.RecordNotification(ctx, host, now, "warning", rec); err != nil {
		t.Fatal(err)
	}
	var (
		typ, severity string
		subject       *string
		detail        []byte
	)
	if err := s.Pool().QueryRow(ctx, `
		SELECT type, subject, severity, detail FROM events WHERE host_id = $1`, host).
		Scan(&typ, &subject, &severity, &detail); err != nil {
		t.Fatal(err)
	}
	if typ != notify.EventType || subject != nil || severity != "warning" ||
		!strings.Contains(string(detail), `"failed": ["channel 2 (ntfy)"]`) ||
		!strings.Contains(string(detail), `"what": "opened"`) {
		t.Errorf("row = %s %v %s %s", typ, subject, severity, detail)
	}

	// Even a record written as critical is not news.
	if err := s.RecordNotification(ctx, host, now.Add(time.Second), "critical", rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.CriticalEventsSince(ctx, now.Add(time.Minute), now.Add(-notify.Freshness))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a notification record reads back as news: %+v", got)
	}
}

// An agent cannot write a row that reads as the hub's record of a message.
func TestIntegrationAgentCannotForgeNotification(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(ctx, t, s, "web1")
	sev := "critical"
	ts := time.Now().UnixMilli()
	n, err := s.InsertEvents(ctx, host, []*netrav1.Event{
		{TsMs: ts, Type: notify.EventType, Severity: &sev, DetailJson: `{"outcome":"sent"}`},
		{TsMs: ts, Type: "nvme_error", Subject: "nvme0", Severity: &sev},
	})
	if err != nil {
		t.Fatal(err)
	}
	var forged int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1 AND type = $2`, host, notify.EventType).
		Scan(&forged); err != nil {
		t.Fatal(err)
	}
	if n != 1 || forged != 0 {
		t.Errorf("inserted %d rows, %d of them notifications; want the nvme row alone", n, forged)
	}
}
