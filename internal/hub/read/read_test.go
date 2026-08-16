package read_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/trick77/netra/internal/hub/read"
	"github.com/trick77/netra/internal/hub/store"
)

// newService brings up a migrated test database and a Service over it.
func newService(t *testing.T) (*read.Service, *pgxpool.Pool) {
	t.Helper()

	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return read.NewService(s.Pool()), s.Pool()
}

// seedHost registers a host and returns its id.
func seedHost(t *testing.T, pool *pgxpool.Pool, hostname string) int32 {
	t.Helper()

	var id int32
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO hosts (hostname) VALUES ($1) RETURNING id`, hostname).Scan(&id); err != nil {
		t.Fatalf("insert host %q: %v", hostname, err)
	}
	return id
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()

	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// The host list reads host_current and nothing else -- the whole point of that
// table (spec 8) is that the list stays cheap however much history sits behind
// it. A host that has never posted must come back with null gauges rather than
// zeros: 0% CPU on a machine never heard from is a much more misleading claim
// than "nothing yet".
func TestIntegrationListHostsReportsCurrentGaugesAndNullsForSilentHosts(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)

	live := seedHost(t, pool, "live")
	seedHost(t, pool, "silent")
	exec(t, pool, `
		INSERT INTO host_current (host_id, last_seen, cpu_total, mem_used, mem_total, uptime_s)
		VALUES ($1, now(), 12.5, 100, 200, 3600)`, live)

	hosts, err := svc.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("got %d hosts, want 2", len(hosts))
	}

	byName := map[string]read.HostSummary{}
	for _, h := range hosts {
		byName[h.Hostname] = h
	}

	got := byName["live"]
	if got.CPUTotal == nil || *got.CPUTotal != 12.5 {
		t.Errorf("live cpu_total = %v, want 12.5", got.CPUTotal)
	}
	if got.MemTotal == nil || *got.MemTotal != 200 {
		t.Errorf("live mem_total = %v, want 200", got.MemTotal)
	}

	silent := byName["silent"]
	if silent.CPUTotal != nil || silent.LastSeen != nil || silent.UptimeS != nil {
		t.Errorf("silent host = %+v, want every gauge null", silent)
	}
}

// Capabilities are the reason this endpoint exists apart from the list: they
// are the only way to tell "this host has no hwmon" from "the sensors
// collector never ran". Without them every NULL elsewhere in the API is
// ambiguous in exactly the way the agent went to trouble to avoid.
func TestIntegrationHostSurfacesCollectorCapabilities(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)

	id := seedHost(t, pool, "capable")
	exec(t, pool, `
		UPDATE hosts
		   SET kernel = '6.8.0', os_name = 'Debian 12', arch = 'amd64', cores = 8,
		       capabilities = '{"sensors":"unavailable","containers":"ok"}'::jsonb
		 WHERE id = $1`, id)

	host, err := svc.Host(ctx, id)
	if err != nil {
		t.Fatalf("Host: %v", err)
	}

	if got := host.Capabilities["sensors"]; got != "unavailable" {
		t.Errorf("capabilities[sensors] = %q, want %q", got, "unavailable")
	}
	if got := host.Capabilities["containers"]; got != "ok" {
		t.Errorf("capabilities[containers] = %q, want %q", got, "ok")
	}
	if host.Kernel == nil || *host.Kernel != "6.8.0" {
		t.Errorf("kernel = %v, want 6.8.0", host.Kernel)
	}
	if host.Cores == nil || *host.Cores != 8 {
		t.Errorf("cores = %v, want 8", host.Cores)
	}
}

// A host whose agent reported no capabilities carries {} rather than null:
// null would mean "we do not know what this agent can collect", which is
// precisely the ambiguity capabilities exist to remove.
func TestIntegrationHostWithoutCapabilitiesReportsAnEmptyObject(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)

	host, err := svc.Host(ctx, seedHost(t, pool, "fresh"))
	if err != nil {
		t.Fatalf("Host: %v", err)
	}
	if host.Capabilities == nil {
		t.Fatal("capabilities = nil, want an empty map")
	}
	if len(host.Capabilities) != 0 {
		t.Errorf("capabilities = %v, want empty", host.Capabilities)
	}
}

func TestIntegrationHostResolvesItsSiteAndProvider(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)

	var providerID, siteID int32
	if err := pool.QueryRow(ctx,
		`INSERT INTO providers (name) VALUES ('hetzner') RETURNING id`).Scan(&providerID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO sites (provider_id, name) VALUES ($1, 'fsn1') RETURNING id`,
		providerID).Scan(&siteID); err != nil {
		t.Fatalf("insert site: %v", err)
	}
	id := seedHost(t, pool, "sited")
	exec(t, pool, `UPDATE hosts SET site_id = $1 WHERE id = $2`, siteID, id)

	host, err := svc.Host(ctx, id)
	if err != nil {
		t.Fatalf("Host: %v", err)
	}
	if host.SiteName == nil || *host.SiteName != "fsn1" {
		t.Errorf("site_name = %v, want fsn1", host.SiteName)
	}
	if host.ProviderName == nil || *host.ProviderName != "hetzner" {
		t.Errorf("provider_name = %v, want hetzner", host.ProviderName)
	}
}

func TestIntegrationHostIsNotFoundForAnUnknownID(t *testing.T) {
	svc, _ := newService(t)

	if _, err := svc.Host(context.Background(), 4242); !errors.Is(err, read.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A host with no containers and a host that does not exist are different
// facts. Conflating them -- returning [] for both -- would make "is this host
// registered?" unanswerable through the API.
func TestIntegrationDimensionListingsSeparateEmptyFromMissing(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "bare")

	t.Run("a registered host with nothing returns an empty list", func(t *testing.T) {
		got, err := svc.Containers(ctx, id)
		if err != nil {
			t.Fatalf("Containers: %v", err)
		}
		if got == nil {
			t.Fatal("containers = nil, want an empty slice so it renders as [] rather than null")
		}
		if len(got) != 0 {
			t.Errorf("containers = %v, want empty", got)
		}
	})

	t.Run("an unknown host is not found", func(t *testing.T) {
		for name, call := range map[string]func() error{
			"containers":  func() error { _, err := svc.Containers(ctx, 4242); return err },
			"filesystems": func() error { _, err := svc.Filesystems(ctx, 4242); return err },
			"addresses":   func() error { _, err := svc.Addresses(ctx, 4242); return err },
			"packages":    func() error { _, err := svc.Packages(ctx, 4242); return err },
			"units":       func() error { _, err := svc.Units(ctx, 4242); return err },
		} {
			if err := call(); !errors.Is(err, read.ErrNotFound) {
				t.Errorf("%s: err = %v, want ErrNotFound", name, err)
			}
		}
	})
}

func TestIntegrationDimensionListingsProjectTheirTables(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "stocked")

	exec(t, pool, `
		INSERT INTO containers (host_id, container_key, name, image, is_agent)
		VALUES ($1, 'proj/web', 'web-1', 'nginx:1.27', FALSE),
		       ($1, 'proj/netra', 'netra-agent', 'ghcr.io/trick77/netra-agent', TRUE)`, id)
	exec(t, pool, `
		INSERT INTO filesystems (host_id, label, mountpoint, device_id)
		VALUES ($1, 'root', '/', 2049)`, id)
	exec(t, pool, `
		INSERT INTO host_addresses (host_id, iface, if_index, address, family, scope)
		VALUES ($1, 'eth0', 2, '10.0.0.5', 4, 'private'),
		       ($1, 'eth0', 2, '2001:db8::1', 6, 'public')`, id)
	exec(t, pool, `
		INSERT INTO host_packages (host_id, name, version, arch, format, size_bytes)
		VALUES ($1, 'bash', '5.2.15', 'amd64', 'dpkg', 1234)`, id)

	t.Run("containers keep the is_agent flag", func(t *testing.T) {
		got, err := svc.Containers(ctx, id)
		if err != nil {
			t.Fatalf("Containers: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d containers, want 2", len(got))
		}
		// Ordered by container_key: proj/netra before proj/web.
		if !got[0].IsAgent {
			t.Errorf("%q is_agent = false; a fleet view that cannot separate netra's own "+
				"container reports the monitoring as part of the workload", got[0].Key)
		}
		if got[1].IsAgent {
			t.Errorf("%q is_agent = true, want false", got[1].Key)
		}
	})

	t.Run("addresses carry the scope the hub derived", func(t *testing.T) {
		got, err := svc.Addresses(ctx, id)
		if err != nil {
			t.Fatalf("Addresses: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d addresses, want 2", len(got))
		}
		// host(address), not the inet itself: rendering the column directly
		// would append a /32 the agent never sent.
		for _, a := range got {
			if a.Address == "10.0.0.5" && (a.Scope == nil || *a.Scope != "private") {
				t.Errorf("10.0.0.5 scope = %v, want private", a.Scope)
			}
			if a.Address == "10.0.0.5/32" {
				t.Errorf("address = %q, want the bare address the agent reported", a.Address)
			}
		}
	})

	t.Run("filesystems expose the mountpoint and st_dev", func(t *testing.T) {
		got, err := svc.Filesystems(ctx, id)
		if err != nil {
			t.Fatalf("Filesystems: %v", err)
		}
		if len(got) != 1 || got[0].Label != "root" {
			t.Fatalf("filesystems = %+v, want one labelled root", got)
		}
		if got[0].Mountpoint == nil || *got[0].Mountpoint != "/" {
			t.Errorf("mountpoint = %v, want /", got[0].Mountpoint)
		}
	})

	t.Run("packages carry version, arch and format", func(t *testing.T) {
		got, err := svc.Packages(ctx, id)
		if err != nil {
			t.Fatalf("Packages: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d packages, want 1", len(got))
		}
		if got[0].Version != "5.2.15" || got[0].Format != "dpkg" {
			t.Errorf("package = %+v, want bash 5.2.15 from dpkg", got[0])
		}
	})
}

// Units answers what NEEDS ATTENTION, not what the host runs.
//
// The state comes off the systemd_units row rather than the newest event, and
// a unit whose state is ordinary is left out entirely -- a healthy host runs
// 300-400 services and listing them buries the row an operator opened the page
// for. See internal/hub/systemdstate.
func TestIntegrationUnitsListOnlyWhatNeedsAttention(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "systemd")

	exec(t, pool, `
		INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
		VALUES ($1, 'ssh.service',     'active',     'running',      now() - INTERVAL '1 hour'),
		       ($1, 'exim4.service',   'failed',     'failed',       now() - INTERVAL '2 hours'),
		       ($1, 'backup.service',  'activating', 'auto-restart', now() - INTERVAL '3 hours'),
		       ($1, 'oneshot.service', 'inactive',   'dead',         now() - INTERVAL '4 hours'),
		       ($1, 'quiet.service',   NULL,         NULL,           NULL)`, id)

	got, err := svc.Units(ctx, id)
	if err != nil {
		t.Fatalf("Units: %v", err)
	}

	byName := map[string]read.Unit{}
	for _, u := range got {
		byName[u.Name] = u
	}

	// A failed unit, and a unit looping in restart backoff. auto-restart is
	// matched on SUBSTATE: systemd reports a service inside its backoff window
	// as `activating`, so a state-only rule would miss a restart loop entirely.
	if len(got) != 2 {
		t.Fatalf("got %d units, want exim4.service and backup.service", len(got))
	}

	exim := byName["exim4.service"]
	if exim.State == nil || *exim.State != "failed" {
		t.Errorf("exim4.service state = %v, want failed", exim.State)
	}
	// Since is the ONSET of the state, which is what makes it usable to tell a
	// unit stuck in a loop from one caught mid-restart.
	if exim.Since == nil {
		t.Error("exim4.service since = nil, want the state's onset")
	}
	if _, ok := byName["backup.service"]; !ok {
		t.Error("backup.service is missing: a unit in auto-restart is looping, and " +
			"its ActiveState alone never says so")
	}

	// The ordinary states, and the unknown one. A unit with no state recorded
	// is not evidence of a problem, so it is not shown -- and it is not
	// claimed to be healthy either.
	for _, name := range []string{"ssh.service", "oneshot.service", "quiet.service"} {
		if _, ok := byName[name]; ok {
			t.Errorf("%s is listed; a unit nobody needs to act on must not be", name)
		}
	}
}

// A unit that is broken without ever LOOKING broken.
//
// A service that runs for a few minutes, dies and comes back is `active` at
// nearly every scrape, and systemd never escalates it to `failed` because it
// never trips the start limit. No snapshot of its current state can reveal it,
// which is why the units query counts transitions as well as reading columns.
func TestIntegrationUnitsListAUnitThatKeepsRestarting(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "flapping")

	var flappy, steady int32
	if err := pool.QueryRow(ctx,
		`INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
		 VALUES ($1, 'backup.service', 'active', 'running', now()) RETURNING id`,
		id).Scan(&flappy); err != nil {
		t.Fatalf("insert flappy: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
		 VALUES ($1, 'ssh.service', 'active', 'running', now()) RETURNING id`,
		id).Scan(&steady); err != nil {
		t.Fatalf("insert steady: %v", err)
	}

	// Six transitions in the last hour: up, down, up, down, up, down.
	exec(t, pool, `
		INSERT INTO systemd_unit_events (host_id, unit_id, ts, state, substate)
		SELECT $1, $2, now() - (g || ' minutes')::interval,
		       CASE WHEN g % 2 = 0 THEN 'active' ELSE 'failed' END, 'x'
		  FROM generate_series(1, 6) AS g`, id, flappy)

	// The steady unit restarted once, hours ago -- outside the window, and
	// nowhere near the threshold either.
	exec(t, pool, `
		INSERT INTO systemd_unit_events (host_id, unit_id, ts, state, substate)
		VALUES ($1, $2, now() - INTERVAL '5 hours', 'active', 'running')`, id, steady)

	got, err := svc.Units(ctx, id)
	if err != nil {
		t.Fatalf("Units: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d units, want backup.service alone -- ssh.service is quiet and "+
			"backup.service is only visible through its history", len(got))
	}
	if got[0].Name != "backup.service" {
		t.Fatalf("listed %s, want backup.service", got[0].Name)
	}
	if got[0].Restarts1h < 6 {
		t.Errorf("restarts_1h = %d, want at least 6 -- the count is what the page "+
			"puts in the warning", got[0].Restarts1h)
	}
	// Its CURRENT state is perfectly healthy, which is the whole point.
	if got[0].State == nil || *got[0].State != "active" {
		t.Errorf("state = %v, want active; a unit that looks fine right now is exactly "+
			"the one this rule exists to catch", got[0].State)
	}
}

func TestIntegrationEventsFilterByHostTypeAndWindow(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()

	a := seedHost(t, pool, "event-a")
	b := seedHost(t, pool, "event-b")
	exec(t, pool, `
		INSERT INTO events (host_id, ts, type, subject, detail)
		VALUES ($1, now() - INTERVAL '1 hour',  'mdraid_degraded', 'md0', '{"state":"degraded"}'),
		       ($1, now() - INTERVAL '2 hours', 'agent_upgrade',   NULL,  '{}'),
		       ($1, now() - INTERVAL '40 days', 'mdraid_degraded', 'md1', '{}')`, a)
	exec(t, pool, `
		INSERT INTO events (host_id, ts, type) VALUES ($1, now() - INTERVAL '30 minutes', 'agent_upgrade')`, b)

	t.Run("the default window is the last day", func(t *testing.T) {
		got, err := svc.Events(ctx, read.EventQuery{HostID: a}, now)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d events, want 2 -- the 40-day-old one is outside the default window", len(got))
		}
		// Newest first.
		if got[0].Type != "mdraid_degraded" {
			t.Errorf("first event = %q, want the newest", got[0].Type)
		}
		if got[0].Subject == nil || *got[0].Subject != "md0" {
			t.Errorf("subject = %v, want md0", got[0].Subject)
		}
		if got[1].Subject != nil {
			t.Errorf("agent_upgrade subject = %v, want null -- it is about the host as a whole", got[1].Subject)
		}
	})

	t.Run("a type filter narrows it", func(t *testing.T) {
		got, err := svc.Events(ctx, read.EventQuery{HostID: a, Type: "agent_upgrade"}, now)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		if len(got) != 1 || got[0].Type != "agent_upgrade" {
			t.Fatalf("events = %+v, want the one agent_upgrade", got)
		}
	})

	t.Run("no host filter reads the whole fleet", func(t *testing.T) {
		got, err := svc.Events(ctx, read.EventQuery{}, now)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d events, want 3 across both hosts", len(got))
		}
		hostnames := map[string]bool{}
		for _, e := range got {
			hostnames[e.Hostname] = true
		}
		if !hostnames["event-a"] || !hostnames["event-b"] {
			t.Errorf("hostnames = %v, want both hosts named", hostnames)
		}
	})

	t.Run("since reaches further back", func(t *testing.T) {
		got, err := svc.Events(ctx, read.EventQuery{
			HostID: a, Since: now.Add(-90 * 24 * time.Hour)}, now)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d events, want all 3", len(got))
		}
	})

	t.Run("an unknown host is not found", func(t *testing.T) {
		if _, err := svc.Events(ctx, read.EventQuery{HostID: 4242}, now); !errors.Is(err, read.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("an inverted window is rejected", func(t *testing.T) {
		_, err := svc.Events(ctx, read.EventQuery{
			Since: now, Until: now.Add(-time.Hour)}, now)
		if !errors.Is(err, read.ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
}

// The limit is clamped rather than rejected: a caller asking for more than the
// cap wants as much as they can have, and newest-first ordering means the rows
// they get are the ones they care about.
func TestIntegrationEventsLimitIsAppliedAndClamped(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "chatty")

	exec(t, pool, `
		INSERT INTO events (host_id, ts, type)
		SELECT $1, now() - (n || ' minutes')::INTERVAL, 'agent_upgrade'
		  FROM generate_series(1, 10) AS n`, id)

	got, err := svc.Events(ctx, read.EventQuery{HostID: id, Limit: 3}, now)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}

	all, err := svc.Events(ctx, read.EventQuery{HostID: id, Limit: 1_000_000}, now)
	if err != nil {
		t.Fatalf("Events with an over-large limit: %v", err)
	}
	if len(all) != 10 {
		t.Errorf("got %d events, want all 10 -- an over-large limit is clamped, not rejected", len(all))
	}
}
