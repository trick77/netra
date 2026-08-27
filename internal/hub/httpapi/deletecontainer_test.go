package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// seedContainer gives a host one container and one sample for it, and returns
// the container's id.
func seedContainer(t *testing.T, s *store.Store, hostID int32, key string) int32 {
	t.Helper()
	ctx := context.Background()

	if _, err := s.InsertContainerSamples(ctx, hostID, []*netrav1.ContainerSample{{
		TsMs:         time.Now().UnixMilli(),
		ContainerKey: key,
		Name:         key,
		Image:        "nginx:1.27",
	}}); err != nil {
		t.Fatalf("insert container sample: %v", err)
	}

	var id int32
	if err := s.Pool().QueryRow(ctx,
		`SELECT id FROM containers WHERE host_id = $1 AND container_key = $2`,
		hostID, key).Scan(&id); err != nil {
		t.Fatalf("read container id: %v", err)
	}
	return id
}

// The operator's answer to a container that was removed on the host: nothing
// on the wire says a container is gone, so the row cannot be deleted by any
// rule the hub could apply without also deleting the history of every
// container that merely stopped.
func TestIntegrationPurgeContainerIs204ThenGone(t *testing.T) {
	srv, s := newAdminFixture(t)
	hostID, _ := createHost(t, srv, "web01")
	containerID := seedContainer(t, s, hostID, "shop/web")

	path := fmt.Sprintf("/api/v1/hosts/%d/containers/%d", hostID, containerID)

	del := doAdmin(t, srv, http.MethodDelete, path, "")
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", del.StatusCode)
	}

	again := doAdmin(t, srv, http.MethodDelete, path, "")
	if again.StatusCode != http.StatusNotFound {
		t.Errorf("second purge status = %d, want 404", again.StatusCode)
	}
}

// The samples go with the row, through container_samples' ON DELETE CASCADE.
// The confirmation in the UI says so, and this is the assertion behind it.
func TestIntegrationPurgeContainerTakesItsSamples(t *testing.T) {
	srv, s := newAdminFixture(t)
	hostID, _ := createHost(t, srv, "web02")
	containerID := seedContainer(t, s, hostID, "shop/web")

	resp := doAdmin(t, srv, http.MethodDelete,
		fmt.Sprintf("/api/v1/hosts/%d/containers/%d", hostID, containerID), "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	var samples int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM container_samples WHERE container_id = $1`,
		containerID).Scan(&samples); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	if samples != 0 {
		t.Errorf("container_samples = %d, want 0 after the purge", samples)
	}
}

// Container ids come from one global sequence, so the host in the path is not
// decoration: without it a stale id from a page about one host could delete
// another host's container.
func TestIntegrationPurgeContainerRefusesAnotherHostsContainer(t *testing.T) {
	srv, s := newAdminFixture(t)
	owner, _ := createHost(t, srv, "owner")
	other, _ := createHost(t, srv, "other")
	containerID := seedContainer(t, s, owner, "shop/web")

	resp := doAdmin(t, srv, http.MethodDelete,
		fmt.Sprintf("/api/v1/hosts/%d/containers/%d", other, containerID), "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}

	var count int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM containers WHERE id = $1`, containerID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("the owner's container was deleted through another host's path")
	}
}

func TestIntegrationPurgeContainerRejectsANonNumericID(t *testing.T) {
	srv, _ := newAdminFixture(t)
	hostID, _ := createHost(t, srv, "web03")

	resp := doAdmin(t, srv, http.MethodDelete,
		fmt.Sprintf("/api/v1/hosts/%d/containers/abc", hostID), "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// Every mutation under /api/v1/ answers to the admin credential, and a DELETE
// that destroys history is not the one to leave open.
func TestIntegrationPurgeContainerRequiresACredential(t *testing.T) {
	srv, s := newAdminFixture(t)
	hostID, _ := createHost(t, srv, "web04")
	containerID := seedContainer(t, s, hostID, "shop/web")

	req, err := http.NewRequest(http.MethodDelete,
		fmt.Sprintf("%s/api/v1/hosts/%d/containers/%d", srv.URL, hostID, containerID), nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := noRedirectClient(srv).Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		t.Errorf("an unauthenticated purge succeeded")
	}
}
