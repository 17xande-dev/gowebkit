package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/auth"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/store"
)

type sweepEvents struct {
	address listeningAddress
	removed chan int
	release <-chan struct{}
}

func (e sweepEvents) Write(p []byte) (int, error) {
	var event struct {
		Msg   string
		Count int
	}
	if err := json.Unmarshal(p, &event); err != nil {
		return 0, err
	}
	if event.Msg == "expired sessions removed" {
		e.removed <- event.Count
		// Hold the real worker inside its completion log while closing.
		<-e.release
	}
	return e.address.Write(p)
}

func TestRunServerSweepsAndJoinsExpiredSessions(t *testing.T) {
	setTestEnv(t, 8080)
	dbtest.SetEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = 0
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	blocked := make(chan struct{})
	release := sync.OnceFunc(func() { close(blocked) })
	defer release()
	events := sweepEvents{address: make(listeningAddress, 1), removed: make(chan int, 1), release: blocked}
	log := slog.New(slog.NewJSONHandler(events, nil))
	opened := make(chan *db.DB, 1)
	live, idle, absolute := auth.HashToken("live"), auth.HashToken("idle"), auth.HashToken("absolute")
	hash, err := auth.HashPassword(t.Context(), "sweep-fixture-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	open := func(ctx context.Context, cfgDB config.DB) (*db.DB, error) {
		d, err := db.Open(ctx, cfgDB)
		if err != nil {
			return nil, err
		}
		accounts := store.New(d)
		seed := func() error {
			user, err := accounts.CreateUser(ctx, auth.User{
				Name: "Sample", Email: "sweep-account@example.com", Role: auth.RoleMember, PasswordHash: hash,
			})
			if err != nil {
				return err
			}
			now := time.Now()
			for token, created := range map[auth.TokenHash]time.Time{
				live: now, idle: now.Add(-cfg.SessionLifetime.Idle - time.Minute),
				absolute: now.Add(-cfg.SessionLifetime.Absolute - time.Minute),
			} {
				if err := accounts.CreateSession(ctx, user.ID, hash, token, created); err != nil {
					return err
				}
			}
			// The absolute-expired row must be fresh enough on idle alone.
			return accounts.TouchSession(ctx, absolute, now)
		}
		if err := seed(); err != nil {
			return nil, errors.Join(err, d.Close())
		}
		opened <- d
		return d, nil
	}
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, cfg, log, open, 20*time.Millisecond) }()
	var addr string
	select {
	case addr = <-events.address:
	case err := <-done:
		t.Fatalf("server did not listen: %v", err)
	case <-ctx.Done():
		t.Fatal("server did not listen")
	}
	d := <-opened
	select {
	case n := <-events.removed:
		if n != 2 {
			t.Fatalf("sweep removed %d sessions, want 2", n)
		}
	case err := <-done:
		t.Fatalf("server exited before sweeping: %v", err)
	case <-ctx.Done():
		t.Fatal("periodic session sweep did not run")
	}
	for _, token := range []auth.TokenHash{idle, absolute} {
		if _, _, err := store.New(d).LookupSession(t.Context(), token); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expired session still stored: %v", err)
		}
	}
	if _, _, err := store.New(d).LookupSession(t.Context(), live); err != nil {
		t.Fatalf("sweep did not keep the live session: %v", err)
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitClosedListener(t, "127.0.0.1:"+port)
	select {
	case err := <-done:
		t.Fatalf("server returned before sweep completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := d.Ping(t.Context()); err != nil {
		t.Fatalf("pool closed before sweep completed: %v", err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not return after sweep completed")
	}
	if err := d.Ping(t.Context()); err == nil {
		t.Fatal("pool is still open after server returned")
	}
}
