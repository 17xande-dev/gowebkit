package handler

import (
	"io/fs"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPagesDefineContent(t *testing.T) {
	assets, err := loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	pages, err := parsePages(templateFiles, assets.url)
	if err != nil {
		t.Fatal(err)
	}
	covered := make(map[string]bool)
	for _, route := range pageRoutes {
		if pages[route.page] == nil || covered[route.page] {
			t.Errorf("rendered route fixture doesn't name a distinct page: %s", route.page)
		}
		covered[route.page] = true
	}
	if len(pages) < 8 || len(pages) != len(covered) {
		t.Fatalf("must cover every page; found %d pages and %d route fixtures", len(pages), len(covered))
	}
	fixture := fstest.MapFS{
		"templates/pages/app/incomplete.html": {Data: []byte(`<p>Not a content definition</p>`)},
		"templates/layouts/app.html":          {Data: []byte(`{{define "layout"}}{{template "content" .}}{{end}}`)},
		"templates/partials/decoy.html":       {Data: []byte(`{{define "content"}}Not in the page{{end}}`)},
	}
	if _, err := parsePages(fixture, assets.url); err == nil || !strings.Contains(err.Error(), "does not define content") {
		t.Fatalf("expected failure for missing content, got %v", err)
	}
	fixture["templates/pages/app/incomplete.html"].Data = []byte(`{{define "content"}}Original page content{{end}}`)
	// A filename match does not make a partial the owner of page content.
	fixture["templates/partials/incomplete.html"] = fixture["templates/partials/decoy.html"]
	delete(fixture, "templates/partials/decoy.html")
	if _, err := parsePages(fixture, assets.url); err == nil || !strings.Contains(err.Error(), "redefines content") {
		t.Fatalf("expected failure for overridden content, got %v", err)
	}
}

var (
	openTag    = regexp.MustCompile(`(?is)<([a-z][\w:-]*)(\s+(?:[^>"']|"[^"]*"|'[^']*')*|/?)>`)
	attribute  = regexp.MustCompile(`(?s)([^\s=/"'<>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'<>]+)))?`)
	scriptBody = regexp.MustCompile(`(?is)<script(?:\s+(?:[^>"']|"[^"]*"|'[^']*')*)?>(.*?)</script\s*>`)
	hxEvent    = regexp.MustCompile(`^(data-)?hx-on($|[:-])`)
)

func eachTag(markup string, visit func(string, map[string]string)) {
	for _, tag := range openTag.FindAllStringSubmatch(markup, -1) {
		attrs := make(map[string]string)
		for _, attr := range attribute.FindAllStringSubmatch(tag[2], -1) {
			// Quoted values and unquoted values occupy distinct capture groups.
			key := strings.ToLower(attr[1])
			if _, alreadySeen := attrs[key]; !alreadySeen {
				attrs[key] = attr[2] + attr[3] + attr[4]
			}
		}
		visit(strings.ToLower(tag[1]), attrs)
	}
}

func checkMarkup(t *testing.T, check func(t *testing.T, markup string)) {
	t.Helper()
	err := fs.WalkDir(templateFiles, "templates", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		content, err := templateFiles.ReadFile(path)
		if err != nil {
			return err
		}
		t.Run(path, func(t *testing.T) { check(t, string(content)) })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checkRendered(t, check)
}

func checkRendered(t *testing.T, check func(t *testing.T, markup string)) {
	t.Helper()
	pagesChecked := 0
	for _, route := range pageRoutes {
		t.Run("rendered/"+route.page, func(t *testing.T) {
			res := renderedPageClient(t, route.path, route.role, route.brokenHome).Get(route.path)
			if res.Status != route.status || !strings.Contains(res.Body, route.heading) {
				t.Fatalf("wrong page at %s (status %d)", route.path, res.Status)
			}
			pagesChecked++
			check(t, res.Body)
		})
	}
	if pagesChecked < 8 {
		t.Errorf("checked only %d rendered pages", pagesChecked)
	}
}

func TestNoInlineStyles(t *testing.T) {
	checkMarkup(t, func(t *testing.T, markup string) {
		eachTag(markup, func(tag string, attrs map[string]string) {
			if _, ok := attrs["style"]; ok || tag == "style" {
				t.Errorf("inline CSS on <%s>", tag)
			}
		})
	})
}

func TestNoInlineEventHandlers(t *testing.T) {
	checkMarkup(t, func(t *testing.T, markup string) {
		eachTag(markup, func(tag string, attrs map[string]string) {
			for name := range attrs {
				if (len(name) > 2 && strings.HasPrefix(name, "on")) || hxEvent.MatchString(name) {
					t.Errorf("inline event handler %s on <%s>", name, tag)
				}
			}
		})
	})
}

func TestNoInlineScripts(t *testing.T) {
	checkMarkup(t, func(t *testing.T, markup string) {
		eachTag(markup, func(tag string, attrs map[string]string) {
			if tag == "script" && attrs["src"] == "" {
				t.Error("script element needs a src attribute")
			}
		})
		for _, script := range scriptBody.FindAllStringSubmatch(markup, -1) {
			if strings.TrimSpace(script[1]) != "" {
				t.Error("script element has inline content")
			}
		}
	})
}

func TestRenderedAssetURLs(t *testing.T) {
	assets, err := loadAssets()
	if err != nil {
		t.Fatal(err)
	}
	checkRendered(t, func(t *testing.T, markup string) {
		found := make(map[string]bool)
		eachTag(markup, func(tag string, attrs map[string]string) {
			for _, key := range []string{"src", "href"} {
				value := attrs[key]
				required := tag == "script" && key == "src" || tag == "link" && key == "href" && attrs["rel"] == "stylesheet"
				if !required && !strings.HasPrefix(value, "/static/") {
					continue
				}
				u, err := url.Parse(value)
				if err != nil || u.Host != "" || u.Scheme != "" || !strings.HasPrefix(u.Path, "/static/") {
					t.Errorf("unexpected resource URL %q", value)
					continue
				}
				name := strings.TrimPrefix(u.Path, "/static/")
				want, err := assets.url(name)
				if err != nil || value != want {
					t.Errorf("resource %q must use its content-hashed URL: %q (%v)", name, value, err)
				}
				found[name] = true
			}
		})
		for _, name := range []string{"styles.css", "htmx.min.js", "logo.svg"} {
			if !found[name] {
				t.Errorf("did not check URL for %s", name)
			}
		}
	})
}

func TestMarkupAttributeQuoting(t *testing.T) {
	for _, markup := range []string{
		`<script data-src="/decoy" src="/static/real.js?v=hash"></script>`,
		`<script data-src='/decoy' SRC='/static/real.js?v=hash'></script>`,
		`<script data-src=/decoy src=/static/real.js?v=hash></script>`,
	} {
		tags := 0
		eachTag(markup, func(tag string, attrs map[string]string) {
			tags++
			if tag != "script" || attrs["src"] != "/static/real.js?v=hash" || attrs["data-src"] != "/decoy" {
				t.Errorf("incorrect tag or attributes for %q: %s %v", markup, tag, attrs)
			}
		})
		if tags != 1 {
			t.Errorf("found %d tags in %q", tags, markup)
		}
	}
}
