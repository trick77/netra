package notify_test

import (
	"strings"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/notify"
)

func TestRenderLines(t *testing.T) {
	resolved := t0.Add(-10 * time.Minute)
	conds := []notify.Condition{
		{Kind: "disk", Subject: "/var", Severity: "warning", OpenedTS: t0.Add(-time.Hour),
			What: notify.Opened, Detail: map[string]any{"pct": 91.2}},
		{Kind: "failed-units", Severity: "critical", OpenedTS: t0.Add(-50 * time.Hour),
			What: notify.Reminder, Detail: map[string]any{"units": []any{"nginx.service", "cron.service"}}},
		{Kind: "silent", Severity: "critical", OpenedTS: t0.Add(-2*time.Hour - 5*time.Minute),
			What: notify.Cleared, ResolvedTS: &resolved, ResolvedReason: "cleared"},
		{Kind: "drive", Subject: "sdb", Severity: "critical", OpenedTS: t0,
			What: notify.Cleared, ResolvedReason: "vanished", Detail: map[string]any{"text": "reallocated sectors"}},
		{Kind: "load", Severity: "critical", OpenedTS: t0, What: notify.Escalated,
			Detail: map[string]any{"value": 12.5, "normal": 3.0}},
		{Kind: "custom", Severity: "warning", OpenedTS: t0, What: notify.Opened},
	}
	events := []notify.Event{{Type: "oom_kill", Severity: "critical", TS: t0,
		Detail: map[string]any{"message": strings.Repeat("x", 400)}}}

	title, body := notify.Render("web1", 7, conds, events, "", t0)

	if title != "[Netra] web1: 7 notifications" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{
		"OPEN warning: Filesystem nearly full /var (91% used), since 2026-11-18 11:00 UTC",
		"STILL OPEN critical: Failed units (nginx.service, cron.service), open for 2 d 2 h",
		"CLEARED: Stopped reporting, after 1 h 55 m",
		"NO LONGER REPORTED: Drive errors sdb (reallocated sectors), after 0 s",
		"WORSE critical: Load above normal (12.5, normal under 3)",
		"OPEN warning: custom, since",
		"EVENT critical: oom_kill at 12:00:00 UTC: " + strings.Repeat("x", 300) + "…",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "/hosts/") {
		t.Errorf("no hub URL, yet a link: %q", body)
	}
}

// A load condition raised while md arrays were at work says so, whether the
// detail came straight from the scan ([]string) or back from JSON ([]any).
func TestRenderNamesTheArraysAtWorkBesideTheLoad(t *testing.T) {
	conds := []notify.Condition{
		{Kind: "load", Severity: "warning", OpenedTS: t0, What: notify.Opened,
			Detail: map[string]any{"value": 9.0, "normal": 1.5, "md_sync": []any{"md1 check", "md2 check"}}},
		{Kind: "load", Subject: "x", Severity: "critical", OpenedTS: t0, What: notify.Escalated,
			Detail: map[string]any{"md_sync": []string{"md0 resync"}}},
	}
	_, body := notify.Render("nas", 7, conds, nil, "", t0)
	for _, want := range []string{
		"Load above normal (9, normal under 1.5; md1 check, md2 check running)",
		"Load above normal x (md0 resync running)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

// Kernel text reaches the title, and the title is a mail header: a CR/LF in
// a process name must not start a new header line.
func TestRenderStripsControlCharacters(t *testing.T) {
	events := []notify.Event{{Type: "oom_kill", Subject: "x\r\nBcc: evil@example.com", Severity: "critical", TS: t0,
		Detail: map[string]any{"message": "Killed process 1 (x\r\nX-Foo: y)\x00"}}}
	title, body := notify.Render("web\n1", 1, nil, events, "", t0)
	if strings.ContainsAny(title, "\r\n\x00") {
		t.Errorf("title carries a control character: %q", title)
	}
	if strings.Contains(body, "\r") || strings.Contains(body, "\x00") || strings.Contains(body, "\nBcc:") {
		t.Errorf("body carries a control character: %q", body)
	}
}

// An onset that is only a floor is said as one.
func TestRenderOpenedAtLeast(t *testing.T) {
	c := notify.Condition{Kind: "disk", Subject: "/var", Severity: "warning",
		OpenedTS: t0.Add(-7 * 24 * time.Hour), OpenedAtLeast: true, What: notify.Opened}
	title, _ := notify.Render("h", 1, []notify.Condition{c}, nil, "", t0)
	if !strings.Contains(title, "since before 2026-11-11 12:00 UTC") {
		t.Errorf("opened = %q", title)
	}
	c.What = notify.Reminder
	title, _ = notify.Render("h", 1, []notify.Condition{c}, nil, "", t0)
	if !strings.Contains(title, "open for over 7 d 0 h") {
		t.Errorf("reminder = %q", title)
	}
}

func TestMutableKind(t *testing.T) {
	for kind, want := range map[string]bool{
		"": true, "disk": true, "failed-units": true, "disk_error": true, "mdraid": true,
		"disks": false, "DISK": false,
	} {
		if got := notify.MutableKind(kind); got != want {
			t.Errorf("MutableKind(%q) = %v, want %v", kind, got, want)
		}
	}
}

func TestRenderFailedUnitsCountOnly(t *testing.T) {
	title, _ := notify.Render("h", 1, []notify.Condition{{Kind: "failed-units", Severity: "warning",
		OpenedTS: t0, What: notify.Opened, Detail: map[string]any{"count": 3.0}}}, nil, "", t0)
	if !strings.Contains(title, "(3 failed)") {
		t.Errorf("title = %q", title)
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second:                 "0 s",
		30 * time.Second:             "30 s",
		45 * time.Minute:             "45 m",
		3*time.Hour + 12*time.Minute: "3 h 12 m",
		52 * time.Hour:               "2 d 4 h",
	} {
		if got := notify.Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}
