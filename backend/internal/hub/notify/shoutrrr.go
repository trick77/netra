package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/containrrr/shoutrrr/pkg/router"
	"github.com/containrrr/shoutrrr/pkg/types"
)

// SendTimeout is how long one channel may take before the send counts as
// failed and is retried next tick.
//
// A timeout abandons the send rather than cancelling it (shoutrrr's services
// take no context), so a server slow to accept DATA may still deliver a copy
// the notifier counted as failed. Long enough for a sluggish mail server,
// short enough that a pass stays near its minute.
const SendTimeout = 45 * time.Second

// channel is one configured URL and what is safe to say about it.
//
// The URL, not a shoutrrr service built from it once: several services (ntfy,
// gotify, pushover, bark, mattermost) write the message's params into their
// own config inside Send, so two sends through one service object race, and
// one message can go out under the other's title. The notifier sends hosts in
// parallel and the test button sends beside it; a service per send has no
// state to share.
type channel struct {
	url string
	// label is "channel 2 (smtp)": the position and the scheme, never more.
	label string
}

// Channels sends through shoutrrr, one URL per channel.
//
// Not shoutrrr's router.Send: its timeout hands the result to an unbuffered
// channel nobody reads any more, so every timed-out send leaks a goroutine
// for good, and its results come back in completion order, so a failure
// cannot be tied to the channel that failed.
type Channels struct {
	channels []channel
	// secrets are the credential-bearing parts of every URL, scrubbed from
	// any error before it can reach a log line.
	secrets []string
	timeout time.Duration
	// router builds a service per send; locate serialises it, as nothing in
	// shoutrrr promises it is safe for concurrent use.
	router *router.ServiceRouter
	locate sync.Mutex
}

// NewChannels parses the channel URLs. A bad URL is an error at startup, not
// a silence at the first problem.
//
// The error never quotes the URL: it carries credentials (an SMTP password, a
// bot token), and this error is logged.
func NewChannels(urls []string) (*Channels, error) {
	r, err := router.New(log.New(io.Discard, "", 0))
	if err != nil {
		return nil, err
	}
	c := &Channels{timeout: SendTimeout, router: r}
	for i, u := range urls {
		label := fmt.Sprintf("channel %d (%s)", i+1, scheme(u))
		if _, err := r.Locate(u); err != nil {
			return nil, fmt.Errorf("BACKEND_NOTIFY_URLS %s: invalid", label)
		}
		c.channels = append(c.channels, channel{url: u, label: label})
		c.secrets = append(c.secrets, secretsOf(u)...)
	}
	// Longest first, so a token containing a shorter secret is scrubbed
	// whole rather than leaving its tail behind.
	sort.Slice(c.secrets, func(i, j int) bool { return len(c.secrets[i]) > len(c.secrets[j]) })
	return c, nil
}

// SetTimeoutForTest shortens SendTimeout.
func (c *Channels) SetTimeoutForTest(d time.Duration) { c.timeout = d }

// Send delivers to every channel at once. It succeeds when at least one
// channel accepted the message: retrying the whole message because one of
// three channels is down would repeat it on the other two every minute. A
// failure comes back as a SendError naming the channels that failed.
//
// Every error names its channel and is scrubbed of the URLs' credentials:
// shoutrrr wraps net/http errors that quote the request URL, and a Telegram
// or Discord token sits in that URL's path.
func (c *Channels) Send(title, body string) error {
	var (
		failed []error
		labels []string
	)
	deliveries, _ := c.Deliver(context.Background(), title, body)
	for _, d := range deliveries {
		if d.Err != nil {
			// %v, not %w: what reaches a log is the scrubbed text, never an
			// error a caller could unwrap back to shoutrrr's own.
			failed = append(failed, fmt.Errorf("%s: %s", d.Channel, d.Err.Error()))
			labels = append(labels, d.Channel)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return &SendError{
		Failed:  labels,
		Partial: len(failed) < len(c.channels),
		Err:     errors.Join(failed...),
	}
}

// Delivery is what became of a message on one channel.
type Delivery struct {
	// Channel is the label, "channel 2 (smtp)".
	Channel string
	// Err is nil when the channel accepted the message. Scrubbed.
	Err error
}

// Labels names the channels, in configuration order. Nil-safe: a nil
// *Channels has none.
func (c *Channels) Labels() []string {
	if c == nil {
		return []string{}
	}
	out := make([]string, len(c.channels))
	for i, ch := range c.channels {
		out[i] = ch.label
	}
	return out
}

// Timeout is how long Deliver waits for the channels, together.
func (c *Channels) Timeout() time.Duration {
	if c == nil {
		return SendTimeout
	}
	return c.timeout
}

// Deliver sends to every channel at once and reports each, in configuration
// order: Send's verdict for the notifier, and the test button's answer.
//
// It returns after Timeout or when ctx ends, whichever is first; a channel
// still sending then is reported as timed out. done closes once every send
// has really returned: a send that was given up on is abandoned, not
// cancelled (shoutrrr takes no context), and may still deliver.
func (c *Channels) Deliver(ctx context.Context, title, body string) (out []Delivery, done <-chan struct{}) {
	finished := make(chan struct{})
	if c == nil || len(c.channels) == 0 {
		close(finished)
		return []Delivery{}, finished
	}

	type result struct {
		i   int
		err error
	}
	// Buffered for every channel, so a send that outlives its timeout can
	// still hand over its result and end rather than block forever.
	results := make(chan result, len(c.channels))
	var wg sync.WaitGroup
	for i, ch := range c.channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- result{i, c.sendOne(ch, title, body)}
		}()
	}
	go func() {
		wg.Wait()
		close(finished)
	}()

	errs := make([]error, len(c.channels))
	answered := make([]bool, len(c.channels))
	timeout := time.NewTimer(c.timeout)
	defer timeout.Stop()
	stopped := ""
wait:
	for left := len(c.channels); left > 0; left-- {
		select {
		case r := <-results:
			answered[r.i] = true
			errs[r.i] = r.err
		case <-timeout.C:
			stopped = fmt.Sprintf("timed out after %s", c.timeout)
			break wait
		case <-ctx.Done():
			stopped = "abandoned: " + ctx.Err().Error()
			break wait
		}
	}

	out = make([]Delivery, len(c.channels))
	for i, ch := range c.channels {
		out[i].Channel = ch.label
		switch {
		case !answered[i]:
			out[i].Err = errors.New(stopped)
		case errs[i] != nil:
			out[i].Err = errors.New(c.scrub(errs[i].Error()))
		}
	}
	return out, finished
}

// sendOne builds the channel's service afresh and sends through it. See
// channel for why it is not built once. The router is shared; Locate is
// serialised on it, the sends are not.
func (c *Channels) sendOne(ch channel, title, body string) error {
	c.locate.Lock()
	service, err := c.router.Locate(ch.url)
	c.locate.Unlock()
	if err != nil {
		// Parsed at startup, so this is not a bad URL; and its error is
		// never quoted, for the reason NewChannels gives.
		return errors.New("could not set up the channel")
	}
	return service.Send(body, &types.Params{"title": title})
}

func (c *Channels) scrub(s string) string {
	for _, secret := range c.secrets {
		s = strings.ReplaceAll(s, secret, "***")
		// net/http quotes the URL escaped, so scrub that spelling too.
		if esc := url.PathEscape(secret); esc != secret {
			s = strings.ReplaceAll(s, esc, "***")
		}
	}
	return s
}

// secretsOf is every part of a channel URL that may be a credential: the
// user and password, every path segment and every query value. The host is
// left alone -- it is not a secret and it is what makes an error readable.
// Short parts (a port, "yes") are left too: scrubbing them would mangle the
// message and they protect nothing.
func secretsOf(raw string) []string {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	var out []string
	add := func(s string) {
		if len(s) >= 6 {
			out = append(out, s)
		}
	}
	if u.User != nil {
		add(u.User.Username())
		if p, ok := u.User.Password(); ok {
			add(p)
		}
	}
	for _, seg := range strings.Split(u.Path, "/") {
		add(seg)
	}
	for _, vs := range u.Query() {
		for _, v := range vs {
			add(v)
			for _, part := range strings.Split(v, ",") {
				add(part)
			}
		}
	}
	return out
}

// SendError reports the channels a message failed on. Partial says another
// channel delivered it, and the message then counts as sent.
type SendError struct {
	// Failed are the failing channels' labels, safe to store.
	Failed  []string
	Partial bool
	Err     error
}

func (e *SendError) Error() string {
	if e.Partial {
		return "some channels failed: " + e.Err.Error()
	}
	return e.Err.Error()
}

func (e *SendError) Unwrap() error { return e.Err }

// scheme is the part of a channel URL that is safe to log.
func scheme(u string) string {
	s, _, ok := strings.Cut(u, "://")
	if !ok {
		return "no scheme"
	}
	return s
}
