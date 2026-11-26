package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
	"github.com/17xande-dev/ratelimit/clientip"
	"github.com/17xande-dev/websec/cookie"
	"github.com/17xande-dev/websec/webtest"
)

func TestFirstAccountSetup(t *testing.T) {
	const token = "first-account-credential-for-tests"
	const wrong = "incorrect-credential-for-tests"
	c, accounts, _, logs := authFixture(t)
	if ok, err := accounts.IssueSetupToken(t.Context(), auth.HashToken(token)); !ok || err != nil {
		t.Fatalf("issue token: %v, %v", ok, err)
	}
	form := c.Page("/setup").Form("/setup").Set("token", token).Set("name", "  First Account  ").
		Set("email", "FIRST-ACCOUNT@EXAMPLE.COM").Set("password", testPassword)
	stale := form.Values()
	c.PostForm("/setup", nil).WantStatus(http.StatusForbidden)
	form.Submit(webtest.WithOrigin("https://other.example")).WantStatus(http.StatusForbidden)
	for _, tt := range []struct{ field, value, message string }{
		{"name", " ", "Enter a name"},
		{"name", strings.Repeat("a", 121), "Enter a name"},
		{"email", "bad mailbox", "Enter a valid email address"},
		{"password", "too short", "Use at least 12 characters"},
		{"token", wrong, "The setup token is invalid"},
	} {
		t.Run(tt.field+"/"+tt.message, func(t *testing.T) {
			data := form.Values()
			data.Set(tt.field, tt.value)
			res := c.With(t).PostForm("/setup", data)
			res.WantStatus(http.StatusUnprocessableEntity)
			if !strings.Contains(res.Body, tt.message) || strings.Contains(res.Body, token) || strings.Contains(res.Body, testPassword) {
				t.Fatal("setup refusal lost its message or echoed a credential")
			}
			if n, err := accounts.UserCount(t.Context()); n != 0 || err != nil {
				t.Fatalf("rejected setup created an account: count %d, err %v", n, err)
			}
		})
	}
	res := form.Submit()
	res.WantRedirect(http.StatusSeeOther, "/")
	webtest.WantCookie(t, res, secureSession)
	c.Follow(res).WantStatus(http.StatusOK)
	user, err := accounts.UserByEmail(t.Context(), "first-account@example.com")
	if err != nil || user.Email != "first-account@example.com" || user.Name != "First Account" ||
		user.Role != auth.RoleAdmin || user.MustChangePassword || user.Disabled {
		t.Fatalf("first admin account was not normalized and enabled: %v", err)
	}
	state, err := accounts.SetupState(t.Context())
	if err != nil || !state.Consumed {
		t.Fatalf("first account did not spend its token: %v", err)
	}
	c.PostForm("/logout", stale).WantStatus(http.StatusForbidden)
	c.Get("/setup").WantStatus(http.StatusNotFound)
	c.PostForm("/setup", stale).WantStatus(http.StatusNotFound)
	c.PostForm("/setup", nil).WantStatus(http.StatusNotFound)
	current := c.Page("/").Form("/logout").Values()
	c.PostForm("/setup", current).WantStatus(http.StatusNotFound)
	webtest.NoSecrets(t, logs.String(), `"pattern":"POST /setup"`, token, wrong, testPassword)
}

func TestSignInSignOutRotateAndRevoke(t *testing.T) {
	c, accounts, app, _ := authFixture(t)
	seedAccount(t, accounts, testEmail, testPassword)
	c.Get("/").WantRedirect(http.StatusSeeOther, "/login?next=%2F")
	form := c.Page("/login").Form("/login").Set("email", testEmail).Set("password", testPassword)
	loginFields := form.Values()
	res := form.Submit()
	res.WantRedirect(http.StatusSeeOther, "/")
	credential := webtest.WantCookie(t, res, secureSession)
	if credential.MaxAge < 43190 || credential.MaxAge > 43200 || credential.Expires.IsZero() {
		t.Fatal("session cookie has no twelve-hour absolute deadline")
	}
	c.PostForm("/login", loginFields).WantStatus(http.StatusForbidden)

	logout := c.Page("/").Form("/logout")
	logoutFields := logout.Values()
	res = logout.Submit()
	res.WantRedirect(http.StatusSeeOther, "/login")
	cleared := secureSession
	cleared.Cleared = true
	webtest.WantCookie(t, res, cleared)
	c.PostForm("/logout", logoutFields).WantStatus(http.StatusForbidden)
	c.Get("/").WantRedirect(http.StatusSeeOther, "/login?next=%2F")
	other := webtest.New(t, app, "https://app.example")
	other.SetCookie(credential)
	other.Get("/").WantRedirect(http.StatusSeeOther, "/login?next=%2F")
}

func TestSignInCookieLocalAndLegacy(t *testing.T) {
	cfg := testConfig(t)
	c, accounts, app, _ := authFixture(t)
	seedAccount(t, accounts, testEmail, testPassword)
	login := c.SignIn("/login", url.Values{"email": {testEmail}, "password": {testPassword}})
	secure := login.SetCookie("__Host-session")
	bare := *secure
	bare.Name = "session"
	legacy := webtest.New(t, app, cfg.BaseURL)
	legacy.SetCookie(&bare)
	legacy.Get("/").WantRedirect(http.StatusSeeOther, "/login?next=%2F")

	cfg.BaseURL, cfg.CookieSecure = "http://app.example", false
	var err error
	cfg.SessionCookie, err = cookie.New(cookie.Config{Name: "session", Insecure: !cfg.CookieSecure})
	if err != nil {
		t.Fatal(err)
	}
	log, _ := webtest.Logger(t)
	app, _, err = New(cfg, log, accounts, nil)
	if err != nil {
		t.Fatal(err)
	}
	local := webtest.New(t, app, cfg.BaseURL)
	login = local.SignIn("/login", url.Values{"email": {testEmail}, "password": {testPassword}})
	webtest.WantCookie(t, login, webtest.CookieWant{
		Name: "session", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	local.Follow(login).WantStatus(http.StatusOK)
}

func TestSignInFailuresAndNoSecrets(t *testing.T) {
	c, accounts, _, logs := authFixture(t)
	user := seedAccount(t, accounts, testEmail, testPassword)
	disabled := seedAccount(t, accounts, "disabled-account@example.com", testPassword)
	if err := accounts.SetDisabled(t.Context(), disabled.ID, true); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	tokenValue := regexp.MustCompile(`(name="csrf_token" value=")[^"]+`)
	for _, tt := range []struct{ email, password, marker string }{
		{testEmail, "different-sample-passphrase", `"user_id":`},
		{disabled.Email, testPassword, `"user_id":`},
		{"unknown-account@example.com", testPassword, "unknown account"},
	} {
		t.Run(tt.email, func(t *testing.T) {
			logs.Reset()
			res := c.With(t).Page("/login").Form("/login").Set("email", tt.email).Set("password", tt.password).Submit()
			res.WantStatus(http.StatusUnprocessableEntity)
			if !strings.Contains(res.Body, signInFailed) || strings.Contains(res.Body, tt.email) || strings.Contains(res.Body, tt.password) {
				t.Fatal("sign-in response lost its generic message or echoed credentials")
			}
			bodies = append(bodies, tokenValue.ReplaceAllString(res.Body, "${1}"))
			text := logs.String()
			webtest.NoSecrets(t, text, tt.marker, tt.email, tt.password, user.PasswordHash, disabled.PasswordHash)
			if !strings.Contains(text, `"client_ip":`) {
				t.Fatal("sign-in refusal lacks the client IP")
			}
			if _, signedIn := c.Cookie("__Host-session"); signedIn {
				t.Fatal("sign-in failure issued a credential")
			}
		})
	}
	if len(bodies) != 3 || bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatal("credential failures returned visibly different pages")
	}
}

func TestLoginRedirectsAreValidated(t *testing.T) {
	nexts := append(webtest.HostileRedirects(), "/login", "/%6cogin", "/logout", "/setup", "/?view=summary")
	for _, next := range nexts {
		// Fresh limits keep throttling from obscuring a redirect assertion.
		c, accounts, _, _ := authFixture(t)
		seedAccount(t, accounts, testEmail, testPassword)
		form := c.Page("/login?"+url.Values{"next": {next}}.Encode()).Form("/login").
			Set("email", testEmail).Set("password", testPassword)
		want := "/"
		if next == "/?view=summary" {
			want = next
		}
		if got := form.Values().Get("next"); got != want {
			t.Fatalf("next field = %q, want %q", got, want)
		}
		form.Set("next", next).Submit().WantRedirect(http.StatusSeeOther, want)
	}
	c, accounts, _, _ := authFixture(t)
	seedAccount(t, accounts, testEmail, testPassword)
	form := c.Page("/login").Form("/login").Set("email", testEmail).Set("password", testPassword)
	c.PostForm("/login?next=%2F%3Fview%3Dsummary", form.Delete("next").Values()).
		WantRedirect(http.StatusSeeOther, "/?view=summary")
}

func TestSignInRateTiersCheckIPBeforeAccount(t *testing.T) {
	cfg := testConfig(t)
	var err error
	cfg.ClientIPSource, err = clientip.ParseSource("forwarded")
	if err != nil {
		t.Fatal(err)
	}
	log, _ := webtest.Logger(t)
	accounts := store.New(dbtest.Open(t))
	h, _, err := New(cfg, log, accounts, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := webtest.New(t, h, cfg.BaseURL)
	seedAccount(t, accounts, testEmail, testPassword)
	fields := c.Page("/login").Form("/login").Set("password", "different-sample-passphrase").Values()
	attempt := func(peer, email string) *webtest.Response {
		fields.Set("email", email)
		return c.PostForm("/login", fields, webtest.Header("X-Forwarded-For", peer))
	}
	const blockedPeer = "192.0.2.1"
	for i := range 10 {
		attempt(blockedPeer, fmt.Sprintf("other-%d@example.com", i)).WantStatus(http.StatusUnprocessableEntity)
	}
	ipRefusal := attempt(blockedPeer, testEmail)
	ipRefusal.WantStatus(http.StatusTooManyRequests)
	if ipRefusal.Header.Get("Retry-After") != "10" {
		t.Fatal("IP refusal lacks the configured retry hint")
	}
	for i, email := range []string{testEmail, strings.ToUpper(testEmail), "  " + testEmail + " ", "Sample <" + testEmail + ">", testEmail} {
		attempt(fmt.Sprintf("192.0.2.%d", i+2), email).WantStatus(http.StatusUnprocessableEntity)
	}
	attempt("192.0.2.8", testEmail).WantStatus(http.StatusTooManyRequests)
	attempt("192.0.2.8", "other-account@example.com").WantStatus(http.StatusUnprocessableEntity)
	// Unvalidated input can exceed the limiter's key limit; AccountKey
	// bounds that input without making the first attempt look throttled.
	attempt("192.0.2.9", strings.Repeat("x", 300)).WantStatus(http.StatusUnprocessableEntity)
}

func TestUnknownAccountRunsDummyPasswordCheck(t *testing.T) {
	c, _, _, logs := authFixture(t)
	fields := c.Page("/login").Form("/login").Set("email", "malformed mailbox").Set("password", testPassword).Values()
	// A canceled request makes the hash library refuse its slot. An invalid
	// address bypasses the database, so that cancellation is observed at the KDF.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c.PostForm("/login", fields, func(r *http.Request) { *r = *r.WithContext(ctx) }).
		WantStatus(http.StatusUnprocessableEntity)
	webtest.NoSecrets(t, logs.String(), "dummy password check failed", testPassword)
}

func TestSetupHasSecretRouteLimit(t *testing.T) {
	c, accounts, _, _ := authFixture(t)
	const token = "first-account-credential-for-tests"
	if ok, err := accounts.IssueSetupToken(t.Context(), auth.HashToken(token)); !ok || err != nil {
		t.Fatalf("issue token: %v, %v", ok, err)
	}
	form := c.Page("/setup").Form("/setup").Set("name", "First Account").
		Set("email", testEmail).Set("password", testPassword).Set("token", "incorrect")
	for range 10 {
		form.Submit().WantStatus(http.StatusUnprocessableEntity)
	}
	form.Set("token", token).Submit().WantStatus(http.StatusTooManyRequests)
	if n, err := accounts.UserCount(t.Context()); n != 0 || err != nil {
		t.Fatalf("throttled setup created an account: count %d, err %v", n, err)
	}
}

func TestCSRFRefusalHasSecurityHeadersAndRoutePattern(t *testing.T) {
	c, _, _, logs := authFixture(t)
	const secret = "query-credential-not-for-logs"
	result := c.PostForm("/login?token="+secret, url.Values{"email": {testEmail}})
	result.WantStatus(http.StatusForbidden)
	if result.Header.Get("Content-Security-Policy") == "" || result.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("CSRF refusal lacks CSP or no-store")
	}
	webtest.NoSecrets(t, logs.String(), `"path":"POST /login"`, secret, testEmail)
	unmatched := c.PostForm("/"+secret, nil)
	unmatched.WantStatus(http.StatusForbidden)
	webtest.NoSecrets(t, logs.String(), `"path":"unmatched"`, secret)
}

func TestCSRFBodyLimitsBeforeHandlers(t *testing.T) {
	c, _, _, logs := authFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			c := c.With(t)
			body := webtest.NewCountingReader(bytes.NewReader([]byte("not an attachment")))
			req := c.NewRequest(method, "/login", body)
			c.Do(req, webtest.Header("Content-Type", "multipart/form-data; boundary=unused")).
				WantStatus(http.StatusUnsupportedMediaType)
			if body.N() != 0 {
				t.Fatal("multipart refusal consumed the request body")
			}
		})
	}
	body := webtest.NewCountingReader(webtest.Filler(300 << 10))
	req := c.NewRequest(http.MethodPost, "/login", body)
	req.ContentLength = 300 << 10
	c.Do(req, webtest.Header("Content-Type", "application/x-www-form-urlencoded")).WantStatus(http.StatusRequestEntityTooLarge)
	if body.N() != 0 {
		t.Fatal("declared oversize form was read")
	}
	webtest.NoSecrets(t, logs.String(), `"path":"POST /login"`)
}

// protectedPatterns is every route registered through the protect closure. A
// new route must be added here, which puts it under the anonymous sweep, the
// must-change sweep and (for admin routes) the member sweep.
var protectedPatterns = []string{
	"GET /{$}", "GET /account/password", "POST /account/password",
	"GET /admin/users", "POST /admin/users", "POST /admin/users/{id}/role",
	"POST /admin/users/{id}/disabled", "POST /admin/users/{id}/password", "POST /admin/mail-test",
	"GET /admin/jobs", "POST /admin/jobs/{id}/retry",
}

func TestAnonymousProtectedRoutes(t *testing.T) {
	cfg := testConfig(t)
	log, _ := webtest.Logger(t)
	h, patterns, err := New(cfg, log, store.New(dbtest.Open(t)), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(slices.Sorted(slices.Values(patterns)), slices.Sorted(slices.Values(protectedPatterns))) {
		t.Fatalf("protected routes changed; list the new route in protectedPatterns: %v", patterns)
	}
	// HEAD is not a declared method of a GET pattern, so the sweep below does
	// not send it; it must get the same sign-in redirect.
	anonymous := webtest.New(t, h, cfg.BaseURL)
	for _, route := range webtest.Routes(t, patterns, map[string]string{"id": "1"}) {
		if route.Method != http.MethodGet {
			continue
		}
		res := anonymous.Do(anonymous.NewRequest(http.MethodHead, route.Path, nil))
		res.WantStatus(http.StatusSeeOther)
		if !strings.HasPrefix(res.Location(), "/login?next=") {
			t.Errorf("HEAD %s redirected to %q", route.Path, res.Location())
		}
	}
	webtest.Sweep{
		Routes:    webtest.Routes(t, patterns, map[string]string{"id": "1"}),
		Min:       len(protectedPatterns),
		Client:    func() *webtest.Client { return webtest.New(t, h, cfg.BaseURL) },
		TokenPage: "/login",
		Refused:   webtest.ByMethod(webtest.RedirectsTo("/login?next="), webtest.Status(http.StatusUnauthorized)),
	}.Run(t)
}

// The setup token comparison cannot be pinned by timing, so pin the call.
func TestSetupTokenIsComparedInConstantTime(t *testing.T) {
	source, err := os.ReadFile("setup.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "subtle.ConstantTimeCompare(state.TokenHash[:], digest[:])") {
		t.Fatal("the setup token is no longer compared with subtle.ConstantTimeCompare on digests")
	}
}

func TestDirectRouteMountsAreExplicit(t *testing.T) {
	source, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`(?m)^[\t ]*mux\.Handle(?:Func)?\(.+$`)
	got := pin.FindAllString(string(source), -1)
	for i := range got {
		got[i] = strings.TrimSpace(got[i])
	}
	if want := []string{
		`mux.Handle(pattern, requireUser(s.gate(own, admin, handler)))`,
		`mux.Handle("GET /healthz", Healthz(s.log, ping))`,
		`mux.Handle("GET /static/{name}", s.assets.serve(s.notFound))`,
		`mux.HandleFunc("GET /login", s.loginPage)`,
		`mux.Handle("POST /login", s.secretLimit(http.HandlerFunc(s.login)))`,
		`mux.HandleFunc("POST /logout", s.logout)`,
		`mux.HandleFunc("GET /setup", s.setupPage)`,
		`mux.Handle("POST /setup", s.secretLimit(http.HandlerFunc(s.setup)))`,
		`mux.HandleFunc("GET /", s.notFound)`,
	}; !slices.Equal(got, want) {
		t.Fatalf("direct mux mounts changed; add protected routes through protect:\n%s", strings.Join(got, "\n"))
	}
}

var secureSession = webtest.CookieWant{
	Name: "__Host-session", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
}

func authFixture(t *testing.T) (*webtest.Client, *store.Store, http.Handler, *webtest.Logs) {
	t.Helper()
	cfg := testConfig(t)
	log, logs := webtest.Logger(t)
	accounts := store.New(dbtest.Open(t))
	h, _, err := New(cfg, log, accounts, nil)
	if err != nil {
		t.Fatal(err)
	}
	return webtest.New(t, h, cfg.BaseURL), accounts, h, logs
}

// A well-formed address that has no account reaches the store first, so it is
// a different path from a malformed one. Each must pay for exactly one dummy
// check, and a real account must not (its own password check costs the same).
func TestUnknownAccountPaysForExactlyOneDummyCheck(t *testing.T) {
	var calls int
	previous := checkDummy
	checkDummy = func(ctx context.Context, plaintext string) (bool, error) {
		calls++
		return previous(ctx, plaintext)
	}
	t.Cleanup(func() { checkDummy = previous })
	c, accounts, _, _ := authFixture(t)
	seedAccount(t, accounts, testEmail, testPassword)
	for _, tt := range []struct {
		name, email string
		want        int
	}{
		{"well-formed unknown", "unknown-account@example.com", 1},
		{"malformed", "malformed mailbox", 1},
		{"known account", testEmail, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls = 0
			c.With(t).Page("/login").Form("/login").Set("email", tt.email).Set("password", "different-sample-passphrase").
				Submit().WantStatus(http.StatusUnprocessableEntity)
			if calls != tt.want {
				t.Fatalf("dummy checks = %d, want %d", calls, tt.want)
			}
		})
	}
}

func TestSignInEndsThePresentedSession(t *testing.T) {
	c, accounts, app, _ := authFixture(t)
	seedAccount(t, accounts, testEmail, testPassword)
	credentials := url.Values{"email": {testEmail}, "password": {testPassword}}
	first := c.SignIn("/login", credentials).SetCookie("__Host-session")
	second := c.SignIn("/login", credentials).SetCookie("__Host-session")
	if first.Value == second.Value {
		t.Fatal("signing in again kept the same credential")
	}
	c.Get("/").WantStatus(http.StatusOK)
	// Another browser still holding the first credential is signed out.
	other := webtest.New(t, app, "https://app.example")
	other.SetCookie(first)
	other.Get("/").WantRedirect(http.StatusSeeOther, "/login?next=%2F")
}

// A spent token with no accounts is the state after the first account was
// removed by hand. The page must say so, and the guard in the store must not
// be the only thing that refuses it.
func TestSetupWithASpentTokenAndNoAccountsIsRefused(t *testing.T) {
	const token = "first-account-credential-for-tests"
	cfg := testConfig(t)
	log, logs := webtest.Logger(t)
	d := dbtest.Open(t)
	accounts := store.New(d)
	h, _, err := New(cfg, log, accounts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := accounts.IssueSetupToken(t.Context(), auth.HashToken(token)); !ok || err != nil {
		t.Fatalf("issue token: %v, %v", ok, err)
	}
	hash, err := auth.HashPassword(t.Context(), testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.ClaimSetup(t.Context(), auth.HashToken(token), auth.User{Name: "First", Email: testEmail, PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(t.Context(), `DELETE FROM users`); err != nil {
		t.Fatal(err)
	}
	c := webtest.New(t, h, cfg.BaseURL)
	res := c.Page("/setup").Form("/setup").Set("token", token).Set("name", "Second Account").
		Set("email", "second-account@example.com").Set("password", testPassword).Submit()
	res.WantStatus(http.StatusUnprocessableEntity)
	if !strings.Contains(res.Body, "The setup token is invalid") {
		t.Fatal("a spent token was not refused with the setup message")
	}
	if n, err := accounts.UserCount(t.Context()); n != 0 || err != nil {
		t.Fatalf("a spent token created an account: count %d, err %v", n, err)
	}
	webtest.NoSecrets(t, logs.String(), "setup refused", token, testPassword)
}

func TestSetupRefusalIsLoggedWithoutTheToken(t *testing.T) {
	const token = "first-account-credential-for-tests"
	const guess = "guessed-credential-for-tests-only"
	c, accounts, _, logs := authFixture(t)
	if ok, err := accounts.IssueSetupToken(t.Context(), auth.HashToken(token)); !ok || err != nil {
		t.Fatalf("issue token: %v, %v", ok, err)
	}
	c.Page("/setup").Form("/setup").Set("token", guess).Set("name", "First Account").
		Set("email", testEmail).Set("password", testPassword).Submit().WantStatus(http.StatusUnprocessableEntity)
	text := logs.String()
	if !strings.Contains(text, "setup refused") || !strings.Contains(text, `"client_ip":`) {
		t.Fatalf("a wrong setup token was not logged with the client: %s", text)
	}
	webtest.NoSecrets(t, text, "setup refused", token, guess, testPassword, testEmail)
}
