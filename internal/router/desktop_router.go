package router

import (
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
)

// StaticFrontend routes API/MCP/health requests to their existing handlers.
// os.Root prevents symlink/path escapes from the read-only distribution root.
type StaticFrontend struct {
	api  http.Handler
	root *os.Root
}

func NewStaticFrontend(api http.Handler, directory string) (*StaticFrontend, error) {
	if directory == "" {
		return &StaticFrontend{api: api}, nil
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open WEB_DIST_DIR: %w", err)
	}
	index, err := root.Open("index.html")
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("WEB_DIST_DIR requires index.html: %w", err)
	}
	index.Close()
	return &StaticFrontend{api: api, root: root}, nil
}

func (h *StaticFrontend) Close() error {
	if h.root != nil {
		return h.root.Close()
	}
	return nil
}

func (h *StaticFrontend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reserved := func(prefix string) bool {
		return r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/")
	}
	if h.root == nil || reserved("/api") || reserved("/mcp") || reserved("/health") {
		h.api.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.api.ServeHTTP(w, r)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		http.NotFound(w, r)
		return
	}
	file, err := h.root.Open(name)
	if err != nil {
		// Missing assets must stay 404. Only document navigation gets SPA HTML.
		if path.Ext(name) != "" || !strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
		file, err = h.root.Open(name)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
	w.Header().Set("Cache-Control", "no-cache")
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
}
