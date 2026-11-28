package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
)

func TestDeployConfigStopTimeout(t *testing.T) {
	raw, err := os.ReadFile("config/deploy.yml")
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "stop_timeout:"); ok {
			value, _, _ = strings.Cut(value, "#")
			values = append(values, strings.TrimSpace(value))
		}
	}
	if len(values) != 1 {
		t.Fatalf("config/deploy.yml: want exactly one top-level stop_timeout, got %v", values)
	}
	stopTimeout, err := time.ParseDuration(values[0] + "s")
	if err != nil || strings.Trim(values[0], "0123456789") != "" {
		t.Fatalf("config/deploy.yml: stop_timeout = %q, want integer seconds", values[0])
	}

	setTestEnv(t, 8080)
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	const queueStop = 5 * time.Second // outbox.Stop's per-call wait
	const margin = 5 * time.Second
	if cfg.ShutdownTimeout > stopTimeout-queueStop-margin {
		t.Fatalf("stop_timeout %v does not cover HTTP drain %v, queue stop %v and margin %v",
			stopTimeout, cfg.ShutdownTimeout, queueStop, margin)
	}
}

// The Kamal deploy runs behind Kamal Proxy, so the socket peer of every
// request is the proxy. With "remote" the per-address sign-in limit collapses
// into one bucket shared by all clients. Kamal Proxy with ssl: true rebuilds
// X-Forwarded-For, which is what "forwarded" needs; this pins that default.
func TestDeployConfigClientIPSource(t *testing.T) {
	raw, err := os.ReadFile("config/deploy.yml")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "CLIENT_IP_SOURCE:"); ok {
			got = append(got, strings.TrimSpace(v))
		}
	}
	if len(got) != 1 || got[0] != "forwarded" {
		t.Fatalf("config/deploy.yml CLIENT_IP_SOURCE = %q, want exactly one value \"forwarded\": "+
			"behind Kamal Proxy %q puts every client in one rate-limit bucket", got, "remote")
	}
}
