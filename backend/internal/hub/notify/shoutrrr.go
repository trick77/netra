package notify

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"sort"
	"strings"
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

// channel is one configured service and what is safe to say about it.
type channel struct {
	service types.Service
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
	c := &Channels{timeout: SendTimeout}
	for i, u := range urls {
		label := fmt.Sprintf("channel %d (%s)", i+1, scheme(u))
		service, err := r.Locate(u)
		if err != nil {
			return nil, fmt.Errorf("BACKEND_NOTIFY_URLS %s: invalid", label)
		}
		c.channels = append(c.channels, channel{service: service, label: label})
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
// partial failure comes back as a PartialError for the caller to log.
//
// Every error names its channel and is scrubbed of the URLs' credentials:
// shoutrrr wraps net/http errors that quote the request URL, and a Telegram
// or Discord token sits in that URL's path.
func (c *Channels) Send(title, body string) error {
	type result struct {
		i   int
		err error
	}
	// Buffered for every channel, so a send that outlives its timeout can
	// still hand over its result and end rather than block forever.
	results := make(chan result, len(c.channels))
	for i, ch := range c.channels {
		go func() {
			results <- result{i, ch.service.Send(body, &types.Params{"title": title})}
		}()
	}

	errs := make([]error, len(c.channels))
	answered := make([]bool, len(c.channels))
	timeout := time.After(c.timeout)
	for left := len(c.channels); left > 0; left-- {
		select {
		case r := <-results:
			answered[r.i] = true
			errs[r.i] = r.err
		case <-timeout:
			left = 0
		}
	}

	var failed []error
	for i, ch := range c.channels {
		switch {
		case !answered[i]:
			failed = append(failed, fmt.Errorf("%s: timed out after %s", ch.label, c.timeout))
		case errs[i] != nil:
			failed = append(failed, fmt.Errorf("%s: %s", ch.label, c.scrub(errs[i].Error())))
		}
	}
	if len(failed) == 0 {
		return nil
	}
	if len(failed) < len(c.channels) {
		return &PartialError{Err: errors.Join(failed...)}
	}
	return errors.Join(failed...)
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

// PartialError reports channels that failed while others delivered. The
// message counts as sent.
type PartialError struct{ Err error }

func (e *PartialError) Error() string { return "some channels failed: " + e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// scheme is the part of a channel URL that is safe to log.
func scheme(u string) string {
	s, _, ok := strings.Cut(u, "://")
	if !ok {
		return "no scheme"
	}
	return s
}
