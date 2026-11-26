package handler

import (
	"bufio"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/jobs"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/mail"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
	"github.com/17xande-dev/mailer"
	"github.com/17xande-dev/outbox"
	"github.com/17xande-dev/outbox/outboxtest"
	"github.com/17xande-dev/websec/webtest"
)

type adminSite struct {
	h        http.Handler
	accounts *store.Store
	queue    *jobs.Queue
	clock    *outboxtest.Clock
	admins   []string
	patterns []string
	logs     *webtest.Logs
	cfgURL   string
}

func newAdminSite(t *testing.T, opts ...Option) *adminSite {
	t.Helper()
	return newAdminSiteWithMail(t, nil, opts...)
}

func newAdminSiteWithMail(t *testing.T, sender mailer.Sender, opts ...Option) *adminSite {
	t.Helper()
	cfg := testConfig(t)
	log, logs := webtest.Logger(t)
	d := dbtest.Open(t)
	accounts := store.New(d)
	delivers := sender != nil
	if !delivers {
		sender = mail.Discard(log)
	}
	clock := outboxtest.NewClock(time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
	queue, err := jobs.New(d, sender, cfg.SiteName, outbox.Options{Logger: log, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	s, err := newSite(cfg, log, accounts, append(opts, WithJobs(queue, delivers))...)
	if err != nil {
		t.Fatal(err)
	}
	h, patterns, admins := s.routesWithAdmin(nil)
	return &adminSite{h: h, accounts: accounts, queue: queue, clock: clock, admins: admins, patterns: patterns, logs: logs, cfgURL: cfg.BaseURL}
}

func (a *adminSite) client(t *testing.T) *webtest.Client { return webtest.New(t, a.h, a.cfgURL) }

func (a *adminSite) signedIn(t *testing.T, email string) *webtest.Client {
	t.Helper()
	c := a.client(t)
	c.SignIn("/login", url.Values{"email": {email}, "password": {testPassword}}).WantStatus(http.StatusSeeOther)
	return c
}

func (a *adminSite) seed(t *testing.T, email string, role auth.Role) auth.User {
	t.Helper()
	return seedAccountRole(t, a.accounts, email, testPassword, role)
}

func post(c *webtest.Client, from, path string, fields url.Values) *webtest.Response {
	if fields == nil {
		fields = url.Values{}
	}
	fields.Set(webtest.TokenField, c.Page(from).Token())
	return c.PostForm(path, fields)
}

func TestPasswordChange(t *testing.T) {
	a := newAdminSite(t)
	user := a.seed(t, testEmail, auth.RoleMember)
	c := a.signedIn(t, testEmail)
	other := a.signedIn(t, testEmail)
	const fresh = "a-different-sample-passphrase"

	form := c.Page("/account/password").Form("/account/password")
	stale := form.Values()
	for _, tt := range []struct{ name, current, next, confirm, message string }{
		{"wrong current", "not-the-current-passphrase", fresh, fresh, "current password is not correct"},
		{"mismatch", testPassword, fresh, fresh + "x", "confirmation differ"},
		{"short", testPassword, "short", "short", "at least 12 characters"},
		{"unchanged", testPassword, testPassword, testPassword, "different from the current"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Bound to the subtest, so a failed assertion reports here and not on the parent.
			sub := c.With(t).Page("/account/password").Form("/account/password")
			res := sub.Set("current", tt.current).Set("new", tt.next).Set("confirm", tt.confirm).Submit()
			res.WantStatus(http.StatusUnprocessableEntity)
			if !strings.Contains(res.Body, tt.message) || strings.Contains(res.Body, tt.current) || strings.Contains(res.Body, tt.next) {
				t.Fatalf("refusal lost its message or echoed a password: %s", res.Body)
			}
			got, err := a.accounts.UserByID(t.Context(), user.ID)
			if err != nil || got.PasswordHash != user.PasswordHash {
				t.Fatalf("refused change altered the account: %v", err)
			}
		})
	}
	res := form.Set("current", testPassword).Set("new", fresh).Set("confirm", fresh).Submit()
	res.WantRedirect(http.StatusSeeOther, "/?notice=password-changed")
	// The form token from before the change is dead.
	stale.Set("current", fresh)
	stale.Set("new", "yet-another-sample-passphrase")
	stale.Set("confirm", "yet-another-sample-passphrase")
	c.PostForm("/account/password", stale).WantStatus(http.StatusForbidden)
	c.Follow(res).WantStatus(http.StatusOK)
	// This session survives; every other one of the same user ends.
	c.Get("/account/password").WantStatus(http.StatusOK)
	other.Get("/").WantRedirect(http.StatusSeeOther, "/login?next=%2F")
	webtest.NoSecrets(t, a.logs.String(), `"user_id"`, testPassword, fresh)

	fresh2 := a.client(t)
	fresh2.SignIn("/login", url.Values{"email": {testEmail}, "password": {fresh}}).WantStatus(http.StatusSeeOther)
	old := a.client(t)
	old.Page("/login").Form("/login").Set("email", testEmail).Set("password", testPassword).Submit().WantStatus(http.StatusUnprocessableEntity)
}

func TestPasswordChangeHasSecretRouteLimit(t *testing.T) {
	a := newAdminSite(t)
	user := a.seed(t, testEmail, auth.RoleMember)
	a.seed(t, "other-account@example.com", auth.RoleMember)
	c := a.signedIn(t, testEmail)
	other := a.signedIn(t, "other-account@example.com")
	attempt := func(c *webtest.Client, current string) *webtest.Response {
		return c.Page("/account/password").Form("/account/password").Set("current", current).
			Set("new", "a-different-sample-passphrase").Set("confirm", "a-different-sample-passphrase").Submit()
	}
	// Two sign-ins used two of the ten shared per-address attempts. Each account
	// stays under its own limit, so only the address tier can refuse the ninth.
	for range 4 {
		attempt(c, "not-the-current-passphrase").WantStatus(http.StatusUnprocessableEntity)
		attempt(other, "not-the-current-passphrase").WantStatus(http.StatusUnprocessableEntity)
	}
	attempt(c, testPassword).WantStatus(http.StatusTooManyRequests)
	if got, err := a.accounts.UserByID(t.Context(), user.ID); err != nil || got.PasswordHash != user.PasswordHash {
		t.Fatalf("throttled change altered the account: %v", err)
	}
}

// A session used from many addresses is still limited per account.
func TestPasswordChangeHasAnAccountLimit(t *testing.T) {
	a := newAdminSite(t)
	user := a.seed(t, testEmail, auth.RoleMember)
	a.seed(t, "other-account@example.com", auth.RoleMember)
	c := a.signedIn(t, testEmail)
	other := a.signedIn(t, "other-account@example.com")
	attempt := func(c *webtest.Client, current string) *webtest.Response {
		return c.Page("/account/password").Form("/account/password").Set("current", current).
			Set("new", "a-different-sample-passphrase").Set("confirm", "a-different-sample-passphrase").Submit()
	}
	for range 5 {
		attempt(c, "not-the-current-passphrase").WantStatus(http.StatusUnprocessableEntity)
	}
	// The right password no longer helps, and the address tier is not the cause.
	attempt(c, testPassword).WantStatus(http.StatusTooManyRequests)
	if got, err := a.accounts.UserByID(t.Context(), user.ID); err != nil || got.PasswordHash != user.PasswordHash {
		t.Fatalf("throttled change altered the account: %v", err)
	}
	attempt(other, "not-the-current-passphrase").WantStatus(http.StatusUnprocessableEntity)
}

func TestMustChangeRedirectsEverySignedInRoute(t *testing.T) {
	a := newAdminSite(t)
	hash, err := auth.HashPassword(t.Context(), testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []auth.Role{auth.RoleMember, auth.RoleAdmin} {
		t.Run(string(role), func(t *testing.T) {
			email := string(role) + "-temporary@example.com"
			if _, err := a.accounts.CreateUser(t.Context(), auth.User{
				Name: "Temporary", Email: email, Role: role, PasswordHash: hash, MustChangePassword: true,
			}); err != nil {
				t.Fatal(err)
			}
			c := a.signedIn(t, email)
			token := c.Page("/account/password").Token()
			open := map[string]bool{"GET /account/password": true, "POST /account/password": true}
			swept := 0
			for _, route := range webtest.Routes(t, a.patterns, map[string]string{"id": "1"}) {
				if open[route.Pattern] {
					continue
				}
				var res *webtest.Response
				if route.Method == http.MethodGet {
					res = c.Get(route.Path)
				} else {
					res = c.PostForm(route.Path, url.Values{webtest.TokenField: {token}})
				}
				res.WantRedirect(http.StatusSeeOther, "/account/password")
				swept++
			}
			webtest.MinCount(t, "swept routes", swept, 9)
			c.Get("/account/password").WantStatus(http.StatusOK)
			// Signing out stays possible, or the user is stuck.
			post(c, "/account/password", "/logout", nil).WantStatus(http.StatusSeeOther)
		})
	}
	// A forced change still needs the current password.
	c := a.signedIn(t, "member-temporary@example.com")
	before, err := a.accounts.UserByEmail(t.Context(), "member-temporary@example.com")
	if err != nil {
		t.Fatal(err)
	}
	c.Page("/account/password").Form("/account/password").Set("current", "not-the-current-passphrase").
		Set("new", "a-different-sample-passphrase").Set("confirm", "a-different-sample-passphrase").Submit().
		WantStatus(http.StatusUnprocessableEntity)
	if after, err := a.accounts.UserByEmail(t.Context(), before.Email); err != nil || after.PasswordHash != before.PasswordHash || !after.MustChangePassword {
		t.Fatalf("a wrong current password changed a forced-change account: %v", err)
	}
	c.Get("/").WantRedirect(http.StatusSeeOther, "/account/password")
	// Changing the password lifts the redirect.
	c.Page("/account/password").Form("/account/password").Set("current", testPassword).
		Set("new", "a-different-sample-passphrase").Set("confirm", "a-different-sample-passphrase").Submit().
		WantRedirect(http.StatusSeeOther, "/?notice=password-changed")
	c.Get("/").WantStatus(http.StatusOK)
}

func TestMemberIsRefusedEveryAdminRoute(t *testing.T) {
	a := newAdminSite(t)
	a.seed(t, "admin-account@example.com", auth.RoleAdmin)
	target := a.seed(t, testEmail, auth.RoleMember)
	c := a.signedIn(t, testEmail)
	token := c.Page("/").Token()
	before, _ := a.accounts.ListUsers(t.Context())
	// Name the queue routes too, so dropping their admin flag fails here even
	// if another route is added and keeps the count above the floor.
	for _, pattern := range []string{"GET /admin/jobs", "POST /admin/jobs/{id}/retry"} {
		if !slices.Contains(a.admins, pattern) {
			t.Errorf("%s is not registered as an admin route", pattern)
		}
	}
	webtest.MinCount(t, "admin routes", len(a.admins), 8)
	for _, route := range webtest.Routes(t, a.admins, map[string]string{"id": "1"}) {
		t.Run(route.Pattern, func(t *testing.T) {
			c := c.With(t)
			fields := url.Values{webtest.TokenField: {token}, "role": {"member"}, "disabled": {"1"},
				"name": {"Intruder"}, "email": {"intruder@example.com"}, "password": {"intruder-sample-passphrase"}}
			var res *webtest.Response
			if route.Method == http.MethodGet {
				res = c.Get(route.Path)
			} else {
				res = c.PostForm(route.Path, fields)
			}
			res.WantStatus(http.StatusForbidden)
		})
	}
	after, _ := a.accounts.ListUsers(t.Context())
	if len(before) != len(after) || !slices.EqualFunc(before, after, func(x, y auth.User) bool { return x == y }) {
		t.Fatal("a refused member request changed accounts")
	}
	if _, err := a.accounts.UserByID(t.Context(), target.ID); err != nil {
		t.Fatalf("a refused member request removed the member: %v", err)
	}
	if pending := outboxtest.Enqueued(t, a.queue, outbox.Filter{}); len(pending) != 0 {
		t.Fatalf("a refused member request queued %d jobs", len(pending))
	}
}

func TestAdminCreatesUsers(t *testing.T) {
	a := newAdminSite(t)
	a.seed(t, "admin-account@example.com", auth.RoleAdmin)
	c := a.signedIn(t, "admin-account@example.com")
	form := c.Page("/admin/users").Form("/admin/users").Set("name", "  New Person ").
		Set("email", "  NEW.Person@Example.COM ").Set("role", "member").Set("password", "temporary-sample-passphrase")
	for _, tt := range []struct {
		name, field, value string
		status             int
	}{
		{"blank name", "name", " ", http.StatusUnprocessableEntity},
		{"long name", "name", strings.Repeat("n", 121), http.StatusUnprocessableEntity},
		{"bad email", "email", "not a mailbox", http.StatusUnprocessableEntity},
		{"short password", "password", "short", http.StatusUnprocessableEntity},
		{"unknown role", "role", "owner", http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := form.Values()
			data.Set(tt.field, tt.value)
			c.With(t).PostForm("/admin/users", data).WantStatus(tt.status)
			if n, _ := a.accounts.UserCount(t.Context()); n != 1 {
				t.Fatalf("refused create made an account; count %d", n)
			}
		})
	}
	form.Submit().WantRedirect(http.StatusSeeOther, "/admin/users?notice=user-created")
	created, err := a.accounts.UserByEmail(t.Context(), "new.person@example.com")
	if err != nil || created.Name != "New Person" || created.Role != auth.RoleMember || !created.MustChangePassword || created.Disabled {
		t.Fatalf("created account = %+v, %v", created, err)
	}
	form.Submit().WantRedirect(http.StatusSeeOther, "/admin/users?notice=email-in-use")
	// The listing shows the account and the notice text, never the password.
	res := c.Get("/admin/users?notice=user-created")
	if !strings.Contains(res.Body, "new.person@example.com") || !strings.Contains(res.Body, notices["user-created"]) ||
		strings.Contains(res.Body, "temporary-sample-passphrase") {
		t.Fatal("listing lost the new account or notice, or shows the password")
	}
}

func TestAdminChangesRoleAndDisables(t *testing.T) {
	a := newAdminSite(t)
	admin := a.seed(t, "admin-account@example.com", auth.RoleAdmin)
	member := a.seed(t, testEmail, auth.RoleMember)
	c := a.signedIn(t, "admin-account@example.com")
	memberClient := a.signedIn(t, testEmail)
	role := func(id int64, v string) *webtest.Response {
		return post(c, "/admin/users", "/admin/users/"+itoa(id)+"/role", url.Values{"role": {v}})
	}
	disabled := func(id int64, v string) *webtest.Response {
		return post(c, "/admin/users", "/admin/users/"+itoa(id)+"/disabled", url.Values{"disabled": {v}})
	}

	// An id that is not a positive integer is an account that does not exist.
	for _, id := range []string{"0", "-1", "x"} {
		post(c, "/admin/users", "/admin/users/"+id+"/role", url.Values{"role": {"member"}}).WantStatus(http.StatusNotFound)
	}

	// The last enabled admin can be neither demoted nor disabled.
	role(admin.ID, "member").WantRedirect(http.StatusSeeOther, "/admin/users?notice=last-admin")
	disabled(admin.ID, "1").WantRedirect(http.StatusSeeOther, "/admin/users?notice=last-admin")
	if got, _ := a.accounts.UserByID(t.Context(), admin.ID); got.Role != auth.RoleAdmin || got.Disabled {
		t.Fatal("the last admin was changed")
	}
	if res := c.Get("/admin/users?notice=last-admin"); !strings.Contains(res.Body, notices["last-admin"]) {
		t.Fatal("refusal notice not shown")
	}
	role(member.ID, "admin").WantRedirect(http.StatusSeeOther, "/admin/users?notice=role-changed")
	role(admin.ID, "member").WantRedirect(http.StatusSeeOther, "/admin/users?notice=role-changed")
	if got, _ := a.accounts.UserByID(t.Context(), admin.ID); got.Role != auth.RoleMember {
		t.Fatal("a second admin did not allow demoting the first")
	}

	// The demoted admin's session is now a member's; the promoted member acts.
	c.Get("/admin/users").WantStatus(http.StatusForbidden)
	c = memberClient

	// Strict parsing: only "0" and "1".
	for _, v := range []string{"", "2", "true", "on", " 1"} {
		disabled(member.ID, v).WantStatus(http.StatusBadRequest)
	}
	role(member.ID, "owner").WantStatus(http.StatusBadRequest)
	role(9999, "member").WantStatus(http.StatusNotFound)
	disabled(9999, "1").WantStatus(http.StatusNotFound)
	post(c, "/admin/users", "/admin/users/abc/role", url.Values{"role": {"member"}}).WantStatus(http.StatusNotFound)

	disabled(admin.ID, "1").WantRedirect(http.StatusSeeOther, "/admin/users?notice=user-disabled")
	disabled(admin.ID, "0").WantRedirect(http.StatusSeeOther, "/admin/users?notice=user-enabled")
	disabled(member.ID, "1").WantRedirect(http.StatusSeeOther, "/admin/users?notice=last-admin")
}

func TestAdminDisableEndsSessionsAndResetForcesChange(t *testing.T) {
	a := newAdminSite(t)
	a.seed(t, "admin-account@example.com", auth.RoleAdmin)
	member := a.seed(t, testEmail, auth.RoleMember)
	c := a.signedIn(t, "admin-account@example.com")
	memberClient := a.signedIn(t, testEmail)

	post(c, "/admin/users", "/admin/users/"+itoa(member.ID)+"/disabled", url.Values{"disabled": {"1"}}).
		WantRedirect(http.StatusSeeOther, "/admin/users?notice=user-disabled")
	memberClient.Get("/").WantRedirect(http.StatusSeeOther, "/login?next=%2F")
	post(c, "/admin/users", "/admin/users/"+itoa(member.ID)+"/disabled", url.Values{"disabled": {"0"}}).
		WantStatus(http.StatusSeeOther)

	memberClient = a.signedIn(t, testEmail)
	path := "/admin/users/" + itoa(member.ID) + "/password"
	post(c, "/admin/users", path, url.Values{"password": {"short"}}).WantStatus(http.StatusUnprocessableEntity)
	post(c, "/admin/users", "/admin/users/9999/password", url.Values{"password": {"temporary-sample-passphrase"}}).
		WantStatus(http.StatusNotFound)
	post(c, "/admin/users", path, url.Values{"password": {"temporary-sample-passphrase"}}).
		WantRedirect(http.StatusSeeOther, "/admin/users?notice=password-reset")
	memberClient.Get("/").WantRedirect(http.StatusSeeOther, "/login?next=%2F")
	got, err := a.accounts.UserByID(t.Context(), member.ID)
	if err != nil || !got.MustChangePassword {
		t.Fatalf("reset account = %+v, %v", got, err)
	}
	again := a.client(t)
	again.SignIn("/login", url.Values{"email": {testEmail}, "password": {"temporary-sample-passphrase"}}).WantStatus(http.StatusSeeOther)
	again.Get("/").WantRedirect(http.StatusSeeOther, "/account/password")
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func TestNoticesComeFromAFixedMap(t *testing.T) {
	a := newAdminSite(t)
	a.seed(t, testEmail, auth.RoleMember)
	c := a.signedIn(t, testEmail)
	for code, text := range notices {
		if !strings.Contains(c.Get("/?notice="+code).Body, html.EscapeString(text)) {
			t.Errorf("notice %q is not shown", code)
		}
	}
	const probe = "probe-text-not-in-the-map"
	body := c.Get("/?notice=" + url.QueryEscape(probe+"<b>")).Body
	if strings.Contains(body, probe) || strings.Contains(body, `role="status" class="notice"`) {
		t.Fatal("an unknown notice code was echoed or rendered")
	}
}

func TestAdminMailTest(t *testing.T) {
	const email = "admin-account@example.com"
	t.Run("delivers to the admin", func(t *testing.T) {
		fake := mailer.NewFake()
		a := newAdminSiteWithMail(t, fake)
		a.seed(t, email, auth.RoleAdmin)
		c := a.signedIn(t, email)
		c.Page("/admin/users").Form("/admin/mail-test").Submit().
			WantRedirect(http.StatusSeeOther, "/admin/users?notice=mail-queued")
		if len(fake.Attempted()) != 0 {
			t.Fatal("the request performed delivery")
		}
		if body := c.Get("/admin/jobs").Body; !strings.Contains(body, "Pending: 1") {
			t.Fatal("page does not show the queued job")
		}
		if n := outboxtest.Drain(t, a.queue); n != 1 {
			t.Fatalf("drained %d jobs, want 1", n)
		}
		sent := fake.Sent()
		if len(sent) != 1 || len(sent[0].To) != 1 || sent[0].To[0] != email ||
			sent[0].Subject == "" || sent[0].Text == "" {
			t.Fatalf("sent = %+v", sent)
		}
		if body := c.Get("/admin/users").Body; !strings.Contains(body, "Mail is configured.") || !strings.Contains(body, `href="/admin/jobs"`) {
			t.Fatal("Users page does not show configured mail and link to the queue")
		}
		if body := c.Get("/admin/jobs").Body; !strings.Contains(body, "Done: 1") {
			t.Fatal("queue page does not show the finished job")
		}
	})
	t.Run("retries a transient failure", func(t *testing.T) {
		fake := mailer.NewFake()
		fake.Err = http.ErrServerClosed
		a := newAdminSiteWithMail(t, fake)
		a.seed(t, email, auth.RoleAdmin)
		c := a.signedIn(t, email)
		post(c, "/admin/users", "/admin/mail-test", nil).WantRedirect(http.StatusSeeOther, "/admin/users?notice=mail-queued")
		outboxtest.Drain(t, a.queue)
		if len(fake.Attempted()) != 1 || len(fake.Sent()) != 0 {
			t.Fatal("failure was not attempted exactly once")
		}
		fake.Err = nil
		a.clock.Advance(2 * time.Minute)
		outboxtest.Drain(t, a.queue)
		if len(fake.Sent()) != 1 || len(fake.Attempted()) != 2 {
			t.Fatal("the transient failure was not retried")
		}
	})
	t.Run("is rate limited", func(t *testing.T) {
		fake := mailer.NewFake()
		a := newAdminSiteWithMail(t, fake)
		a.seed(t, email, auth.RoleAdmin)
		c := a.signedIn(t, email)
		for range 9 {
			post(c, "/admin/users", "/admin/mail-test", nil).WantStatus(http.StatusSeeOther)
		}
		post(c, "/admin/users", "/admin/mail-test", nil).WantStatus(http.StatusTooManyRequests)
		if n := outboxtest.Drain(t, a.queue); n != 9 {
			t.Fatalf("drained %d jobs, want 9", n)
		}
	})
	t.Run("discards by default", func(t *testing.T) {
		a := newAdminSite(t)
		a.seed(t, email, auth.RoleAdmin)
		c := a.signedIn(t, email)
		post(c, "/admin/users", "/admin/mail-test", nil).WantRedirect(http.StatusSeeOther, "/admin/users?notice=mail-queued")
		outboxtest.Drain(t, a.queue)
		if body := c.Get("/admin/users").Body; !strings.Contains(body, "Mail is not configured") {
			t.Fatal("page hides that mail is discarded")
		}
		if !strings.Contains(a.logs.String(), "discarding message") {
			t.Fatal("the discarded message was not logged")
		}
		webtest.NoSecrets(t, a.logs.String(), "discarding message", email)
	})
}

type recipientRefusal struct{ error }

func (recipientRefusal) ErrorCode() int { return 550 }

func deadMailJob(t *testing.T) (*adminSite, *webtest.Client, *mailer.Fake, outbox.Info, auth.User) {
	t.Helper()
	fake := mailer.NewFake()
	fake.Err = recipientRefusal{errors.New("550 5.1.1 refused <distinctive-refusal@example.com>")}
	a := newAdminSiteWithMail(t, fake)
	user := a.seed(t, "admin-account@example.com", auth.RoleAdmin)
	c := a.signedIn(t, user.Email)
	c.Page("/admin/users").Form("/admin/mail-test").Submit().
		WantRedirect(http.StatusSeeOther, "/admin/users?notice=mail-queued")
	outboxtest.Drain(t, a.queue)
	dead := outboxtest.Enqueued(t, a.queue, outbox.Filter{Kind: "mail-test", State: outbox.StateDead})
	if len(dead) != 1 || dead[0].Attempts != 1 {
		t.Fatalf("permanent refusal must be dead after one attempt: %+v", dead)
	}
	return a, c, fake, dead[0], user
}

func TestMailTestPermanentRefusalIsNotRetried(t *testing.T) {
	a, _, fake, _, _ := deadMailJob(t)
	a.clock.Advance(24 * time.Hour)
	if n := outboxtest.Drain(t, a.queue); n != 0 || len(fake.Attempted()) != 1 {
		t.Fatalf("permanent refusal retried: drained %d, attempted %d", n, len(fake.Attempted()))
	}
}

func TestMailTestRefusalKeepsAddressOutOfLogs(t *testing.T) {
	const address = "distinctive-refusal@example.com"
	relay := newRefusingRelay(t, address, "550 5.1.1 refused")
	a := newAdminSiteWithMail(t, relay)
	a.seed(t, address, auth.RoleAdmin)
	c := a.signedIn(t, address)
	c.Page("/admin/users").Form("/admin/mail-test").Submit().
		WantRedirect(http.StatusSeeOther, "/admin/users?notice=mail-queued")
	outboxtest.Drain(t, a.queue)
	dead := outboxtest.Enqueued(t, a.queue, outbox.Filter{Kind: "mail-test", State: outbox.StateDead})
	if len(dead) != 1 || dead[0].LastError != "The mail server permanently refused the message." {
		t.Fatalf("stored refusal = %+v", dead)
	}
	webtest.NoSecrets(t, a.logs.String(), `"error_class":"*outbox.permanentError"`, address)
}

// A temporary refusal takes the default description path. Whatever the relay
// quotes, such as the recipient, must reach neither the stored text, the admin
// page nor the logs.
func TestMailTestTransientRefusalKeepsAddressOutOfStoredTextPageAndLogs(t *testing.T) {
	const address = "quoted-recipient@example.com"
	relay := newRefusingRelay(t, address, "450 4.2.1 mailbox busy")
	a := newAdminSiteWithMail(t, relay)
	a.seed(t, address, auth.RoleAdmin)
	c := a.signedIn(t, address)
	c.Page("/admin/users").Form("/admin/mail-test").Submit().
		WantRedirect(http.StatusSeeOther, "/admin/users?notice=mail-queued")
	outboxtest.Drain(t, a.queue)
	found := outboxtest.Enqueued(t, a.queue, outbox.Filter{Kind: "mail-test"})
	if len(found) != 1 || found[0].State == outbox.StateDone {
		t.Fatalf("expected one job still pending after a 4xx reply: %+v", found)
	}
	const fixed = "Job failed; see the server log for its error class."
	if found[0].LastError != fixed {
		t.Fatalf("stored description = %q, want the fixed default", found[0].LastError)
	}
	if page := c.Get("/admin/jobs").Body; strings.Contains(page, address) {
		t.Fatal("the queue page shows the recipient")
	}
	webtest.NoSecrets(t, a.logs.String(), `"error_class"`, address)
}

// newRefusingRelay answers every recipient with reply followed by the address,
// the way a relay quotes what it was given.
func newRefusingRelay(t *testing.T, address, reply string) mailer.Sender {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.WriteString(conn, "220 relay.example SMTP\r\n")
		lines := bufio.NewScanner(conn)
		for lines.Scan() {
			command, _, _ := strings.Cut(strings.ToUpper(lines.Text()), " ")
			switch command {
			case "EHLO", "HELO", "NOOP", "MAIL", "RSET":
				_, _ = io.WriteString(conn, "250 OK\r\n")
			case "RCPT":
				_, _ = io.WriteString(conn, reply+" <"+address+">\r\n")
			case "QUIT":
				_, _ = io.WriteString(conn, "221 Bye\r\n")
				return
			default:
				_, _ = io.WriteString(conn, "451 unexpected command\r\n")
				return
			}
		}
	}()
	sender, err := mailer.NewSMTPSender(mailer.SMTPConfig{
		Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port,
		From: "sender@example.com", TLS: mailer.TLSNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sender
}

func TestAdminJobRetryRequiresCSRF(t *testing.T) {
	a, c, _, dead, _ := deadMailJob(t)
	path := "/admin/jobs/" + itoa(dead.ID) + "/retry"
	c.PostForm(path, url.Values{webtest.TokenField: {"invalid-token"}}).WantStatus(http.StatusForbidden)
	got, err := a.queue.Get(t.Context(), dead.ID)
	if err != nil || got.State != outbox.StateDead || got.RetryCount != 0 {
		t.Fatalf("forged retry changed job: %+v, %v", got, err)
	}
}

func TestAdminSeesAndRetriesDeadJob(t *testing.T) {
	a, c, fake, dead, user := deadMailJob(t)
	path := "/admin/jobs/" + itoa(dead.ID) + "/retry"
	res := c.Get("/admin/jobs")
	for _, text := range []string{"Dead: 1", "mail-test", "1/6 attempts", "The mail server permanently refused the message."} {
		if !strings.Contains(res.Body, text) {
			t.Errorf("queue page lacks %q", text)
		}
	}
	c.Get(path).WantStatus(http.StatusNotFound)
	c.Page("/admin/jobs").Form(path).Submit().WantRedirect(http.StatusSeeOther, "/admin/jobs?notice=job-retried")
	post(c, "/admin/jobs", path, nil).WantRedirect(http.StatusSeeOther, "/admin/jobs?notice=job-not-dead")
	got, err := a.queue.Get(t.Context(), dead.ID)
	if err != nil || got.State != outbox.StatePending || got.RetriedBy != "user:"+itoa(user.ID) || got.RetryCount != 1 {
		t.Fatalf("retried job = %+v, %v", got, err)
	}
	fake.Err = nil
	if n := outboxtest.Drain(t, a.queue); n != 1 || len(fake.Sent()) != 1 {
		t.Fatalf("retried job did not send: ran %d, sent %d", n, len(fake.Sent()))
	}
}

func TestAdminPageShowsNewestBackup(t *testing.T) {
	date := time.Date(2025, 3, 9, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name string
		opts []Option
		want string
	}{
		{"newest", []Option{WithBackups(func() (time.Time, bool, error) { return date, true, nil })}, "Newest backup: 2025-03-09"},
		{"none yet", []Option{WithBackups(func() (time.Time, bool, error) { return time.Time{}, false, nil })}, "No backups have been made yet."},
		{"failing", []Option{WithBackups(func() (time.Time, bool, error) { return time.Time{}, false, http.ErrAbortHandler })}, "Backups could not be read."},
		{"disabled", nil, "Backups are turned off."},
		{"described by the variant", []Option{WithoutBackups("Backups are made elsewhere.")}, "Backups are made elsewhere."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := newAdminSite(t, tt.opts...)
			a.seed(t, "admin-account@example.com", auth.RoleAdmin)
			c := a.signedIn(t, "admin-account@example.com")
			if body := c.Get("/admin/users").Body; !strings.Contains(body, tt.want) {
				t.Fatalf("page lacks %q", tt.want)
			}
		})
	}
}
