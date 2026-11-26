package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/auth"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/store"
	"github.com/17xande-dev/websec/webtest"
)

func TestCheckConfigParity(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	for _, tt := range []struct {
		name string
		env  map[string]string
		ok   bool
	}{
		{"development", nil, true},
		{"HTTPS", map[string]string{"COOKIE_SECURE": "true", "BASE_URL": "https://app.example"}, true},
		{"mail relay", map[string]string{"SMTP_HOST": "relay.example", "EMAIL_FROM": "sender@example.com"}, true},
		{"noncanonical origin", map[string]string{"BASE_URL": "https://app.example/", "COOKIE_SECURE": "true"}, false},
		{"numeric cookie setting", map[string]string{"COOKIE_SECURE": "1", "BASE_URL": "https://app.example"}, false},
		{"empty cookie setting", map[string]string{"COOKIE_SECURE": ""}, true},
		{"unsecured HTTPS cookies", map[string]string{"COOKIE_SECURE": "false", "BASE_URL": "https://app.example"}, false},
		{"invalid session", map[string]string{"SESSION_ABSOLUTE": "1h"}, false},
		{"invalid client source", map[string]string{"CLIENT_IP_SOURCE": "other"}, false},
		{"invalid SMTP", map[string]string{"SMTP_HOST": "relay.example", "EMAIL_FROM": "invalid"}, false},
		{"unreadable secret", map[string]string{"SETUP_TOKEN_FILE": filepath.Join(t.TempDir(), "missing")}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setTestEnv(t, ln.Addr().(*net.TCPAddr).Port)
			dbtest.SetEnv(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			cfg, loadErr := config.Load()
			var stdout, stderr bytes.Buffer
			checkErr := run([]string{"-check-config"}, &stdout, &stderr)
			if (loadErr == nil) != tt.ok || (checkErr == nil) != tt.ok {
				t.Fatalf("Load error %v; -check-config error %v; expected success %v", loadErr, checkErr, tt.ok)
			}
			if tt.ok {
				if stdout.String() != "Configuration OK\n" || stderr.Len() != 0 {
					t.Fatalf("check-config output: stdout %q, stderr %q", &stdout, &stderr)
				}
				d := dbtest.Open(t)
				srv, err := newServer(cfg, newLogger(io.Discard, cfg), store.New(d), d.Ping)
				if err != nil {
					t.Fatal(err)
				}
				if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 ||
					srv.IdleTimeout <= 0 || srv.MaxHeaderBytes != 64<<10 {
					t.Error("server timeouts or header bound are not configured")
				}
				for _, target := range []string{"/", "/login", "/setup", "/healthz", "/missing"} {
					rec := httptest.NewRecorder()
					srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
					head := rec.Header()
					if head.Get("Content-Security-Policy") == "" || head.Get("X-Request-ID") == "" {
						t.Errorf("middleware not installed for %s", target)
					}
					if target == "/healthz" && head.Get("Set-Cookie") != "" {
						t.Error("readiness probe should not get a CSRF or session cookie")
					}
					want := http.StatusOK
					if target == "/" {
						want = http.StatusSeeOther
					}
					if target == "/missing" {
						want = http.StatusNotFound
					}
					if rec.Code != want {
						t.Errorf("%s: status %d, want %d", target, rec.Code, want)
					}
					if hsts := head.Get("Strict-Transport-Security"); (hsts != "") != cfg.CookieSecure {
						t.Errorf("%s: HSTS %q with secure cookies %v", target, hsts, cfg.CookieSecure)
					}
				}
				return
			}

			// The occupied port makes opening a listener before validation visible.
			startErr := run(nil, &stdout, &stderr)
			if checkErr.Error() != "configuration: "+loadErr.Error() {
				t.Errorf("check-config did not report the config error: %v", checkErr)
			}
			if startErr == nil || startErr.Error() != checkErr.Error() || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Errorf("server validation: err %v, stdout %q, stderr %q", startErr, &stdout, &stderr)
			}
		})
	}
}

func setTestEnv(t *testing.T, port int) {
	t.Helper()
	for key, value := range map[string]string{
		"PORT": strconv.Itoa(port), "BASE_URL": "http://localhost:" + strconv.Itoa(port),
		"COOKIE_SECURE": "false", "SITE_NAME": "Sample", "SETUP_TOKEN": "", "SETUP_TOKEN_FILE": "",
		"SESSION_IDLE": "2h", "SESSION_ABSOLUTE": "12h", "CLIENT_IP_SOURCE": "remote", "PASSWORD_CONCURRENCY": "2",
		"LOG_LEVEL": "info", "LOG_FORMAT": "json", "SHUTDOWN_TIMEOUT": "15s",
		"SMTP_HOST": "", "SMTP_PORT": "587", "SMTP_USERNAME": "", "SMTP_PASSWORD": "", "SMTP_PASSWORD_FILE": "",
		"SMTP_TLS": "starttls", "EMAIL_FROM": "",
	} {
		t.Setenv(key, value)
	}
}

func TestStartupLogger(t *testing.T) {
	var out bytes.Buffer
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			out.Reset()
			log := newLogger(&out, config.Config{LogFormat: format})
			log.Debug("not visible")
			log.Info("server listening", "version", buildVersion())
			if out.Len() == 0 || strings.Contains(out.String(), "not visible") {
				t.Fatalf("unexpected logger output %q", &out)
			}
			if format == "json" {
				var event struct {
					Version string
				}
				if err := json.Unmarshal(out.Bytes(), &event); err != nil || event.Version == "" {
					t.Errorf("bad startup event: %q (%v)", &out, err)
				}
			} else if !strings.Contains(out.String(), "version=") {
				t.Errorf("missing version: %q", &out)
			}
		})
	}
}

func TestGracefulShutdownWaitsForActiveRequest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered, blocked := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(blocked) })
	ctx, cancel := context.WithCancel(t.Context())
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-blocked
		_, _ = io.WriteString(w, "finished")
	})}
	t.Cleanup(func() {
		release()
		cancel()
		_ = srv.Close()
		_ = ln.Close()
	})
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, ln, 5*time.Second) }()

	type result struct {
		status int
		body   string
		err    error
	}
	requestDone := make(chan result, 1)
	go func() {
		client := &http.Client{Timeout: 6 * time.Second}
		resp, err := client.Get("http://" + ln.Addr().String())
		if err != nil {
			requestDone <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		requestDone <- result{status: resp.StatusCode, body: string(body), err: err}
	}()
	select {
	case <-entered:
	case res := <-requestDone:
		t.Fatalf("request completed before entering handler: %+v", res)
	case <-time.After(2 * time.Second):
		t.Fatal("request never entered handler")
	}
	cancel()

	// A refused new connection proves shutdown reached the server while the
	// old request was still blocked, not after it happened to finish.
	awaitClosedListener(t, ln.Addr().String())
	select {
	case err := <-done:
		t.Fatalf("server returned before handler finished: %v", err)
	default:
	}
	release()

	select {
	case res := <-requestDone:
		if res.err != nil || res.status != http.StatusOK || res.body != "finished" {
			t.Fatalf("in-flight request failed: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not finish after draining requests")
	}
}

func awaitClosedListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if errors.Is(err, syscall.ECONNREFUSED) {
			return
		}
		if conn != nil {
			_ = conn.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener did not refuse connections: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Signing in through the server built from the environment proves the handler
// uses the configured session cookie, with the production attributes.
func TestConfiguredServerIssuesTheProductionSessionCookie(t *testing.T) {
	setTestEnv(t, 8080)
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("BASE_URL", "https://app.example")
	dbtest.SetEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	d := dbtest.Open(t)
	accounts := store.New(d)
	const email, plaintext = "sample-account@example.com", "sample-passphrase-for-tests"
	hash, err := auth.HashPassword(t.Context(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.CreateUser(t.Context(), auth.User{Name: "Sample Account", Email: email, Role: auth.RoleMember, PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	srv, err := newServer(cfg, newLogger(io.Discard, cfg), accounts, d.Ping)
	if err != nil {
		t.Fatal(err)
	}
	c := webtest.New(t, srv.Handler, cfg.BaseURL)
	res := c.SignIn("/login", url.Values{"email": {email}, "password": {plaintext}})
	res.WantRedirect(http.StatusSeeOther, "/")
	webtest.WantCookie(t, res, webtest.CookieWant{
		Name: "__Host-session", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}
