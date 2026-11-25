package main

import (
	"os"
	"strings"
	"testing"
)

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
