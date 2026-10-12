package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The UI's polled reads drop out of the access log while they succeed; a
// failing read, a mutation and agent ingest keep their line.
func TestPolledRead(t *testing.T) {
	for _, c := range []struct {
		method, path string
		status       int
		want         bool
	}{
		{http.MethodGet, "/api/v1/hosts", 200, true},
		{http.MethodGet, "/api/v1/hosts/3/metrics", 200, true},
		{http.MethodGet, "/api/v1/hosts", 304, true},
		{http.MethodGet, "/api/v1/hosts", 401, false},
		{http.MethodGet, "/api/v1/hosts", 500, false},
		{http.MethodPost, "/api/v1/hosts", 201, false},
		{http.MethodDelete, "/api/v1/hosts/3", 204, false},
		{http.MethodPost, "/api/agent/v1/ingest", 200, false},
		{http.MethodGet, "/", 200, false},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := polledRead(r, c.status); got != c.want {
			t.Errorf("polledRead(%s %s, %d) = %v, want %v", c.method, c.path, c.status, got, c.want)
		}
	}
}
