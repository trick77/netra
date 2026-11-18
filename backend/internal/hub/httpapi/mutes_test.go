package httpapi_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

type muteWire struct {
	ID       int32      `json:"id"`
	HostID   int32      `json:"host_id"`
	Hostname string     `json:"hostname"`
	Kind     string     `json:"kind"`
	Until    *time.Time `json:"until"`
	Note     string     `json:"note"`
}

func TestMutesLifecycle(t *testing.T) {
	srv, _ := newAdminFixture(t)
	hostID, _ := createHost(t, srv, "web1")

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/mutes",
		fmt.Sprintf(`{"host_id":%d,"kind":" disk ","minutes":60,"note":"resizing"}`, hostID))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, readBody(t, resp))
	}
	var timed muteWire
	decodeJSON(t, resp, &timed)
	if timed.Hostname != "web1" || timed.Kind != "disk" || timed.Note != "resizing" || timed.Until == nil {
		t.Errorf("created = %+v", timed)
	}
	if d := time.Until(*timed.Until); d < 55*time.Minute || d > 65*time.Minute {
		t.Errorf("until is %v away, want about an hour", d)
	}

	resp = doAdmin(t, srv, http.MethodPost, "/api/v1/mutes", fmt.Sprintf(`{"host_id":%d}`, hostID))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create host-wide: %d %s", resp.StatusCode, readBody(t, resp))
	}
	var forever muteWire
	decodeJSON(t, resp, &forever)
	if forever.Kind != "" || forever.Until != nil {
		t.Errorf("host-wide, until lifted = %+v", forever)
	}

	resp = doAdmin(t, srv, http.MethodGet, "/api/v1/mutes", "")
	var list []muteWire
	decodeJSON(t, resp, &list)
	if len(list) != 2 || list[0].ID != forever.ID {
		t.Fatalf("list = %+v, want both, newest first", list)
	}

	// Narrowed to one host: another host's mute stays out.
	otherID, _ := createHost(t, srv, "web2")
	resp = doAdmin(t, srv, http.MethodPost, "/api/v1/mutes", fmt.Sprintf(`{"host_id":%d}`, otherID))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create on web2: %d", resp.StatusCode)
	}
	var other muteWire
	decodeJSON(t, resp, &other)
	resp = doAdmin(t, srv, http.MethodGet, fmt.Sprintf("/api/v1/mutes?host=%d", hostID), "")
	list = nil
	decodeJSON(t, resp, &list)
	if len(list) != 2 {
		t.Fatalf("web1's list = %+v, want its two", list)
	}
	if resp := doAdmin(t, srv, http.MethodGet, "/api/v1/mutes?host=x", ""); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad host filter: %d, want 400", resp.StatusCode)
	}
	resp = doAdmin(t, srv, http.MethodDelete, fmt.Sprintf("/api/v1/mutes/%d", other.ID), "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete web2's: %d", resp.StatusCode)
	}

	resp = doAdmin(t, srv, http.MethodDelete, fmt.Sprintf("/api/v1/mutes/%d", timed.ID), "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp = doAdmin(t, srv, http.MethodDelete, fmt.Sprintf("/api/v1/mutes/%d", timed.ID), "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("second delete: %d, want 404", resp.StatusCode)
	}

	resp = doAdmin(t, srv, http.MethodGet, "/api/v1/mutes", "")
	list = nil
	decodeJSON(t, resp, &list)
	if len(list) != 1 || list[0].ID != forever.ID {
		t.Errorf("after delete = %+v", list)
	}

	// Deleting the host takes its mutes with it.
	resp = doAdmin(t, srv, http.MethodDelete, fmt.Sprintf("/api/v1/hosts/%d", hostID), "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete host: %d", resp.StatusCode)
	}
	resp = doAdmin(t, srv, http.MethodGet, "/api/v1/mutes", "")
	body := readBody(t, resp)
	if strings.TrimSpace(body) != "[]" {
		t.Errorf("mutes after host delete = %s, want []", body)
	}
}

func TestMuteRejectsBadInput(t *testing.T) {
	srv, _ := newAdminFixture(t)
	hostID, _ := createHost(t, srv, "web1")

	for name, body := range map[string]string{
		"unknown host":   `{"host_id":99999}`,
		"negative":       fmt.Sprintf(`{"host_id":%d,"minutes":-1}`, hostID),
		"over a year":    fmt.Sprintf(`{"host_id":%d,"minutes":%d}`, hostID, 367*24*60),
		"unknown kind":   fmt.Sprintf(`{"host_id":%d,"kind":"disks"}`, hostID),
		"long note":      fmt.Sprintf(`{"host_id":%d,"note":%q}`, hostID, strings.Repeat("n", 501)),
		"malformed JSON": `{`,
	} {
		resp := doAdmin(t, srv, http.MethodPost, "/api/v1/mutes", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, resp.StatusCode, readBody(t, resp))
		}
	}

	resp := doAdmin(t, srv, http.MethodDelete, "/api/v1/mutes/abc", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad id: %d, want 400", resp.StatusCode)
	}
}
