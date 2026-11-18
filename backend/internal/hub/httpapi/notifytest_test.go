package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/httpapi"
	"github.com/trick77/netra/internal/hub/notify"
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

// A second click while a slow channel holds the first is refused, not queued.
//
// Refused even after the first has answered "timed out": the abandoned send
// may still deliver, and stacking another behind it only adds a duplicate.
func TestNotifyTestOneAtATime(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer hang.Close()

	channels, err := notify.NewChannels([]string{"generic+" + hang.URL + "/hook"})
	if err != nil {
		t.Fatal(err)
	}
	channels.SetTimeoutForTest(200 * time.Millisecond)
	srv := notifyAPI(t, channels)

	code, body := postEmpty(t, srv.URL+"/api/v1/notify/test")
	if code != http.StatusOK || !strings.Contains(string(body), "timed out after 200ms") {
		t.Fatalf("first test: %d %s, want 200 reporting the timeout", code, body)
	}
	code, body = postEmpty(t, srv.URL+"/api/v1/notify/test")
	if code != http.StatusConflict || !strings.Contains(string(body), "still being sent") {
		t.Errorf("second test while the first send hangs: %d %s, want 409", code, body)
	}

	// The stuck send returns: the next test runs.
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body = postEmpty(t, srv.URL+"/api/v1/notify/test")
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still refused after the send returned: %d %s", code, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A channel that never answers does not lock the button until a restart:
// past the cap the next test runs (and reports the same timeout).
func TestNotifyTestStuckSendReleasesAtCap(t *testing.T) {
	stuck := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-stuck
	}))
	// Cleanups run last-first: release the handlers, then close the server,
	// which waits for them.
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(stuck) })

	channels, err := notify.NewChannels([]string{"generic+" + hang.URL + "/hook"})
	if err != nil {
		t.Fatal(err)
	}
	channels.SetTimeoutForTest(100 * time.Millisecond)
	srv := notifyAPI(t, channels)

	if code, body := postEmpty(t, srv.URL+"/api/v1/notify/test"); code != http.StatusOK {
		t.Fatalf("first test: %d %s", code, body)
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
		time.Sleep(50 * time.Millisecond)
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
