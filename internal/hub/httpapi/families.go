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
// A failure in any family fails the whole request with a 503. That is
// deliberate: these are primary rows on the same natural keys as the host
// samples already written, so silently dropping them would hide exactly the
// data the collectors exist to deliver, and host_samples dedupes the replay
// that the retry produces.
func (h *IngestHandler) storeFamilies(ctx context.Context, hostID int32, req *netrav1.IngestRequest) error {
	// Every family's timestamps are bounded the same way host samples' are: a
	// far-future row would outlive every retention policy, and one poison row
	// must be dropped individually rather than failing the batch -- which
	// would make the agent re-send the identical batch forever.
	future := time.Now().Add(maxPlausibleFuture)

	cores, dropped := filterCpuCores(req.GetCpuCores(), future)
	logDropped(hostID, "cpu core", dropped)
	if _, err := h.store.InsertCpuCoreSamples(ctx, hostID, cores); err != nil {
		return fmt.Errorf("cpu cores: %w", err)
	}

	disks, dropped := filterDiskIo(req.GetDiskIo(), future)
	logDropped(hostID, "disk io", dropped)
	if _, err := h.store.InsertDiskIoSamples(ctx, hostID, disks); err != nil {
		return fmt.Errorf("disk io: %w", err)
	}

	sensors, dropped := filterSensors(req.GetSensors(), future)
	logDropped(hostID, "sensor", dropped)
	if _, err := h.store.InsertSensorSamples(ctx, hostID, sensors); err != nil {
		return fmt.Errorf("sensors: %w", err)
	}

	nets, dropped := filterNet(req.GetNet(), future)
	logDropped(hostID, "net", dropped)
	if _, err := h.store.InsertNetSamples(ctx, hostID, nets); err != nil {
		return fmt.Errorf("net: %w", err)
	}

	containers, dropped := filterContainers(req.GetContainers(), future)
	logDropped(hostID, "container", dropped)
	if _, err := h.store.InsertContainerSamples(ctx, hostID, containers); err != nil {
		return fmt.Errorf("containers: %w", err)
	}

	filesystems, dropped := filterFilesystems(req.GetFilesystems(), future)
	logDropped(hostID, "filesystem", dropped)
	if _, err := h.store.InsertFilesystemSamples(ctx, hostID, filesystems); err != nil {
		return fmt.Errorf("filesystems: %w", err)
	}

	smart, dropped := filterSmart(req.GetSmart(), future)
	logDropped(hostID, "smart", dropped)
	if _, err := h.store.InsertSmartAttributes(ctx, hostID, smart); err != nil {
		return fmt.Errorf("smart: %w", err)
	}

	processes, dropped := filterProcesses(req.GetProcesses(), future)
	logDropped(hostID, "process", dropped)
	if _, err := h.store.InsertProcessSamples(ctx, hostID, processes); err != nil {
		return fmt.Errorf("processes: %w", err)
	}

	collectors, dropped := filterCollectors(req.GetCollectors(), future)
	logDropped(hostID, "collector", dropped)
	if _, err := h.store.InsertCollectorSamples(ctx, hostID, collectors); err != nil {
		return fmt.Errorf("collectors: %w", err)
	}

	events, dropped := filterEvents(req.GetEvents(), future)
	logDropped(hostID, "event", dropped)
	if _, err := h.store.InsertEvents(ctx, hostID, events); err != nil {
		return fmt.Errorf("events: %w", err)
	}

	unitEvents, dropped := filterUnitEvents(req.GetSystemdEvents(), future)
	logDropped(hostID, "systemd unit event", dropped)
	if _, err := h.store.InsertSystemdUnitEvents(ctx, hostID, unitEvents); err != nil {
		return fmt.Errorf("systemd events: %w", err)
	}

	pkgEvents, dropped := filterPackageEvents(req.GetPackageEvents(), future)
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

// filterByTs is the shared bounds check. Generic over the row type because
// every family answers the same question about its own ts_ms, and writing the
// loop a dozen times invites one of them drifting.
func filterByTs[T any](rows []T, future time.Time, ts func(T) int64) ([]T, int) {
	if len(rows) == 0 {
		return rows, 0
	}
	out := make([]T, 0, len(rows))
	dropped := 0
	for _, r := range rows {
		if !plausibleTs(ts(r), future) {
			dropped++
			continue
		}
		out = append(out, r)
	}
	return out, dropped
}

func filterCpuCores(rows []*netrav1.CpuCoreSample, f time.Time) ([]*netrav1.CpuCoreSample, int) {
	return filterByTs(rows, f, func(r *netrav1.CpuCoreSample) int64 { return r.GetTsMs() })
}

func filterDiskIo(rows []*netrav1.DiskIoSample, f time.Time) ([]*netrav1.DiskIoSample, int) {
	return filterByTs(rows, f, func(r *netrav1.DiskIoSample) int64 { return r.GetTsMs() })
}

func filterSensors(rows []*netrav1.SensorSample, f time.Time) ([]*netrav1.SensorSample, int) {
	return filterByTs(rows, f, func(r *netrav1.SensorSample) int64 { return r.GetTsMs() })
}

func filterNet(rows []*netrav1.NetSample, f time.Time) ([]*netrav1.NetSample, int) {
	return filterByTs(rows, f, func(r *netrav1.NetSample) int64 { return r.GetTsMs() })
}

func filterContainers(rows []*netrav1.ContainerSample, f time.Time) ([]*netrav1.ContainerSample, int) {
	return filterByTs(rows, f, func(r *netrav1.ContainerSample) int64 { return r.GetTsMs() })
}

func filterFilesystems(rows []*netrav1.FilesystemSample, f time.Time) ([]*netrav1.FilesystemSample, int) {
	return filterByTs(rows, f, func(r *netrav1.FilesystemSample) int64 { return r.GetTsMs() })
}

func filterSmart(rows []*netrav1.SmartAttribute, f time.Time) ([]*netrav1.SmartAttribute, int) {
	return filterByTs(rows, f, func(r *netrav1.SmartAttribute) int64 { return r.GetTsMs() })
}

func filterProcesses(rows []*netrav1.ProcessSample, f time.Time) ([]*netrav1.ProcessSample, int) {
	return filterByTs(rows, f, func(r *netrav1.ProcessSample) int64 { return r.GetTsMs() })
}

func filterCollectors(rows []*netrav1.CollectorSample, f time.Time) ([]*netrav1.CollectorSample, int) {
	return filterByTs(rows, f, func(r *netrav1.CollectorSample) int64 { return r.GetTsMs() })
}

func filterEvents(rows []*netrav1.Event, f time.Time) ([]*netrav1.Event, int) {
	return filterByTs(rows, f, func(r *netrav1.Event) int64 { return r.GetTsMs() })
}

func filterUnitEvents(rows []*netrav1.SystemdUnitEvent, f time.Time) ([]*netrav1.SystemdUnitEvent, int) {
	return filterByTs(rows, f, func(r *netrav1.SystemdUnitEvent) int64 { return r.GetTsMs() })
}

func filterPackageEvents(rows []*netrav1.PackageEvent, f time.Time) ([]*netrav1.PackageEvent, int) {
	return filterByTs(rows, f, func(r *netrav1.PackageEvent) int64 { return r.GetTsMs() })
}
