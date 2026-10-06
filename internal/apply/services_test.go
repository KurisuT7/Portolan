package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KurisuT7/Portolan/internal/agentproto"
	"github.com/KurisuT7/Portolan/internal/model"
)

func TestMain(m *testing.M) {
	settleDelay = 0
	os.Exit(m.Run())
}

const runningUnit = "ActiveState=active\nSubState=running\nMainPID=42"

func recordedOptions(t *testing.T) (Options, *[]string) {
	t.Helper()
	var calls []string
	o := Options{RuntimeRoot: t.TempDir(), RunCommand: func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "show" {
			return runningUnit, nil
		}
		return "", nil
	}}
	if err := defaultsAndValidate(&o); err != nil {
		t.Fatal(err)
	}
	return o, &calls
}

func releaseFixture(t *testing.T, o Options, name string, forwards ...model.Forward) string {
	t.Helper()
	path := filepath.Join(o.RuntimeRoot, name)
	if err := writeRelease(path, agentproto.SyncPayload{Revision: 1, Forwards: forwards}, o); err != nil {
		t.Fatal(err)
	}
	return path
}

func realmForward(id string, port uint16) model.Forward {
	return model.Forward{ID: id, Name: id, ListenPort: port, TargetHost: "example.com", TargetPort: 443,
		Networks: []string{"tcp", "udp"}, Enabled: true, Engine: model.ForwardRealm}
}

// procFixture creates a /proc layout in which pid 42 holds the given socket
// inodes and the network tables contain the given lines.
func procFixture(t *testing.T, inodes []string, tables map[string][]string) string {
	t.Helper()
	root := t.TempDir()
	fd := filepath.Join(root, "42", "fd")
	if err := os.MkdirAll(fd, 0o700); err != nil {
		t.Fatal(err)
	}
	for index, inode := range inodes {
		if err := os.Symlink("socket:["+inode+"]", filepath.Join(fd, string(rune('3'+index)))); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o700); err != nil {
		t.Fatal(err)
	}
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode"
	for name, lines := range tables {
		if err := os.WriteFile(filepath.Join(root, "net", name), []byte(strings.Join(append([]string{header}, lines...), "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestPlanOnlyChangesAffectedServices(t *testing.T) {
	o, calls := recordedOptions(t)
	a, b := realmForward("a", 20001), realmForward("b", 20002)
	sing := realmForward("sing", 20003)
	sing.Engine = model.ForwardSingBox
	before := releaseFixture(t, o, "before", a, b, sing)
	a.TargetPort = 8443
	after := releaseFixture(t, o, "after", a, b, sing)
	plan, err := planServices(o, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if want := []serviceChange{{"portolan-realm@a.service", true, true}}; !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan = %#v, want %#v", plan, want)
	}
	if err := activateServices(context.Background(), o, plan, nil); err != nil {
		t.Fatal(err)
	}
	for _, call := range *calls {
		if strings.Contains(call, "@b.") || strings.Contains(call, "sing-box") {
			t.Fatalf("unrelated service touched: %s", call)
		}
	}
	plan, err = planServices(o, after, after)
	if err != nil || len(plan) != 0 {
		t.Fatalf("identical snapshot: %#v, %v", plan, err)
	}
}

func TestPortSwapStopsAllChangedListenersBeforeStarting(t *testing.T) {
	o, calls := recordedOptions(t)
	plan := []serviceChange{{"a", true, true}, {"b", true, true}, {"removed", true, false}, {"added", false, true}}
	if err := activateServices(context.Background(), o, plan, nil); err != nil {
		t.Fatal(err)
	}
	wantPrefix := []string{"stop a", "stop b", "stop removed"}
	if !reflect.DeepEqual((*calls)[:3], wantPrefix) {
		t.Fatalf("unsafe activation order: %v", *calls)
	}
	if !strings.Contains(strings.Join(*calls, "\n"), "show --property=ActiveState,SubState,MainPID added") {
		t.Fatal("missing Realm status check")
	}
}

func TestActivationRejectsServiceThatDoesNotStayRunning(t *testing.T) {
	for name, settled := range map[string]string{
		"exited":     "ActiveState=inactive\nSubState=dead\nMainPID=0",
		"restarted":  "ActiveState=active\nSubState=running\nMainPID=43",
		"restarting": "ActiveState=activating\nSubState=auto-restart\nMainPID=0",
	} {
		t.Run(name, func(t *testing.T) {
			o, _ := recordedOptions(t)
			shows := 0
			o.RunCommand = func(_ context.Context, _ string, args ...string) (string, error) {
				if args[0] != "show" {
					return "", nil
				}
				shows++
				if shows == 1 {
					return runningUnit, nil
				}
				return settled, nil
			}
			err := activateServices(context.Background(), o, []serviceChange{{"portolan-sing-box.service", false, true}}, nil)
			if err == nil || !strings.Contains(err.Error(), "did not stay running") {
				t.Fatalf("unstable service accepted: %v", err)
			}
		})
	}
}

func TestActivationRequiresOwnedListeners(t *testing.T) {
	tcp := "   0: 00000000000000000000000000000000:4E21 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000   998        0 1001 1 0000000000000000 100 0 0 10 0"
	udp := " 1: 00000000000000000000000000000000:4E21 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000   998        0 1002 2 0000000000000000 0"
	foreign := " 1: 00000000000000000000000000000000:4E21 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 2002 2 0000000000000000 0"
	unit := "portolan-realm@a.service"
	listeners := map[string][]listener{unit: {{"tcp", 20001}, {"udp", 20001}}}
	plan := []serviceChange{{unit, false, true}}

	o, _ := recordedOptions(t)
	o.ProcRoot = procFixture(t, []string{"1001", "1002"}, map[string][]string{"tcp6": {tcp}, "udp6": {udp}})
	if err := activateServices(context.Background(), o, plan, listeners); err != nil {
		t.Fatalf("owned listeners rejected: %v", err)
	}

	// Realm keeps running when only one of its bind tasks fails, and the port
	// may be held by an unrelated process.
	o.ProcRoot = procFixture(t, []string{"1001"}, map[string][]string{"tcp6": {tcp}, "udp6": {foreign}})
	err := activateServices(context.Background(), o, plan, listeners)
	if err == nil || !strings.Contains(err.Error(), "is not listening on udp port 20001") {
		t.Fatalf("missing UDP socket accepted: %v", err)
	}
}

func TestExpectedListenersFollowEnginesAndNetworks(t *testing.T) {
	o, _ := recordedOptions(t)
	sing := realmForward("sing", 30001)
	sing.Engine = model.ForwardSingBox
	sing.Networks = []string{"udp"}
	disabled := realmForward("off", 30002)
	disabled.Enabled = false
	payload := agentproto.SyncPayload{
		Nodes:    []model.Node{{ID: "n", ListenPort: 443, Enabled: true}},
		Forwards: []model.Forward{realmForward("r", 30000), sing, disabled},
	}
	want := map[string][]listener{
		"portolan-sing-box.service": {{"tcp", 443}, {"udp", 30001}},
		"portolan-realm@r.service":  {{"tcp", 30000}, {"udp", 30000}},
	}
	if got := expectedListeners(o, payload); !reflect.DeepEqual(got, want) {
		t.Fatalf("listeners = %#v", got)
	}
}

func TestRollbackRestoresBothEnginesAndDisablesNewInstances(t *testing.T) {
	o, calls := recordedOptions(t)
	previous := releaseFixture(t, o, "previous")
	current := releaseFixture(t, o, "candidate")
	if err := switchCurrent(o.RuntimeRoot, current); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	plan := []serviceChange{{"sing-box", true, true}, {"realm-old", true, false}, {"realm-new", false, true}}
	if err := rollback(context.Background(), o, previous, plan); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(o.RuntimeRoot, "current"))
	if err != nil || target != previous {
		t.Fatalf("current = %s, %v", target, err)
	}
	for _, expected := range []string{"stop realm-new", "enable --now realm-old", "enable --now sing-box", "disable realm-new"} {
		if !strings.Contains(strings.Join(*calls, "\n"), expected) {
			t.Errorf("missing %q: %v", expected, *calls)
		}
	}
}

func TestRollbackContinuesAfterIndividualFailure(t *testing.T) {
	o, _ := recordedOptions(t)
	previous := releaseFixture(t, o, "previous")
	if err := switchCurrent(o.RuntimeRoot, previous); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	var calls []string
	o.RunCommand = func(_ context.Context, _ string, args ...string) (string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		if call == "enable --now broken" {
			return "", errors.New("failed")
		}
		if args[0] == "show" {
			return runningUnit, nil
		}
		return "", nil
	}
	err := rollback(context.Background(), o, previous, []serviceChange{{"broken", true, true}, {"healthy", true, true}})
	if err == nil {
		t.Fatal("recovery failure was hidden")
	}
	if !strings.Contains(strings.Join(calls, "\n"), "enable --now healthy") {
		t.Fatal("other service was not restored")
	}
}

func TestFirstApplyFailureLeavesNoActiveCandidate(t *testing.T) {
	o, calls := recordedOptions(t)
	current := releaseFixture(t, o, "candidate")
	if err := switchCurrent(o.RuntimeRoot, current); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := rollback(context.Background(), o, "", []serviceChange{{"new", false, true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(o.RuntimeRoot, "current")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate retained: %v", err)
	}
	if want := []string{"stop new", "disable new"}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls: %v", *calls)
	}
}

func TestStopAndStartFailuresAreNotIgnored(t *testing.T) {
	for _, fail := range []string{"stop changed", "enable --now changed", "disable removed"} {
		t.Run(fail, func(t *testing.T) {
			o, _ := recordedOptions(t)
			o.RunCommand = func(_ context.Context, _ string, args ...string) (string, error) {
				if strings.Join(args, " ") == fail {
					return "", errors.New("failure")
				}
				if args[0] == "show" {
					return runningUnit, nil
				}
				return "", nil
			}
			if err := activateServices(context.Background(), o, []serviceChange{{"changed", true, true}, {"removed", true, false}}, nil); err == nil {
				t.Fatal("failure hidden")
			}
		})
	}
}
