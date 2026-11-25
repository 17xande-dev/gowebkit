package handler

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"time"
)

//go:embed static
var staticFiles embed.FS

var staticTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
	".svg": "image/svg+xml",
}

type asset struct {
	data        []byte
	digest      string
	contentType string
}

type assetSet map[string]asset

func loadAssets() (assetSet, error) {
	entries, err := fs.ReadDir(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	assets := make(assetSet)
	for _, entry := range entries {
		name := entry.Name()
		contentType, ok := staticTypes[path.Ext(name)]
		if entry.IsDir() || !ok {
			continue
		}
		data, err := staticFiles.ReadFile(path.Join("static", name))
		if err != nil {
			return nil, err
		}
		assets[name] = asset{data: data, digest: fmt.Sprintf("%x", sha256.Sum256(data)), contentType: contentType}
	}
	return assets, nil
}

func (a assetSet) url(name string) (string, error) {
	item, ok := a[name]
	if !ok {
		return "", fmt.Errorf("unknown asset %q", name)
	}
	return "/static/" + url.PathEscape(name) + "?v=" + item.digest, nil
}

func (a assetSet) serve(notFound http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		item, ok := a[name]
		if !ok {
			notFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", item.contentType)
		h.Set("ETag", `"`+item.digest+`"`)
		// Old links can still work, but only a URL that names these exact bytes
		// is safe to cache without revalidation after the next deploy.
		versions := r.URL.Query()["v"]
		if len(versions) == 1 && versions[0] == item.digest {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(item.data))
	})
}
