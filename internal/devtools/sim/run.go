package sim

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// The batch bounds mirror the agent's. maxBatchRows is below the agent's
// 20000 because the simulator's rows carry more optional fields than a real
// scrape usually does, and maxBodyBytes is the hub's cap on a POST body --
// exceeding it is a 413 that no retry would fix.
const (
	maxBatchRows = 12000
	maxBodyBytes = 4 << 20
	// bodyHeadroom keeps a batch clear of the hub's cap even when the size
	// estimate is taken before the last scrape is appended.
	bodyHeadroom = 256 << 10
)

// rawRetention is how long the hub keeps raw samples. Beyond it the simulator
// drops to a 5-minute grid: those rows are deleted by the retention policy
// within a day regardless, and only the 5m bucket they were rolled into
// survives -- so generating them per minute is five times the work for
// identical surviving history.
const rawRetention = 7 * 24 * time.Hour

// Config parameterises one simulator run.
type Config struct {
	Seed     uint64
	Backfill time.Duration
	Live     bool
	Fresh    bool

	// Now is injectable so a test can pin the window. Nil means time.Now.
	Now func() time.Time

	// Refresher materialises the continuous aggregates behind the backfill.
	// Nil when no DSN was given, in which case only the last few hours roll
	// up, via the hub's own scheduled policies.
	Refresher *Refresher

	Log *slog.Logger
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// host is one simulated machine mid-run: its hub identity, its generator, and
// the batch it is accumulating.
type host struct {
	profile *Profile
	id      int32
	token   string
	gen     *Generator

	seq      uint64
	meta     *netrav1.Metadata
	metaHash []byte
	// sendMeta is set when the hub answers request_metadata, and cleared once
	// the full block has gone out. The hub only writes cpu_model, kernel and
	// capabilities when it receives that block, so a simulator that skipped
	// this handshake would leave every descriptive column NULL.
	//
	// It starts TRUE, which is where this differs from the real agent. The
	// agent posts a hash first and attaches the block when asked, because it
	// runs forever and the next scrape is a minute away. A simulator run can
	// be a single POST -- a short backfill of a small host is one batch --
	// and request_metadata on the response to the last POST can never be
	// acted on. Sending it up front costs one block per host and makes the
	// descriptive columns land whatever the run looks like.
	sendMeta bool

	pending     []*Scrape
	pendingRows int
}

// Run provisions the fleet, backfills the window and optionally keeps
// ticking. It is the whole simulator; everything else builds the data it
// posts.
func Run(ctx context.Context, hub *Hub, profiles []*Profile, cfg Config) error {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}

	now := cfg.now().Truncate(time.Minute)
	// Aligned to the coarse step, not just to the minute. The grid advances
	// from `from`, so an origin at HH:31 puts every coarse-region sample on
	// :31, :36, :41 -- never on the hour and never on a five-minute boundary.
	// The families gated on those boundaries (SMART hourly, collector health
	// every five minutes) then never fire outside the fine-grained window,
	// which left smart_attributes holding seven days of a ninety-day run.
	from := now.Add(-cfg.Backfill).Truncate(coarseStep)

	hosts, err := provision(ctx, hub, profiles, cfg, from, now, log)
	if err != nil {
		return err
	}

	if cfg.Backfill > 0 {
		if err := backfill(ctx, hub, hosts, cfg, from, now, log); err != nil {
			return err
		}
	}
	if !cfg.Live {
		return nil
	}
	return live(ctx, hub, hosts, cfg, log)
}

// provision registers every simulated host, creating its site and provider
// first so the fleet has a dimension hierarchy behind it rather than a set of
// hosts hanging off nothing.
func provision(ctx context.Context, hub *Hub, profiles []*Profile, cfg Config, from, to time.Time, log *slog.Logger) ([]*host, error) {
	if cfg.Fresh {
		existing, err := hub.ListHosts(ctx)
		if err != nil {
			return nil, err
		}
		wanted := map[string]bool{}
		for _, p := range profiles {
			wanted[p.Hostname] = true
		}
		for _, h := range existing {
			if !wanted[h.Hostname] {
				continue
			}
			log.Info("deleting simulated host and all of its history", "hostname", h.Hostname, "id", h.ID)
			if err := hub.DeleteHost(ctx, h.ID, h.Hostname); err != nil {
				return nil, err
			}
		}
	}

	hosts := make([]*host, 0, len(profiles))
	for _, p := range profiles {
		id, token, err := hub.EnsureHost(ctx, p.Hostname)
		if err != nil {
			return nil, fmt.Errorf("host %s: %w", p.Hostname, err)
		}

		meta := p.Metadata("sim", "sim", "simulated")
		hosts = append(hosts, &host{
			profile:  p,
			id:       id,
			token:    token,
			gen:      NewGenerator(p, cfg.Seed, from, to),
			meta:     meta,
			metaHash: HashMetadata(meta),
			sendMeta: true,
		})
		// Where the host is rides its metadata now, the way a real agent
		// reports it -- see Profile.Metadata, which has always carried
		// Location/Provider/Facility even while the hub discarded them.
		log.Info("simulating host", "hostname", p.Hostname, "id", id,
			"location", p.Location)
	}
	return hosts, nil
}

// backfill walks the window oldest-first, flushing each host's batch as it
// fills and materialising the continuous aggregates segment by segment.
//
// Oldest-first is not arbitrary. host_current's upsert only accepts a sample
// newer than the one it holds, so walking forwards leaves every host's "last
// seen" tile correct with no extra work -- and walking backwards would leave
// all of them pinned to the oldest sample in the window.
func backfill(ctx context.Context, hub *Hub, hosts []*host, cfg Config, from, to time.Time, log *slog.Logger) error {
	segments := refreshSegments(from, to)
	seg := 0
	started := time.Now()
	var scrapes int

	for ts := from; ts.Before(to); ts = ts.Add(gridStep(ts, to)) {
		for seg < len(segments) && !ts.Before(segments[seg].to) {
			if err := checkpoint(ctx, hub, hosts, cfg, segments[seg], log); err != nil {
				return err
			}
			seg++
		}

		opt := optionsFor(ts, from, to)
		for _, h := range hosts {
			h.append(h.gen.Scrape(ts, opt))
			if h.pendingRows >= maxBatchRows {
				if err := h.flush(ctx, hub, true); err != nil {
					return err
				}
			}
		}
		scrapes++
		if scrapes%2000 == 0 {
			log.Info("backfilling", "at", ts.Format(time.RFC3339), "remaining", to.Sub(ts).Round(time.Hour).String())
		}
	}

	for ; seg < len(segments); seg++ {
		if err := checkpoint(ctx, hub, hosts, cfg, segments[seg], log); err != nil {
			return err
		}
	}
	log.Info("backfill complete", "scrapes", scrapes, "took", time.Since(started).Round(time.Second).String())
	return nil
}

// checkpoint flushes every host and then materialises the aggregates for the
// segment just completed, so no wide range of raw rows is ever left waiting
// for a refresh that the retention policy might beat.
func checkpoint(ctx context.Context, hub *Hub, hosts []*host, cfg Config, seg window, log *slog.Logger) error {
	for _, h := range hosts {
		if err := h.flush(ctx, hub, true); err != nil {
			return err
		}
	}
	if cfg.Refresher == nil {
		return nil
	}
	log.Info("refreshing aggregates", "from", seg.from.Format(time.RFC3339), "to", seg.to.Format(time.RFC3339))
	return cfg.Refresher.Refresh(ctx, seg.from, seg.to)
}

// live keeps posting one scrape per host per minute, which is what the fleet
// looks like from the hub's point of view once the history is in place.
func live(ctx context.Context, hub *Hub, hosts []*host, cfg Config, log *slog.Logger) error {
	log.Info("live mode: posting one scrape per host every 60s")
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		ts := cfg.now().Truncate(time.Minute)
		for _, h := range hosts {
			h.append(h.gen.Scrape(ts, Options{Smart: ts.Minute() == 0, Collectors: true, Inventory: ts.Minute()%30 == 0}))
			if err := h.flush(ctx, hub, false); err != nil {
				// A live tick that fails is not fatal: the hub may be
				// restarting, and the next tick carries the next sample. The
				// simulator has no ring buffer to replay from, so the gap is
				// simply a gap -- which is itself a useful thing to have in
				// the data.
				log.Warn("live post failed", "hostname", h.profile.Hostname, "err", err)
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (h *host) append(s *Scrape) {
	h.pending = append(h.pending, s)
	h.pendingRows += s.Rows()
}

// flush posts everything this host has accumulated, splitting the batch if it
// would exceed the hub's body cap.
func (h *host) flush(ctx context.Context, hub *Hub, isBackfill bool) error {
	if len(h.pending) == 0 {
		return nil
	}
	pending := h.pending
	h.pending = nil
	h.pendingRows = 0
	return h.post(ctx, hub, pending, isBackfill)
}

func (h *host) post(ctx context.Context, hub *Hub, scrapes []*Scrape, isBackfill bool) error {
	if len(scrapes) == 0 {
		return nil
	}

	req := h.request(scrapes, isBackfill)
	if proto.Size(req) > maxBodyBytes-bodyHeadroom && len(scrapes) > 1 {
		// Halving rather than estimating per scrape: the split is rare, and
		// a size estimate accurate enough to avoid it would have to marshal
		// every scrape twice.
		//
		// req is discarded here, so everything request() consumed while
		// building it has to be given back first. sendMeta in particular: it
		// is one-shot, and leaving it cleared would drop the metadata block
		// this run was going to send -- the block only goes out again if the
		// hub asks for it, which it cannot do if this was the last POST.
		if req.GetMetadata() != nil {
			h.sendMeta = true
		}
		h.seq--
		mid := len(scrapes) / 2
		if err := h.post(ctx, hub, scrapes[:mid], isBackfill); err != nil {
			return err
		}
		return h.post(ctx, hub, scrapes[mid:], isBackfill)
	}

	resp, err := hub.Ingest(ctx, h.token, req)
	if err != nil {
		// The id is in the message because it is what a follow-up psql query
		// needs; the hostname alone means another lookup.
		return fmt.Errorf("%s (host_id %d): %w", h.profile.Hostname, h.id, err)
	}
	if resp.GetRequestMetadata() {
		h.sendMeta = true
	}
	return nil
}

// request packs scrapes into one IngestRequest. The per-entity families ride
// on the request rather than on a host sample because a batch spans many
// scrapes and each row carries its own timestamp.
func (h *host) request(scrapes []*Scrape, isBackfill bool) *netrav1.IngestRequest {
	h.seq++
	req := &netrav1.IngestRequest{
		Seq:          h.seq,
		MetadataHash: h.metaHash,
		Backfill:     isBackfill,
	}
	if h.sendMeta {
		req.Metadata = h.meta
		h.sendMeta = false
	}

	for _, s := range scrapes {
		if s.Host != nil {
			req.HostSamples = append(req.HostSamples, s.Host)
		}
		req.CpuCores = append(req.CpuCores, s.Cores...)
		req.DiskIo = append(req.DiskIo, s.Disks...)
		req.Sensors = append(req.Sensors, s.Sensors...)
		req.Net = append(req.Net, s.Nets...)
		req.Collectors = append(req.Collectors, s.Collectors...)
		req.Events = append(req.Events, s.Events...)
		req.Containers = append(req.Containers, s.Containers...)
		req.Filesystems = append(req.Filesystems, s.Filesystems...)
		req.Smart = append(req.Smart, s.Smart...)
		req.SystemdEvents = append(req.SystemdEvents, s.SystemdEvents...)
		req.PackageEvents = append(req.PackageEvents, s.PackageEvents...)

		// Inventory is a whole-set replacement hub-side, so the newest set in
		// the batch wins rather than every set being concatenated.
		if len(s.Addresses) > 0 {
			req.Addresses = s.Addresses
		}
		if len(s.Interfaces) > 0 {
			req.Interfaces = s.Interfaces
		}
		if len(s.Packages) > 0 {
			req.Packages = s.Packages
		}

		// Newest snapshot wins, for the reason the agent supersedes rather
		// than buffers them: a snapshot states the present, so an older one
		// carried alongside it is not history, it is a stale answer that would
		// be applied second and undo the fresher one.
		if s.SystemdSnapshot != nil {
			req.SystemdSnapshot = s.SystemdSnapshot
		}
	}
	return req
}

// coarseStep is the grid outside the raw retention window, and the alignment
// every backfill origin is snapped to.
const coarseStep = 5 * time.Minute

// gridStep is 60s inside the raw retention window and coarseStep before it.
func gridStep(ts, now time.Time) time.Duration {
	if ts.Before(now.Add(-rawRetention)) {
		return coarseStep
	}
	return time.Minute
}

// optionsFor decides which of the expensive families this instant carries.
func optionsFor(ts, from, now time.Time) Options {
	return Options{
		// Hourly, like the real agent's self-gated SMART collector: reading
		// SMART spins up sleeping drives, and the values change over days.
		Smart: ts.Truncate(time.Hour).Equal(ts),
		// Every instant, which is the real agent's cadence: a collector
		// reports its own health on each scrape.
		//
		// It used to be every five minutes, which is the least that fills
		// the 5m and 1h tiers -- but the Device availability panel puts the
		// `ok` column on the WINDOW's grid, so at the 1h and 6h ranges
		// (raw tier, 60s step) a 5m cadence became one reading per five
		// nulls: a row of isolated dots on the one panel whose job is
		// showing a continuous up/down line. Production never looks like
		// that, so neither should the dataset this is developed against.
		//
		// The extra cost is bounded by the raw retention window rather than
		// the backfill: outside it gridStep is already coarseStep, so those
		// instants are unchanged, and only the last 7 days go from 5m to
		// 60s. On a 90-day run that is roughly a third more rows in what is
		// already the largest table -- around 1.6M, more than
		// cpu_core_samples, since the fleet reports 63 collectors. Knowingly
		// accepted: this table has no rows at all in production, so the
		// simulator is the only place its UI can be developed at all.
		Collectors: true,
		// Inventory describes what the host HAS rather than what it
		// measured, so it goes out daily instead of on every scrape --
		// re-sending it per minute would rewrite the whole package list a
		// hundred thousand times.
		//
		// The first and last instants are included explicitly, not as
		// belt-and-braces. A midnight-only rule leaves host_addresses and
		// host_packages EMPTY for any window that does not span one, which is
		// every short run; and without the last instant, last_seen on the
		// inventory rows would trail the rest of the history by up to a day.
		Inventory: ts.Equal(from) ||
			ts.Truncate(24*time.Hour).Equal(ts) ||
			!ts.Add(2*gridStep(ts, now)).Before(now),
	}
}

// window is one refresh segment.
type window struct {
	from time.Time
	to   time.Time
}

// coarseRefreshSegment is how much backfill is posted before the aggregates
// are materialised over it.
//
// It is deliberately well under rawRetention rather than equal to it. Every
// row written in the coarse region is ALREADY older than the 7-day drop
// threshold at the moment it is inserted, so the only thing standing between
// it and the retention job is how long it sits unmaterialised. A 7-day
// segment left exactly zero margin: a retention job firing mid-backfill would
// delete chunks before the 5m tier ever read them, the 1h tier would inherit
// the hole, and the run would still report "backfill complete" with no raw
// rows left to rebuild from.
const coarseRefreshSegment = 2 * 24 * time.Hour

// refreshSegments splits the backfill into the ranges the aggregates are
// materialised over: two-day chunks across the coarse region, daily across
// the recent raw one.
//
// Coarser than per-day everywhere because refresh_continuous_aggregate plans
// and locks per call, and finer than the retention threshold for the reason
// above. The inserts dominate a run's wall clock either way.
func refreshSegments(from, to time.Time) []window {
	var out []window
	coarseEnd := to.Add(-rawRetention)
	for ts := from; ts.Before(coarseEnd); {
		next := ts.Add(coarseRefreshSegment)
		if next.After(coarseEnd) {
			next = coarseEnd
		}
		out = append(out, window{from: ts, to: next})
		ts = next
	}
	for ts := maxTime(from, coarseEnd); ts.Before(to); {
		next := ts.Add(24 * time.Hour)
		if next.After(to) {
			next = to
		}
		out = append(out, window{from: ts, to: next})
		ts = next
	}
	return out
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
