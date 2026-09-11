package collector

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
    "ImageID": "sha256:b0b0b",
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
	// ImageID is the join key into /images/json. A wrong tag here would make
	// pulledImage answer "pulled" for every container and silently switch
	// the local/ rewrite off fleet-wide.
	if got[0].ImageID != "sha256:d0e5d" {
		t.Errorf("ImageID = %q, want sha256:d0e5d", got[0].ImageID)
	}
	if got[1].ImageID != "" {
		t.Errorf("ImageID = %q for a container without one, want empty", got[1].ImageID)
	}
}

// A trimmed real /images/json body. The three shapes RepoDigests takes are
// the whole point: a populated list (pulled), an empty list and a JSON null,
// where the daemon writes null for a locally built image on some versions and
// [] on others, and both must read as "never pulled".
const imagesJSONFixture = `[
  {
    "Id": "sha256:d0e5d",
    "RepoTags": ["nginx:1.27"],
    "RepoDigests": ["nginx@sha256:aaaa"],
    "Size": 192000000
  },
  {
    "Id": "sha256:b1b1b",
    "RepoTags": ["shop-worker:latest"],
    "RepoDigests": [],
    "Size": 51000000
  },
  {
    "Id": "sha256:c2c2c",
    "RepoTags": ["shop-web:latest"],
    "RepoDigests": null,
    "Size": 51000000
  }
]`

func TestDecodeImages(t *testing.T) {
	// Given a real /images/json body.
	got, err := decodeImages(strings.NewReader(imagesJSONFixture))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Then only the image with a digest reads as pulled.
	want := map[string]bool{"sha256:d0e5d": true, "sha256:b1b1b": false, "sha256:c2c2c": false}
	if len(got) != len(want) {
		t.Fatalf("decoded %d images, want %d", len(got), len(want))
	}
	for id, pulled := range want {
		if got[id] != pulled {
			t.Errorf("pulled[%q] = %v, want %v", id, got[id], pulled)
		}
	}

	// And a body that is not the list shape is an error, not an empty map:
	// an empty map would read as "every image unknown", which pulledImage
	// turns into "pulled" -- the right outcome, but by accident.
	if _, err := decodeImages(strings.NewReader(`{"message":"page not found"}`)); err == nil {
		t.Error("decodeImages on an object body: want error, got nil")
	}
}

// Every way of not knowing answers "pulled", because pulled is the answer
// that leaves the image string alone. The one FALSE is an id the daemon
// listed with no digest.
func TestPulledImage(t *testing.T) {
	known := map[string]bool{"sha256:d0e5d": true, "sha256:b1b1b": false}
	for _, tc := range []struct {
		name   string
		pulled map[string]bool
		id     string
		want   bool
	}{
		{"listed with digest", known, "sha256:d0e5d", true},
		{"listed without digest", known, "sha256:b1b1b", false},
		{"id not in the list", known, "sha256:zzzz", true},
		{"daemon sent no ImageID", known, "", true},
		{"image list failed", nil, "sha256:b1b1b", true},
	} {
		if got := pulledImage(tc.pulled, tc.id); got != tc.want {
			t.Errorf("%s: pulledImage = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// fakeDaemon points the package's three request-building paths at an httptest
// server instead of the unix socket, and at a socket file that exists so the
// presence check passes. Restored by t.Cleanup.
//
// This is what makes the request path testable at all: everything from the
// socket check through the two GETs to the rows that come out is the part
// that used to need a real daemon.
func fakeDaemon(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	socket := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatalf("create fake socket: %v", err)
	}

	oldURL, oldClient, oldSocket := dockerBaseURL, dockerClient, dockerSocket
	dockerBaseURL, dockerClient, dockerSocket = srv.URL+"/"+dockerAPIVersion, srv.Client(), socket
	t.Cleanup(func() {
		dockerBaseURL, dockerClient, dockerSocket = oldURL, oldClient, oldSocket
	})
}

func TestSystemDockerContainers(t *testing.T) {
	// Given a daemon that answers both calls, and reports the third fixture
	// container's image as never pulled.
	fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + dockerAPIVersion + "/containers/json":
			_, _ = io.WriteString(w, containersJSONFixture)
		case "/" + dockerAPIVersion + "/images/json":
			_, _ = io.WriteString(w, `[
			  {"Id": "sha256:d0e5d", "RepoDigests": ["nginx@sha256:aaaa"]},
			  {"Id": "sha256:b0b0b", "RepoDigests": []}
			]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	// When the collector asks.
	got, err := SystemDockerContainers(t.Context())
	if err != nil {
		t.Fatalf("SystemDockerContainers: %v", err)
	}

	// Then the rows carry the daemon's own fields, with the leading slash off
	// the name, and only the unpulled image is rewritten.
	if len(got) != 3 {
		t.Fatalf("got %d containers, want 3", len(got))
	}
	if got[0].Name != "boring_feynman" || got[0].Image != "nginx:1.27" {
		t.Errorf("container 0 = %q/%q, want boring_feynman/nginx:1.27", got[0].Name, got[0].Image)
	}
	if got[0].Project != "shop" || got[0].Health != HealthNone {
		t.Errorf("container 0 project/health = %q/%q, want shop/%s", got[0].Project, got[0].Health, HealthNone)
	}
	if !got[1].IsAgent {
		t.Error("the netra-agent container did not report IsAgent")
	}
	// busybox: listed by the daemon with no digest, so it never came from a
	// registry -- the case the whole rewrite exists for.
	if got[2].Image != "local/busybox" {
		t.Errorf("container 2 image = %q, want local/busybox", got[2].Image)
	}
}

// The image list is an enrichment and its failure must cost only the rewrite.
// A daemon that 500s it -- or one too old to have the endpoint -- still gets
// a full set of rows, with every image exactly as the container named it.
func TestSystemDockerContainersSurvivesNoImageList(t *testing.T) {
	fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+dockerAPIVersion+"/containers/json" {
			_, _ = io.WriteString(w, containersJSONFixture)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})

	got, err := SystemDockerContainers(t.Context())
	if err != nil {
		t.Fatalf("SystemDockerContainers: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d containers, want 3", len(got))
	}
	if got[2].Image != "busybox" {
		t.Errorf("container 2 image = %q, want busybox untouched", got[2].Image)
	}
}

// The socket not being THERE is a supported configuration and has its own
// error, which the caller keys on to decide whether a raw cgroup id may be
// reported. A daemon that answers with a non-200 is the different fact.
func TestSystemDockerContainersErrors(t *testing.T) {
	t.Run("no socket", func(t *testing.T) {
		fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		dockerSocket = filepath.Join(t.TempDir(), "absent.sock")

		_, err := SystemDockerContainers(t.Context())
		if !errors.Is(err, ErrNoDockerSocket) {
			t.Errorf("err = %v, want ErrNoDockerSocket", err)
		}
	})

	t.Run("daemon refuses the list", func(t *testing.T) {
		fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

		_, err := SystemDockerContainers(t.Context())
		if err == nil {
			t.Fatal("want an error, got nil")
		}
		if errors.Is(err, ErrNoDockerSocket) {
			t.Errorf("err = %v, must NOT be ErrNoDockerSocket: the socket is mounted", err)
		}
	})

	t.Run("body is not the list shape", func(t *testing.T) {
		fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"message": "not a list"}`)
		})

		if _, err := SystemDockerContainers(t.Context()); err == nil {
			t.Error("want a decode error, got nil")
		}
	})
}

// Inspect rides the same request path, so one test covers what it adds: the
// id in the URL and the two fields decodeInspect reads back.
func TestSystemDockerInspect(t *testing.T) {
	var asked string
	fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		_, _ = io.WriteString(w, `{"RestartCount": 3,
		  "State": {"StartedAt": "2026-08-10T09:00:00.123456789Z"}}`)
	})

	got, err := SystemDockerInspect(t.Context(), "8dfafdbc3a40")
	if err != nil {
		t.Fatalf("SystemDockerInspect: %v", err)
	}
	if want := "/" + dockerAPIVersion + "/containers/8dfafdbc3a40/json"; asked != want {
		t.Errorf("asked %q, want %q", asked, want)
	}
	if got.RestartCount != 3 {
		t.Errorf("RestartCount = %d, want 3", got.RestartCount)
	}
	if got.StartedAt.IsZero() {
		t.Error("StartedAt is zero, want the instant the body names")
	}

	// A daemon that will not inspect is the no-inspect capability, not a
	// container with zero restarts.
	fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := SystemDockerInspect(t.Context(), "8dfafdbc3a40"); err == nil {
		t.Error("want an error on 403, got nil")
	}
}

// The Hub cases are the ones that matter most: "nginx:1.27" has no registry in
// the string and is NOT local, because Docker's grammar makes a hostless ref a
// docker.io ref. Only the daemon's word (pulled=false) or a loopback host
// makes an image local, and either way any registry host is stripped so the
// result always starts "local/<name>".
func TestLocalImageRef(t *testing.T) {
	for _, tc := range []struct {
		ref    string
		pulled bool
		want   string
	}{
		{"nginx:1.27", true, "nginx:1.27"},
		{"timescale/timescaledb:latest-pg17", true, "timescale/timescaledb:latest-pg17"},
		{"ghcr.io/trick77/netra-agent:latest", true, "ghcr.io/trick77/netra-agent:latest"},
		{"registry.home.lan:5000/svc:2", true, "registry.home.lan:5000/svc:2"},
		{"shop-worker:latest", false, "local/shop-worker:latest"},
		{"shop-worker", false, "local/shop-worker"},
		{"ghcr.io/me/app:dev", false, "local/me/app:dev"},
		{"localhost/svc:2", true, "local/svc:2"},
		{"localhost:5000/svc:2", true, "local/svc:2"},
		{"127.0.0.1:5000/team/svc:2", true, "local/team/svc:2"},
		{"[::1]:5000/svc", true, "local/svc"},
		{"sha256:d0e5dabcdef", false, "sha256:d0e5dabcdef"},
	} {
		if got := localImageRef(tc.ref, tc.pulled); got != tc.want {
			t.Errorf("localImageRef(%q, %v) = %q, want %q", tc.ref, tc.pulled, got, tc.want)
		}
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
