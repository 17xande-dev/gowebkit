// Package config validates process settings before any service starts.
package config

import (
	"log/slog"
	"net/mail"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/17xande-dev/env"
	"github.com/17xande-dev/mailer"
	"github.com/17xande-dev/ratelimit/clientip"
	"github.com/17xande-dev/websec/cookie"
	"github.com/17xande-dev/websec/csrf"
	"github.com/17xande-dev/websec/session"
)

// Config holds validated settings for the server and its operator commands.
type Config struct {
	DB
	Port                int
	BaseURL             string
	CookieSecure        bool
	SessionCookie       *cookie.Cookie
	SiteName            string
	SetupToken          env.Secret
	SessionLifetime     session.Lifetime
	ClientIPSource      clientip.Source
	PasswordConcurrency int
	LogLevel            slog.Level
	LogFormat           string
	ShutdownTimeout     time.Duration
	SMTP                SMTP
}

// SMTP holds the relay settings without exposing the password in diagnostics.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password env.Secret
	From     string
	TLS      mailer.TLSPolicy
}

// Load reads the environment and validates it without connecting to a service.
func Load() (Config, error) { return load(os.LookupEnv) }

func load(lookup env.Lookup) (Config, error) {
	r := env.New(lookup)
	cfg := Config{
		Port:                r.Int("PORT", 8080, 1, 65535),
		CookieSecure:        r.Bool("COOKIE_SECURE", false),
		SiteName:            r.String("SITE_NAME", "My App"),
		SetupToken:          r.Secret("SETUP_TOKEN"),
		PasswordConcurrency: r.Int("PASSWORD_CONCURRENCY", 2, 1, 64),
		LogFormat:           r.OneOf("LOG_FORMAT", "json", "json", "text"),
		ShutdownTimeout:     r.Duration("SHUTDOWN_TIMEOUT", 15*time.Second, time.Nanosecond, 0),
		DB:                  loadDB(r),
	}
	defaultOrigin := "http://localhost"
	if cfg.Port != 80 {
		defaultOrigin += ":" + strconv.Itoa(cfg.Port)
	}
	cfg.BaseURL = r.String("BASE_URL", defaultOrigin)
	cfg.SessionLifetime = session.Default
	cfg.SessionLifetime.Idle = r.Duration("SESSION_IDLE", cfg.SessionLifetime.Idle, time.Nanosecond, 0)
	cfg.SessionLifetime.Absolute = r.Duration("SESSION_ABSOLUTE", cfg.SessionLifetime.Absolute, time.Nanosecond, 0)

	var err error
	cfg.ClientIPSource, err = clientip.ParseSource(r.String("CLIENT_IP_SOURCE", "remote"))
	if err != nil {
		r.Fail("CLIENT_IP_SOURCE", "must be remote, forwarded, or cloudflare")
	}
	switch r.OneOf("LOG_LEVEL", "info", "debug", "info", "warn", "error") {
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "info":
		cfg.LogLevel = slog.LevelInfo
	case "warn":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	}
	cfg.SMTP = loadSMTP(r)
	if err := csrf.ValidateOrigin(cfg.BaseURL, true); err != nil {
		r.Fail("BASE_URL", "must be a canonical origin")
	} else if cfg.CookieSecure != strings.HasPrefix(cfg.BaseURL, "https://") {
		r.Fail("BASE_URL", "HTTPS requires secure cookies and HTTP requires insecure cookies; check COOKIE_SECURE")
	}
	cfg.SessionCookie, err = cookie.New(cookie.Config{Name: "session", Insecure: !cfg.CookieSecure})
	if err != nil {
		r.Fail("COOKIE_SECURE", "invalid session cookie configuration")
	}
	if err := cfg.SessionLifetime.Validate(); err != nil {
		r.Fail("SESSION_IDLE", "must not exceed SESSION_ABSOLUTE")
	}
	if strings.TrimSpace(cfg.SiteName) == "" {
		r.Fail("SITE_NAME", "must not be blank")
	}
	if token := cfg.SetupToken.Reveal(); cfg.SetupToken.IsSet() &&
		(!utf8.ValidString(token) || utf8.RuneCountInString(token) < 32 ||
			strings.ContainsFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })) {
		r.Fail("SETUP_TOKEN", "must be at least 32 characters without whitespace or control characters")
	}
	if err := r.Err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loadSMTP(r *env.Reader) SMTP {
	cfg := SMTP{
		Host:     r.String("SMTP_HOST", ""),
		Port:     r.Int("SMTP_PORT", 587, 1, 65535),
		Username: r.String("SMTP_USERNAME", ""),
		Password: r.Secret("SMTP_PASSWORD"),
		From:     r.String("EMAIL_FROM", ""),
	}
	var err error
	cfg.TLS, err = mailer.ParseTLSPolicy(r.String("SMTP_TLS", "starttls"))
	if err != nil {
		r.Fail("SMTP_TLS", "must be starttls, tls, or none")
	}
	if cfg.Host == "" {
		if cfg.Username != "" || cfg.Password.IsSet() || cfg.From != "" {
			r.Fail("SMTP_HOST", "is required when SMTP credentials or EMAIL_FROM are set")
		}
		return cfg
	}
	if _, err := netip.ParseAddr(cfg.Host); err != nil &&
		(strings.ContainsAny(cfg.Host, ":/@\\?#[]") || strings.IndexFunc(cfg.Host, unicode.IsSpace) >= 0) {
		r.Fail("SMTP_HOST", "must be a hostname or IP address without a port")
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		r.Fail("EMAIL_FROM", "must be a valid mailbox when SMTP is enabled")
	}
	if (cfg.Username != "") != cfg.Password.IsSet() || (cfg.Username != "" && strings.TrimSpace(cfg.Username) == "") {
		r.Fail("SMTP_USERNAME", "must be non-blank and supplied together with SMTP_PASSWORD when using credentials")
	}
	if cfg.TLS == mailer.TLSNone && (cfg.Username != "" || cfg.Password.IsSet()) {
		r.Fail("SMTP_TLS", "SMTP credentials require TLS")
	}
	// The relay constructor's host, sender, port and TLS checks are covered
	// above; only the transport needs the unredacted password.
	return cfg
}
