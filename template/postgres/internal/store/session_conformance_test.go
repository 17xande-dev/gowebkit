package store_test

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/handler"
	"github.com/17xande-dev/websec/cookie"
	"github.com/17xande-dev/websec/session"
	"github.com/17xande-dev/websec/session/sessiontest"
	"github.com/17xande-dev/websec/webtest"
)

func TestSessionConformance(t *testing.T) {
	accounts, d := contractStore(t)
	const origin = "https://app.example"
	const plaintext = "conformance-sample-passphrase"
	const cookieName = "__Host-session"
	life := session.Lifetime{Idle: 2 * time.Hour, Absolute: 12 * time.Hour}
	cookies, err := cookie.New(cookie.Config{Name: "session"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		BaseURL: origin, CookieSecure: true, SessionCookie: cookies,
		SessionLifetime: life, SiteName: "Sample",
	}
	log, _ := webtest.Logger(t)
	app, _, err := handler.New(cfg, log, accounts, d.Ping)
	if err != nil {
		t.Fatal(err)
	}
	users := map[string]auth.User{}
	suiteCtx := t.Context()
	hashPassword := func(t *testing.T, value string) string {
		t.Helper()
		hash, err := auth.HashPassword(t.Context(), value)
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	identity := regexp.MustCompile(`<p id="signed-in-account">Signed in as (Session Account [0-9]+)\.</p>`)
	sessiontest.Run(t, sessiontest.Harness{
		Lifetime:     life,
		CookieTokens: true,
		NewUser: func(t *testing.T) string {
			n := len(users) + 1
			name := "Session Account " + strconv.Itoa(n)
			u, err := accounts.CreateUser(t.Context(), auth.User{
				Name: name, Email: fmt.Sprintf("session-%d@example.com", n), Role: auth.RoleMember,
				PasswordHash: hashPassword(t, plaintext),
			})
			if err != nil {
				t.Fatal(err)
			}
			users[name] = u
			t.Cleanup(func() {
				if err := accounts.RevokeSessions(suiteCtx, u.ID); err != nil {
					t.Error(err)
				}
			})
			return name
		},
		Create: func(t *testing.T, user string) string {
			// The suite signs in 100 times to the same account. Reconstruct
			// process-local limits without changing policy or bypassing the route.
			loginApp, _, err := handler.New(cfg, log, accounts, d.Ping)
			if err != nil {
				t.Fatal(err)
			}
			browser := webtest.New(t, loginApp, origin)
			res := browser.SignIn("/login", url.Values{"email": {users[user].Email}, "password": {plaintext}})
			res.WantRedirect(http.StatusSeeOther, "/")
			return webtest.WantCookie(t, res, webtest.CookieWant{
				Name: cookieName, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
			}).Value
		},
		Lookup: func(t *testing.T, token string) (string, bool, error) {
			c := webtest.New(t, app, origin)
			req := c.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
			res := c.Do(req)
			switch res.Status {
			case http.StatusSeeOther:
				res.WantRedirect(http.StatusSeeOther, "/login?next=%2F")
				return "", false, nil
			case http.StatusOK:
				m := identity.FindStringSubmatch(res.Body)
				if m != nil {
					return m[1], true, nil
				}
				return "", false, fmt.Errorf("protected page did not identify the signed-in account")
			default:
				return "", false, fmt.Errorf("unexpected protected page status: %d", res.Status)
			}
		},
		Backdate: func(t *testing.T, token string, created, seen time.Time) {
			backdate(t, d, token, created, seen)
		},
		LastSeen: func(t *testing.T, token string) time.Time {
			_, stored, err := accounts.LookupSession(t.Context(), auth.HashToken(token))
			if err != nil {
				t.Fatal(err)
			}
			return stored.LastSeen
		},
		Sweep: func(t *testing.T) int {
			n, err := accounts.SweepSessions(t.Context(), life, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			return int(n)
		},
		Count:   func(t *testing.T) int { return len(rawRows(t, d)) },
		RawRows: func(t *testing.T) []string { return rawRows(t, d) },
		Disable: func(t *testing.T, user string) {
			if err := accounts.SetDisabled(t.Context(), users[user].ID, true); err != nil {
				t.Fatal(err)
			}
		},
		// The session effects use the real store; their HTTP account screens
		// are separate from the sign-in and request-time lookup paths above.
		ChangePassword: func(t *testing.T, user, current string) string {
			u := users[user]
			newHash := hashPassword(t, "changed-conformance-passphrase")
			if err := accounts.ChangePassword(t.Context(), u.ID, u.PasswordHash, newHash, auth.HashToken(current)); err != nil {
				t.Fatal(err)
			}
			return current
		},
		RevokeUser: func(t *testing.T, user string) {
			if err := accounts.RevokeSessions(t.Context(), users[user].ID); err != nil {
				t.Fatal(err)
			}
		},
	})
}
