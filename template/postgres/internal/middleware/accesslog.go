package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// RoutePattern labels requests before routing, including those refused by a
// wrapper. Only method-qualified route patterns are logged.
func RoutePattern(mux *http.ServeMux, r *http.Request) string {
	_, pattern := mux.Handler(r)
	// Handler can return a bare redirect path rather than a registered
	// pattern. A qualified match starts with a method, never with a slash.
	if i := strings.IndexAny(pattern, " \t"); i > 0 {
		method := pattern[:i]
		if method == r.Method || (method == http.MethodGet && r.Method == http.MethodHead) {
			return pattern
		}
	}
	return "unmatched"
}

// AccessLog records the outcome using the registered route, not the URL.
func AccessLog(log *slog.Logger, mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		pattern := RoutePattern(mux, r)
		out := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(out, r)
		status := out.status
		if status == 0 {
			status = http.StatusOK
		}
		log.InfoContext(r.Context(), "request",
			"method", r.Method, "pattern", pattern, "status", status,
			"duration", time.Since(start), "request_id", ID(r.Context()))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 && (status >= 200 || status == http.StatusSwitchingProtocols) {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusWriter) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err == nil && w.status == 0 {
		w.status = http.StatusOK
	}
	return err
}
