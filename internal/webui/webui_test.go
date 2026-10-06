package webui

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestConsoleAllowsOnlyItsOwnInlineScripts(t *testing.T) {
	t.Parallel()
	inline := `self.boot=1`
	handler, ok, err := NewFS(fstest.MapFS{
		"index.html":                     {Data: []byte(`<html><script src="/_next/static/chunks/app.js"></script><script>` + inline + `</script></html>`)},
		"404.html":                       {Data: []byte(`<html>missing</html>`)},
		"_next/static/chunks/app.js":     {Data: []byte(`console.log(1)`)},
		"_next/static/fonts/geist.woff2": {Data: []byte(`font`)},
	})
	if err != nil || !ok {
		t.Fatalf("NewFS() ok=%v err=%v", ok, err)
	}
	sum := sha256.Sum256([]byte(inline))
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := page.Header().Get("Content-Security-Policy")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), inline) {
		t.Fatalf("index returned %d: %s", page.Code, page.Body.String())
	}
	scriptSrc := ""
	for _, directive := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.TrimSpace(directive), "script-src ") {
			scriptSrc = directive
		}
	}
	if !strings.Contains(scriptSrc, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") ||
		strings.Contains(scriptSrc, "unsafe") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("unexpected policy %q", csp)
	}
	if page.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("index cache policy = %q", page.Header().Get("Cache-Control"))
	}

	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/_next/static/fonts/geist.woff2", nil))
	if asset.Code != http.StatusOK || asset.Header().Get("Content-Type") != "font/woff2" ||
		!strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset returned %d with %v", asset.Code, asset.Header())
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/servers", nil))
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "missing") {
		t.Fatalf("unknown path returned %d: %s", missing.Code, missing.Body.String())
	}
}

func TestConsoleIsOptional(t *testing.T) {
	t.Parallel()
	if _, ok, err := NewFS(fstest.MapFS{".gitkeep": {}}); ok || err != nil {
		t.Fatalf("a tree without index.html must report no console: ok=%v err=%v", ok, err)
	}
}
