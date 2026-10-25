// Package httpserver routes Ledger's HTTP requests.
package httpserver

import (
	"log/slog"
	"net/http"

	"example.com/ledger/internal/logctx"
)

// New returns the service's handler.
func New(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := slog.Default().With("path", r.URL.Path, "request_id", r.Header.Get("X-Request-Id"))
		mux.ServeHTTP(w, r.WithContext(logctx.With(r.Context(), l)))
	})
}
