package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KurisuT7/Portolan/internal/agentproto"
	"github.com/KurisuT7/Portolan/internal/cores/corestest"
	"github.com/KurisuT7/Portolan/internal/model"
)

type coreFixture struct {
	options Options
	archive string
	calls   *[]string
}

// newCoreFixture installs old binaries and activates a release with one
// sing-box node on port 20443 and one Realm forward on port 20001.
func newCoreFixture(t *testing.T, core model.Core, run func(name string, args []string) (string, error)) coreFixture {
	t.Helper()
	bin := t.TempDir()
	var calls []string
	o := Options{RuntimeRoot: t.TempDir(), SingBoxBinary: filepath.Join(bin, "sing-box"), RealmBinary: filepath.Join(bin, "realm"),
		RunCommand: func(_ context.Context, name string, args ...string) (string, error) {
			calls = append(calls, filepath.Base(name)+" "+strings.Join(args, " "))
			return run(name, args)
		}}
	if err := defaultsAndValidate(&o); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{o.SingBoxBinary: "old sing-box", o.RealmBinary: "old realm"} {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	release := filepath.Join(o.RuntimeRoot, "releases", "9")
	payload := agentproto.SyncPayload{Revision: 9, Forwards: []model.Forward{realmForward("edge", 20001)}, Nodes: []model.Node{{
		ID: "node", Name: "node", Protocol: model.ProtocolShadowsocks, ListenPort: 20443, Enabled: true,
		SS: &model.SSSpec{Method: "aes-256-gcm", Password: "example-password"}}}}
	if err := writeRelease(release, payload, o); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrent(o.RuntimeRoot, release); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	o.ProcRoot = procFixture(t, []string{"1001", "1002", "1003"}, map[string][]string{
		"tcp6": {
			"   0: 00000000000000000000000000000000:4FDB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000   998        0 1001 1 0 100 0 0 10 0",
			"   1: 00000000000000000000000000000000:4E21 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000   998        0 1002 1 0 100 0 0 10 0",
		},
		"udp6": {" 2: 00000000000000000000000000000000:4E21 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000   998        0 1003 2 0 0"},
	})
	archive := filepath.Join(t.TempDir(), "core.tar.gz")
	if err := os.WriteFile(archive, corestest.Archive(core, "9.9.9", []byte("new "+string(core))), 0o600); err != nil {
		t.Fatal(err)
	}
	return coreFixture{options: o, archive: archive, calls: &calls}
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUpdateCoreChecksThenRestartsOnlyThatCore(t *testing.T) {
	fixture := newCoreFixture(t, model.CoreSingBox, func(_ string, args []string) (string, error) {
		if args[0] == "show" {
			return runningUnit, nil
		}
		return "", nil
	})
	o := fixture.options
	if err := UpdateCore(context.Background(), model.CoreSingBox, fixture.archive, o); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(*fixture.calls, "\n")
	if !strings.Contains(calls, ".sing-box.new check -c ") || !strings.Contains(calls, "restart portolan-sing-box.service") || strings.Contains(calls, "portolan-realm@") {
		t.Fatalf("calls:\n%s", calls)
	}
	if got := fileText(t, o.SingBoxBinary); got != "new sing-box" {
		t.Fatalf("binary = %q", got)
	}
	for _, leftover := range []string{o.SingBoxBinary + ".previous", filepath.Join(filepath.Dir(o.SingBoxBinary), ".sing-box.new")} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Fatalf("%s left behind: %v", leftover, err)
		}
	}
}

func TestUpdateCoreKeepsBinaryWhenNewSingBoxRejectsConfiguration(t *testing.T) {
	fixture := newCoreFixture(t, model.CoreSingBox, func(_ string, args []string) (string, error) {
		if args[0] == "check" {
			return "decode config: unknown field", errors.New("exit status 1")
		}
		return runningUnit, nil
	})
	err := UpdateCore(context.Background(), model.CoreSingBox, fixture.archive, fixture.options)
	if !errors.Is(err, ErrCoreRejected) {
		t.Fatalf("error = %v", err)
	}
	if got := fileText(t, fixture.options.SingBoxBinary); got != "old sing-box" {
		t.Fatalf("binary replaced despite rejection: %q", got)
	}
	if strings.Contains(strings.Join(*fixture.calls, "\n"), "restart") {
		t.Fatalf("service restarted: %v", *fixture.calls)
	}
}

func TestUpdateCoreKeepsBinaryWhenNewCoreCannotRun(t *testing.T) {
	fixture := newCoreFixture(t, model.CoreRealm, func(_ string, args []string) (string, error) {
		if args[0] == "-v" {
			return "version `GLIBC_2.38' not found", errors.New("exit status 1")
		}
		return runningUnit, nil
	})
	err := UpdateCore(context.Background(), model.CoreRealm, fixture.archive, fixture.options)
	if !errors.Is(err, ErrCoreArchive) {
		t.Fatalf("error = %v", err)
	}
	if got := fileText(t, fixture.options.RealmBinary); got != "old realm" {
		t.Fatalf("binary replaced despite failed run: %q", got)
	}
	if strings.Contains(strings.Join(*fixture.calls, "\n"), "restart") {
		t.Fatalf("service restarted: %v", *fixture.calls)
	}
}

func TestUpdateCoreRestoresPreviousRealmWhenInstancesFail(t *testing.T) {
	var realmBinary string
	fixture := newCoreFixture(t, model.CoreRealm, func(_ string, args []string) (string, error) {
		if args[0] != "show" {
			return "", nil
		}
		// The new Realm keeps crashing; the restored one runs normally.
		if data, _ := os.ReadFile(realmBinary); string(data) == "new realm" {
			return "ActiveState=activating\nSubState=auto-restart\nMainPID=0", nil
		}
		return runningUnit, nil
	})
	realmBinary = fixture.options.RealmBinary
	err := UpdateCore(context.Background(), model.CoreRealm, fixture.archive, fixture.options)
	if !errors.Is(err, ErrCoreRestart) || errors.Is(err, ErrCoreRollbackFailed) {
		t.Fatalf("error = %v", err)
	}
	if got := fileText(t, realmBinary); got != "old realm" {
		t.Fatalf("previous binary not restored: %q", got)
	}
	calls := strings.Join(*fixture.calls, "\n")
	if strings.Count(calls, "restart portolan-realm@edge.service") != 2 || strings.Contains(calls, "restart portolan-sing-box") {
		t.Fatalf("calls:\n%s", calls)
	}
}

func TestUpdateCoreReportsFailedRollback(t *testing.T) {
	fixture := newCoreFixture(t, model.CoreRealm, func(_ string, args []string) (string, error) {
		if args[0] == "show" {
			return "ActiveState=failed\nSubState=failed\nMainPID=0", nil
		}
		return "", nil
	})
	err := UpdateCore(context.Background(), model.CoreRealm, fixture.archive, fixture.options)
	if !errors.Is(err, ErrCoreRestart) || !errors.Is(err, ErrCoreRollbackFailed) {
		t.Fatalf("error = %v", err)
	}
}
