package config

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/mailer"
	"github.com/17xande-dev/websec/session"
	"github.com/17xande-dev/websec/webtest"
)

func from(values map[string]string) (Config, error) {
	return load(func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	})
}

func TestDefaults(t *testing.T) {
	cfg, err := from(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || cfg.BaseURL != "http://localhost:8080" || cfg.CookieSecure ||
		cfg.SessionLifetime != session.Default || cfg.PasswordConcurrency != 2 ||
		cfg.ShutdownTimeout != 15*time.Second || cfg.LogFormat != "json" ||
		cfg.SMTP.Host != "" || cfg.SessionCookie == nil {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestStrictBooleans(t *testing.T) {
	for _, value := range []string{"true", "false", "1", "0", "", "TRUE", "False", "yes", " true", "false "} {
		t.Run("value="+value, func(t *testing.T) {
			cfg, err := from(map[string]string{"COOKIE_SECURE": value, "BASE_URL": "https://app.example"})
			switch value {
			case "true", "false":
				if err != nil || cfg.CookieSecure != (value == "true") {
					t.Fatalf("CookieSecure = %v, err = %v", cfg.CookieSecure, err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), "COOKIE_SECURE") {
					t.Fatalf("expected boolean refusal, got %v", err)
				}
			}
		})
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values map[string]string
		key    string
	}{
		{"port", map[string]string{"PORT": "65536"}, "PORT"},
		{"origin path", map[string]string{"BASE_URL": "https://app.example/"}, "BASE_URL"},
		{"origin default port", map[string]string{"BASE_URL": "https://app.example:443"}, "BASE_URL"},
		{"origin scheme", map[string]string{"COOKIE_SECURE": "true"}, "BASE_URL"},
		{"site name", map[string]string{"SITE_NAME": " "}, "SITE_NAME"},
		{"session duration", map[string]string{"SESSION_IDLE": "soon"}, "SESSION_IDLE"},
		{"session idle", map[string]string{"SESSION_IDLE": "0"}, "SESSION_IDLE"},
		{"session absolute", map[string]string{"SESSION_ABSOLUTE": "1h"}, "SESSION_ABSOLUTE"},
		{"ip source", map[string]string{"CLIENT_IP_SOURCE": "guess"}, "CLIENT_IP_SOURCE"},
		{"hash concurrency", map[string]string{"PASSWORD_CONCURRENCY": "0"}, "PASSWORD_CONCURRENCY"},
		{"hash concurrency above cap", map[string]string{"PASSWORD_CONCURRENCY": "65"}, "PASSWORD_CONCURRENCY"},
		{"short token", map[string]string{"SETUP_TOKEN": "short"}, "SETUP_TOKEN"},
		{"short multibyte token", map[string]string{"SETUP_TOKEN": strings.Repeat("é", 16)}, "SETUP_TOKEN"},
		{"log level", map[string]string{"LOG_LEVEL": "trace"}, "LOG_LEVEL"},
		{"log format", map[string]string{"LOG_FORMAT": "xml"}, "LOG_FORMAT"},
		{"shutdown", map[string]string{"SHUTDOWN_TIMEOUT": "-1s"}, "SHUTDOWN_TIMEOUT"},
		{"SMTP port", map[string]string{"SMTP_PORT": "0"}, "SMTP_PORT"},
		{"SMTP TLS", map[string]string{"SMTP_TLS": "optional"}, "SMTP_TLS"},
		{"SMTP host", map[string]string{"SMTP_HOST": "https://app.example"}, "SMTP_HOST"},
		{"SMTP missing host", map[string]string{"EMAIL_FROM": "sender@example.com"}, "SMTP_HOST"},
		{"SMTP sender", map[string]string{"SMTP_HOST": "relay.example", "EMAIL_FROM": "invalid"}, "EMAIL_FROM"},
		{"SMTP partial credentials", map[string]string{
			"SMTP_HOST": "relay.example", "EMAIL_FROM": "sender@example.com", "SMTP_USERNAME": "sender",
		}, "SMTP_PASSWORD"},
		{"SMTP blank username", map[string]string{
			"SMTP_HOST": "relay.example", "EMAIL_FROM": "sender@example.com", "SMTP_USERNAME": " ",
		}, "SMTP_USERNAME"},
		{"SMTP cleartext credentials", map[string]string{
			"SMTP_HOST": "relay.example", "EMAIL_FROM": "sender@example.com", "SMTP_TLS": "none",
			"SMTP_USERNAME": "sender", "SMTP_PASSWORD": "secret-sample",
		}, "require TLS"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := from(tt.values)
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected %s error, got %v", tt.key, err)
			}
		})
	}
}

func TestOverrides(t *testing.T) {
	cfg, err := from(map[string]string{
		"PORT":      "9000",
		"SITE_NAME": "Sample", "SESSION_IDLE": "1h", "SESSION_ABSOLUTE": "10h",
		"SMTP_HOST": "relay.example", "SMTP_PORT": "1025", "SMTP_TLS": "none", "EMAIL_FROM": "sender@example.com",
		"CLIENT_IP_SOURCE": "forwarded", "LOG_LEVEL": "debug", "LOG_FORMAT": "text", "SHUTDOWN_TIMEOUT": "2s",
		"PASSWORD_CONCURRENCY": "3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://localhost:9000" || cfg.SiteName != "Sample" || cfg.SessionLifetime.Idle != time.Hour ||
		cfg.SessionLifetime.Absolute != 10*time.Hour || cfg.SMTP.Port != 1025 || cfg.SMTP.TLS != mailer.TLSNone ||
		cfg.LogFormat != "text" || cfg.ShutdownTimeout != 2*time.Second || cfg.PasswordConcurrency != 3 {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestDefaultOriginOmitsHTTPDefaultPort(t *testing.T) {
	cfg, err := from(map[string]string{"PORT": "80"})
	if err != nil || cfg.BaseURL != "http://localhost" {
		t.Fatalf("base URL %q, err %v", cfg.BaseURL, err)
	}
}

func TestSecretFiles(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	passwordFile := filepath.Join(dir, "password")
	token := strings.Repeat("x", 32)
	password := "  secret sample  "
	for path, value := range map[string]string{tokenFile: token + "\n", passwordFile: password + "\r\n"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	values := map[string]string{
		"SETUP_TOKEN_FILE":   tokenFile,
		"SMTP_PASSWORD_FILE": passwordFile, "SMTP_USERNAME": "sender",
		"SMTP_HOST": "relay.example", "EMAIL_FROM": "sender@example.com",
	}
	cfg, err := from(values)
	if err != nil || cfg.SetupToken != token || cfg.SMTP.Password != password {
		t.Fatalf("secret file resolution failed: %v", err)
	}
	values["SMTP_PASSWORD"] = password
	_, err = from(values)
	if err == nil || !strings.Contains(err.Error(), "set only one") || strings.Contains(err.Error(), password) {
		t.Fatalf("expected redacted conflict, got %v", err)
	}
	for _, path := range []string{filepath.Join(dir, "not-found"), dir} {
		_, err := from(map[string]string{"SETUP_TOKEN_FILE": path})
		if err == nil || !strings.Contains(err.Error(), "SETUP_TOKEN_FILE") || strings.Contains(err.Error(), path) {
			t.Fatalf("expected redacted file error, got %v", err)
		}
	}
	for _, value := range []string{"\n", strings.Repeat("x", (64<<10)+1)} {
		if err := os.WriteFile(tokenFile, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := from(map[string]string{"SETUP_TOKEN_FILE": tokenFile})
		if err == nil || !strings.Contains(err.Error(), "SETUP_TOKEN_FILE") {
			t.Fatalf("expected refusal for file of length %d, got %v", len(value), err)
		}
	}
}

// The handler tests build their own session cookie, so this is the only test
// that sees the one production code gets. The expectations are literal.
func TestSessionCookieFromConfig(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values map[string]string
		want   webtest.CookieWant
	}{
		{"secure", map[string]string{"COOKIE_SECURE": "true", "BASE_URL": "https://app.example"}, webtest.CookieWant{
			Name: "__Host-session", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		}},
		{"local", map[string]string{"COOKIE_SECURE": "false", "BASE_URL": "http://app.example"}, webtest.CookieWant{
			Name: "session", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		}},
		{"default", nil, webtest.CookieWant{
			Name: "session", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := from(tt.values)
			if err != nil {
				t.Fatal(err)
			}
			h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				cfg.SessionCookie.Set(w, "sample-value", time.Now().Add(time.Hour))
			})
			res := webtest.New(t, h, cfg.BaseURL).Get("/")
			ck := webtest.WantCookie(t, res, tt.want)
			if ck.Value != "sample-value" || ck.MaxAge <= 0 {
				t.Fatalf("cookie value %q, max-age %d", ck.Value, ck.MaxAge)
			}
		})
	}
}
