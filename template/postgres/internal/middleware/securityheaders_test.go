package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const defaultCSP = "default-src 'self'; img-src 'self'; font-src 'self'; style-src 'self'; script-src 'self'; form-action 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'"

func TestDefaultSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	SecurityHeaders(Policy{})(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	for name, want := range map[string]string{
		"Content-Security-Policy":   defaultCSP,
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "same-origin",
		"Permissions-Policy":        "camera=(), geolocation=(), microphone=()",
		"Strict-Transport-Security": "",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestSecurityPolicyWidening(t *testing.T) {
	for _, tt := range []struct {
		name         string
		policy       Policy
		replacements []string
		hsts         string
	}{
		{"images", Policy{ImgSources: []string{"https://images.example"}}, []string{
			"img-src 'self'", "img-src 'self' https://images.example",
		}, ""},
		{"form submissions", Policy{FormActions: []string{"https://receiver.example"}}, []string{
			"form-action 'self'", "form-action 'self' https://receiver.example",
		}, ""},
		{"frames", Policy{FrameAncestors: []string{"https://embed.example:8443"}}, []string{
			"frame-ancestors 'none'", "frame-ancestors https://embed.example:8443",
		}, ""},
		{"fonts", Policy{FontSources: []string{"https://fonts.example"}}, []string{
			"font-src 'self'", "font-src 'self' https://fonts.example",
			"style-src 'self'", "style-src 'self' https://fonts.example",
		}, ""},
		{"HSTS", Policy{HSTS: true}, nil, "max-age=63072000"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			SecurityHeaders(tt.policy)(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			csp := rec.Header().Get("Content-Security-Policy")
			if want := strings.NewReplacer(tt.replacements...).Replace(defaultCSP); csp != want {
				t.Errorf("CSP = %q, want %q", csp, want)
			}
			if strings.Contains(csp, "unsafe-") {
				t.Errorf("unsafe CSP: %q", csp)
			}
			if got := rec.Header().Get("Strict-Transport-Security"); got != tt.hsts {
				t.Errorf("HSTS = %q, want %q", got, tt.hsts)
			}
		})
	}
}

func TestSecurityPolicyRejectsInvalidSources(t *testing.T) {
	for name, source := range map[string]string{
		"path": "https://host.example/media", "slash": "https://host.example/",
		"query": "https://host.example?q=value", "fragment": "https://host.example#value",
		"quote": "https://host.example'", "keyword": "'unsafe-inline'",
		"separator": "https://host.example;script-src", "comma": "https://host.example,other",
		"space": "https://host.example https://other.example", "newline": "https://host.example\n",
		"wildcard host": "https://*.example", "wildcard port": "https://host.example:*",
		"scheme": "http://host.example", "missing scheme": "host.example",
		"missing host": "https://:443", "userinfo": "https://person@host",
		"empty port": "https://host.example:", "named port": "https://host.example:https",
		"zero port": "https://host.example:0", "large port": "https://host.example:65536",
	} {
		t.Run(name, func(t *testing.T) {
			values := []string{source}
			for _, p := range []Policy{{ImgSources: values}, {FormActions: values}, {FrameAncestors: values}, {FontSources: values}} {
				func() {
					defer func() {
						if recover() == nil {
							t.Errorf("accepted invalid CSP source %q in %+v", source, p)
						}
					}()
					SecurityHeaders(p)
				}()
			}
		})
	}
}
