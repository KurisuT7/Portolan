package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KurisuT7/Portolan/internal/agentproto"
	"github.com/KurisuT7/Portolan/internal/buildinfo"
	"github.com/KurisuT7/Portolan/internal/model"
)

func TestOlderReleaseComparesNumericParts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		a, b string
		want bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.9.3", "v0.10.0", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.2.1", "v0.2.0", false},
		{"dev", "v0.2.0", false},
		{"v0.2.0", "dev", false},
	} {
		if got := olderRelease(test.a, test.b); got != test.want {
			t.Errorf("olderRelease(%q, %q) = %v", test.a, test.b, got)
		}
	}
}

// Not parallel: the test sets the stamped Agent version.
func TestAgentUpdatesReachOnlyAgentsThatInstallThem(t *testing.T) {
	previous := buildinfo.AgentVersion
	t.Cleanup(func() { buildinfo.AgentVersion = previous })
	buildinfo.AgentVersion = "v0.2.1"
	server := newTestServer(t)
	ctx := context.Background()
	downloads := t.TempDir()
	for _, arch := range []string{"amd64", "arm64"} {
		if err := os.WriteFile(filepath.Join(downloads, "portolan-agent-linux-"+arch), []byte("agent-"+arch), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	server.downloadsDir = downloads
	enroll := func(name, agentVersion string) (model.Server, string) {
		created, token, err := server.store.CreateServer(ctx, model.Server{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		_, agentToken, err := server.store.Enroll(ctx, token, "198.51.100.73")
		if err != nil {
			t.Fatal(err)
		}
		if agentVersion != "" {
			status := model.RuntimeStatus{AgentVersion: agentVersion, Units: []model.UnitStatus{}}
			if err := server.store.SaveRuntimeStatus(ctx, created.ID, status); err != nil {
				t.Fatal(err)
			}
		}
		return created, agentToken
	}
	legacy, _ := enroll("Legacy", "")
	old, _ := enroll("Before self-update", "v0.1.0")
	capable, capableToken := enroll("Capable", "v0.2.0")
	current, _ := enroll("Current", "v0.2.1")
	development, _ := enroll("Development", "dev")
	cookie, csrf := adminSession(t, server)
	post := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, path, nil, cookie, csrf))
		return recorder
	}
	for _, blocked := range []model.Server{legacy, old, current, development} {
		if recorder := post("/api/v1/servers/" + blocked.ID + "/agent-update"); recorder.Code != http.StatusConflict {
			t.Fatalf("%s update: %d %s", blocked.Name, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := post("/api/v1/servers/" + capable.ID + "/agent-update"); recorder.Code != http.StatusAccepted {
		t.Fatalf("single update: %d %s", recorder.Code, recorder.Body.String())
	}
	rollout := post("/api/v1/agent-updates")
	if rollout.Code != http.StatusAccepted || strings.TrimSpace(rollout.Body.String()) != `{"queued":1}` {
		t.Fatalf("rollout: %d %s", rollout.Code, rollout.Body.String())
	}
	// Enrollment queued the initial sync first.
	job, err := server.store.NextJob(ctx, capable.ID)
	for err == nil && job != nil && job.Type == "sync" {
		job, err = server.store.NextJob(ctx, capable.ID)
	}
	if err != nil || job == nil || job.Type != agentproto.AgentUpdateJob {
		t.Fatalf("job = %#v err=%v", job, err)
	}
	var payload agentproto.AgentUpdatePayload
	digest := sha256.Sum256([]byte("agent-arm64"))
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.Version != "v0.2.1" || payload.SHA256["arm64"] != hex.EncodeToString(digest[:]) {
		t.Fatalf("payload = %#v err=%v", payload, err)
	}

	download := func(path, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-Portolan-Server-ID", capable.ID)
		}
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	if binary := download("/api/v1/agent/binary/arm64", capableToken); binary.Code != http.StatusOK || binary.Body.String() != "agent-arm64" {
		t.Fatalf("Agent binary: %d %q", binary.Code, binary.Body.String())
	}
	if recorder := download("/api/v1/agent/binary/mips", capableToken); recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown architecture: %d", recorder.Code)
	}
	if recorder := download("/api/v1/agent/binary/arm64", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous download: %d", recorder.Code)
	}

	buildinfo.AgentVersion = "dev"
	if recorder := post("/api/v1/agent-updates"); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("development panel rollout: %d %s", recorder.Code, recorder.Body.String())
	}
}
