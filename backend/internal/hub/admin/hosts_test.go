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

	host, plain, err := svc.CreateHost(ctx, "web01")
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

func TestCreateHostRejectsAnEmptyHostname(t *testing.T) {
	svc, _ := newService(t)

	if _, _, err := svc.CreateHost(context.Background(), "  "); !errors.Is(err, admin.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

// A hostname is the identity now.
//
// It used to be (site_id, hostname): two machines at different sites sharing a
// name was normal, and that was what sites were for. With sites gone there is
// no second half to the key, so the same protection has to hold against the
// name alone -- otherwise the admin API creates rows indistinguishable in
// every view that shows a hostname.
func TestCreateHostRejectsADuplicateHostname(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	if _, _, err := svc.CreateHost(ctx, "web01"); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	if _, _, err := svc.CreateHost(ctx, "web01"); !errors.Is(err, admin.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

// The discriminating test for rotation. tokens has no UNIQUE constraint on
// host_id, so an insert-only rotation leaves the old token live -- silently,
// with no symptom until a revoked agent is noticed still posting.
func TestRotateTokenInvalidatesTheOldToken(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	host, first, err := svc.CreateHost(ctx, "web01")
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	name, second, err := svc.RotateToken(ctx, host.ID)
	if err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	if second == first {
		t.Fatal("rotation returned the same token")
	}
	// The UI heads the token page with this rather than listing every host.
	if name != "web01" {
		t.Errorf("hostname = %q, want %q", name, "web01")
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

	if _, _, err := svc.RotateToken(context.Background(), 4242); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A host that does not exist must leave no token behind either -- a failed
// rotation that committed the DELETE would lock an agent out with no
// replacement.
func TestRotateTokenOnUnknownHostLeavesOtherHostsAlone(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	host, plain, err := svc.CreateHost(ctx, "web01")
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	if _, _, err := svc.RotateToken(ctx, host.ID+1000); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	if _, err := auth.NewAuthenticator(s.Pool()).Authenticate(ctx, plain); err != nil {
		t.Errorf("an unrelated host's token stopped working: %v", err)
	}
}

func TestDeleteHostCascadesItsTokens(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	host, plain, err := svc.CreateHost(ctx, "web01")
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

	if _, _, err := svc.CreateHost(ctx, "web02"); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	first, _, err := svc.CreateHost(ctx, "web01")
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
