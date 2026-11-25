// Package config validates process settings before any service starts.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	SetupToken          string
	SessionLifetime     session.Lifetime
	ClientIPSource      clientip.Source
	PasswordConcurrency int
	LogLevel            slog.Level
	LogFormat           string
	ShutdownTimeout     time.Duration
	SMTP                mailer.SMTPConfig
}

// Load reads the environment and validates it without connecting to a service.
func Load() (Config, error) { return load(os.LookupEnv) }

func load(lookup func(string) (string, bool)) (Config, error) {
	e := env{lookup: lookup}
	cfg := Config{
		Port:                e.integer("PORT", 8080, 1, 65535),
		CookieSecure:        e.boolean("COOKIE_SECURE", false),
		SiteName:            e.text("SITE_NAME", "My App"),
		SetupToken:          e.secret("SETUP_TOKEN", "SETUP_TOKEN_FILE"),
		PasswordConcurrency: e.integer("PASSWORD_CONCURRENCY", 2, 1, 64),
		LogFormat:           e.text("LOG_FORMAT", "json"),
		ShutdownTimeout:     e.duration("SHUTDOWN_TIMEOUT", 15*time.Second),
		DB:                  loadDB(&e),
	}
	defaultOrigin := "http://localhost"
	if cfg.Port != 80 {
		defaultOrigin += ":" + strconv.Itoa(cfg.Port)
	}
	cfg.BaseURL = e.text("BASE_URL", defaultOrigin)
	cfg.SessionLifetime = session.Default
	cfg.SessionLifetime.Idle = e.duration("SESSION_IDLE", cfg.SessionLifetime.Idle)
	cfg.SessionLifetime.Absolute = e.duration("SESSION_ABSOLUTE", cfg.SessionLifetime.Absolute)

	var err error
	cfg.ClientIPSource, err = clientip.ParseSource(e.text("CLIENT_IP_SOURCE", "remote"))
	if err != nil {
		e.reject("CLIENT_IP_SOURCE must be remote, forwarded, or cloudflare")
	}
	switch e.text("LOG_LEVEL", "info") {
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "info":
		cfg.LogLevel = slog.LevelInfo
	case "warn":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		e.reject("LOG_LEVEL must be debug, info, warn, or error")
	}
	cfg.SMTP = loadSMTP(&e)
	if err := csrf.ValidateOrigin(cfg.BaseURL, !cfg.CookieSecure); err != nil {
		e.reject("BASE_URL must be a canonical origin (HTTP requires COOKIE_SECURE=false)")
	}
	cfg.SessionCookie, err = cookie.New(cookie.Config{Name: "session", Insecure: !cfg.CookieSecure})
	if err != nil {
		e.reject("invalid session cookie configuration")
	}
	if err := cfg.SessionLifetime.Validate(); err != nil {
		e.reject("SESSION_IDLE and SESSION_ABSOLUTE: " + err.Error())
	}
	if strings.TrimSpace(cfg.SiteName) == "" {
		e.reject("SITE_NAME must not be blank")
	}
	if cfg.SetupToken != "" && (!utf8.ValidString(cfg.SetupToken) || utf8.RuneCountInString(cfg.SetupToken) < 32 ||
		strings.ContainsFunc(cfg.SetupToken, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })) {
		e.reject("SETUP_TOKEN must be at least 32 characters without whitespace or control characters")
	}
	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		e.reject("LOG_FORMAT must be json or text")
	}
	if cfg.ShutdownTimeout <= 0 {
		e.reject("SHUTDOWN_TIMEOUT must be positive")
	}
	if e.err != nil {
		return Config{}, e.err
	}
	return cfg, nil
}

func loadSMTP(e *env) mailer.SMTPConfig {
	cfg := mailer.SMTPConfig{
		Host:     e.text("SMTP_HOST", ""),
		Port:     e.integer("SMTP_PORT", 587, 1, 65535),
		Username: e.text("SMTP_USERNAME", ""),
		Password: e.secret("SMTP_PASSWORD", "SMTP_PASSWORD_FILE"),
		From:     e.text("EMAIL_FROM", ""),
	}
	var err error
	cfg.TLS, err = mailer.ParseTLSPolicy(e.text("SMTP_TLS", "starttls"))
	if err != nil {
		e.reject("SMTP_TLS must be starttls, tls, or none")
	}
	if cfg.Host == "" {
		if cfg.Username != "" || cfg.Password != "" || cfg.From != "" {
			e.reject("SMTP_HOST is required when SMTP credentials or EMAIL_FROM are set")
		}
		return cfg
	}
	if _, err := netip.ParseAddr(cfg.Host); err != nil &&
		(strings.ContainsAny(cfg.Host, ":/@\\?#[]") || strings.IndexFunc(cfg.Host, unicode.IsSpace) >= 0) {
		e.reject("SMTP_HOST must be a hostname or IP address without a port")
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		e.reject("EMAIL_FROM must be a valid mailbox when SMTP is enabled")
	}
	if (cfg.Username != "") != (cfg.Password != "") || (cfg.Username != "" && strings.TrimSpace(cfg.Username) == "") {
		e.reject("SMTP_USERNAME and SMTP_PASSWORD must be supplied together")
	}
	if cfg.TLS == mailer.TLSNone && (cfg.Username != "" || cfg.Password != "") {
		e.reject("SMTP credentials require TLS")
	}
	// The constructor only validates; it doesn't dial the relay.
	if _, err := mailer.NewSMTPSender(cfg); err != nil {
		e.reject("invalid SMTP configuration")
	}
	return cfg
}

type env struct {
	lookup func(string) (string, bool)
	err    error
}

func (e *env) reject(message string) {
	if e.err == nil {
		e.err = errors.New(message)
	}
}

func (e *env) text(key, fallback string) string {
	if v, ok := e.lookup(key); ok {
		return v
	}
	return fallback
}

func (e *env) boolean(key string, fallback bool) bool {
	v, ok := e.lookup(key)
	if !ok {
		return fallback
	}
	switch v {
	case "false":
		return false
	case "true":
		return true
	default:
		e.reject(key + " must be true or false")
		return fallback
	}
}

func (e *env) integer(key string, fallback, min, max int) int {
	v, err := strconv.Atoi(e.text(key, strconv.Itoa(fallback)))
	if err != nil || v < min || (max != 0 && v > max) {
		e.reject(key + " is not an integer in range")
		return fallback
	}
	return v
}

func (e *env) duration(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(e.text(key, fallback.String()))
	if err != nil {
		e.reject(key + " must be a duration")
		return fallback
	}
	return v
}

func (e *env) secret(key, fileKey string) string {
	value, path := e.text(key, ""), e.text(fileKey, "")
	if path == "" {
		return value
	}
	if value != "" {
		e.reject(fmt.Sprintf("set only one of %s and %s", key, fileKey))
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		e.reject(fileKey + " must name a readable regular file")
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		// Neither the path nor the value belongs in startup diagnostics.
		e.reject(fileKey + " could not be opened")
		return ""
	}
	defer f.Close()
	const maxSecret = 64 << 10
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		e.reject(fileKey + " must name a regular file")
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSecret+1))
	if err != nil || len(data) > maxSecret {
		e.reject(fileKey + " could not be read or exceeds 64 KiB")
		return ""
	}
	value = string(data)
	if body, ok := strings.CutSuffix(value, "\n"); ok {
		value = strings.TrimSuffix(body, "\r")
	}
	if value == "" {
		e.reject(fileKey + " is empty")
	}
	return value
}
