package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// ingestPath is the agent-facing endpoint. The simulator posts to the same
// URL a real agent does rather than to a back door, so a run exercises the
// authentication, the body-size cap and the timestamp filter alongside
// everything else.
const ingestPath = "/api/agent/v1/ingest"

// Hub is the simulator's client for one netra hub: the admin API for
// provisioning, and the agent API for posting samples.
type Hub struct {
	baseURL    string
	adminToken string
	hc         *http.Client
}

// NewHub builds a client. The timeout is generous because a backfill POST
// carries twenty thousand rows and the hub inserts them synchronously.
func NewHub(baseURL, adminToken string) *Hub {
	return &Hub{
		baseURL:    strings.TrimRight(baseURL, "/"),
		adminToken: adminToken,
		hc:         &http.Client{Timeout: 120 * time.Second},
	}
}

// HostRef is a host as the read API lists it.
type HostRef struct {
	ID       int32  `json:"id"`
	Hostname string `json:"hostname"`
	SiteID   *int32 `json:"site_id"`
}

// EnsureHost finds the simulated host by hostname or creates it, and returns
// a working token either way.
//
// Finding before creating is not an optimisation. hosts carries a unique
// (site_id, hostname) with NULLS NOT DISTINCT, so a second run that blindly
// POSTed would get a 409 for every host in the fleet. The plaintext token is
// shown once at creation and is not readable back, so an existing host gets
// its token rotated -- which is also the documented way an operator recovers
// a token they lost.
func (h *Hub) EnsureHost(ctx context.Context, hostname string, siteID *int32) (int32, string, error) {
	if err := checkSimulated(hostname); err != nil {
		return 0, "", err
	}
	hosts, err := h.ListHosts(ctx)
	if err != nil {
		return 0, "", err
	}
	for _, existing := range hosts {
		if existing.Hostname == hostname {
			token, err := h.RotateToken(ctx, existing.ID)
			if err != nil {
				return 0, "", err
			}
			return existing.ID, token, nil
		}
	}
	return h.CreateHost(ctx, hostname, siteID)
}

// ListHosts returns every host the hub knows about.
func (h *Hub) ListHosts(ctx context.Context) ([]HostRef, error) {
	var out []HostRef
	if err := h.adminJSON(ctx, http.MethodGet, "/api/v1/hosts", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateHost registers a host and returns its id and freshly minted token.
func (h *Hub) CreateHost(ctx context.Context, hostname string, siteID *int32) (int32, string, error) {
	var out struct {
		ID    int32  `json:"id"`
		Token string `json:"token"`
	}
	body := map[string]any{"hostname": hostname, "site_id": siteID}
	if err := h.adminJSON(ctx, http.MethodPost, "/api/v1/hosts", body, &out); err != nil {
		return 0, "", err
	}
	return out.ID, out.Token, nil
}

// RotateToken mints a replacement token for an existing host.
func (h *Hub) RotateToken(ctx context.Context, id int32) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	path := fmt.Sprintf("/api/v1/hosts/%d/token", id)
	if err := h.adminJSON(ctx, http.MethodPost, path, struct{}{}, &out); err != nil {
		return "", err
	}
	return out.Token, nil
}

// DeleteHost removes a host and, by cascade, everything it ever reported.
//
// The hostname is passed alongside the id purely so it can be checked: this
// is the most destructive call the simulator can make, and it must be
// impossible to aim at a host the simulator did not create.
func (h *Hub) DeleteHost(ctx context.Context, id int32, hostname string) error {
	if err := checkSimulated(hostname); err != nil {
		return err
	}
	return h.adminJSON(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/hosts/%d", id), nil, nil)
}

// checkSimulated refuses to touch a host the simulator did not create.
func checkSimulated(hostname string) error {
	if !strings.HasPrefix(hostname, HostnamePrefix) {
		return fmt.Errorf("refusing to touch %q: netra-sim only manages hosts named %s*", hostname, HostnamePrefix)
	}
	return nil
}

// EnsureProvider returns the id of a provider with this name, creating it if
// it does not exist.
func (h *Hub) EnsureProvider(ctx context.Context, name string) (int32, error) {
	var list []struct {
		ID   int32  `json:"id"`
		Name string `json:"name"`
	}
	if err := h.adminJSON(ctx, http.MethodGet, "/api/v1/providers", nil, &list); err != nil {
		return 0, err
	}
	for _, p := range list {
		if p.Name == name {
			return p.ID, nil
		}
	}

	var out struct {
		ID int32 `json:"id"`
	}
	if err := h.adminJSON(ctx, http.MethodPost, "/api/v1/providers", map[string]any{"name": name}, &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// SiteSpec is the location a simulated host sits at.
type SiteSpec struct {
	Name        string
	Provider    string
	Facility    string
	CountryCode string
	Timezone    string
	Latitude    float64
	Longitude   float64
}

// EnsureSite returns the id of a site with this name under this provider,
// creating and describing it if it does not exist. The coordinates are filled
// in because a site without them is invisible to anything that draws a map.
func (h *Hub) EnsureSite(ctx context.Context, spec SiteSpec) (int32, error) {
	providerID, err := h.EnsureProvider(ctx, spec.Provider)
	if err != nil {
		return 0, err
	}

	var list []struct {
		ID         int32  `json:"id"`
		Name       string `json:"name"`
		ProviderID *int32 `json:"provider_id"`
	}
	if err := h.adminJSON(ctx, http.MethodGet, "/api/v1/sites", nil, &list); err != nil {
		return 0, err
	}
	for _, s := range list {
		if s.Name == spec.Name && s.ProviderID != nil && *s.ProviderID == providerID {
			return s.ID, nil
		}
	}

	var out struct {
		ID int32 `json:"id"`
	}
	body := map[string]any{"name": spec.Name, "provider_id": providerID}
	if err := h.adminJSON(ctx, http.MethodPost, "/api/v1/sites", body, &out); err != nil {
		return 0, err
	}

	patch := map[string]any{
		"facility":     spec.Facility,
		"country_code": spec.CountryCode,
		"timezone":     spec.Timezone,
		"latitude":     spec.Latitude,
		"longitude":    spec.Longitude,
	}
	if err := h.adminJSON(ctx, http.MethodPatch, fmt.Sprintf("/api/v1/sites/%d", out.ID), patch, nil); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// Ingest posts one batch as an agent would: a marshalled protobuf body with a
// bearer token.
func (h *Hub) Ingest(ctx context.Context, token string, req *netrav1.IngestRequest) (*netrav1.IngestResponse, error) {
	raw, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+ingestPath, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := h.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post ingest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read ingest response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ingest: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out netrav1.IngestResponse
	if err := proto.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("unmarshal ingest response: %w", err)
	}
	if out.GetAckSeq() != req.GetSeq() {
		// The hub echoes the sequence once the whole batch is stored, so a
		// mismatch means it did not. Treating it as success would leave a
		// hole in the history that nothing later notices.
		return nil, fmt.Errorf("ingest: hub acked seq %d, sent %d", out.GetAckSeq(), req.GetSeq())
	}
	return &out, nil
}

// adminJSON performs one admin API call. out may be nil for a call whose
// response body is not needed.
func (h *Hub) adminJSON(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.adminToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := h.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode body: %w", method, path, err)
	}
	return nil
}
