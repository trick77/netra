package collector

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The decode is worth pinning on its own because nothing else can catch it
// getting this wrong. dockerContainer names its fields by JSON tag, and a wrong
// or missing tag does not fail to compile and does not fail to decode -- it
// yields the zero value, which containerNet reads as "the socket did not
// answer" and quietly falls back to the namespace comparison. Every host would
// then report container_network rather than traffic, for a one-word typo.
//
// The body is a trimmed real /containers/json response (API v1.41), keeping the
// nesting that matters: NetworkMode lives under HostConfig, not at the top
// level, and the sibling keys netra does NOT read are left in so the fixture
// stays a subset of the real shape rather than a restatement of the struct.
const containersJSONFixture = `[
  {
    "Id": "8dfafdbc3a40",
    "Names": ["/boring_feynman"],
    "Image": "nginx:1.27",
    "ImageID": "sha256:d0e5d",
    "Command": "nginx -g 'daemon off;'",
    "Created": 1367854155,
    "State": "running",
    "Status": "Up 4 days",
    "Labels": {
      "com.docker.compose.project": "shop",
      "com.docker.compose.service": "web",
      "traefik.enable": "true"
    },
    "HostConfig": { "NetworkMode": "shop_default" },
    "NetworkSettings": { "Networks": {} }
  },
  {
    "Id": "9cd87474be90",
    "Names": ["/netra-agent-1"],
    "Image": "ghcr.io/trick77/netra-agent:latest",
    "Labels": {},
    "HostConfig": { "NetworkMode": "host" }
  },
  {
    "Id": "3176a2479c92",
    "Names": ["/sidecar"],
    "Image": "busybox",
    "Labels": {},
    "HostConfig": { "NetworkMode": "container:8dfafdbc3a40" }
  }
]`

func TestDockerContainerDecodesNetworkMode(t *testing.T) {
	// Given a real /containers/json body.
	var got []dockerContainer

	// When it is decoded the way SystemDockerContainers decodes it.
	if err := json.Unmarshal([]byte(containersJSONFixture), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Then every NetworkMode arrives, including the two that mean "do not
	// measure this container's counters".
	if len(got) != 3 {
		t.Fatalf("decoded %d containers, want 3", len(got))
	}
	for i, want := range []string{"shop_default", "host", "container:8dfafdbc3a40"} {
		if got[i].HostConfig.NetworkMode != want {
			t.Errorf("container %d NetworkMode = %q, want %q",
				i, got[i].HostConfig.NetworkMode, want)
		}
	}

	// And the fields it has always read are untouched by the addition.
	if got[0].Image != "nginx:1.27" {
		t.Errorf("Image = %q, want nginx:1.27", got[0].Image)
	}
	if got[0].Labels["com.docker.compose.service"] != "web" {
		t.Errorf("compose service = %q, want web", got[0].Labels["com.docker.compose.service"])
	}
}

// sharesForeignNetNS is the whole policy in one predicate, so it is tested as
// one. "none" is the case most easily got wrong: a container with nothing but
// lo genuinely measures zero, which is a knowable answer rather than a missing
// one, so it must NOT be skipped.
func TestSharesForeignNetNS(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want bool
	}{
		{"host", true},
		{"container:8dfafdbc3a40", true},
		{"bridge", false},
		{"none", false},
		{"shop_default", false},
		// Not a mode Docker emits, but the empty string means "the socket did
		// not answer" everywhere else in this file, and it must never be read
		// here as an instruction to skip.
		{"", false},
	} {
		if got := sharesForeignNetNS(tc.mode); got != tc.want {
			t.Errorf("sharesForeignNetNS(%q) = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

// The health suffix is the only health /containers/json carries, and Docker
// writes Status for people rather than for parsers. Every case here is a real
// Status string, and the point of the table is the ones that must NOT be read
// as health: "(Paused)" is a state wearing the same parentheses, and a plain
// "Up 4 days" is an image with no HEALTHCHECK at all.
//
// HealthNone rather than the empty string for those, deliberately. Empty means
// "the socket did not answer" everywhere else in this package; here the agent
// looked and there was nothing to find, and the UI words the two differently.
func TestParseHealth(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{
		{"Up 2 hours (healthy)", HealthHealthy},
		{"Up 5 minutes (unhealthy)", HealthUnhealthy},
		{"Up 3 seconds (health: starting)", HealthStarting},
		{"Up 4 days", HealthNone},
		{"Up 2 hours (Paused)", HealthNone},
		{"Exited (0) 3 minutes ago", HealthNone},
		{"Restarting (1) 12 seconds ago", HealthNone},
		{"", HealthNone},
	} {
		if got := parseHealth(tc.status); got != tc.want {
			t.Errorf("parseHealth(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

// State and Status were in this response all along and were being decoded
// away, which is how the container page came to say state was "never read from
// Docker". A missing or misspelled tag yields the zero value rather than an
// error, so nothing but this test can catch it coming back.
func TestDockerContainerDecodesStateAndStatus(t *testing.T) {
	// Given the same real /containers/json body.
	var got []dockerContainer

	// When it is decoded.
	if err := json.Unmarshal([]byte(containersJSONFixture), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Then the daemon's own state and status arrive.
	if got[0].State != "running" {
		t.Errorf("State = %q, want running", got[0].State)
	}
	if got[0].Status != "Up 4 days" {
		t.Errorf("Status = %q, want %q", got[0].Status, "Up 4 days")
	}

	// And a container the fixture describes with neither reports neither,
	// rather than having "running" invented for it.
	if got[1].State != "" {
		t.Errorf("State = %q, want empty for a container the body did not describe", got[1].State)
	}
}

// The inspect body, pinned for the same reason the list body is: one wrong tag
// and every container reports zero restarts, which reads as a healthy fleet.
func TestDecodeInspectReadsBothFields(t *testing.T) {
	// Given a trimmed real /containers/{id}/json body, with sibling keys netra
	// does not read left in so the fixture stays a subset of the real shape.
	const body = `{
	  "Id": "8dfafdbc3a40",
	  "Created": "2026-08-01T09:12:44.1Z",
	  "RestartCount": 7,
	  "State": {
	    "Status": "running",
	    "Restarting": false,
	    "StartedAt": "2026-08-01T09:12:45.123456789Z"
	  },
	  "HostConfig": { "RestartPolicy": { "Name": "unless-stopped" } }
	}`

	// When it is decoded the way SystemDockerInspect decodes it.
	got, err := decodeInspect(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decodeInspect: %v", err)
	}

	// Then the restart count arrives.
	if got.RestartCount != 7 {
		t.Errorf("RestartCount = %d, want 7", got.RestartCount)
	}
	// And so does the start time, off the SAME response -- which is the whole
	// point: it costs no extra request, only a struct tag.
	if want := time.Date(2026, 8, 1, 9, 12, 45, 123456789, time.UTC); !got.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, want)
	}
}

// A body that is not JSON at all must fail rather than yield a zero status: a
// silent zero would report "this container has never restarted", which is the
// reading an operator most wants to be able to trust.
func TestDecodeInspectRejectsGarbage(t *testing.T) {
	if _, err := decodeInspect(strings.NewReader("<html>nope</html>")); err == nil {
		t.Error("decodeInspect accepted a non-JSON body")
	}
}

// The three ways there is no start time to be had. All must be the zero value
// and none may be an error: a container that has never run, an older daemon
// whose response has no State.StartedAt, and a value that will not parse.
// Docker's own zero time is the one that would otherwise slip through as a real
// instant and put the container's uptime at two thousand years.
func TestStartedAtIsZeroWhenThereIsNoneToRead(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"absent", ""},
		{"docker's zero time", "0001-01-01T00:00:00Z"},
		{"unparseable", "not a timestamp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseStartedAt(tc.in); !got.IsZero() {
				t.Errorf("parseStartedAt(%q) = %v, want the zero time", tc.in, got)
			}
		})
	}
}

// A response with no State object at all must still yield the restart count.
// The two fields fail together only when the daemon refuses the whole call --
// not when one key is missing.
func TestDockerInspectWithoutStateStillCarriesTheCount(t *testing.T) {
	got, err := decodeInspect(strings.NewReader(`{"RestartCount": 4}`))
	if err != nil {
		t.Fatalf("decodeInspect: %v", err)
	}
	if got.RestartCount != 4 {
		t.Errorf("RestartCount = %d, want 4", got.RestartCount)
	}
	if !got.StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, want the zero time", got.StartedAt)
	}
}
