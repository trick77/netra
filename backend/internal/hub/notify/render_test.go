package notify_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

	if title != "[netra] web1: 7 notifications" {
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

func TestChannelsRejectBadURLWithoutQuotingIt(t *testing.T) {
	_, err := notify.NewChannels([]string{"smtp://user:s3cret@mail:587/?to=a@b&from=c@d", "nope://x:topsecret@y"})
	if err == nil {
		t.Fatal("a bad URL was accepted")
	}
	if strings.Contains(err.Error(), "topsecret") {
		t.Errorf("the error leaks a credential: %v", err)
	}
	if !strings.Contains(err.Error(), "channel 2 (nope)") {
		t.Errorf("the error does not say which entry: %v", err)
	}
	if _, err := notify.NewChannels([]string{"no-scheme"}); err == nil || !strings.Contains(err.Error(), "no scheme") {
		t.Errorf("a schemeless entry: %v", err)
	}
}

func TestChannelsSend(t *testing.T) {
	var hits atomic.Int32
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()

	hook := func(s *httptest.Server) string {
		return "generic+" + s.URL + "/hook"
	}

	all, err := notify.NewChannels([]string{hook(ok)})
	if err != nil {
		t.Fatal(err)
	}
	if err := all.Send("t", "b"); err != nil {
		t.Errorf("send: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("webhook hit %d times", hits.Load())
	}

	some, err := notify.NewChannels([]string{hook(ok), hook(down)})
	if err != nil {
		t.Fatal(err)
	}
	err = some.Send("t", "b")
	var sendErr *notify.SendError
	if !errors.As(err, &sendErr) || !sendErr.Partial ||
		len(sendErr.Failed) != 1 || sendErr.Failed[0] != "channel 2 (generic+http)" {
		t.Errorf("one of two failing: %v, want a partial SendError naming channel 2", err)
	}

	if err != nil && !strings.Contains(err.Error(), "channel 2 (generic+http)") {
		t.Errorf("the failure does not name its channel: %v", err)
	}

	none, err := notify.NewChannels([]string{hook(down)})
	if err != nil {
		t.Fatal(err)
	}
	err = none.Send("t", "b")
	if !errors.As(err, &sendErr) || sendErr.Partial || len(sendErr.Failed) != 1 {
		t.Errorf("all failing: %v, want a SendError that is not partial", err)
	}
}

// A channel that never answers is a timeout, not a goroutine stuck forever,
// and the error that reaches the log carries no token from the URL.
func TestChannelsTimeoutAndScrub(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer hang.Close()
	defer close(release)

	slow, err := notify.NewChannels([]string{"generic+" + hang.URL + "/hook"})
	if err != nil {
		t.Fatal(err)
	}
	slow.SetTimeoutForTest(100 * time.Millisecond)
	start := time.Now()
	err = slow.Send("t", "b")
	if err == nil || !strings.Contains(err.Error(), "channel 1 (generic+http): timed out") {
		t.Errorf("hanging channel: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Send waited %v on a hanging channel", d)
	}

	// Nothing listens here: the transport error quotes the URL, token and all.
	gone := httptest.NewServer(http.NotFoundHandler())
	addr := gone.URL
	gone.Close()
	leaky, err := notify.NewChannels([]string{"generic+" + addr + "/bot123456:SECRETTOKEN/send?apikey=hunter2hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	err = leaky.Send("t", "b")
	if err == nil {
		t.Fatal("a closed port accepted the message")
	}
	for _, secret := range []string{"SECRETTOKEN", "hunter2hunter2"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error leaks %q: %v", secret, err)
		}
	}
}
