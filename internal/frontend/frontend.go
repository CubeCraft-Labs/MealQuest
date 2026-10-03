// Package frontend serves the embedded PWA build with an SPA fallback.
package frontend

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
)

// embedded holds the web build. The build copies web/dist over the committed
// placeholder index.html.
//
//go:embed all:dist
var embedded embed.FS

const (
	indexFile = "index.html"

	cacheImmutable = "public, max-age=31536000, immutable"
	cacheNoCache   = "no-cache"
)

// noCacheFiles must be revalidated on every load: they name the hashed assets
// (or, for the service worker, control their caching), so a stale copy pins an
// old release.
var noCacheFiles = map[string]bool{
	indexFile:              true,
	"sw.js":                true,
	"manifest.webmanifest": true,
}

// Assets returns the embedded web build rooted at its dist directory.
func Assets() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // "dist" is a valid path, so Sub cannot fail
	}
	return sub
}

// NewHandler returns a handler that serves files from fsys. A GET or HEAD for a
// path that is not a file in fsys is answered with index.html so client-side
// routes load the app. Paths equal to or under a reserved prefix (for example
// "/api") are never answered with index.html; they are passed to notFound.
// fsys must contain index.html.
func NewHandler(fsys fs.FS, logger *slog.Logger, notFound http.Handler, reserved ...string) (http.Handler, error) {
	if _, err := fs.Stat(fsys, indexFile); err != nil {
		return nil, fmt.Errorf("frontend assets: %w", err)
	}
	return &handler{fsys: fsys, logger: logger, notFound: notFound, reserved: reserved}, nil
}

type handler struct {
	fsys     fs.FS
	logger   *slog.Logger
	notFound http.Handler
	reserved []string
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	urlPath := path.Clean("/" + r.URL.Path)
	if h.isReserved(urlPath) {
		h.notFound.ServeHTTP(w, r)
		return
	}

	name := strings.TrimPrefix(urlPath, "/")
	if name == "" || !h.isFile(name) {
		name = indexFile
	}
	h.serveFile(w, r, name)
}

func (h *handler) isReserved(urlPath string) bool {
	for _, prefix := range h.reserved {
		if urlPath == prefix || strings.HasPrefix(urlPath, prefix+"/") {
			return true
		}
	}
	return false
}

func (h *handler) isFile(name string) bool {
	info, err := fs.Stat(h.fsys, name)
	return err == nil && info.Mode().IsRegular()
}

func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	f, err := h.fsys.Open(name)
	if err != nil {
		h.fail(w, r, name, err)
		return
	}
	defer f.Close()
	content, ok := f.(io.ReadSeeker)
	if !ok {
		h.fail(w, r, name, errors.New("file does not implement io.Seeker"))
		return
	}

	if noCacheFiles[name] {
		w.Header().Set("Cache-Control", cacheNoCache)
	} else {
		w.Header().Set("Cache-Control", cacheImmutable)
	}
	if path.Ext(name) == ".webmanifest" {
		w.Header().Set("Content-Type", "application/manifest+json")
	}
	// Embedded files carry no modification time; ServeContent omits
	// Last-Modified for the zero time.
	http.ServeContent(w, r, name, time.Time{}, content)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, name string, err error) {
	h.logger.ErrorContext(r.Context(), "serve frontend asset", "file", name, "error", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}
