package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
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
}

// EnsureHost finds the simulated host by hostname or creates it, and returns
// a working token either way.
//
// Finding before creating is not an optimisation. hosts carries a unique
// hostname with NULLS NOT DISTINCT, so a second run that blindly POSTed would
// get a 409 for every host in the fleet. The plaintext token is
// shown once at creation and is not readable back, so an existing host gets
// its token rotated -- which is also the documented way an operator recovers
// a token they lost.
func (h *Hub) EnsureHost(ctx context.Context, hostname string) (int32, string, error) {
	if err := checkSimulated(hostname); err != nil {
		return 0, "", err
	}
	hosts, err := h.ListHosts(ctx)
	if err != nil {
		return 0, "", err
	}
	for _, existing := range hosts {
		if existing.Hostname != hostname {
			continue
		}
		// Rotating is the only way to get a usable token: the plaintext is
		// shown once at creation and is not readable back. It also revokes
		// whatever token was in use, so a netra-sim --live running elsewhere
		// against this hub starts getting 401s from here on.
		slog.Warn("rotating the token of an existing simulated host; "+
			"any netra-sim --live already running against this hub will lose its credential",
			"hostname", hostname, "id", existing.ID)
		token, err := h.RotateToken(ctx, existing.ID)
		if err != nil {
			return 0, "", err
		}
		return existing.ID, token, nil
	}
	return h.CreateHost(ctx, hostname)
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
func (h *Hub) CreateHost(ctx context.Context, hostname string) (int32, string, error) {
	var out struct {
		ID    int32  `json:"id"`
		Token string `json:"token"`
	}
	body := map[string]any{"hostname": hostname}
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

// checkSimulated refuses to touch anything the simulator did not create.
//
// Hosts are all it has to cover now. It also guarded sites and providers,
// which were matched BY NAME -- an unguarded EnsureSite would silently adopt a
// real one and mix invented data into its rollups. Those are gone, and with
// them that whole class of collateral: a host is matched by id and the
// simulator can only reach one it made.
func checkSimulated(name string) error {
	if !strings.HasPrefix(name, HostnamePrefix) {
		return fmt.Errorf("refusing to touch %q: netra-sim only manages records named %s*", name, HostnamePrefix)
	}
	return nil
}

// ingestAttempts is how many times a batch is posted before the run gives up.
//
// A 503 from the hub is not a failure the caller should inherit: it means
// storage is briefly unavailable -- a Postgres hiccup, a retention job
// dropping a chunk, the pool momentarily exhausted -- and the response
// carries retry_after_s precisely so the client waits and re-posts, which is
// what the real agent does. Aborting instead threw away a ninety-day backfill
// forty minutes in, over a condition the protocol defines as transient.
const ingestAttempts = 4

// maxRetryAfter bounds what the hub can ask the simulator to wait, so a
// mistaken large value cannot stall a run indefinitely.
const maxRetryAfter = 2 * time.Minute

// Ingest posts one batch as an agent would: a marshalled protobuf body with a
// bearer token, retrying while the hub says the failure is transient.
func (h *Hub) Ingest(ctx context.Context, token string, req *netrav1.IngestRequest) (*netrav1.IngestResponse, error) {
	raw, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	var lastErr error
	for attempt := range ingestAttempts {
		if attempt > 0 {
			wait := retryAfter(lastErr)
			slog.Warn("ingest failed, retrying", "attempt", attempt, "in", wait.String(), "err", lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}

		resp, err := h.postIngest(ctx, token, raw, req.GetSeq())
		if err == nil {
			return resp, nil
		}
		lastErr = err

		var retry retryableError
		if !errors.As(err, &retry) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("ingest: giving up after %d attempts: %w", ingestAttempts, lastErr)
}

// retryableError marks a failure the hub told us to come back from, carrying
// the delay it asked for.
type retryableError struct {
	err   error
	after time.Duration
}

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

// retryAfter is the delay the hub asked for, or a default when it named none.
func retryAfter(err error) time.Duration {
	var retry retryableError
	if errors.As(err, &retry) && retry.after > 0 {
		return min(retry.after, maxRetryAfter)
	}
	return 5 * time.Second
}

func (h *Hub) postIngest(ctx context.Context, token string, raw []byte, seq uint64) (*netrav1.IngestResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+ingestPath, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := h.hc.Do(httpReq)
	if err != nil {
		// A connection that failed mid-run is the same class of problem as a
		// 503: the hub may simply be restarting.
		return nil, retryableError{err: fmt.Errorf("post ingest: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read ingest response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		failure := fmt.Errorf("ingest: %s: %s", resp.Status, strings.TrimSpace(string(body)))
		if resp.StatusCode != http.StatusServiceUnavailable {
			return nil, failure
		}
		// The 503 body is a protobuf carrying retry_after_s. It is only
		// trusted when it says so in the content type, so a proxy's HTML
		// error page cannot dictate how long the simulator sleeps.
		var status netrav1.IngestResponse
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-protobuf") {
			_ = proto.Unmarshal(body, &status)
		}
		return nil, retryableError{err: failure, after: time.Duration(status.GetRetryAfterS()) * time.Second}
	}

	var out netrav1.IngestResponse
	if err := proto.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("unmarshal ingest response: %w", err)
	}
	if out.GetAckSeq() != seq {
		// The hub echoes the sequence once the whole batch is stored, so a
		// mismatch means it did not. Treating it as success would leave a
		// hole in the history that nothing later notices.
		return nil, fmt.Errorf("ingest: hub acked seq %d, sent %d", out.GetAckSeq(), seq)
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
