// Package webui serves the console that `npm run build` exports as static
// files. Release builds copy the export into dist/ before compiling the panel.
package webui

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

var (
	inlineScript = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)
	scriptSource = regexp.MustCompile(`(?i)\ssrc\s*=`)
)

var contentTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".txt":   "text/plain; charset=utf-8",
	".woff2": "font/woff2",
}

type Handler struct {
	files fs.FS
	csp   string
}

// New returns the embedded console. ok is false when the binary was built
// without it, in which case the panel serves only the API.
func New() (handler *Handler, ok bool, err error) {
	files, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, false, err
	}
	return NewFS(files)
}

// NewFS serves the console in files. ok is false when files has no index.html.
func NewFS(files fs.FS) (*Handler, bool, error) {
	if _, err := fs.Stat(files, "index.html"); errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	hashes, err := inlineScriptHashes(files)
	if err != nil {
		return nil, false, err
	}
	csp := "default-src 'self'; script-src 'self'"
	for _, hash := range hashes {
		csp += " 'sha256-" + hash + "'"
	}
	// React sets style attributes, which a hash cannot allow.
	csp += "; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
		"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	return &Handler{files: files, csp: csp}, true, nil
}

// inlineScriptHashes allows exactly the inline scripts the export contains.
func inlineScriptHashes(files fs.FS) ([]string, error) {
	var hashes []string
	err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || path.Ext(name) != ".html" {
			return err
		}
		page, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		for _, match := range inlineScript.FindAllSubmatch(page, -1) {
			if len(match[2]) == 0 || scriptSource.Match(match[1]) {
				continue
			}
			sum := sha256.Sum256(match[2])
			hash := base64.StdEncoding.EncodeToString(sum[:])
			if !slices.Contains(hashes, hash) {
				hashes = append(hashes, hash)
			}
		}
		return nil
	})
	return hashes, err
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	status := http.StatusOK
	if info, err := fs.Stat(h.files, name); err != nil || info.IsDir() || strings.HasPrefix(name, ".") {
		// The console routes with the URL fragment, so any other path is missing.
		name, status = "404.html", http.StatusNotFound
		if _, err := fs.Stat(h.files, name); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	if contentType, ok := contentTypes[path.Ext(name)]; ok {
		w.Header().Set("Content-Type", contentType)
	}
	if strings.HasPrefix(name, "_next/static/") {
		// Every file under _next/static has a content hash or build ID in its path.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if path.Ext(name) == ".html" {
		w.Header().Set("Content-Security-Policy", h.csp)
	}
	if status != http.StatusOK {
		page, err := fs.ReadFile(h.files, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write(page)
		return
	}
	http.ServeFileFS(w, r, h.files, name)
}
