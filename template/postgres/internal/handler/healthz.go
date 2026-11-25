package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Healthz checks readiness with a two-second deadline. The check must honor
// cancellation; a missing check reports unavailable.
func Healthz(log *slog.Logger, ping func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var err error
		if ping == nil {
			err = errors.New("database check not configured")
		} else if err = ping(ctx); err == nil {
			err = ctx.Err()
		}
		if err != nil {
			log.WarnContext(ctx, "health check failed", "error", err)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	})
}
