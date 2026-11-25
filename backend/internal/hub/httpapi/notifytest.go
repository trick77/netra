package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/toolkit/notify"
)

// NotifyChannels is what the settings page needs from the notification
// channels: their names, how long a send may take, and a message sent
// straight through them. *notify.Channels; nil, or one with no channels,
// means notifications are off.
type NotifyChannels interface {
	Labels() []string
	Timeout() time.Duration
	Deliver(ctx context.Context, title, body string) ([]notify.Delivery, <-chan struct{})
}

// notifyOff is the answer when there is nothing to test. It names the
// variable and where it has to be, because "notifications are off" is a
// question about the hub's environment, not about this page.
const notifyOff = "notifications are off: BACKEND_NOTIFY_URLS is empty in the hub's environment"

// deliveryJSON is one channel's result. error is the scrubbed text, for an
// operator fixing a URL: admin-only, and never stored.
type deliveryJSON struct {
	Channel string `json:"channel"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// notifyTester sends the settings page's test message, one at a time.
type notifyTester struct {
	channels NotifyChannels
	hubURL   string
	now      func() time.Time
	// busy is held from the click until every send has really returned (or
	// holdCap), not only until the answer: a second click meanwhile would
	// stack another connection on a server that is already slow.
	busy sync.Mutex
}

// labels names the configured channels; empty when notifications are off.
func (t *notifyTester) labels() []string {
	if t.channels == nil {
		return []string{}
	}
	return t.channels.Labels()
}

// timeout is how long a test's answer may take.
func (t *notifyTester) timeout() time.Duration {
	if t.channels == nil {
		return notify.SendTimeout
	}
	return t.channels.Timeout()
}

// timeoutSeconds is that, for the page to say: rounded up, so the page never
// promises less than the wait.
func (t *notifyTester) timeoutSeconds() int {
	return int(math.Ceil(t.timeout().Seconds()))
}

// holdCap bounds how long sends still out keep the next test out.
func (t *notifyTester) holdCap() time.Duration {
	return 4 * t.timeout()
}

// test sends one message through every channel and answers each channel's
// result, 200 even when every one failed: the failures ARE the answer.
//
// It tests the channels, nothing else: no mute applies, no host is involved,
// and nothing is written to the events log. A client that goes away cancels
// the sends: nobody is left to read the answer. A hub shutting down does not
// cancel it: the drain waits for the answer, up to its own limit.
func (t *notifyTester) test(w http.ResponseWriter, r *http.Request) {
	if len(t.labels()) == 0 {
		httpx.WriteError(w, http.StatusConflict, notifyOff)
		return
	}
	if !t.busy.TryLock() {
		httpx.WriteError(w, http.StatusConflict,
			"a test notification is still being sent; try again when it has finished")
		return
	}

	body := fmt.Sprintf("A test notification from netra, sent %s from the settings page.\n"+
		"If you are reading this, the channel works.",
		t.now().UTC().Format("2006-01-02 15:04:05 UTC"))
	if t.hubURL != "" {
		body += "\n\n" + t.hubURL
	}
	deliveries, done := t.channels.Deliver(r.Context(), "[netra] test notification", body)
	// Released when the sends return, which is right after the timeout:
	// Deliver cancels what is still out. holdCap only guards a send that
	// ignores its cancellation (a service without a context): the button
	// must not stay locked until the hub restarts.
	go func() {
		select {
		case <-done:
		case <-time.After(t.holdCap()):
		}
		t.busy.Unlock()
	}()

	out := []deliveryJSON{}
	for _, d := range deliveries {
		res := deliveryJSON{Channel: d.Channel, OK: d.Err == nil}
		if d.Err != nil {
			res.Error = d.Err.Error()
			slog.Warn("notify: test failed", "channel", d.Channel, "err", d.Err)
		} else {
			slog.Info("notify: test sent", "channel", d.Channel)
		}
		out = append(out, res)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
