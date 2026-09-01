package collector

import (
	"encoding/json"
	"testing"
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
func TestDockerInspectDecodesRestartCount(t *testing.T) {
	// Given a trimmed real /containers/{id}/json body, with sibling keys netra
	// does not read left in so the fixture stays a subset of the real shape.
	const body = `{
	  "Id": "8dfafdbc3a40",
	  "Created": "2026-08-01T09:12:44.1Z",
	  "RestartCount": 7,
	  "State": { "Status": "running", "Restarting": false },
	  "HostConfig": { "RestartPolicy": { "Name": "unless-stopped" } }
	}`
	var got dockerInspect

	// When it is decoded the way SystemDockerInspect decodes it.
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Then the restart count arrives.
	if got.RestartCount != 7 {
		t.Errorf("RestartCount = %d, want 7", got.RestartCount)
	}
}
