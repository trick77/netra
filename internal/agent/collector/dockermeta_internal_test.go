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
