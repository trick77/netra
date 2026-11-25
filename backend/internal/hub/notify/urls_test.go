package notify_test

import (
	"strings"
	"testing"

	tknotify "github.com/trick77/toolkit/notify"

	"github.com/trick77/netra/internal/hub/notify"
)

func TestURLError(t *testing.T) {
	good := "ntfy://ntfy.sh/netra"
	for _, tc := range []struct {
		url        string
		want, lack []string
	}{
		{"slack://hooks:s3cretTok@channel", []string{"channel 2 (slack): unsupported service", "supported: generic, gotify, ntfy, pushover, smtp, telegram"}, []string{"s3cretTok"}},
		{"smtp://u:s3cretPass@mail.example.com:587/?from=netra@example.com&to=root", []string{"channel 2 (smtp): invalid URL: from= and to= need full addresses"}, []string{"s3cretPass", "supported:"}},
		{"ntfy://ntfy.sh/my.topic", []string{"channel 2 (ntfy): invalid URL: a topic is up to 64"}, []string{"supported:"}},
		{"gotify://gotify.example.com/short", []string{"channel 2 (gotify): invalid URL: the token is an application token"}, nil},
		{"generic+http://example.com/hook#", []string{"BACKEND_NOTIFY_URLS channel 2 (generic+http): invalid URL"}, []string{"supported:", "): invalid URL:"}},
	} {
		urls := []string{good, tc.url}
		_, err := tknotify.NewChannels(urls)
		if err == nil {
			t.Fatalf("%s: accepted", tknotify.Scheme(tc.url))
		}
		msg := notify.URLError(urls, err).Error()
		for _, w := range tc.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%q lacks %q", msg, w)
			}
		}
		for _, l := range tc.lack {
			if strings.Contains(msg, l) {
				t.Errorf("%q carries %q", msg, l)
			}
		}
	}
}
