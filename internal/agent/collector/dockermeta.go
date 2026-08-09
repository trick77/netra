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
}

// SystemDockerContainers is the production ContainerLister.
//
// It reads names, images and compose labels. It deliberately does NOT read
// stats: the /containers/{id}/stats endpoint streams, costs the daemon real
// work per container, and reports the same numbers cgroup v2 already has --
// which is why the socket stays an enrichment rather than a dependency.
func SystemDockerContainers(ctx context.Context) ([]ContainerMeta, error) {
	if _, err := os.Stat(dockerSocket); err != nil {
		return nil, fmt.Errorf("docker socket unavailable: %w", err)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", dockerSocket)
			},
		},
	}

	// The host part is ignored for a unix socket but must be present and
	// valid for net/http to build the request.
	endpoint := "http://docker/" + dockerAPIVersion + "/containers/json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build docker request: %w", err)
	}

	resp, err := client.Do(req)
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
			IsAgent: strings.Contains(c.Image, "netra-agent"),
		})
	}

	return out, nil
}
