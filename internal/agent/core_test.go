package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/KurisuT7/portolan/internal/agentproto"
	"github.com/KurisuT7/portolan/internal/cores/corestest"
	"github.com/KurisuT7/portolan/internal/model"
)

func TestCoreUpdateJobInstallsVerifiedArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake core is a shell script")
	}
	script := func(label string) []byte { return []byte("#!/bin/sh\n# " + label + "\nexit 0\n") }
	archive := corestest.Archive(model.CoreSingBox, "9.9.9", script("new"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/cores/sing-box/9.9.9/"+runtime.GOARCH || r.Header.Get("Authorization") == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	bin := t.TempDir()
	singBox := filepath.Join(bin, "sing-box")
	if err := os.WriteFile(singBox, script("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{
		PanelURL: server.URL, ServerID: "srv_test", AgentToken: strings.Repeat("a", 32), RuntimeRoot: t.TempDir(),
		SingBoxBinary: singBox, RealmBinary: filepath.Join(bin, "realm"), AllowInsecureHTTP: true, SkipServiceActions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// An active release makes the update run the new sing-box against it.
	sync, _ := json.Marshal(agentproto.SyncPayload{Revision: 3, Nodes: []model.Node{{
		ID: "node", Name: "node", Protocol: model.ProtocolShadowsocks, ListenPort: 20443, Enabled: true,
		SS: &model.SSSpec{Method: "aes-256-gcm", Password: "example-password"}}}})
	if _, err := client.executeJob(context.Background(), agentproto.Job{Type: "sync", Payload: sync}); err != nil {
		t.Fatal(err)
	}

	update := func(digest string) (map[string]string, error) {
		payload, _ := json.Marshal(agentproto.CoreUpdatePayload{Version: "9.9.9", SHA256: map[string]string{runtime.GOARCH: digest}})
		result, err := client.executeJob(context.Background(), agentproto.Job{Type: "update-sing-box", Payload: payload})
		var decoded map[string]string
		if jsonErr := json.Unmarshal([]byte(result), &decoded); jsonErr != nil {
			t.Fatalf("result %q: %v", result, jsonErr)
		}
		return decoded, err
	}
	failed, err := update(strings.Repeat("0", 64))
	if err == nil || failed["message"] != "核心文件下载或校验失败，未替换。" {
		t.Fatalf("mismatched archive: %#v err=%v", failed, err)
	}
	if data, _ := os.ReadFile(singBox); !strings.Contains(string(data), "old") {
		t.Fatal("binary replaced by an unverified archive")
	}
	succeeded, err := update(corestest.SHA256(archive))
	if err != nil || succeeded["version"] != "9.9.9" || succeeded["message"] != "" {
		t.Fatalf("update: %#v err=%v", succeeded, err)
	}
	if data, _ := os.ReadFile(singBox); !strings.Contains(string(data), "new") {
		t.Fatalf("binary not replaced: %q", data)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(client.config.RuntimeRoot, ".core-*")); len(leftovers) != 0 {
		t.Fatalf("downloads left behind: %v", leftovers)
	}
}
