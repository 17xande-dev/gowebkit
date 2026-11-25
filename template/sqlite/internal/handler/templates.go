package handler

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path"
	"strings"
)

//go:embed templates
var templateFiles embed.FS

func parsePages(files fs.FS, assetURL func(string) (string, error)) (map[string]*template.Template, error) {
	const dir = "templates/pages"
	sets := make(map[string]*template.Template)
	err := fs.WalkDir(files, dir, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Ext(file) != ".html" {
			return nil
		}
		rel := strings.TrimPrefix(file, dir+"/")
		layout, _, ok := strings.Cut(rel, "/")
		if !ok || (layout != "app" && layout != "plain") {
			return fmt.Errorf("page %s must be under pages/app or pages/plain", file)
		}

		// Parse each page in isolation: its own content must not be supplied by
		// a layout, a partial, or another page that happens to parse first.
		set, err := template.New(path.Base(file)).Funcs(template.FuncMap{"asset": assetURL}).ParseFS(files, file)
		if err != nil {
			return fmt.Errorf("parse page %s: %w", file, err)
		}
		content := set.Lookup("content")
		if content == nil {
			return fmt.Errorf("page %s does not define content", file)
		}
		contentTree := content.Tree
		if _, err := set.ParseFS(files, "templates/layouts/"+layout+".html", "templates/partials/*.html"); err != nil {
			return fmt.Errorf("parse layout and partials for %s: %w", file, err)
		}
		// Comparing trees also catches a partial with the same base filename.
		if set.Lookup("content").Tree != contentTree {
			return fmt.Errorf("layout or partial redefines content for page %s", file)
		}
		if set.Lookup("layout") == nil {
			return fmt.Errorf("layout %s does not define layout", layout)
		}
		sets[strings.TrimSuffix(rel, ".html")] = set
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(sets) == 0 {
		return nil, fmt.Errorf("no pages under %s", dir)
	}
	return sets, nil
}

func executePage(sets map[string]*template.Template, out io.Writer, page string, data pageData) error {
	if set := sets[page]; set != nil {
		return set.ExecuteTemplate(out, "layout", data)
	}
	return fmt.Errorf("page %q is not registered", page)
}
