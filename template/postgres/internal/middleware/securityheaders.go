package middleware

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Policy adds trusted, code-defined CSP sources to the same-origin defaults.
// Each source must be an HTTPS host with an optional port, not a URL path,
// wildcard or CSP keyword. Never populate these fields from a request.
type Policy struct {
	ImgSources     []string
	FormActions    []string
	FrameAncestors []string
	// FontSources also permits host-provided font stylesheets, not scripts.
	FontSources []string
	// HSTS should be enabled only on an HTTPS deployment.
	HSTS bool
}

var httpsSource = regexp.MustCompile(`^https://[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*(?::([0-9]+))?$`)

// SecurityHeaders wraps the entire router so error and health responses get
// the same baseline as pages. It panics on an invalid code-defined CSP source.
func SecurityHeaders(p Policy) func(http.Handler) http.Handler {
	for _, sources := range [][]string{p.ImgSources, p.FormActions, p.FrameAncestors, p.FontSources} {
		for _, source := range sources {
			match := httpsSource.FindStringSubmatch(source)
			if match == nil {
				panic("CSP sources must be HTTPS hosts with an optional port")
			}
			if match[1] != "" {
				port, err := strconv.ParseUint(match[1], 10, 16)
				if err != nil || port == 0 {
					panic("CSP source ports must be between 1 and 65535")
				}
			}
		}
	}
	const self = "'self'"
	selfWith := func(extra []string) string {
		if len(extra) == 0 {
			return self
		}
		return self + " " + strings.Join(extra, " ")
	}
	frame := "'none'"
	if len(p.FrameAncestors) != 0 {
		frame = strings.Join(p.FrameAncestors, " ")
	}
	csp := strings.Join([]string{
		"default-src " + self,
		"img-src " + selfWith(p.ImgSources),
		"font-src " + selfWith(p.FontSources),
		"style-src " + selfWith(p.FontSources),
		"script-src " + self,
		"form-action " + selfWith(p.FormActions),
		"base-uri 'none'",
		"object-src 'none'",
		"frame-ancestors " + frame,
	}, "; ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			// Same-origin requests can still supply a Referer for origin
			// checks without disclosing the URL across an origin boundary.
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
			if p.HSTS {
				h.Set("Strict-Transport-Security", "max-age=63072000")
			}
			next.ServeHTTP(w, r)
		})
	}
}
