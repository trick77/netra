package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// storeFamilies writes every per-entity family in a request.
//
// One function rather than a dozen inline blocks in ServeHTTP so the handler
// stays readable, and so a new family is one entry here rather than another
// twelve-line copy of the same 503 handling.
//
// A TRANSIENT failure in any family fails the whole request with a 503. That
// is deliberate: these are primary rows on the same natural keys as the host
// samples already written, so silently dropping them would hide exactly the
// data the collectors exist to deliver, and host_samples dedupes the replay
// that the retry produces.
//
// A row Postgres will reject identically forever is NOT such a failure, and is
// quarantined in the store rather than 503'd (see store.poisonRow). The
// difference matters because the agent answers a 503 by re-sending the
// IDENTICAL batch: one NUL byte in a process comm would otherwise wedge that
// host's ring buffer permanently, which is the failure mode maxBatchRows and
// the timestamp filter below already exist to prevent.
//
// Quarantine rather than per-family isolation, deliberately. Isolating the
// families would let eleven of them land while the twelfth still 503'd -- but
// the agent then re-sends everything anyway, so the eleven gain nothing, and
// the poisoned family stays poisoned forever. Quarantine is what actually
// guarantees forward progress: it drops the individual rows that can never be
// stored and keeps every row that can, including the good rows of the family
// that carried the poison.
func (h *IngestHandler) storeFamilies(ctx context.Context, hostID int32, req *netrav1.IngestRequest) error {
	// Every family's timestamps are bounded the same way host samples' are: a
	// far-future row would outlive every retention policy, and one poison row
	// must be dropped individually rather than failing the batch -- which
	// would make the agent re-send the identical batch forever.
	future := time.Now().Add(maxPlausibleFuture)

	cores, dropped := filterByTs(req.GetCpuCores(), future)
	logDropped(hostID, "cpu core", dropped)
	if _, err := h.store.InsertCpuCoreSamples(ctx, hostID, cores); err != nil {
		return fmt.Errorf("cpu cores: %w", err)
	}

	disks, dropped := filterByTs(req.GetDiskIo(), future)
	logDropped(hostID, "disk io", dropped)
	if _, err := h.store.InsertDiskIoSamples(ctx, hostID, disks); err != nil {
		return fmt.Errorf("disk io: %w", err)
	}

	sensors, dropped := filterByTs(req.GetSensors(), future)
	logDropped(hostID, "sensor", dropped)
	if _, err := h.store.InsertSensorSamples(ctx, hostID, sensors); err != nil {
		return fmt.Errorf("sensors: %w", err)
	}

	nets, dropped := filterByTs(req.GetNet(), future)
	logDropped(hostID, "net", dropped)
	if _, err := h.store.InsertNetSamples(ctx, hostID, nets); err != nil {
		return fmt.Errorf("net: %w", err)
	}

	containers, dropped := filterByTs(req.GetContainers(), future)
	logDropped(hostID, "container", dropped)
	if _, err := h.store.InsertContainerSamples(ctx, hostID, containers); err != nil {
		return fmt.Errorf("containers: %w", err)
	}

	filesystems, dropped := filterByTs(req.GetFilesystems(), future)
	logDropped(hostID, "filesystem", dropped)
	if _, err := h.store.InsertFilesystemSamples(ctx, hostID, filesystems); err != nil {
		return fmt.Errorf("filesystems: %w", err)
	}

	smart, dropped := filterByTs(req.GetSmart(), future)
	logDropped(hostID, "smart", dropped)
	if _, err := h.store.InsertSmartAttributes(ctx, hostID, smart); err != nil {
		return fmt.Errorf("smart: %w", err)
	}

	processes, dropped := filterByTs(req.GetProcesses(), future)
	logDropped(hostID, "process", dropped)
	if _, err := h.store.InsertProcessSamples(ctx, hostID, processes); err != nil {
		return fmt.Errorf("processes: %w", err)
	}

	collectors, dropped := filterByTs(req.GetCollectors(), future)
	logDropped(hostID, "collector", dropped)
	if _, err := h.store.InsertCollectorSamples(ctx, hostID, collectors); err != nil {
		return fmt.Errorf("collectors: %w", err)
	}

	events, dropped := filterByTs(req.GetEvents(), future)
	logDropped(hostID, "event", dropped)
	if _, err := h.store.InsertEvents(ctx, hostID, events); err != nil {
		return fmt.Errorf("events: %w", err)
	}

	unitEvents, dropped := filterByTs(req.GetSystemdEvents(), future)
	logDropped(hostID, "systemd unit event", dropped)
	if _, err := h.store.InsertSystemdUnitEvents(ctx, hostID, unitEvents); err != nil {
		return fmt.Errorf("systemd events: %w", err)
	}

	pkgEvents, dropped := filterByTs(req.GetPackageEvents(), future)
	logDropped(hostID, "package event", dropped)
	if _, err := h.store.InsertPackageEvents(ctx, hostID, pkgEvents); err != nil {
		return fmt.Errorf("package events: %w", err)
	}

	// Inventory carries no timestamps: it describes what the host HAS, and the
	// hub stamps first_seen and last_seen itself.
	if _, err := h.store.UpsertHostAddresses(ctx, hostID, req.GetAddresses()); err != nil {
		return fmt.Errorf("addresses: %w", err)
	}
	if _, err := h.store.UpsertHostPackages(ctx, hostID, req.GetPackages()); err != nil {
		return fmt.Errorf("packages: %w", err)
	}

	return nil
}

func logDropped(hostID int32, family string, n int) {
	if n > 0 {
		slog.Warn("dropped rows with implausible timestamps",
			"host_id", hostID, "family", family, "dropped", n)
	}
}

// filterByTs is the shared bounds check, over every row type that carries a
// ts_ms. Constrained on the generated getter rather than taking a func(T) int64
// so a family cannot be added without one -- the twelve identical accessor
// wrappers this replaces were exactly the drift risk they were written to
// prevent.
func filterByTs[T interface{ GetTsMs() int64 }](rows []T, future time.Time) ([]T, int) {
	if len(rows) == 0 {
		return rows, 0
	}
	out := make([]T, 0, len(rows))
	dropped := 0
	for _, r := range rows {
		if !plausibleTs(r.GetTsMs(), future) {
			dropped++
			continue
		}
		out = append(out, r)
	}
	return out, dropped
}
