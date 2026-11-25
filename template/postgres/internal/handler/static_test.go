package handler

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestStaticAssets(t *testing.T) {
	h := testRouter(t, false, nil)
	for name, contentType := range map[string]string{
		"styles.css":  "text/css; charset=utf-8",
		"htmx.min.js": "text/javascript; charset=utf-8",
		"logo.svg":    "image/svg+xml",
	} {
		t.Run(name, func(t *testing.T) {
			assetPath := "/static/" + name
			rec := request(h, http.MethodGet, assetPath)
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != contentType || rec.Body.Len() == 0 {
				t.Fatalf("unexpected asset response: %d %v", rec.Code, rec.Header())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("unversioned Cache-Control = %q", got)
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(rec.Body.Bytes()))
			etag := rec.Header().Get("ETag")
			if etag != `"`+digest+`"` {
				t.Fatalf("invalid asset ETag: %q", etag)
			}
			rec = request(h, http.MethodGet, assetPath+"?v=old")
			if got := rec.Header().Get("Cache-Control"); rec.Code != http.StatusOK || got != "no-cache" {
				t.Fatalf("wrong cache policy for old version: %d %q", rec.Code, got)
			}
			rec = request(h, http.MethodGet, assetPath+"?v="+digest)
			if got := rec.Header().Get("Cache-Control"); rec.Code != http.StatusOK || got != "public, max-age=31536000, immutable" {
				t.Fatalf("wrong cache policy for current version: %d %q", rec.Code, got)
			}
			req := httptest.NewRequest(http.MethodGet, assetPath, nil)
			req.Header.Set("If-None-Match", etag)
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
				t.Errorf("conditional GET: code %d, body length %d", rec.Code, rec.Body.Len())
			}
		})
	}
	for _, asset := range []string{"README.md", "htmx.LICENSE", "not-found.css", "%2fstyles.css"} {
		if rec := request(h, http.MethodGet, "/static/"+asset); rec.Code != http.StatusNotFound {
			t.Errorf("unservable asset %q: status = %d", asset, rec.Code)
		}
	}
}

func TestHTMXSHA256(t *testing.T) {
	script, err := staticFiles.ReadFile("static/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	readme, err := staticFiles.ReadFile("static/README.md")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("- SHA-256: `%x`", sha256.Sum256(script))
	if !slices.Contains(strings.Split(string(readme), "\n"), want) {
		t.Fatalf("static/README.md must contain %q", want)
	}
}
