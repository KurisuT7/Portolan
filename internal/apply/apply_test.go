package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/KurisuT7/portolan/internal/agentproto"
	"github.com/KurisuT7/portolan/internal/model"
)

func TestApplyWritesValidatedVersionedRelease(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "runtime")
	var commandLine string
	payload := agentproto.SyncPayload{Revision: 42, Nodes: []model.Node{{
		ID: "node_1", ServerID: "srv", Name: "secure ss", Protocol: model.ProtocolShadowsocks, ListenPort: 20001, Enabled: true,
		SS: &model.SSSpec{Method: "aes-256-gcm", Password: "a-long-random-password"},
	}}, Forwards: []model.Forward{{
		ID: "fwd_1", IngressServerID: "srv", Name: "edge", ListenPort: 30001, Networks: []string{"tcp", "udp"},
		TargetHost: "198.51.100.8", TargetPort: 20001, Engine: model.ForwardSingBox, Enabled: true,
	}}}
	result, err := Apply(context.Background(), payload, Options{
		RuntimeRoot: root, SingBoxBinary: "sing-box", SkipServiceActions: true,
		RunCommand: func(_ context.Context, name string, arguments ...string) (string, error) {
			commandLine = name + " " + strings.Join(arguments, " ")
			return "", nil
		},
	})
	if err != nil {
		if runtime.GOOS == "windows" && (errors.Is(err, os.ErrPermission) || strings.Contains(err.Error(), "privilege")) {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if result.NodeCount != 1 || result.ForwardCount != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !strings.Contains(commandLine, "check -c") || !strings.Contains(commandLine, "-C") {
		t.Fatalf("sing-box check was not run: %s", commandLine)
	}
	current, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"sing-box/00-base.json", "sing-box/conf.d/10-node-node_1.json", "sing-box/conf.d/20-forward-fwd_1.json", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(current, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("missing generated file %s: %v", relative, err)
		}
	}
}

func TestApplyRejectsDuplicatePortsBeforeWriting(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	payload := agentproto.SyncPayload{Revision: 7,
		Nodes:    []model.Node{{ID: "node", Name: "node", Protocol: model.ProtocolShadowsocks, ListenPort: 443, Enabled: true, SS: &model.SSSpec{Method: "aes-256-gcm", Password: "secret"}}},
		Forwards: []model.Forward{{ID: "forward", Name: "forward", ListenPort: 443, Networks: []string{"tcp"}, TargetHost: "example.com", TargetPort: 443, Engine: model.ForwardSingBox, Enabled: true}},
	}
	_, err := Apply(context.Background(), payload, Options{RuntimeRoot: root, SingBoxBinary: "sing-box", SkipServiceActions: true,
		RunCommand: func(context.Context, string, ...string) (string, error) { return "", nil }})
	if err == nil || !strings.Contains(err.Error(), "shared") {
		t.Fatalf("expected duplicate port error, got %v", err)
	}
}
