package panelupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewerComparesNumericParts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		a, b string
		want bool
	}{
		{"v0.3.0", "v0.2.9", true},
		{"v0.10.0", "v0.9.3", true},
		{"v0.2.1", "v0.2.1", false},
		{"v0.2.0", "v0.2.1", false},
		{"v0.3.0", "dev", false},
		{"dev", "v0.2.0", false},
		{"v0.3.0-rc.1", "v0.2.0", false},
	} {
		if got := Newer(test.a, test.b); got != test.want {
			t.Errorf("Newer(%q, %q) = %v", test.a, test.b, got)
		}
	}
}

func TestLatestReadsTheStableReleaseOnce(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/repos/"+Repository+"/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.3.0","draft":false,"prerelease":false}`))
	}))
	t.Cleanup(github.Close)
	releases := &Releases{API: github.URL, HTTP: github.Client()}
	for range 2 {
		latest, err := releases.Latest(context.Background())
		if err != nil || latest != "v0.3.0" {
			t.Fatalf("Latest() = %q, %v", latest, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("GitHub was asked %d times, want 1", calls.Load())
	}
}

func TestLatestRejectsATagThatIsNotARelease(t *testing.T) {
	t.Parallel()
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.3.0; rm -rf /"}`))
	}))
	t.Cleanup(github.Close)
	releases := &Releases{API: github.URL, HTTP: github.Client()}
	if latest, err := releases.Latest(context.Background()); err == nil {
		t.Fatalf("Latest() accepted %q", latest)
	}
}

func testUpdater(t *testing.T) Updater {
	t.Helper()
	state, status := t.TempDir(), t.TempDir()
	return Updater{Request: filepath.Join(state, "update-request"), Status: filepath.Join(status, "status.json")}
}

func TestStartWritesARequestTheUpdaterCanRead(t *testing.T) {
	t.Parallel()
	updater := testUpdater(t)
	if err := updater.Start("v0.3.0"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(updater.Request)
	if err != nil || string(data) != "v0.3.0\n" {
		t.Fatalf("request = %q, %v", data, err)
	}
	state, unanswered, err := updater.Read(time.Now())
	if err != nil || state.Pending != "v0.3.0" || unanswered || !state.Busy() {
		t.Fatalf("Read() = %+v, unanswered %v, %v", state, unanswered, err)
	}
	if err := updater.Start("v0.3.0\nv9.9.9"); err == nil {
		t.Fatal("Start accepted a request that is not a single release")
	}
}

func TestReadReleasesAnUpdaterThatStoppedAnswering(t *testing.T) {
	t.Parallel()
	updater := testUpdater(t)
	if err := updater.Start("v0.3.0"); err != nil {
		t.Fatal(err)
	}
	state, unanswered, err := updater.Read(time.Now().Add(pendingDeadline))
	if err != nil || state.Pending != "" || !unanswered || state.Busy() {
		t.Fatalf("an unanswered request still blocks: %+v, unanswered %v, %v", state, unanswered, err)
	}

	started := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	if err := os.WriteFile(updater.Status, []byte(`{"state":"running","from":"v0.2.1","target":"v0.3.0","started_at":"2026-10-09T08:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(updater.Request); err != nil {
		t.Fatal(err)
	}
	state, _, err = updater.Read(started.Add(time.Minute))
	if err != nil || state.Last == nil || state.Last.State != "running" || !state.Busy() {
		t.Fatalf("running update = %+v, %v", state.Last, err)
	}
	state, _, err = updater.Read(started.Add(staleUpdate))
	if err != nil || state.Last.State != "failed" || state.Busy() {
		t.Fatalf("an update that never finished = %+v, %v", state.Last, err)
	}
}

func TestAvailableNeedsTheStatusDirectory(t *testing.T) {
	t.Parallel()
	updater := testUpdater(t)
	if !updater.Available() {
		t.Fatal("updater with a status directory is unavailable")
	}
	updater.Status = filepath.Join(t.TempDir(), "missing", "status.json")
	if updater.Available() {
		t.Fatal("updater without a status directory is available")
	}
}
