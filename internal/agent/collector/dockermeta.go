package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// dockerSocket is where the Docker API socket is mounted. The agent asks it
// for names and labels ONLY -- every metric comes from cgroup v2, so a host
// that declines to mount the socket still gets numbers.
const dockerSocket = "/var/run/docker.sock"

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
// It reads names, images and compose labels. It deliberately does NOT read
// stats: the /containers/{id}/stats endpoint streams, costs the daemon real
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

func SystemDockerContainers(ctx context.Context) ([]ContainerMeta, error) {
	if _, err := os.Stat(dockerSocket); err != nil {
		return nil, fmt.Errorf("docker socket unavailable: %w", err)
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
			// So the hub can exclude the agent from "what is running here"
			// without every UI hard-coding an image name.
			IsAgent:     strings.Contains(c.Image, "netra-agent"),
			NetworkMode: c.HostConfig.NetworkMode,
		})
	}

	return out, nil
}
