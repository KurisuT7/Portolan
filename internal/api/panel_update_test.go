package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/KurisuT7/Portolan/internal/buildinfo"
	"github.com/KurisuT7/Portolan/internal/panelupdate"
)

// Not parallel: the test sets the stamped panel version.
func TestPanelUpdateRequestsOnlyTheLatestNewerRelease(t *testing.T) {
	previous := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = previous })
	buildinfo.Version = "v0.2.1"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.3.0"}`))
	}))
	t.Cleanup(github.Close)
	server := newTestServer(t)
	server.releases = &panelupdate.Releases{API: github.URL, HTTP: github.Client()}
	cookie, csrf := adminSession(t, server)
	send := func(method, body string) (int, map[string]any) {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(method, "/api/v1/panel/update", []byte(body), cookie, csrf))
		var response map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &response)
		return recorder.Code, response
	}

	code, status := send(http.MethodGet, "")
	if code != http.StatusOK || status["current"] != "v0.2.1" || status["latest"] != "v0.3.0" || status["updater"] != false {
		t.Fatalf("status without an updater = %d %v", code, status)
	}
	if code, _ := send(http.MethodPost, `{"version":"v0.3.0"}`); code != http.StatusConflict {
		t.Fatalf("update without an updater returned %d", code)
	}

	statusDir := t.TempDir()
	server.updater = &panelupdate.Updater{Request: filepath.Join(t.TempDir(), "update-request"), Status: filepath.Join(statusDir, "status.json")}
	if code, _ := send(http.MethodPost, `{"version":"v0.2.9"}`); code != http.StatusConflict {
		t.Fatalf("update to a release other than the latest returned %d", code)
	}
	if code, response := send(http.MethodPost, `{"version":"v0.3.0"}`); code != http.StatusAccepted || response["pending"] != "v0.3.0" {
		t.Fatalf("update returned %d %v", code, response)
	}
	if data, err := os.ReadFile(server.updater.Request); err != nil || string(data) != "v0.3.0\n" {
		t.Fatalf("update request = %q, %v", data, err)
	}
	if code, _ := send(http.MethodPost, `{"version":"v0.3.0"}`); code != http.StatusConflict {
		t.Fatalf("a second update while one is queued returned %d", code)
	}
	if code, status := send(http.MethodGet, ""); code != http.StatusOK || status["pending"] != "v0.3.0" || status["updater"] != true {
		t.Fatalf("status with a queued update = %d %v", code, status)
	}

	if err := os.Remove(server.updater.Request); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(statusDir, "status.json"), []byte(`{"state":"failed","from":"v0.2.1","target":"v0.3.0","started_at":"2026-10-09T08:00:00Z","finished_at":"2026-10-09T08:01:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, status = send(http.MethodGet, "")
	last, _ := status["last"].(map[string]any)
	if code != http.StatusOK || last["state"] != "failed" || last["target"] != "v0.3.0" {
		t.Fatalf("status after a failed update = %d %v", code, status)
	}
	if code, _ := send(http.MethodPost, `{"version":"v0.3.0"}`); code != http.StatusAccepted {
		t.Fatalf("retry after a failed update returned %d", code)
	}

	buildinfo.Version = "v0.3.0"
	if err := os.Remove(server.updater.Request); err != nil {
		t.Fatal(err)
	}
	if code, _ := send(http.MethodPost, `{"version":"v0.3.0"}`); code != http.StatusConflict {
		t.Fatalf("update to the running release returned %d", code)
	}
}
