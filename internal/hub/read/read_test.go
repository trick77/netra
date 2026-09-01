package read_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
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

// Every GAUGE on the host list comes from host_current -- the whole point of
// that table (spec 8) is that the list stays cheap however much history sits
// behind it. (The one other table it touches is systemd_units, for the failed
// unit NAMES, which is a plain table read through a unique index and is
// covered below.) A host that has never posted must come back with null
// gauges rather than
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

// The fleet overview's failed-unit count, and specifically the difference
// between "nothing is wrong" and "nobody has looked".
//
// The count is the agent's own services_failed summary carried forward on
// host_current, NOT a derivation from systemd_unit_events -- see migration
// 0003. The two facts that rules the event log out are worth restating,
// because both look fine in a hand-seeded test and fail in production: the
// collector emits a unit event only for a unit already failed at its first
// scrape or on a transition, so a healthy host has NO unit rows at all; and
// the event log is pruned at 90 days, so a long-failed unit loses the event
// that proves it.
//
// This test therefore seeds host_current the way ingest writes it, and never
// touches systemd_units.
func TestIntegrationListHostsReportsFailedServicesAndKnowsWhenItHasNotLooked(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)

	seedCurrent := func(hostname string, servicesFailed any) {
		id := seedHost(t, pool, hostname)
		exec(t, pool, `
			INSERT INTO host_current (host_id, last_seen, services_failed)
			VALUES ($1, now(), $2)`, id, servicesFailed)
	}

	seedCurrent("broken", 2)
	// A host running systemd with nothing wrong. This is the case a
	// systemd_unit_events derivation got wrong: it emits no events, so the
	// event log cannot tell it apart from "unwatched" below.
	seedCurrent("healthy", 0)
	// A host with no systemd at all -- the agent sends no summary and the
	// column stays NULL.
	seedCurrent("nosystemd", nil)
	// Registered, never posted: no host_current row whatsoever.
	seedHost(t, pool, "unwatched")

	hosts, err := svc.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	byName := map[string]read.HostSummary{}
	for _, h := range hosts {
		byName[h.Hostname] = h
	}

	for _, tc := range []struct {
		host string
		want *int32
	}{
		{"broken", ptr(int32(2))},
		{"healthy", ptr(int32(0))},
		{"nosystemd", nil},
		{"unwatched", nil},
	} {
		// Presence first: byName returns a zero HostSummary for a missing key,
		// whose ServicesFailed is nil, so a nil expectation would otherwise
		// pass even if the join had dropped the row entirely.
		got, ok := byName[tc.host]
		if !ok {
			t.Errorf("%s missing from ListHosts", tc.host)
			continue
		}
		switch {
		case tc.want == nil && got.ServicesFailed != nil:
			t.Errorf("%s services_failed = %d, want null -- netra has not looked",
				tc.host, *got.ServicesFailed)
		case tc.want != nil && got.ServicesFailed == nil:
			t.Errorf("%s services_failed = null, want %d", tc.host, *tc.want)
		case tc.want != nil && got.ServicesFailed != nil && *got.ServicesFailed != *tc.want:
			t.Errorf("%s services_failed = %d, want %d",
				tc.host, *got.ServicesFailed, *tc.want)
		}
	}
}

// The names behind the count, and the ways the two are allowed to disagree.
//
// FailedUnits is read from systemd_units while the count beside it comes from
// the agent's summary on host_current, so this test seeds them independently
// -- including the case where a host has a count and no unit rows at all,
// which is what a host heard from once looks like and what the fleet band has
// to keep rendering as a bare count.
func TestIntegrationListHostsNamesTheFailedUnitsBehindTheCount(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)

	seedUnits := func(hostname string, servicesFailed int, units map[string]string) int32 {
		id := seedHost(t, pool, hostname)
		exec(t, pool, `
			INSERT INTO host_current (host_id, last_seen, services_failed)
			VALUES ($1, now(), $2)`, id, servicesFailed)
		for name, state := range units {
			exec(t, pool, `
				INSERT INTO systemd_units (host_id, unit_name, state)
				VALUES ($1, $2, $3)`, id, name, state)
		}
		return id
	}

	seedUnits("one", 1, map[string]string{
		"docker.service": "failed",
		// Not failed, so not named: these are the 300-odd healthy units every
		// host runs, and naming one here would be the band reporting a unit
		// that is fine.
		"sshd.service": "active",
	})
	// More failures than the list names. The cap is what keeps a host
	// mid-cascade from taking forty lines of a band that shows one line per
	// condition.
	seedUnits("many", 5, map[string]string{
		"a.service": "failed", "b.service": "failed", "c.service": "failed",
		"d.service": "failed", "e.service": "failed",
	})
	// A summary and no unit rows: the hub knows the count and cannot name a
	// single one of them.
	seedUnits("unnamed", 2, nil)
	seedUnits("healthy", 0, map[string]string{"sshd.service": "active"})

	hosts, err := svc.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	byName := map[string]read.HostSummary{}
	for _, h := range hosts {
		byName[h.Hostname] = h
	}

	for _, tc := range []struct {
		host string
		want []string
	}{
		{"one", []string{"docker.service"}},
		// Alphabetical and capped at three -- a stable answer, not whichever
		// three rows the planner happened to reach first.
		{"many", []string{"a.service", "b.service", "c.service"}},
		{"unnamed", []string{}},
		{"healthy", []string{}},
	} {
		got, ok := byName[tc.host]
		if !ok {
			t.Errorf("%s missing from ListHosts", tc.host)
			continue
		}
		if len(got.FailedUnits) != len(tc.want) {
			t.Errorf("%s failed_units = %v, want %v", tc.host, got.FailedUnits, tc.want)
			continue
		}
		for i, name := range tc.want {
			if got.FailedUnits[i] != name {
				t.Errorf("%s failed_units[%d] = %q, want %q",
					tc.host, i, got.FailedUnits[i], name)
			}
		}
		// Never null: a JSON null here would read as a third state next to
		// "named" and "cannot name", and there is no third state.
		if got.FailedUnits == nil {
			t.Errorf("%s failed_units is null, want an empty list", tc.host)
		}
	}
}

// The onset behind the count: one timestamp per host, the OLDEST of its
// failed units.
//
// Five units failing at five different times is one condition that started
// when the first of them went, and that is the number the fleet list prints
// in its Since column. The cases that matter are the ones where a plausible
// wrong answer exists: a host whose newest failure is recent (the minimum has
// to win), a host whose units carry no state_ts at all (null, not now()), and
// a host with a count and no unit rows (also null).
func TestIntegrationListHostsDatesTheOldestFailedUnit(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)

	old := time.Date(2026, 8, 10, 6, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)

	spread := seedHost(t, pool, "spread")
	exec(t, pool, `
		INSERT INTO host_current (host_id, last_seen, services_failed)
		VALUES ($1, now(), 3)`, spread)
	exec(t, pool, `
		INSERT INTO systemd_units (host_id, unit_name, state, state_ts)
		VALUES ($1, 'a.service', 'failed', $2),
		       ($1, 'b.service', 'failed', $3),
		       ($1, 'c.service', 'failed', $3)`, spread, old, recent)
	// A healthy unit that entered its state before any of the failures. It
	// must not date the condition: NotableSQL is inside the aggregate, so
	// this row is never seen by the min at all.
	exec(t, pool, `
		INSERT INTO systemd_units (host_id, unit_name, state, state_ts)
		VALUES ($1, 'sshd.service', 'active', $2)`,
		spread, old.Add(-72*time.Hour))

	// Four failures, and the list only NAMES three. The onset is taken over
	// every notable unit, so the one that started first still dates the
	// condition even though its name never appears.
	beyond := seedHost(t, pool, "beyond-the-cap")
	exec(t, pool, `
		INSERT INTO host_current (host_id, last_seen, services_failed)
		VALUES ($1, now(), 4)`, beyond)
	exec(t, pool, `
		INSERT INTO systemd_units (host_id, unit_name, state, state_ts)
		VALUES ($1, 'a.service', 'failed', $3),
		       ($1, 'b.service', 'failed', $3),
		       ($1, 'c.service', 'failed', $3),
		       ($1, 'z.service', 'failed', $2)`, beyond, old, recent)

	// state_ts is nullable: a unit the agent reported without one.
	undated := seedHost(t, pool, "undated")
	exec(t, pool, `
		INSERT INTO host_current (host_id, last_seen, services_failed)
		VALUES ($1, now(), 1)`, undated)
	exec(t, pool, `
		INSERT INTO systemd_units (host_id, unit_name, state)
		VALUES ($1, 'docker.service', 'failed')`, undated)

	// A summary and no unit rows -- nothing to take a minimum over.
	unnamed := seedHost(t, pool, "unnamed")
	exec(t, pool, `
		INSERT INTO host_current (host_id, last_seen, services_failed)
		VALUES ($1, now(), 2)`, unnamed)

	hosts, err := svc.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	byName := map[string]read.HostSummary{}
	for _, h := range hosts {
		byName[h.Hostname] = h
	}

	for _, tc := range []struct {
		host string
		want *time.Time
	}{
		{"spread", &old},
		{"beyond-the-cap", &old},
		{"undated", nil},
		{"unnamed", nil},
	} {
		got, ok := byName[tc.host]
		if !ok {
			t.Errorf("%s missing from ListHosts", tc.host)
			continue
		}
		if tc.want == nil {
			if got.FailedSince != nil {
				t.Errorf("%s failed_since = %v, want null", tc.host, *got.FailedSince)
			}
			continue
		}
		if got.FailedSince == nil {
			t.Errorf("%s failed_since is null, want %v", tc.host, *tc.want)
			continue
		}
		if !got.FailedSince.Equal(*tc.want) {
			t.Errorf("%s failed_since = %v, want %v",
				tc.host, *got.FailedSince, *tc.want)
		}
	}

	// The detail embeds the summary, so it has to select the column too.
	detail, err := svc.Host(ctx, spread)
	if err != nil {
		t.Fatalf("Host: %v", err)
	}
	if detail.FailedSince == nil || !detail.FailedSince.Equal(old) {
		t.Errorf("detail failed_since = %v, want %v", detail.FailedSince, old)
	}
}

// The detail endpoint embeds HostSummary, so a column the list selects and
// the detail does not comes back as a confident null for every host -- the
// trap the Threads and Capabilities comments both warn about.
func TestIntegrationHostDetailCarriesFailedServicesToo(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)

	id := seedHost(t, pool, "broken")
	exec(t, pool, `
		INSERT INTO host_current (host_id, last_seen, services_failed)
		VALUES ($1, now(), 3)`, id)
	exec(t, pool, `
		INSERT INTO systemd_units (host_id, unit_name, state)
		VALUES ($1, 'docker.service', 'failed')`, id)

	host, err := svc.Host(ctx, id)
	if err != nil {
		t.Fatalf("Host: %v", err)
	}
	if host.ServicesFailed == nil || *host.ServicesFailed != 3 {
		t.Errorf("detail services_failed = %v, want 3", host.ServicesFailed)
	}
	// The names travel with the count for the same reason: this endpoint
	// embeds the summary, so selecting one and not the other publishes a null
	// that reads as "no failed unit is named" on every host detail there is.
	if len(host.FailedUnits) != 1 || host.FailedUnits[0] != "docker.service" {
		t.Errorf("detail failed_units = %v, want [docker.service]", host.FailedUnits)
	}
}

func ptr[T any](v T) *T { return &v }

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

// Where a host is comes from its own agent, not from a table somebody fills
// in by hand. The agent has always reported AGENT_LOCATION/AGENT_PROVIDER/
// AGENT_FACILITY and the hub always discarded them; these two tests are what
// stops that happening again.
//
// On the SUMMARY, which is what makes the fleet list free: it draws a location
// under every hostname out of the call it already makes. HostDetail embeds
// HostSummary, so the detail path is asserted here too -- a column selected by
// one query and not the other comes back as a confident null on the side that
// missed it, which is the trap the Threads and Capabilities comments warn
// about and the reason both queries are checked.
func TestIntegrationHostCarriesTheLocationItsAgentReported(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)

	id := seedHost(t, pool, "located")
	exec(t, pool,
		`UPDATE hosts SET location = $1, provider = $2, facility = $3 WHERE id = $4`,
		"Roubaix, France", "OVH", "RBX2", id)

	host, err := svc.Host(ctx, id)
	if err != nil {
		t.Fatalf("Host: %v", err)
	}
	if host.Location == nil || *host.Location != "Roubaix, France" {
		t.Errorf("location = %v, want Roubaix, France", host.Location)
	}
	if host.Provider == nil || *host.Provider != "OVH" {
		t.Errorf("provider = %v, want OVH", host.Provider)
	}
	if host.Facility == nil || *host.Facility != "RBX2" {
		t.Errorf("facility = %v, want RBX2", host.Facility)
	}

	hosts, err := svc.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	var listed *read.HostSummary
	for i := range hosts {
		if hosts[i].ID == id {
			listed = &hosts[i]
		}
	}
	if listed == nil {
		t.Fatal("host missing from the list")
	}
	if listed.Location == nil || *listed.Location != "Roubaix, France" {
		t.Errorf("list location = %v, want Roubaix, France", listed.Location)
	}
	if listed.Provider == nil || *listed.Provider != "OVH" {
		t.Errorf("list provider = %v, want OVH", listed.Provider)
	}
}

// Setting none of the variables is the common case, and it has to arrive as
// null rather than as an empty string: the fleet row writes no line at all for
// a host with no location, and "" would render a separator with nothing either
// side of it.
func TestIntegrationHostReportingNoLocationIsNull(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)

	id := seedHost(t, pool, "unlocated")

	host, err := svc.Host(ctx, id)
	if err != nil {
		t.Fatalf("Host: %v", err)
	}
	if host.Location != nil {
		t.Errorf("location = %v, want nil", *host.Location)
	}
	if host.Provider != nil {
		t.Errorf("provider = %v, want nil", *host.Provider)
	}
	if host.Facility != nil {
		t.Errorf("facility = %v, want nil", *host.Facility)
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
			"interfaces":  func() error { _, err := svc.Interfaces(ctx, 4242); return err },
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

	// Every Docker column is nullable, and a container inserted without them
	// must come back as null rather than as a default. A "running" or a zero
	// restart count invented here is the hub asserting something no agent ever
	// said -- which is the failure the Not collected card existed to describe.
	t.Run("containers report Docker's fields as null when nobody sent them", func(t *testing.T) {
		got, err := svc.Containers(ctx, id)
		if err != nil {
			t.Fatalf("Containers: %v", err)
		}
		for _, c := range got {
			if c.DockerState != nil {
				t.Errorf("%q docker_state = %q, want null", c.Key, *c.DockerState)
			}
			if c.Health != nil {
				t.Errorf("%q health = %q, want null", c.Key, *c.Health)
			}
			if c.RestartCount != nil {
				t.Errorf("%q restart_count = %d, want null", c.Key, *c.RestartCount)
			}
			if c.Labels != nil {
				t.Errorf("%q labels = %v, want null", c.Key, c.Labels)
			}
		}
	})

	t.Run("containers carry Docker's fields when an agent sent them", func(t *testing.T) {
		exec(t, pool, `
			UPDATE containers
			   SET docker_state = 'running', health = 'unhealthy',
			       restart_count = 12, labels = '{"traefik.enable":"true"}'::jsonb,
			       state_ts = now()
			 WHERE host_id = $1 AND container_key = 'proj/web'`, id)

		got, err := svc.Containers(ctx, id)
		if err != nil {
			t.Fatalf("Containers: %v", err)
		}
		// Ordered by container_key, so proj/web is second.
		web := got[1]
		if web.DockerState == nil || *web.DockerState != "running" {
			t.Errorf("docker_state = %v, want running", web.DockerState)
		}
		if web.Health == nil || *web.Health != "unhealthy" {
			t.Errorf("health = %v, want unhealthy", web.Health)
		}
		if web.RestartCount == nil || *web.RestartCount != 12 {
			t.Errorf("restart_count = %v, want 12", web.RestartCount)
		}
		if web.Labels["traefik.enable"] != "true" {
			t.Errorf("labels = %v, want traefik.enable=true", web.Labels)
		}
		if web.StateSince == nil {
			t.Error("state_since = nil for a container whose state is recorded")
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
		// The column the list sorts on by default. A zero value here is a
		// packages tab that arrives ordered by nothing.
		if got[0].VersionChangedAt.IsZero() {
			t.Errorf("version_changed_at is zero for %q", got[0].Name)
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

	// Only the failed unit. backup.service is sitting in systemd's restart
	// backoff, which sounds like it belongs here but is a single sighting
	// rather than a rate -- see systemdstate.Notable. A unit that really is
	// looping is caught by its transition count instead, which
	// TestIntegrationUnitsListAUnitThatKeepsRestarting covers.
	if len(got) != 1 {
		t.Fatalf("got %d units, want exim4.service alone", len(got))
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
	// The ordinary states, the transient one, and the unknown one. A unit with
	// no state recorded is not evidence of a problem, so it is not shown --
	// and it is not claimed to be healthy either.
	for _, name := range []string{
		"ssh.service", "oneshot.service", "quiet.service", "backup.service",
	} {
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

// The log is a union of three tables, and this is the test that says so. Before
// it, /api/v1/events read `events` alone -- so it showed mdraid and nothing
// else, while package_events was written on every apt-get and read by nothing.
//
// The unit branch is the one with a judgement in it. systemd_unit_events
// records EVERY transition, including the inactive/activating/active cycle a
// timer produces on schedule; piping those into the log would bury it. Only
// entering `failed` and leaving it become events.
func TestIntegrationEventsUnionsPackagesAndUnits(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()

	id := seedHost(t, pool, "union")

	exec(t, pool, `
		INSERT INTO events (host_id, ts, type, subject, detail)
		VALUES ($1, now() - INTERVAL '10 hours', 'mdraid', 'md0', '{"state":"degraded"}')`, id)

	exec(t, pool, `
		INSERT INTO package_events (host_id, ts, name, action, from_version, to_version)
		VALUES ($1, now() - INTERVAL '2 hours', 'curl',    'upgrade', '8.5.0', '8.5.0-2'),
		       ($1, now() - INTERVAL '2 hours', 'libssl3', 'remove',  '3.0.13', NULL),
		       ($1, now() - INTERVAL '2 hours', 'ripgrep', 'install', NULL,     '14.1.0')`, id)

	var unitID int32
	if err := pool.QueryRow(ctx, `
		INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
		VALUES ($1, 'postgresql.service', 'active', 'running', now())
		RETURNING id`, id).Scan(&unitID); err != nil {
		t.Fatalf("seed unit: %v", err)
	}
	var timerID int32
	if err := pool.QueryRow(ctx, `
		INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
		VALUES ($1, 'logrotate.service', 'inactive', 'dead', now())
		RETURNING id`, id).Scan(&timerID); err != nil {
		t.Fatalf("seed timer unit: %v", err)
	}

	// postgresql goes active -> failed -> active. Both the failure and the
	// recovery are events; the run-of-the-mill states around them are not.
	exec(t, pool, `
		INSERT INTO systemd_unit_events (host_id, unit_id, ts, state, substate)
		VALUES ($1, $2, now() - INTERVAL '8 hours', 'active', 'running'),
		       ($1, $2, now() - INTERVAL '5 hours', 'failed', 'failed'),
		       ($1, $2, now() - INTERVAL '3 hours', 'active', 'running')`, id, unitID)

	// A timer firing on schedule. Three transitions, no event: this is the
	// noise the gate exists for.
	exec(t, pool, `
		INSERT INTO systemd_unit_events (host_id, unit_id, ts, state, substate)
		VALUES ($1, $2, now() - INTERVAL '7 hours', 'activating', 'start'),
		       ($1, $2, now() - INTERVAL '6 hours', 'active',     'running'),
		       ($1, $2, now() - INTERVAL '4 hours', 'inactive',   'dead')`, id, timerID)

	got, err := svc.Events(ctx, read.EventQuery{HostID: id}, now)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	byType := map[string]int{}
	for _, e := range got {
		byType[e.Type]++
	}
	if byType["mdraid"] != 1 || byType["package"] != 3 || byType["unit"] != 2 {
		t.Fatalf("types = %v, want 1 mdraid, 3 package, 2 unit (the failure and the recovery)", byType)
	}
	if len(got) != 6 {
		t.Fatalf("got %d events, want 6 -- the timer's three transitions are not events", len(got))
	}

	t.Run("the union is ordered as one log, not three", func(t *testing.T) {
		for i := 1; i < len(got); i++ {
			if got[i].TS.After(got[i-1].TS) {
				t.Fatalf("event %d (%s) is newer than the one before it (%s) -- "+
					"the branches were concatenated rather than merged",
					i, got[i].TS, got[i-1].TS)
			}
		}
		if got[0].Type != "package" {
			t.Errorf("newest event is %q, want the 2-hour-old package row", got[0].Type)
		}
		if got[len(got)-1].Type != "mdraid" {
			t.Errorf("oldest event is %q, want the 10-hour-old mdraid row", got[len(got)-1].Type)
		}
	})

	t.Run("every row carries a distinct id", func(t *testing.T) {
		seen := map[string]bool{}
		for _, e := range got {
			if e.ID == "" {
				t.Fatalf("event %+v has no id", e)
			}
			if seen[e.ID] {
				t.Fatalf("id %q appears twice -- a duplicate key drops a row from the list", e.ID)
			}
			seen[e.ID] = true
		}
	})

	t.Run("a package event carries its versions", func(t *testing.T) {
		var upgrade, removal *read.Event
		for i := range got {
			if got[i].Subject == nil {
				continue
			}
			switch *got[i].Subject {
			case "curl":
				upgrade = &got[i]
			case "libssl3":
				removal = &got[i]
			}
		}
		if upgrade == nil || removal == nil {
			t.Fatal("want an event for curl and one for libssl3")
		}

		var detail map[string]any
		if err := json.Unmarshal(upgrade.Detail, &detail); err != nil {
			t.Fatalf("unmarshal detail: %v", err)
		}
		if detail["action"] != "upgrade" ||
			detail["from_version"] != "8.5.0" || detail["to_version"] != "8.5.0-2" {
			t.Errorf("detail = %v, want the upgrade and both versions", detail)
		}

		// A removal has no to_version, and the key is dropped rather than sent
		// as null -- the UI reads presence, not nullness. A fresh map, because
		// json.Unmarshal merges into a non-empty one rather than replacing it.
		var removed map[string]any
		if err := json.Unmarshal(removal.Detail, &removed); err != nil {
			t.Fatalf("unmarshal detail: %v", err)
		}
		if _, ok := removed["to_version"]; ok {
			t.Errorf("detail = %v, want no to_version on a removal", removed)
		}
		if removed["action"] != "remove" || removed["from_version"] != "3.0.13" {
			t.Errorf("detail = %v, want the removal and the version it had", removed)
		}
	})

	t.Run("a unit failure states its severity and what it came from", func(t *testing.T) {
		var failure, recovery map[string]any
		for _, e := range got {
			if e.Type != "unit" {
				continue
			}
			if e.Subject == nil || *e.Subject != "postgresql.service" {
				t.Fatalf("unit event subject = %v, want postgresql.service", e.Subject)
			}
			var detail map[string]any
			if err := json.Unmarshal(e.Detail, &detail); err != nil {
				t.Fatalf("unmarshal detail: %v", err)
			}
			if detail["state"] == "failed" {
				failure = detail
			} else {
				recovery = detail
			}
		}
		if failure == nil || recovery == nil {
			t.Fatalf("want both a failure and a recovery, got %v and %v", failure, recovery)
		}
		// Stated outright, because the host tab's eventSeverity trusts only a
		// stated severity and never infers one.
		if failure["severity"] != "critical" {
			t.Errorf("failure severity = %v, want critical", failure["severity"])
		}
		if failure["previous_state"] != "active" {
			t.Errorf("failure previous_state = %v, want active", failure["previous_state"])
		}
		if _, ok := recovery["severity"]; ok {
			t.Errorf("recovery detail = %v, want no severity -- recovering is not an emergency", recovery)
		}
		if recovery["previous_state"] != "failed" {
			t.Errorf("recovery previous_state = %v, want failed", recovery["previous_state"])
		}
	})

	t.Run("a type filter selects one branch", func(t *testing.T) {
		for _, tc := range []struct {
			typ  string
			want int
		}{
			{"package", 3}, {"unit", 2}, {"mdraid", 1},
		} {
			got, err := svc.Events(ctx, read.EventQuery{HostID: id, Type: tc.typ}, now)
			if err != nil {
				t.Fatalf("Events(%s): %v", tc.typ, err)
			}
			if len(got) != tc.want {
				t.Errorf("Events(%s) returned %d rows, want %d", tc.typ, len(got), tc.want)
			}
			for _, e := range got {
				if e.Type != tc.typ {
					t.Errorf("Events(%s) returned a %q row", tc.typ, e.Type)
				}
			}
		}
	})

	t.Run("no host filter still reaches every branch", func(t *testing.T) {
		got, err := svc.Events(ctx, read.EventQuery{}, now)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		byType := map[string]int{}
		for _, e := range got {
			if e.HostID == id {
				byType[e.Type]++
			}
		}
		if byType["package"] != 3 || byType["unit"] != 2 || byType["mdraid"] != 1 {
			t.Errorf("fleet-wide types for this host = %v, want the same 6 rows", byType)
		}
	})
}

// The recovery half of a unit event needs the transition BEFORE the window to
// know it was a recovery. Without the lookback the row reads as a unit turning
// active out of nowhere, which is not notable, and the recovery vanishes from
// the log exactly when someone is looking for it.
func TestIntegrationEventsUnitRecoveryFromOutsideTheWindow(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()

	id := seedHost(t, pool, "late-recovery")
	var unitID int32
	if err := pool.QueryRow(ctx, `
		INSERT INTO systemd_units (host_id, unit_name, state, substate, state_ts)
		VALUES ($1, 'nginx.service', 'active', 'running', now())
		RETURNING id`, id).Scan(&unitID); err != nil {
		t.Fatalf("seed unit: %v", err)
	}

	// Failed three days ago -- well outside the default 24h window -- and
	// recovered an hour ago, inside it.
	exec(t, pool, `
		INSERT INTO systemd_unit_events (host_id, unit_id, ts, state, substate)
		VALUES ($1, $2, now() - INTERVAL '3 days', 'failed', 'failed'),
		       ($1, $2, now() - INTERVAL '1 hour', 'active', 'running')`, id, unitID)

	got, err := svc.Events(ctx, read.EventQuery{HostID: id}, now)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want the one recovery", len(got))
	}

	var detail map[string]any
	if err := json.Unmarshal(got[0].Detail, &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if detail["previous_state"] != "failed" {
		t.Errorf("previous_state = %v, want failed -- the lookback did not reach the failure",
			detail["previous_state"])
	}
	if got[0].TS.Before(now.Add(-2 * time.Hour)) {
		t.Errorf("ts = %v, want the recovery inside the window, not the failure outside it", got[0].TS)
	}
}

// One apt run is one timestamp: the agent takes a single clock read per scrape
// and stamps the whole package diff with it. Unbounded, a dist-upgrade writing
// 400 rows takes 400 of the 500 slots on the page and pushes the array that
// went degraded off it -- so the branch keeps a few and counts the rest.
func TestIntegrationEventsCapsOnePackageRun(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()

	id := seedHost(t, pool, "dist-upgrade")

	// Ten packages, one timestamp: one `apt upgrade`.
	exec(t, pool, `
		INSERT INTO package_events (host_id, ts, name, action, from_version, to_version)
		SELECT $1, now() - INTERVAL '2 hours', 'pkg-' || lpad(n::text, 3, '0'),
		       'upgrade', '1.0', '1.1'
		  FROM generate_series(1, 10) AS n`, id)

	// A separate, ordinary run of two, an hour later.
	exec(t, pool, `
		INSERT INTO package_events (host_id, ts, name, action, from_version, to_version)
		VALUES ($1, now() - INTERVAL '1 hour', 'curl',    'upgrade', '8.5.0', '8.5.0-2'),
		       ($1, now() - INTERVAL '1 hour', 'libssl3', 'upgrade', '3.0.13', '3.0.14')`, id)

	got, err := svc.Events(ctx, read.EventQuery{HostID: id}, now)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	if len(got) != 5 {
		t.Fatalf("got %d events, want 5 -- three of the ten-package run, plus both of the two", len(got))
	}

	t.Run("the big run is capped and says how many it hid", func(t *testing.T) {
		var marked int
		big := 0
		for _, e := range got {
			if e.Subject == nil || !strings.HasPrefix(*e.Subject, "pkg-") {
				continue
			}
			big++
			var detail map[string]any
			if err := json.Unmarshal(e.Detail, &detail); err != nil {
				t.Fatalf("unmarshal detail: %v", err)
			}
			if detail["run_size"] != float64(10) {
				t.Errorf("run_size = %v, want 10 -- the run is counted before it is cut",
					detail["run_size"])
			}
			if more, ok := detail["more"]; ok {
				marked++
				if more != float64(7) {
					t.Errorf("more = %v, want 7", more)
				}
			}
		}
		if big != 3 {
			t.Errorf("got %d rows of the ten-package run, want 3", big)
		}
		if marked != 1 {
			t.Errorf("%d rows carry `more`, want exactly 1", marked)
		}
	})

	t.Run("an ordinary run is untouched and carries neither key", func(t *testing.T) {
		small := 0
		for _, e := range got {
			if e.Subject == nil || strings.HasPrefix(*e.Subject, "pkg-") {
				continue
			}
			small++
			var detail map[string]any
			if err := json.Unmarshal(e.Detail, &detail); err != nil {
				t.Fatalf("unmarshal detail: %v", err)
			}
			if _, ok := detail["run_size"]; ok {
				t.Errorf("detail = %v, want no run_size on a run under the cap", detail)
			}
			if _, ok := detail["more"]; ok {
				t.Errorf("detail = %v, want no `more` on a run under the cap", detail)
			}
		}
		if small != 2 {
			t.Errorf("got %d rows of the two-package run, want both", small)
		}
	})
}

// The reason the cap exists, stated as a test: an array that went degraded
// before a dist-upgrade must still be on the page after it.
func TestIntegrationEventsPackageBurstDoesNotEvictAnArray(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()

	id := seedHost(t, pool, "crowded")

	exec(t, pool, `
		INSERT INTO events (host_id, ts, type, subject, detail)
		VALUES ($1, now() - INTERVAL '5 hours', 'mdraid', 'md0',
		        '{"state":"clean","level":"raid1","raid_disks":2,"degraded":1,"sync_action":"idle"}')`, id)

	exec(t, pool, `
		INSERT INTO package_events (host_id, ts, name, action, from_version, to_version)
		SELECT $1, now() - INTERVAL '1 hour', 'pkg-' || lpad(n::text, 4, '0'),
		       'upgrade', '1.0', '1.1'
		  FROM generate_series(1, 400) AS n`, id)

	got, err := svc.Events(ctx, read.EventQuery{HostID: id}, now)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	var array bool
	for _, e := range got {
		if e.Type == "mdraid" {
			array = true
		}
	}
	if !array {
		t.Fatalf("the degraded array is not on the page: %d rows, all packages -- "+
			"one apt run evicted the event the log exists for", len(got))
	}
	if len(got) != 4 {
		t.Errorf("got %d rows, want 4 -- three packages and the array", len(got))
	}
}

// The marker must survive the page boundary cutting through a run. Every kept
// row of a run shares one ts, so a limit can land mid-run; putting `more` on
// the LAST row loses it exactly then, and the reader sees a truncated run with
// nothing saying so.
func TestIntegrationEventsRunStraddlingTheLimitKeepsItsMarker(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()

	id := seedHost(t, pool, "straddle")

	exec(t, pool, `
		INSERT INTO package_events (host_id, ts, name, action, to_version)
		SELECT $1, now() - INTERVAL '1 hour', 'pkg-' || lpad(n::text, 3, '0'),
		       'upgrade', '1.1'
		  FROM generate_series(1, 20) AS n`, id)

	// A limit of 1 cuts after the first row of the run -- the harshest
	// straddle there is.
	got, err := svc.Events(ctx, read.EventQuery{HostID: id, Limit: 1}, now)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}

	var detail map[string]any
	if err := json.Unmarshal(got[0].Detail, &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if detail["more"] != float64(17) {
		t.Errorf("more = %v, want 17 -- the one row that survived the cut must "+
			"still say the run was bigger", detail["more"])
	}
}

// Interfaces is a sibling of Addresses rather than more columns on it, and the
// case that motivates the split is an interface with NO address -- a failed
// bond, an unplugged spare NIC. That row is exactly what an address-keyed
// listing cannot return, so it is the one this test insists on.
func TestIntegrationInterfacesListLinksWithoutAddresses(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "linked")

	exec(t, pool, `
		INSERT INTO host_interfaces (
			host_id, iface, if_index, oper_state, speed_mbps, duplex, mtu, mac, description)
		VALUES ($1, 'eth0', 2, 'up', 1000, 'full', 1500, '52:54:00:3a:1c:07', 'uplink'),
		       ($1, 'bond0', 3, 'lowerlayerdown', NULL, NULL, 9000, '52:54:00:3a:1c:09', NULL),
		       ($1, 'lo', 1, 'unknown', NULL, NULL, 65536, NULL, NULL)`, id)
	// One address, on one of the three interfaces.
	exec(t, pool, `
		INSERT INTO host_addresses (host_id, iface, if_index, address, family, scope)
		VALUES ($1, 'eth0', 2, '10.0.0.5', 4, 'private')`, id)

	got, err := svc.Interfaces(ctx, id)
	if err != nil {
		t.Fatalf("Interfaces: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d interfaces, want 3", len(got))
	}

	byName := map[string]read.Interface{}
	for _, i := range got {
		byName[i.Iface] = i
	}

	bond, ok := byName["bond0"]
	if !ok {
		t.Fatal("bond0 is missing; an interface with no address is the case this listing exists for")
	}
	if bond.OperState == nil || *bond.OperState != "lowerlayerdown" {
		t.Errorf("bond0 oper_state = %v, want the kernel's own word", bond.OperState)
	}
	// NULL, not 0: a link that is down has no speed to report, and reporting
	// "0 Mb/s" would put it in the same bucket as an idle 10 Gb link.
	if bond.SpeedMbps != nil {
		t.Errorf("bond0 speed_mbps = %v, want absent", *bond.SpeedMbps)
	}
	if bond.Duplex != nil {
		t.Errorf("bond0 duplex = %v, want absent", *bond.Duplex)
	}

	eth := byName["eth0"]
	if eth.SpeedMbps == nil || *eth.SpeedMbps != 1000 {
		t.Errorf("eth0 speed_mbps = %v, want 1000", eth.SpeedMbps)
	}
	if eth.MTU == nil || *eth.MTU != 1500 {
		t.Errorf("eth0 mtu = %v, want 1500", eth.MTU)
	}
	if eth.MAC == nil || *eth.MAC != "52:54:00:3a:1c:07" {
		t.Errorf("eth0 mac = %v", eth.MAC)
	}
	// The alias reads from here now rather than from every address row.
	if eth.Description == nil || *eth.Description != "uplink" {
		t.Errorf("eth0 description = %v, want uplink", eth.Description)
	}

	// A device with no link layer of its own reports absence, not an empty
	// string that every `?? ABSENT` downstream would treat as a measurement.
	if lo := byName["lo"]; lo.MAC != nil {
		t.Errorf("lo mac = %v, want absent", *lo.MAC)
	}
}

// Down links sink to the end of the list, alphabetical within each group.
//
// The names are chosen so a plain ORDER BY iface would interleave them: eth0
// down sorts between enp1s0 and lo, and wg0 up sorts after both down links.
// Only down and lowerlayerdown sink -- unknown is what every virtual device
// reports and stays with the healthy block.
func TestIntegrationInterfacesSortDownLinksLast(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "sunken")

	exec(t, pool, `
		INSERT INTO host_interfaces (host_id, iface, if_index, oper_state)
		VALUES ($1, 'eth0', 2, 'down'),
		       ($1, 'wg0', 5, 'up'),
		       ($1, 'lo', 1, 'unknown'),
		       ($1, 'bond0', 3, 'lowerlayerdown'),
		       ($1, 'enp1s0', 4, 'up'),
		       ($1, 'tap9', 6, NULL)`, id)

	got, err := svc.Interfaces(ctx, id)
	if err != nil {
		t.Fatalf("Interfaces: %v", err)
	}
	names := make([]string, len(got))
	for i, iface := range got {
		names[i] = iface.Iface
	}
	// tap9 has never reported a state. It belongs with the healthy block, NOT
	// below the down one, where a reader would take its position as a verdict.
	want := []string{"enp1s0", "lo", "tap9", "wg0", "bond0", "eth0"}
	if !slices.Equal(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}
}

// A registered host with no interfaces is an empty list, not a 404 -- the same
// distinction every other dimension listing draws.
func TestIntegrationInterfacesSeparateEmptyFromMissing(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "bare-links")

	got, err := svc.Interfaces(ctx, id)
	if err != nil {
		t.Fatalf("Interfaces: %v", err)
	}
	if got == nil {
		t.Fatal("interfaces = nil, want an empty slice so it renders as [] rather than null")
	}
	if len(got) != 0 {
		t.Errorf("interfaces = %v, want empty", got)
	}
}

// Drives folds one row per (device, attr_id) into one entry per drive, taking
// the NEWEST reading of each attribute.
//
// A listing rather than the `smart` metric family, which is keyed on
// (device, attr_id): "which drives are in trouble" through that API is a
// hundred series to read six numbers off.
var seededLastSeen = time.Date(2026, 8, 23, 11, 0, 0, 0, time.UTC)

func TestIntegrationDrivesFoldTheNewestReadingPerAttribute(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "spinning")

	// first_seen and last_seen get DIFFERENT values on purpose: they are
	// adjacent columns of the same type, and identical fixtures would let a
	// swapped SELECT pass.
	exec(t, pool, `
		INSERT INTO devices (host_id, device, model, serial, first_seen, last_seen)
		VALUES ($1, 'sda', 'ST16000NM000J', 'ZR5A1M0K',
		        TIMESTAMPTZ '2026-01-05T09:00:00Z', TIMESTAMPTZ '2026-08-23T11:00:00Z'),
		       ($1, 'nvme0n1', 'SAMSUNG MZQL2', 'S64FNE0R',
		        TIMESTAMPTZ '2026-01-05T09:00:00Z', TIMESTAMPTZ '2026-08-23T11:00:00Z')`, id)

	// Two readings of attribute 5 on sda, an hour apart. The later one is the
	// answer; the earlier must not win on ordering.
	exec(t, pool, `
		INSERT INTO smart_attributes (host_id, ts, device_id, attr_id, raw, normalized)
		SELECT $1, ts, d.id, attr_id, raw, normalized
		  FROM devices d,
		       (VALUES
		          ('sda', TIMESTAMPTZ '2026-08-23T10:00:00Z', 5::smallint, 8::bigint, 96::smallint),
		          ('sda', TIMESTAMPTZ '2026-08-23T11:00:00Z', 5::smallint, 12::bigint, 94::smallint),
		          ('sda', TIMESTAMPTZ '2026-08-23T11:00:00Z', 194::smallint, 38::bigint, 82::smallint),
		          ('nvme0n1', TIMESTAMPTZ '2026-08-23T11:00:00Z', 1001::smallint, 7::bigint, NULL)
		       ) AS v(device, ts, attr_id, raw, normalized)
		 WHERE d.host_id = $1 AND d.device = v.device`, id)

	got, err := svc.Drives(ctx, id)
	if err != nil {
		t.Fatalf("Drives: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d drives, want 2", len(got))
	}

	// Ordered by device: nvme0n1 before sda.
	nvme, sda := got[0], got[1]
	if nvme.Device != "nvme0n1" || sda.Device != "sda" {
		t.Fatalf("drives = %s, %s; want them ordered by device", nvme.Device, sda.Device)
	}

	if sda.Model == nil || *sda.Model != "ST16000NM000J" {
		t.Errorf("sda model = %v", sda.Model)
	}
	if len(sda.Attributes) != 2 {
		t.Fatalf("sda has %d attributes, want 2 (one per id, newest)", len(sda.Attributes))
	}
	// Ordered by attr_id, so 5 comes first.
	if sda.Attributes[0].ID != 5 {
		t.Fatalf("first attribute = %d, want 5", sda.Attributes[0].ID)
	}
	if sda.Attributes[0].Raw == nil || *sda.Attributes[0].Raw != 12 {
		t.Errorf("attribute 5 raw = %v, want the 11:00 reading (12), not the 10:00 one (8)",
			sda.Attributes[0].Raw)
	}
	// The exact value the fixture seeded, not merely non-zero: last_seen and
	// first_seen are adjacent TIMESTAMPTZ columns, and an IsZero() check
	// passes just as happily if the SELECT reads them in the wrong order.
	if !sda.LastSeen.Equal(seededLastSeen) {
		t.Errorf("last_seen = %s, want %s -- the devices row's own column",
			sda.LastSeen.UTC(), seededLastSeen.UTC())
	}

	// NVMe rows carry no normalized value: the health log has no such scale,
	// and the collector declines to invent one.
	if len(nvme.Attributes) != 1 {
		t.Fatalf("nvme0n1 has %d attributes, want 1", len(nvme.Attributes))
	}
	if nvme.Attributes[0].Normalized != nil {
		t.Errorf("nvme normalized = %v, want absent", *nvme.Attributes[0].Normalized)
	}
}

// A drive the hub has an id for but no attributes from is a real state: its
// readings aged out under smart_attributes' retention while the devices row
// is still inside the prune's longer horizon. It must still appear -- the UI
// says "not read" about it, which is a different fact from a drive that is
// failing -- and the row still carries the last_seen its readings had.
func TestIntegrationDrivesIncludeOnesWithNoAttributes(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "unread")

	exec(t, pool, `
		INSERT INTO devices (host_id, device, model, serial)
		VALUES ($1, 'sdz', NULL, NULL)`, id)

	got, err := svc.Drives(ctx, id)
	if err != nil {
		t.Fatalf("Drives: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d drives, want 1", len(got))
	}
	if got[0].Attributes == nil {
		// nil marshals as null; the UI reads .length on it.
		t.Error("attributes = nil, want an empty slice")
	}
	if len(got[0].Attributes) != 0 {
		t.Errorf("attributes = %v, want empty", got[0].Attributes)
	}
	// Set even with no readings, which is the point of storing it on the
	// devices row rather than deriving it: this drive's attributes are gone
	// and the row still says when they were last taken.
	if got[0].LastSeen.IsZero() {
		t.Error("last_seen is zero for a drive with no attributes; " +
			"it comes from the devices row, which exists")
	}
}

func TestIntegrationDrivesSeparateEmptyFromMissing(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "diskless")

	got, err := svc.Drives(ctx, id)
	if err != nil {
		t.Fatalf("Drives: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("drives = %v, want an empty slice", got)
	}
	if _, err := svc.Drives(ctx, 4242); !errors.Is(err, read.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound for an unknown host", err)
	}
}
