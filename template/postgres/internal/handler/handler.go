// Package handler serves the application's embedded pages and assets.
package handler

import (
	"bytes"
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/mail"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/middleware"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
	"github.com/17xande-dev/mailer"
	"github.com/17xande-dev/ratelimit"
	"github.com/17xande-dev/ratelimit/clientip"
	"github.com/17xande-dev/websec/cookie"
	"github.com/17xande-dev/websec/csrf"
	"github.com/17xande-dev/websec/session"
)

// New builds the HTTP stack and reports the registered protected route patterns.
func New(cfg config.Config, log *slog.Logger, accounts *store.Store, ping func(context.Context) error, opts ...Option) (http.Handler, []string, error) {
	s, err := newSite(cfg, log, accounts, opts...)
	if err != nil {
		return nil, nil, err
	}
	h, protected := s.routes(ping)
	return h, protected, nil
}

// Option supplies an optional collaborator. Without one the site discards mail
// and reports no backups, which is what a deployment with neither configured has.
type Option func(*site)

// WithMail sets the transport for administrator test messages; delivering says
// whether it reaches anyone, so the admin page can tell the truth about it.
func WithMail(sender mailer.Sender, delivering bool) Option {
	return func(s *site) { s.mail, s.mailDelivers = sender, delivering }
}

// WithBackups sets the lookup behind the admin page's newest-backup line.
// Omit it when backups are disabled.
func WithBackups(newest func() (time.Time, bool, error)) Option {
	return func(s *site) { s.newestBackup = newest }
}

// WithoutBackups sets the line the admin page shows when the application makes
// no backups itself. The database variant words it, because "turned off" is
// true for one and misleading for the other. Without it the page says so plainly.
func WithoutBackups(note string) Option {
	return func(s *site) { s.noBackupsNote = note }
}

func newSite(cfg config.Config, log *slog.Logger, accounts *store.Store, opts ...Option) (*site, error) {
	mux := http.NewServeMux()
	requestForgery, err := csrf.New(csrf.Config{
		Origin:      cfg.BaseURL,
		Insecure:    !cfg.CookieSecure,
		Logger:      log,
		LogPath:     func(r *http.Request) string { return middleware.RoutePattern(mux, r) },
		OnBodyError: csrfBodyError,
	})
	if err != nil {
		return nil, err
	}
	ipLimit, err := ratelimit.New(ratelimit.Config{Rate: 1.0 / 10, Burst: 10})
	if err != nil {
		return nil, err
	}
	accountLimit, err := ratelimit.New(ratelimit.Config{Rate: 1.0 / 60, Burst: 5})
	if err != nil {
		return nil, err
	}
	assets, err := loadAssets()
	if err != nil {
		return nil, err
	}
	pages, err := parsePages(templateFiles, assets.url)
	if err != nil {
		return nil, err
	}
	s := &site{
		siteName: cfg.SiteName, log: log, pages: pages, assets: assets, mux: mux,
		accounts: accounts, csrf: requestForgery, cookies: cfg.SessionCookie,
		life: cfg.SessionLifetime, secure: cfg.CookieSecure, clientIP: cfg.ClientIPSource,
		secretLimit: ipLimit.Middleware(cfg.ClientIPSource, ratelimit.HTTPOptions{
			RetryAfter: 10 * time.Second, Exceeded: http.HandlerFunc(rateRefusal),
		}),
		accountLimit: accountLimit,
		mail:         mail.Discard(log),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

type site struct {
	siteName     string
	log          *slog.Logger
	pages        map[string]*template.Template
	assets       assetSet
	mux          *http.ServeMux
	accounts     *store.Store
	csrf         *csrf.Protect
	cookies      *cookie.Cookie
	life         session.Lifetime
	secure       bool
	clientIP     clientip.Source
	secretLimit  func(http.Handler) http.Handler
	accountLimit *ratelimit.Limiter
	mail         mailer.Sender
	mailDelivers bool
	newestBackup func() (time.Time, bool, error)
	// noBackupsNote overrides the default line when there is no backup lookup.
	noBackupsNote string
}

type pageData struct {
	SiteName string
	Title    string
	CSRF     string
	Next     string
	Message  string
	UserName string
	Notice   string
	IsAdmin  bool

	// Admin and account pages.
	Users         []auth.User
	SelfID        int64
	Roles         []auth.Role
	BackupLine    string
	MailDelivers  bool
	MustChange    bool
	EnabledAdmins int64
}

// notices maps the fixed codes a redirect may carry in ?notice= to text. The
// query value only selects an entry; it is never echoed, so a crafted link
// cannot put its own words on a page.
var notices = map[string]string{
	"password-changed": "Your password was changed.",
	"user-created":     "The account was created. They must change the temporary password at first sign-in.",
	"role-changed":     "The role was changed.",
	"user-disabled":    "The account was disabled and its sessions ended.",
	"user-enabled":     "The account was enabled.",
	"password-reset":   "A temporary password was set and the account's sessions ended.",
	"last-admin":       "That change was refused: at least one enabled admin must remain.",
	"email-in-use":     "That email address already has an account.",
	"mail-sent":        "A test message was sent to your address.",
	"mail-failed":      "The test message could not be sent. See the server log.",
}

func (s *site) routes(ping func(context.Context) error) (http.Handler, []string) {
	h, protected, _ := s.routesWithAdmin(ping)
	return h, protected
}

// routesWithAdmin also reports which protected patterns are admin-only, for
// the member-role sweep.
func (s *site) routesWithAdmin(ping func(context.Context) error) (http.Handler, []string, []string) {
	mux := s.mux
	var protected, adminRoutes []string
	requireUser := middleware.RequireUser(s.accounts, s.cookies, s.life, s.log)
	// Every signed-in route goes through this one closure, so the anonymous,
	// member and must-change sweeps see it. own marks the pages that stay open
	// while a password change is pending; admin marks the admin-only routes.
	protect := func(pattern string, own, admin bool, handler http.Handler) {
		protected = append(protected, pattern)
		if admin {
			adminRoutes = append(adminRoutes, pattern)
		}
		mux.Handle(pattern, requireUser(s.gate(own, admin, handler)))
	}
	protect("GET /{$}", false, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := middleware.User(r)
		s.render(w, r, http.StatusOK, "app/home", "Home", pageData{UserName: user.Name})
	}))
	protect("GET /account/password", true, false, http.HandlerFunc(s.passwordPage))
	protect("POST /account/password", true, false, s.secretLimit(http.HandlerFunc(s.passwordChange)))
	protect("GET /admin/users", false, true, http.HandlerFunc(s.usersPage))
	protect("POST /admin/users", false, true, http.HandlerFunc(s.userCreate))
	protect("POST /admin/users/{id}/role", false, true, http.HandlerFunc(s.userRole))
	protect("POST /admin/users/{id}/disabled", false, true, http.HandlerFunc(s.userDisabled))
	protect("POST /admin/users/{id}/password", false, true, http.HandlerFunc(s.userPassword))
	protect("POST /admin/mail-test", false, true, s.secretLimit(http.HandlerFunc(s.mailTest)))
	mux.Handle("GET /healthz", Healthz(s.log, ping))
	mux.Handle("GET /static/{name}", s.assets.serve(s.notFound))
	mux.HandleFunc("GET /login", s.loginPage)
	mux.Handle("POST /login", s.secretLimit(http.HandlerFunc(s.login)))
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /setup", s.setupPage)
	mux.Handle("POST /setup", s.secretLimit(http.HandlerFunc(s.setup)))
	mux.HandleFunc("GET /", s.notFound)

	csrfProtected := s.csrf.Handler(mux)
	firstAccountOnly := s.onlyWithoutUsers(csrfProtected)
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch middleware.RoutePattern(mux, r) {
		case "GET /healthz":
			mux.ServeHTTP(w, r)
		case "GET /setup", "POST /setup":
			// Once setup is closed, even a POST without a token gets a 404.
			// An open setup route always goes through CSRF before its handler.
			firstAccountOnly.ServeHTTP(w, r)
		default:
			csrfProtected.ServeHTTP(w, r)
		}
	})
	secured := middleware.SecurityHeaders(middleware.Policy{HSTS: s.secure})(dispatch)
	return middleware.RequestID(middleware.AccessLog(s.log, mux, secured)), protected, adminRoutes
}

// gate refuses a member on an admin route, and sends an account that must
// change its password to the change page until it has.
func (s *site) gate(own, admin bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := middleware.User(r)
		if user.MustChangePassword && !own {
			http.Redirect(w, r, "/account/password", http.StatusSeeOther)
			return
		}
		if admin && user.Role != auth.RoleAdmin {
			s.render(w, r, http.StatusForbidden, "app/forbidden", "Not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *site) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "plain/not-found", "Page not found")
}

func (s *site) render(w http.ResponseWriter, r *http.Request, status int, page, title string, fields ...pageData) {
	var data pageData
	if len(fields) != 0 {
		data = fields[0]
	}
	data.SiteName, data.Title, data.CSRF = s.siteName, title, csrf.Token(r)
	data.Notice = notices[r.URL.Query().Get("notice")]
	if user, ok := middleware.User(r); ok {
		data.IsAdmin = user.Role == auth.RoleAdmin
	}
	var body bytes.Buffer
	if err := executePage(s.pages, &body, page, data); err != nil {
		s.log.ErrorContext(r.Context(), "page rendering failed", "page", page, "error", err)
		body.Reset()
		status = http.StatusInternalServerError
		data.Title = "Something went wrong"
		if err := executePage(s.pages, &body, "plain/server-error", data); err != nil {
			s.log.ErrorContext(r.Context(), "error page rendering failed", "error", err)
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, http.StatusText(status), status)
			return
		}
	}
	// Both the intended page and the fallback must finish before headers commit.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body.Bytes())
	}
}
