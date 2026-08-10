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

// storageFailureRetryAfter is handed back to the agent on a 503 so it waits
// at least this long before retrying, rather than relying on its own
// exponential backoff for a failure mode the hub can characterise directly.
const storageFailureRetryAfter = 30 * time.Second

// minPlausibleTs and maxPlausibleFuture bound the timestamps the hub accepts
// (spec §7.5, §9 "Clock skew"). A sample outside this range is dropped
// individually rather than failing the whole batch: Postgres would otherwise
// reject the entire INSERT, the hub would 503, and the agent would re-send
// the identical poison batch forever. A far-future sample is also dangerous
// on its own because it would permanently poison host_current, whose
// ON CONFLICT guard only accepts updates with a later timestamp.
var minPlausibleTs = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

const maxPlausibleFuture = time.Hour

// IngestHandler accepts agent metric batches.
type IngestHandler struct {
	auth  *auth.Authenticator
	store *store.Store
}

// NewIngestHandler wires the handler. The hub hands back no scrape cadence:
// the agent's interval is a fixed 60s constant and there is no per-host
// cadence column here to override it with.
//
// There is deliberately no ingest rate limiting. Agent-to-hub is a trusted
// loop — a valid token means a host we deployed — so the 60s cadence is the
// agent's own fixed constant rather than something enforced here. Two cases
// would break a naive "one POST per host per minute" rule and are both
// accepted as normal: replaying the ring buffer after an outage flushes
// batches back to back (IngestRequest.backfill), and an agent restart can
// land a second sample inside the same minute, once.
func NewIngestHandler(a *auth.Authenticator, s *store.Store) *IngestHandler {
	return &IngestHandler{auth: a, store: s}
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

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var req netrav1.IngestRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		http.Error(w, "malformed body", http.StatusBadRequest)
		return
	}

	if req.GetBackfill() {
		// Informational: TimescaleDB invalidates continuous aggregates
		// automatically on INSERT into an older chunk, so no action is
		// needed here beyond making replay observable.
		slog.Debug("ingesting backfilled batch", "host_id", hostID, "seq", req.GetSeq())
	}

	samples, dropped := filterByTs(req.GetHostSamples(), time.Now().Add(maxPlausibleFuture))
	logDropped(hostID, "host sample", dropped)

	if _, err := h.store.InsertHostSamples(ctx, hostID, samples); err != nil {
		slog.Error("insert host samples", "host_id", hostID, "err", err)
		writeProtoStatus(w, http.StatusServiceUnavailable, &netrav1.IngestResponse{
			RetryAfterS: uint32(storageFailureRetryAfter.Seconds()),
		})
		return
	}

	// Immediately after the host samples it summarises, and BEFORE anything
	// below that can 503 out of the handler -- storeFamilies and the
	// agent_samples insert both can. host_current is the cache "last seen"
	// reads from, so a host whose samples have just landed must not read as
	// stale in the UI while its data is in fact arriving on every retry.
	// Anywhere further down and that guarantee only covers the failures that
	// happen to come after it.
	if s := latest(samples); s != nil {
		if err := h.store.UpsertHostCurrent(ctx, hostID, s); err != nil {
			slog.Error("upsert host_current", "host_id", hostID, "err", err)
		}
	}

	if err := h.storeFamilies(ctx, hostID, &req); err != nil {
		slog.Error("insert per-entity families", "host_id", hostID, "err", err)
		writeProtoStatus(w, http.StatusServiceUnavailable, &netrav1.IngestResponse{
			RetryAfterS: uint32(storageFailureRetryAfter.Seconds()),
		})
		return
	}

	// A 503 rather than a logged-and-ignored failure, unlike host_current
	// above. host_current is a derived cache that the next scrape rebuilds,
	// whereas these are primary rows on the same natural key as the host
	// samples just written -- silently dropping them would hide exactly the
	// agent-health problems they exist to expose. Asking for a retry is cheap
	// because host_samples dedupes the replay.
	if _, err := h.store.InsertAgentSamples(ctx, hostID, samples); err != nil {
		slog.Error("insert agent samples", "host_id", hostID, "err", err)
		writeProtoStatus(w, http.StatusServiceUnavailable, &netrav1.IngestResponse{
			RetryAfterS: uint32(storageFailureRetryAfter.Seconds()),
		})
		return
	}

	requestMetadata, err := h.reconcileMetadata(ctx, hostID, &req)
	if err != nil {
		slog.Error("reconcile metadata", "host_id", hostID, "err", err)
		writeProtoStatus(w, http.StatusServiceUnavailable, &netrav1.IngestResponse{
			RetryAfterS: uint32(storageFailureRetryAfter.Seconds()),
		})
		return
	}

	writeProto(w, &netrav1.IngestResponse{
		AckSeq:          req.GetSeq(),
		RequestMetadata: requestMetadata,
	})
}

// plausibleTs is the bounds check every family shares, so no two of them can
// drift apart on what counts as a poison timestamp. Host samples go through
// filterByTs in families.go like everything else -- dropping a poisoned sample
// individually rather than failing the batch keeps one bad timestamp from
// stalling the rest of a host's ingest.
func plausibleTs(tsMs int64, future time.Time) bool {
	ts := time.UnixMilli(tsMs).UTC()
	return !ts.Before(minPlausibleTs) && !ts.After(future)
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
	writeProtoStatus(w, http.StatusOK, m)
}

// writeProtoStatus writes m as the protobuf body of a non-200 response, so
// fields like retry_after_s reach the agent even on a failure status. Plain
// http.Error would discard them.
func writeProtoStatus(w http.ResponseWriter, status int, m proto.Message) {
	raw, err := proto.Marshal(m)
	if err != nil {
		slog.Error("marshal response", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
