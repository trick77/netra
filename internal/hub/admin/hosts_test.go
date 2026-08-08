package admin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/trick77/netra/internal/hub/admin"
	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/store"
)

// newService opens a fresh schema and returns a Service over it, plus the
// store so a test can assert against the database directly.
func newService(t *testing.T) (*admin.Service, *store.Store) {
	t.Helper()

	s := store.OpenTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return admin.NewService(s.Pool()), s
}

func TestCreateHostMintsAWorkingToken(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	host, plain, err := svc.CreateHost(ctx, "web01", nil)
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if host.Hostname != "web01" {
		t.Errorf("Hostname = %q, want %q", host.Hostname, "web01")
	}

	gotID, err := auth.NewAuthenticator(s.Pool()).Authenticate(ctx, plain)
	if err != nil {
		t.Fatalf("the minted token does not authenticate: %v", err)
	}
	if gotID != host.ID {
		t.Errorf("token resolves to host %d, want %d", gotID, host.ID)
	}
}

// Absent is NULL, never 0 -- the same rule the collectors follow. A 0 here
// would be a foreign key to a host id that can never exist.
func TestCreateHostWithNoSiteStoresNullNotZero(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	host, _, err := svc.CreateHost(ctx, "web01", nil)
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	var siteID *int32
	if err := s.Pool().QueryRow(ctx,
		`SELECT site_id FROM hosts WHERE id = $1`, host.ID).Scan(&siteID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if siteID != nil {
		t.Errorf("site_id = %d, want NULL", *siteID)
	}
}

func TestCreateHostRejectsAnEmptyHostname(t *testing.T) {
	svc, _ := newService(t)

	if _, _, err := svc.CreateHost(context.Background(), "  ", nil); !errors.Is(err, admin.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

// Two machines at the same site cannot share a name: every view that shows a
// hostname would show two identical rows.
func TestCreateHostRejectsADuplicateHostnameAtTheSameSite(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	var siteID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO sites (name) VALUES ('zrh') RETURNING id`).Scan(&siteID); err != nil {
		t.Fatalf("insert site: %v", err)
	}

	if _, _, err := svc.CreateHost(ctx, "web01", &siteID); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	if _, _, err := svc.CreateHost(ctx, "web01", &siteID); !errors.Is(err, admin.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

// The reason the constraint is on (site_id, hostname) and not hostname alone:
// two machines at different sites sharing a name is normal.
func TestCreateHostAllowsTheSameHostnameAtDifferentSites(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	var zrh, fsn int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO sites (name) VALUES ('zrh') RETURNING id`).Scan(&zrh); err != nil {
		t.Fatalf("insert site: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO sites (name) VALUES ('fsn1') RETURNING id`).Scan(&fsn); err != nil {
		t.Fatalf("insert site: %v", err)
	}

	if _, _, err := svc.CreateHost(ctx, "web01", &zrh); err != nil {
		t.Fatalf("CreateHost at zrh: %v", err)
	}
	if _, _, err := svc.CreateHost(ctx, "web01", &fsn); err != nil {
		t.Errorf("CreateHost at fsn1: %v — the same name at another site is legitimate", err)
	}
}

// The NULLS NOT DISTINCT case, and the reason that clause exists. Postgres
// treats every NULL as unique by default, so without it two hosts with no
// site assigned -- the common case on a new hub -- would collide freely and
// the constraint would protect nothing where it is needed most.
func TestCreateHostRejectsADuplicateHostnameWithNoSite(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	if _, _, err := svc.CreateHost(ctx, "web01", nil); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	if _, _, err := svc.CreateHost(ctx, "web01", nil); !errors.Is(err, admin.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict — NULL site_id must not defeat the constraint", err)
	}
}

// A site id that does not exist is the caller's mistake, so it must read as
// invalid input rather than as a hub failure the operator cannot act on.
func TestCreateHostRejectsAnUnknownSite(t *testing.T) {
	svc, _ := newService(t)

	siteID := int32(4242)
	if _, _, err := svc.CreateHost(context.Background(), "web01", &siteID); !errors.Is(err, admin.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

// The discriminating test for rotation. tokens has no UNIQUE constraint on
// host_id, so an insert-only rotation leaves the old token live -- silently,
// with no symptom until a revoked agent is noticed still posting.
func TestRotateTokenInvalidatesTheOldToken(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	host, first, err := svc.CreateHost(ctx, "web01", nil)
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	second, err := svc.RotateToken(ctx, host.ID)
	if err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	if second == first {
		t.Fatal("rotation returned the same token")
	}

	a := auth.NewAuthenticator(s.Pool())
	if _, err := a.Authenticate(ctx, first); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("old token error = %v, want ErrUnauthorized — rotation must revoke", err)
	}
	if _, err := a.Authenticate(ctx, second); err != nil {
		t.Errorf("new token rejected: %v", err)
	}

	// Authenticating the new token would pass even if the old row survived,
	// so the row count is asserted separately.
	var rows int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM tokens WHERE host_id = $1`, host.ID).Scan(&rows); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if rows != 1 {
		t.Errorf("tokens rows = %d, want exactly 1 after rotation", rows)
	}
}

func TestRotateTokenOnUnknownHostIsNotFound(t *testing.T) {
	svc, _ := newService(t)

	if _, err := svc.RotateToken(context.Background(), 4242); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A host that does not exist must leave no token behind either -- a failed
// rotation that committed the DELETE would lock an agent out with no
// replacement.
func TestRotateTokenOnUnknownHostLeavesOtherHostsAlone(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	host, plain, err := svc.CreateHost(ctx, "web01", nil)
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	if _, err := svc.RotateToken(ctx, host.ID+1000); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	if _, err := auth.NewAuthenticator(s.Pool()).Authenticate(ctx, plain); err != nil {
		t.Errorf("an unrelated host's token stopped working: %v", err)
	}
}

func TestDeleteHostCascadesItsTokens(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	host, plain, err := svc.CreateHost(ctx, "web01", nil)
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if err := svc.DeleteHost(ctx, host.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}

	if _, err := auth.NewAuthenticator(s.Pool()).Authenticate(ctx, plain); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("the token of a deleted host still authenticates: %v", err)
	}
}

func TestDeleteUnknownHostIsNotFound(t *testing.T) {
	svc, _ := newService(t)

	if err := svc.DeleteHost(context.Background(), 4242); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestListHostsIsEmptyOnAFreshSchema(t *testing.T) {
	svc, _ := newService(t)

	hosts, err := svc.ListHosts(context.Background())
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(hosts) != 0 {
		t.Errorf("len = %d, want 0", len(hosts))
	}
}

func TestListHostsReportsLastSeenAndOrdersByHostname(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	if _, _, err := svc.CreateHost(ctx, "web02", nil); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	first, _, err := svc.CreateHost(ctx, "web01", nil)
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_current (host_id, last_seen) VALUES ($1, now())`, first.ID); err != nil {
		t.Fatalf("insert host_current: %v", err)
	}

	hosts, err := svc.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("len = %d, want 2", len(hosts))
	}
	if hosts[0].Hostname != "web01" {
		t.Errorf("hosts[0] = %q, want web01 — the list is ordered by hostname", hosts[0].Hostname)
	}
	if hosts[0].LastSeen == nil {
		t.Error("web01 has no LastSeen, want the host_current timestamp")
	}
	// A host that has never posted has no last_seen. That is absent, not zero.
	if hosts[1].LastSeen != nil {
		t.Errorf("web02 LastSeen = %v, want nil — it has never posted", *hosts[1].LastSeen)
	}
}
