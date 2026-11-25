package notify

import (
	"fmt"
	"slices"
	"strings"

	tknotify "github.com/trick77/toolkit/notify"
)

// urlHints say what the URL of a supported service most likely got wrong:
// the rules shoutrrr v0.8 did not check at startup, so a URL that worked
// before the upgrade stops the hub with only "invalid URL".
var urlHints = map[string]string{
	"smtp":   "from= and to= need full addresses (name@host)",
	"ntfy":   "a topic is up to 64 letters, digits, _ and -",
	"gotify": "the token is an application token (A and 14 characters, or gtfya.…)",
}

// URLError words NewChannels' error for BACKEND_NOTIFY_URLS: the list of
// services when the failing URL names another one, a hint when its service
// has one. Never the URL itself: it carries credentials.
func URLError(urls []string, err error) error {
	scheme := ""
	// NewChannels stops at the first bad URL, so the first one that fails
	// alone is the one err names.
	for _, u := range urls {
		if _, e := tknotify.NewChannels([]string{u}); e != nil {
			scheme = tknotify.Scheme(u)
			break
		}
	}
	base, _, _ := strings.Cut(scheme, "+")
	switch {
	case scheme != "" && !slices.Contains(tknotify.Schemes(), base):
		return fmt.Errorf("BACKEND_NOTIFY_URLS %w; supported: %s", err, strings.Join(tknotify.Schemes(), ", "))
	case urlHints[base] != "":
		return fmt.Errorf("BACKEND_NOTIFY_URLS %w: %s", err, urlHints[base])
	}
	return fmt.Errorf("BACKEND_NOTIFY_URLS %w", err)
}
