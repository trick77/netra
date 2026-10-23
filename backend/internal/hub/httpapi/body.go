package httpapi

import "net/http"

// innermost is the writer at the bottom of the Unwrap chain (AccessLog's
// status recorder sits on top of every route). http.MaxBytesReader looks
// there for net/http's own response, which it tells to close the connection
// after an over-limit body. Handed the wrapper instead, it cannot: net/http
// then reads up to another 256 KiB of the rest to keep the connection alive
// for the client that just went over the limit.
func innermost(w http.ResponseWriter) http.ResponseWriter {
	for {
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		w = u.Unwrap()
	}
}
