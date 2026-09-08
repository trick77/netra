package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// dockerSocket is where the Docker API socket is mounted. The agent asks it
// for names and labels ONLY -- every metric comes from cgroup v2, so a host
// that declines to mount the socket still gets numbers.
const dockerSocket = "/var/run/docker.sock"

// ErrNoDockerSocket is the socket not being THERE, as opposed to being there
// and not answering. From a failed list call the two look identical and they
// mean opposite things: the first is an operator who chose not to mount it --
// a supported configuration, and the only one in which containers may be
// reported under their raw cgroup id -- and the second is a fault, during
// which reporting a raw id invents a container per cgroup scope that never
// goes away. See the row guard in Collect.
var ErrNoDockerSocket = errors.New("docker socket not mounted")

// dockerAPIVersion is pinned low deliberately: /containers/json has been
// stable since long before this, and pinning avoids a newer daemon changing
// the default response shape under the agent.
const dockerAPIVersion = "v1.41"

// dockerContainer is the subset of /containers/json netra reads.
type dockerContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`

	// State is the daemon's own word: "running", "paused", "restarting". It
	// arrives at the top level of the SAME response as the fields above, and
	// was being decoded away for as long as this struct has existed -- the
	// container detail page said state was "never read from Docker" while the
	// answer sat in a body the agent had already parsed.
	//
	// The list defaults to all=false, so "exited" is not reachable here. That
	// is a property of the request, not of this field.
	State string `json:"State"`

	// Status is the human-readable summary -- "Up 4 days", "Up 2 hours
	// (healthy)" -- and its parenthesised suffix is the ONLY place the list
	// endpoint carries health. parseHealth reads it.
	//
	// The alternative is /containers/{id}/json, which reports health as a
	// structured State.Health.Status. That costs one unix-socket round trip
	// per container per scrape to learn a word already present in a string in
	// hand -- the same objection this file makes to /containers/{id}/stats
	// below. Parsing a display string is the price, and parseHealth pays it
	// narrowly: three exact tokens, everything else "none".
	Status string `json:"Status"`

	// HostConfig.NetworkMode answers "is this container on the host's network
	// namespace" directly, which is the only question containerNet needed the
	// namespace links for. It arrives in the SAME response as the fields
	// above -- no extra request, no extra daemon work -- and it is the
	// daemon's own statement rather than an inference from two readlinks.
	//
	// It matters because the inference is not reachable on a stock install:
	// readlink on /proc/<pid>/ns/net goes through ptrace_may_access, and a
	// non-dumpable target requires CAP_SYS_PTRACE even when the uids match.
	// `security_opt: no-new-privileges` makes netra's own targets
	// non-dumpable, so every container read was denied -- measured on a live
	// host, where only --cap-add SYS_PTRACE lifted it.
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
}

// SystemDockerContainers is the production ContainerLister.
//
// It reads names, images, labels, state and health. It deliberately does NOT
// read stats: the /containers/{id}/stats endpoint streams, costs the daemon real
// work per container, and reports the same numbers cgroup v2 already has --
// which is why the socket stays an enrichment rather than a dependency.
// dockerClient is built ONCE and reused for the life of the process.
//
// It used to be constructed inside SystemDockerContainers, which leaked a file
// descriptor per scrape. The response body is fully decoded, so the unix-socket
// connection goes back into that call's own idle pool -- and a hand-built
// http.Transport has IdleConnTimeout zero, meaning never reaped. Nothing closed
// the transport either, so every scrape stranded one connection to
// /var/run/docker.sock for the life of the agent: 1440 a day, on exactly the
// hosts the container collector exists for.
//
// One shared client keeps a single connection alive and reuses it, which is
// also what the daemon would prefer.
var dockerClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", dockerSocket)
		},
	},
}

// Health values, as Docker itself words them. "none" is the one worth naming:
// it is `docker ps --filter health=none`, an image that defines no HEALTHCHECK,
// and it is a MEASUREMENT -- the agent looked and there is nothing to report.
// The absence of any value is the different fact that the agent could not look,
// and it travels as an unset proto field rather than as a fifth constant.
const (
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
	HealthStarting  = "starting"
	HealthNone      = "none"
)

// parseHealth reads the health suffix out of a /containers/json Status string.
//
// Docker builds Status for people, not for parsers: "Up 4 days", "Up 2 hours
// (healthy)", "Up 3 seconds (health: starting)", "Up 2 hours (Paused)". The
// suffix is present only when the image defines a HEALTHCHECK, and it is the
// only health the list endpoint carries.
//
// So the match is exact and closed: three known spellings, and anything else --
// including "(Paused)", which is a state and not a health -- is HealthNone.
// Guessing from an unrecognised suffix would put a word on a status badge that
// Docker never said, which is the failure the whole card exists to avoid.
func parseHealth(status string) string {
	switch {
	case strings.Contains(status, "(healthy)"):
		return HealthHealthy
	case strings.Contains(status, "(unhealthy)"):
		return HealthUnhealthy
	case strings.Contains(status, "(health: starting)"):
		return HealthStarting
	default:
		return HealthNone
	}
}

// dockerInspect is the subset of /containers/{id}/json netra reads.
//
// Two fields, and neither is carried by the list endpoint -- which is the whole
// reason the inspect call is rationed; see the inspect cache in
// containerinspect.go, not this function.
//
// StartedAt costs NOTHING to add. The response was already fetched in full and
// decoded away to read one integer out of it, so a container's true start time
// is a struct tag, not a request.
type dockerInspect struct {
	RestartCount uint64 `json:"RestartCount"`
	State        struct {
		// RFC3339Nano, Docker's own format. Kept as a STRING rather than
		// decoded into time.Time so an unparseable or missing value costs only
		// the start time: a decode error here would discard the RestartCount
		// beside it, and the two are meant to fail together only when the
		// daemon refuses the whole call.
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
}

// SystemDockerInspect is the production ContainerInspector.
//
// It reuses the package's one dockerClient rather than building a transport per
// call, for the reason spelled out above it: a hand-built http.Transport has
// IdleConnTimeout zero, and this function runs on far more scrapes than
// SystemDockerContainers has containers.
func SystemDockerInspect(ctx context.Context, id string) (ContainerStatus, error) {
	endpoint := "http://docker/" + dockerAPIVersion + "/containers/" + url.PathEscape(id) + "/json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ContainerStatus{}, fmt.Errorf("build docker inspect request: %w", err)
	}

	resp, err := dockerClient.Do(req)
	if err != nil {
		return ContainerStatus{}, fmt.Errorf("inspect container: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return ContainerStatus{}, fmt.Errorf("docker returned %s", resp.Status)
	}

	var out dockerInspect
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ContainerStatus{}, fmt.Errorf("decode docker inspect response: %w", err)
	}
	return ContainerStatus{
		RestartCount: out.RestartCount,
		StartedAt:    parseStartedAt(out.State.StartedAt),
	}, nil
}

// parseStartedAt turns Docker's State.StartedAt into an instant, or the zero
// time when there is not one to be had.
//
// Three things all mean "no start time", and all three must reach the caller as
// the zero value rather than as an error: an empty string (an older daemon, or
// a response shape without the State object), a value that will not parse, and
// Docker's own zero time "0001-01-01T00:00:00Z", which is what it reports for a
// container that has never run. The last is why IsZero is checked after
// parsing succeeds and not only before.
func parseStartedAt(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func SystemDockerContainers(ctx context.Context) ([]ContainerMeta, error) {
	// Stat, deliberately, and NOT a connect probe. setup-agent.sh bind-mounts
	// the socket FILE, which pins the inode inside this container, so the stat
	// still succeeds after dockerd unlinks and recreates the host socket on a
	// restart -- the dial below then fails and the caller treats it as the
	// fault it is. A connect probe would report that restart as "no socket
	// mounted" and re-open the path that keys containers by their raw id.
	if _, err := os.Stat(dockerSocket); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoDockerSocket, err)
	}

	// The host part is ignored for a unix socket but must be present and
	// valid for net/http to build the request.
	endpoint := "http://docker/" + dockerAPIVersion + "/containers/json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build docker request: %w", err)
	}

	resp, err := dockerClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query docker: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker returned %s", resp.Status)
	}

	var containers []dockerContainer
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, fmt.Errorf("decode docker response: %w", err)
	}

	out := make([]ContainerMeta, 0, len(containers))
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			// Docker returns names with a leading slash.
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, ContainerMeta{
			ID:      c.ID,
			Name:    name,
			Image:   c.Image,
			Project: c.Labels["com.docker.compose.project"],
			Service: c.Labels["com.docker.compose.service"],
			State:   c.State,
			Health:  parseHealth(c.Status),
			// The whole map, not just the two compose keys above. Those two
			// survived only folded into container_key, and only when BOTH were
			// set -- so a container started outside compose carried no label
			// anywhere, while the UI said labels "survive".
			//
			// Nil, not an empty map, when the daemon reports no Labels key, so
			// "looked and found none" stays distinguishable from "never
			// looked" all the way to the wire.
			Labels: c.Labels,
			// So the hub can exclude the agent from "what is running here"
			// without every UI hard-coding an image name.
			IsAgent:     strings.Contains(c.Image, "netra-agent"),
			NetworkMode: c.HostConfig.NetworkMode,
		})
	}

	return out, nil
}
