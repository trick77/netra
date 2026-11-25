package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/toolkit/notify"

	"github.com/trick77/netra/internal/hub/httpapi"
)

type delivery struct {
	Channel string `json:"channel"`
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
}

// notifyAPI serves the admin handler with these channels and no database:
// the config and test routes read neither store.
func notifyAPI(t *testing.T, channels httpapi.NotifyChannels) *httptest.Server {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	srv := httptest.NewServer(httpapi.NewAdminHandler(nil, nil, now, "https://netra.example.com", channels))
	t.Cleanup(srv.Close)
	return srv
}

func postEmpty(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func TestNotifyTestOff(t *testing.T) {
	srv := notifyAPI(t, nil)

	code, body := postEmpty(t, srv.URL+"/api/v1/notify/test")
	if code != http.StatusConflict || !strings.Contains(string(body), "BACKEND_NOTIFY_URLS is empty") {
		t.Errorf("off: %d %s, want 409 naming the variable", code, body)
	}

	resp, err := http.Get(srv.URL + "/api/v1/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cfg struct {
		HubURL         string   `json:"hub_url"`
		NotifyChannels []string `json:"notify_channels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HubURL != "https://netra.example.com" || cfg.NotifyChannels == nil || len(cfg.NotifyChannels) != 0 {
		t.Errorf("config = %+v, want the hub URL and an empty (not null) channel list", cfg)
	}
}

func TestNotifyTestReportsEachChannel(t *testing.T) {
	var got string
	ok := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	}))
	defer ok.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer down.Close()

	channels, err := notify.NewChannels([]string{"generic+" + ok.URL + "/hook", "generic+" + down.URL + "/hook"})
	if err != nil {
		t.Fatal(err)
	}
	srv := notifyAPI(t, channels)

	code, body := postEmpty(t, srv.URL+"/api/v1/notify/test")
	if code != http.StatusOK {
		t.Fatalf("test: %d %s, want 200 even with a channel down", code, body)
	}
	var out []delivery
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 ||
		out[0] != (delivery{Channel: "channel 1 (generic+http)", OK: true}) ||
		out[1].Channel != "channel 2 (generic+http)" || out[1].OK || out[1].Error == "" {
		t.Errorf("results = %+v, want channel 1 ok and channel 2 failed with a reason", out)
	}
	if !strings.Contains(got, "test notification from netra") || !strings.Contains(got, "https://netra.example.com") {
		t.Errorf("the message read %q", got)
	}

	resp, err := http.Get(srv.URL + "/api/v1/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, _ := io.ReadAll(resp.Body); !strings.Contains(string(b), `"notify_channels":["channel 1 (generic+http)","channel 2 (generic+http)"]`) {
		t.Errorf("config = %s, want both channel labels", b)
	}
}

// A second click while a slow channel holds the first is refused, not queued;
// once the first is answered the next one runs.
func TestNotifyTestOneAtATime(t *testing.T) {
	arrived := make(chan struct{}, 4)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		arrived <- struct{}{}
		<-release
	}))
	defer slow.Close()
	defer close(release)

	channels, err := notify.NewChannels([]string{"generic+" + slow.URL + "/hook"})
	if err != nil {
		t.Fatal(err)
	}
	srv := notifyAPI(t, channels)

	type answer struct {
		code int
		body []byte
		err  error
	}
	first := make(chan answer, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/api/v1/notify/test", "application/json", nil)
		if err != nil {
			first <- answer{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		first <- answer{resp.StatusCode, body, err}
	}()
	<-arrived

	code, body := postEmpty(t, srv.URL+"/api/v1/notify/test")
	if code != http.StatusConflict || !strings.Contains(string(body), "still being sent") {
		t.Errorf("second test while the first is out: %d %s, want 409", code, body)
	}

	release <- struct{}{}
	if a := <-first; a.err != nil || a.code != http.StatusOK || !strings.Contains(string(a.body), `"ok":true`) {
		t.Fatalf("first test: %d %s %v, want 200 ok", a.code, a.body, a.err)
	}
	go func() { <-arrived; release <- struct{}{} }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body = postEmpty(t, srv.URL+"/api/v1/notify/test")
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still refused after the first was answered: %d %s", code, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A channel that never answers is cancelled at the timeout: the server sees
// the request go away, and the next test runs.
func TestNotifyTestTimeoutCancelsTheSend(t *testing.T) {
	cancelled := make(chan struct{}, 4)
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// The server notices a client leaving only once the body is read.
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
			cancelled <- struct{}{}
		case <-time.After(10 * time.Second):
		}
	}))
	defer hang.Close()

	channels, err := notify.NewChannels([]string{"generic+" + hang.URL + "/hook"})
	if err != nil {
		t.Fatal(err)
	}
	channels.SetTimeout(100 * time.Millisecond)
	srv := notifyAPI(t, channels)

	code, body := postEmpty(t, srv.URL+"/api/v1/notify/test")
	if code != http.StatusOK || !strings.Contains(string(body), "timed out after 100ms") {
		t.Fatalf("first test: %d %s, want 200 reporting the timeout", code, body)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the timed-out send was never cancelled")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := postEmpty(t, srv.URL+"/api/v1/notify/test")
		if code == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still locked after the send was cancelled: %d %s", code, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// stuckChannels answers at once and never reports its sends returned: a
// service that ignores its cancellation.
type stuckChannels struct{}

func (stuckChannels) Labels() []string       { return []string{"channel 1 (stuck)"} }
func (stuckChannels) Timeout() time.Duration { return 250 * time.Millisecond }
func (stuckChannels) Deliver(context.Context, string, string) ([]notify.Delivery, <-chan struct{}) {
	return []notify.Delivery{{Channel: "channel 1 (stuck)"}}, make(chan struct{})
}

// A send that never returns does not lock the button until a restart: past
// the cap the next test runs.
func TestNotifyTestStuckSendReleasesAtCap(t *testing.T) {
	srv := notifyAPI(t, stuckChannels{})
	if code, body := postEmpty(t, srv.URL+"/api/v1/notify/test"); code != http.StatusOK {
		t.Fatalf("first test: %d %s", code, body)
	}
	if code, _ := postEmpty(t, srv.URL+"/api/v1/notify/test"); code != http.StatusConflict {
		t.Errorf("second test before the cap: %d, want 409", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := postEmpty(t, srv.URL+"/api/v1/notify/test")
		if code == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still locked long past the cap: %d %s", code, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A nil *Channels in the interface is off, not a panic.
func TestNotifyTestTypedNilIsOff(t *testing.T) {
	var none *notify.Channels
	srv := notifyAPI(t, none)
	code, body := postEmpty(t, srv.URL+"/api/v1/notify/test")
	if code != http.StatusConflict || !strings.Contains(string(body), "BACKEND_NOTIFY_URLS is empty") {
		t.Errorf("typed nil: %d %s, want 409 off", code, body)
	}
	resp, err := http.Get(srv.URL + "/api/v1/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, _ := io.ReadAll(resp.Body); !strings.Contains(string(b), `"notify_channels":[]`) ||
		!strings.Contains(string(b), `"notify_timeout_s":45`) {
		t.Errorf("config = %s", b)
	}
}
