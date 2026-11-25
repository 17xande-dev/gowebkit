package handler

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
	"github.com/17xande-dev/password"
	"github.com/17xande-dev/websec/cookie"
	"github.com/17xande-dev/websec/session"
	"github.com/17xande-dev/websec/webtest"
)

const partialResponse = "partial output that must not escape"

func TestMain(m *testing.M) {
	defer password.SetParamsForTesting(password.Params{Memory: 64, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})()
	m.Run()
}

func testRouter(t *testing.T, brokenHome bool, ping func(context.Context) error) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := newSite(testConfig(t), log, store.New(dbtest.Open(t)))
	if err != nil {
		t.Fatal(err)
	}
	return testPageHandler(s, brokenHome, ping)
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cookies, err := cookie.New(cookie.Config{Name: "session"})
	if err != nil {
		t.Fatal(err)
	}
	return config.Config{
		BaseURL: "https://app.example", CookieSecure: true, SiteName: "Sample",
		SessionCookie: cookies, SessionLifetime: session.Default,
	}
}

func testPageHandler(s *site, brokenHome bool, ping func(context.Context) error) http.Handler {
	if brokenHome {
		s.pages["app/home"] = template.Must(template.New("layout").Parse(partialResponse + "{{.Missing}}"))
	}
	h, _ := s.routes(ping)
	return h
}

const testEmail, testPassword = "sample-account@example.com", "sample-passphrase-for-tests"

func seedAccount(t *testing.T, accounts *store.Store, email, plaintext string) auth.User {
	t.Helper()
	return seedAccountRole(t, accounts, email, plaintext, auth.RoleMember)
}

func seedAccountRole(t *testing.T, accounts *store.Store, email, plaintext string, role auth.Role) auth.User {
	t.Helper()
	hash, err := auth.HashPassword(t.Context(), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	user, err := accounts.CreateUser(t.Context(), auth.User{Name: "Sample Account", Role: role, Email: email, PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func renderedPageClient(t *testing.T, path string, role auth.Role, brokenHome bool) *webtest.Client {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := testConfig(t)
	accounts := store.New(dbtest.Open(t))
	s, err := newSite(cfg, log, accounts)
	if err != nil {
		t.Fatal(err)
	}
	c := webtest.New(t, testPageHandler(s, brokenHome, nil), cfg.BaseURL)
	if role != "" {
		seedAccountRole(t, accounts, testEmail, testPassword, role)
		c.SignIn("/login", url.Values{"email": {testEmail}, "password": {testPassword}}).
			WantRedirect(http.StatusSeeOther, "/")
	}
	return c
}

func request(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestPageRoutes(t *testing.T) {
	for _, route := range pageRoutes {
		t.Run(route.page, func(t *testing.T) {
			c := renderedPageClient(t, route.path, route.role, route.brokenHome)
			res := c.Get(route.path)
			res.WantStatus(route.status)
			body := res.Body
			if !strings.Contains(body, route.heading) {
				t.Fatalf("unexpected body %q", body)
			}
			if !strings.Contains(body, route.layout) || strings.Contains(body, partialResponse) {
				t.Fatalf("wrong layout or partial rendering escaped: %q", body)
			}
			if got := res.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q", got)
			}
			head := c.Do(c.NewRequest(http.MethodHead, route.path, nil))
			head.WantStatus(route.status)
			if head.Body != "" {
				t.Errorf("HEAD returned body %q", head.Body)
			}
		})
	}
}

var pageRoutes = []struct {
	page, path, heading, layout string
	status                      int
	role                        auth.Role
	brokenHome                  bool
}{
	{"app/home", "/", "Welcome to Sample", "app-shell", http.StatusOK, auth.RoleMember, false},
	{"app/password", "/account/password", "Change password", "app-shell", http.StatusOK, auth.RoleMember, false},
	{"app/users", "/admin/users", "Create an account", "app-shell", http.StatusOK, auth.RoleAdmin, false},
	{"app/forbidden", "/admin/users", "Not allowed", "app-shell", http.StatusForbidden, auth.RoleMember, false},
	{"plain/not-found", "/missing", "Page not found", "plain-shell", http.StatusNotFound, "", false},
	{"plain/server-error", "/", "Something went wrong", "plain-shell", http.StatusInternalServerError, auth.RoleMember, true},
	{"plain/login", "/login", "Sign in", "plain-shell", http.StatusOK, "", false},
	{"plain/setup", "/setup", "Set up account", "plain-shell", http.StatusOK, "", false},
}

func TestHealthz(t *testing.T) {
	for _, tt := range []struct {
		name   string
		ping   func(context.Context) error
		status int
		body   string
	}{
		{"ready", func(ctx context.Context) error {
			end, hasDeadline := ctx.Deadline()
			if !hasDeadline || time.Until(end) <= 0 || time.Until(end) > 2*time.Second {
				t.Errorf("unexpected health deadline: %v, %v", end, hasDeadline)
			}
			return nil
		}, http.StatusOK, "ok\n"},
		{"unavailable", func(context.Context) error { return errors.New("private detail") }, http.StatusServiceUnavailable, "unavailable\n"},
		{"missing check", nil, http.StatusServiceUnavailable, "unavailable\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := request(testRouter(t, false, tt.ping), http.MethodGet, "/healthz")
			if rec.Code != tt.status || rec.Body.String() != tt.body || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("got status %d, body %q, headers %v", rec.Code, rec.Body.String(), rec.Header())
			}
		})
	}
}

func TestHealthzLogsFailureWithoutExposingIt(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	cfg := testConfig(t)
	h, _, err := New(cfg, log, store.New(dbtest.Open(t)), func(context.Context) error { return errors.New("sample database error") })
	if err != nil {
		t.Fatal(err)
	}
	rec := request(h, http.MethodGet, "/healthz")
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "unavailable\n" {
		t.Fatalf("health response: status=%d body=%q", rec.Code, rec.Body)
	}
	for _, detail := range []string{"level=WARN", "health check failed", `error="sample database error"`} {
		if !strings.Contains(logs.String(), detail) {
			t.Errorf("health log lacks %q: %s", detail, &logs)
		}
	}
}
