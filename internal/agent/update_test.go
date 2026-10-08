package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/KurisuT7/Portolan/internal/agentproto"
	"github.com/KurisuT7/Portolan/internal/buildinfo"
)

type completion struct {
	job     string
	success bool
	message string
}

type updateFixture struct {
	client      *Client
	binary      string
	completions func() []completion
}

// newUpdateFixture runs a panel that serves newBinary and an Agent whose own
// binary holds "old-agent". checkErr is what the new binary's check returns.
func newUpdateFixture(t *testing.T, newBinary string, checkErr error) updateFixture {
	t.Helper()
	var mu sync.Mutex
	var completed []completion
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/agent/binary/"+runtime.GOARCH:
			_, _ = w.Write([]byte(newBinary))
		case strings.HasPrefix(r.URL.Path, "/api/v1/agent/jobs/") && strings.HasSuffix(r.URL.Path, "/complete"):
			var body struct {
				Success bool   `json:"success"`
				Result  string `json:"result"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			var result struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal([]byte(body.Result), &result)
			mu.Lock()
			completed = append(completed, completion{strings.Split(r.URL.Path, "/")[5], body.Success, result.Message})
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(panel.Close)
	root := t.TempDir()
	binary := filepath.Join(root, "lib", "portolan-agent")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("old-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{
		PanelURL: panel.URL, ServerID: "srv_test", AgentToken: strings.Repeat("a", 32), RuntimeRoot: filepath.Join(root, "runtime"),
		AllowInsecureHTTP: true, SkipServiceActions: true, Path: filepath.Join(root, "etc", "agent.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o700); err != nil {
		t.Fatal(err)
	}
	client.executable = binary
	client.run = func(_ context.Context, name string, arguments ...string) (string, error) {
		data, err := os.ReadFile(name)
		if err != nil {
			return "", err
		}
		switch {
		case len(arguments) == 1 && arguments[0] == "version":
			return strings.TrimPrefix(string(data), "agent "), nil
		case len(arguments) > 0 && arguments[0] == "check":
			return "", checkErr
		}
		return "", errors.New("unexpected command")
	}
	return updateFixture{client: client, binary: binary, completions: func() []completion {
		mu.Lock()
		defer mu.Unlock()
		return append([]completion(nil), completed...)
	}}
}

func agentUpdateJob(t *testing.T, version, binary string) agentproto.Job {
	t.Helper()
	digest := sha256.Sum256([]byte(binary))
	payload, err := json.Marshal(agentproto.AgentUpdatePayload{Version: version, SHA256: map[string]string{runtime.GOARCH: hex.EncodeToString(digest[:])}})
	if err != nil {
		t.Fatal(err)
	}
	return agentproto.Job{ID: "job_update", Type: agentproto.AgentUpdateJob, Payload: payload}
}

func fileContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func messageOf(t *testing.T, result string) string {
	t.Helper()
	var decoded struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Message
}

// Not parallel: the test stands in for the new process by changing the
// stamped version.
func TestAgentUpdateReplacesBinaryAndNewProcessCompletesJob(t *testing.T) {
	fixture := newUpdateFixture(t, "agent v0.2.1", nil)
	job := agentUpdateJob(t, "v0.2.1", "agent v0.2.1")
	if _, err := fixture.client.executeJob(context.Background(), job); !errors.Is(err, ErrReplaced) {
		t.Fatalf("update returned %v", err)
	}
	if got := fileContent(t, fixture.binary); got != "agent v0.2.1" {
		t.Fatalf("binary = %q", got)
	}
	if got := fileContent(t, fixture.binary+".previous"); got != "old-agent" {
		t.Fatalf("previous binary = %q", got)
	}
	fixture.client.finishUpdate(context.Background())
	if len(fixture.completions()) != 0 {
		t.Fatal("the replaced process completed the job itself")
	}

	// The new process starts without the replacing flag.
	fixture.client.replacing.Store(false)
	previous := buildinfo.Version
	buildinfo.Version = "v0.2.1"
	defer func() { buildinfo.Version = previous }()
	if !fixture.client.finishUpdate(context.Background()) {
		t.Fatal("update was not settled")
	}
	if got := fixture.completions(); len(got) != 1 || got[0] != (completion{"job_update", true, ""}) {
		t.Fatalf("completions = %#v", got)
	}
	for _, leftover := range []string{fixture.binary + ".previous", fixture.client.updateMarkerPath()} {
		if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s was kept: %v", leftover, err)
		}
	}
}

func TestAgentUpdateKeepsBinaryWhenTheNewOneIsUnusable(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		// signed is the binary whose digest the job carries.
		name, served, signed, version string
		checkErr                      error
		message                       string
	}{
		{"digest mismatch", "tampered", "agent v0.2.1", "v0.2.1", nil, updateDownload},
		{"wrong version", "agent v0.2.0", "agent v0.2.0", "v0.2.1", nil, updateCannotRun},
		{"panel unreachable", "agent v0.2.1", "agent v0.2.1", "v0.2.1", errors.New("connection refused"), updateCannotReach},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newUpdateFixture(t, test.served, test.checkErr)
			job := agentUpdateJob(t, test.version, test.signed)
			result, err := fixture.client.executeJob(context.Background(), job)
			if err == nil || errors.Is(err, ErrReplaced) || messageOf(t, result) != test.message {
				t.Fatalf("result = %s err=%v", result, err)
			}
			if got := fileContent(t, fixture.binary); got != "old-agent" {
				t.Fatalf("binary = %q", got)
			}
			if _, err := os.Stat(fixture.client.updateMarkerPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("update record was written: %v", err)
			}
		})
	}
}

func TestRollbackRestoresPreviousBinaryAndOldProcessReportsFailure(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script in place of systemctl")
	}
	fixture := newUpdateFixture(t, "agent v0.2.1", nil)
	if _, err := fixture.client.executeJob(context.Background(), agentUpdateJob(t, "v0.2.1", "agent v0.2.1")); !errors.Is(err, ErrReplaced) {
		t.Fatalf("update returned %v", err)
	}
	root := filepath.Dir(fixture.client.config.Path)
	restarted := filepath.Join(root, "restarted")
	systemctl := filepath.Join(root, "systemctl")
	if err := os.WriteFile(systemctl, []byte("#!/bin/sh\necho \"$@\" >"+restarted+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := fixture.client.config
	config.SystemctlBinary = systemctl
	if err := SaveConfig(config.Path, config); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(context.Background(), config.Path); err != nil {
		t.Fatal(err)
	}
	if got := fileContent(t, fixture.binary); got != "old-agent" {
		t.Fatalf("binary after rollback = %q", got)
	}
	if got := strings.TrimSpace(fileContent(t, restarted)); got != "restart "+agentService {
		t.Fatalf("systemctl arguments = %q", got)
	}
	// A second run finds nothing pending.
	if err := Rollback(context.Background(), config.Path); err != nil {
		t.Fatal(err)
	}
	// The restarted old process starts without the replacing flag.
	fixture.client.replacing.Store(false)
	if !fixture.client.finishUpdate(context.Background()) {
		t.Fatal("rollback was not settled")
	}
	if got := fixture.completions(); len(got) != 1 || got[0] != (completion{"job_update", false, updateReverted}) {
		t.Fatalf("completions = %#v", got)
	}
}
