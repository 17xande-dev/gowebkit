package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/17xande-dev/websec/session"
)

func runSessionSweep(ctx context.Context, log *slog.Logger, sweep func(context.Context, session.Lifetime, time.Time) (int64, error), life session.Lifetime, purge func(context.Context) (int, error), ticks <-chan time.Time) {
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok || ctx.Err() != nil {
				return
			}
		}
		// The tick may have waited while the preceding sweep finished.
		n, err := sweep(ctx, life, time.Now())
		if err != nil {
			if ctx.Err() == nil {
				log.ErrorContext(ctx, "session sweep failed", "error", err)
			}
		} else if n != 0 {
			log.InfoContext(ctx, "expired sessions removed", "count", n)
		}
		if ctx.Err() == nil {
			if _, err := purge(ctx); err != nil && ctx.Err() == nil {
				log.ErrorContext(ctx, "job purge failed", "error_class", fmt.Sprintf("%T", err))
			}
		}
	}
}

func runBackups(ctx context.Context, log *slog.Logger, backup func(context.Context, time.Time) (string, bool, error), now time.Time, ticks <-chan time.Time) {
	for ctx.Err() == nil {
		path, made, err := backup(ctx, now)
		if err != nil {
			if ctx.Err() == nil {
				log.ErrorContext(ctx, "database backup failed", "error", err)
			}
		} else if made {
			log.InfoContext(ctx, "database backup saved", "file", filepath.Base(path))
		}
		select {
		case <-ctx.Done():
			return
		case next, ok := <-ticks:
			if !ok {
				return
			}
			now = next
		}
	}
}
