// Package httpapi holds the hub's HTTP surface.
package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/store"
)

// maxBodyBytes caps a single ingest POST. A 60s batch of host samples is a
// few hundred bytes; this is generous headroom that still bounds memory.
const maxBodyBytes = 4 << 20

// IngestHandler accepts agent metric batches.
type IngestHandler struct {
	auth     *auth.Authenticator
	store    *store.Store
	interval time.Duration
}

// NewIngestHandler wires the handler. interval is the scrape interval the hub
// hands back to agents.
func NewIngestHandler(a *auth.Authenticator, s *store.Store, interval time.Duration) *IngestHandler {
	return &IngestHandler{auth: a, store: s, interval: interval}
}

func (h *IngestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostID, err := h.auth.Authenticate(ctx, bearer(r))
	if errors.Is(err, auth.ErrUnauthorized) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		slog.Error("authenticate", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var req netrav1.IngestRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		http.Error(w, "malformed body", http.StatusBadRequest)
		return
	}

	if _, err := h.store.InsertHostSamples(ctx, hostID, req.GetHostSamples()); err != nil {
		slog.Error("insert host samples", "host_id", hostID, "err", err)
		// 503, not 500: the agent should buffer and retry rather than discard.
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	if s := latest(req.GetHostSamples()); s != nil {
		if err := h.store.UpsertHostCurrent(ctx, hostID, s); err != nil {
			slog.Error("upsert host_current", "host_id", hostID, "err", err)
		}
	}

	requestMetadata, err := h.reconcileMetadata(ctx, hostID, &req)
	if err != nil {
		slog.Error("reconcile metadata", "host_id", hostID, "err", err)
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	writeProto(w, &netrav1.IngestResponse{
		AckSeq:          req.GetSeq(),
		RequestMetadata: requestMetadata,
		IntervalS:       uint32(h.interval.Seconds()),
	})
}

// reconcileMetadata stores a supplied metadata block and reports whether the
// hub still needs one. There is no connection to hang "on connect" off, so the
// hash comparison is what makes the handshake self-healing across hub
// restarts and agent upgrades alike.
func (h *IngestHandler) reconcileMetadata(ctx context.Context, hostID int32, req *netrav1.IngestRequest) (bool, error) {
	if md := req.GetMetadata(); md != nil {
		if err := h.store.SaveMetadata(ctx, hostID, req.GetMetadataHash(), md); err != nil {
			return false, err
		}
		return false, nil
	}

	stored, err := h.store.MetadataHash(ctx, hostID)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(stored, req.GetMetadataHash()) || len(stored) == 0, nil
}

func latest(samples []*netrav1.HostSample) *netrav1.HostSample {
	var out *netrav1.HostSample
	for _, s := range samples {
		if out == nil || s.GetTsMs() > out.GetTsMs() {
			out = s
		}
	}
	return out
}

func bearer(r *http.Request) string {
	const prefix = "Bearer "
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, prefix) {
		return ""
	}
	return strings.TrimPrefix(v, prefix)
}

func writeProto(w http.ResponseWriter, m proto.Message) {
	raw, err := proto.Marshal(m)
	if err != nil {
		slog.Error("marshal response", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(raw)
}
