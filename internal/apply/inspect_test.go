package apply

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KurisuT7/Portolan/internal/agentproto"
	"github.com/KurisuT7/Portolan/internal/model"
)

func TestInspectReportsActiveReleaseVersionsAndUnits(t *testing.T) {
	o, _ := recordedOptions(t)
	sing := realmForward("sing", 30001)
	sing.Engine = model.ForwardSingBox
	release := filepath.Join(o.RuntimeRoot, "releases", "77")
	if err := writeRelease(release, agentproto.SyncPayload{Revision: 77, Forwards: []model.Forward{realmForward("edge", 30000), sing}}, o); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrent(o.RuntimeRoot, release); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	var shown []string
	o.RunCommand = func(_ context.Context, name string, args ...string) (string, error) {
		switch name {
		case o.SingBoxBinary:
			return "sing-box version 1.14.2\n\nEnvironment: go1.25.12 linux/amd64", nil
		case o.RealmBinary:
			return "Realm 2.9.4 [brutal][batched-udp]", nil
		}
		shown = args[2:]
		return "Id=portolan-realm@edge.service\nActiveState=failed\nSubState=failed\n\n" +
			"ActiveState=active\nSubState=running\nId=portolan-sing-box.service", nil
	}
	status, err := Inspect(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	want := model.RuntimeStatus{AppliedRevision: 77, SingBoxVersion: "1.14.2", RealmVersion: "2.9.4", Units: []model.UnitStatus{
		{Name: "portolan-realm@edge.service", ActiveState: "failed", SubState: "failed"},
		{Name: "portolan-sing-box.service", ActiveState: "active", SubState: "running"},
	}}
	if !reflect.DeepEqual(status, want) {
		t.Fatalf("status = %#v", status)
	}
	if strings.Join(shown, " ") != "portolan-realm@edge.service portolan-sing-box.service" {
		t.Fatalf("queried units: %v", shown)
	}
	if err := status.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestActivePortsListEveryListenerOfTheActiveRelease(t *testing.T) {
	o, _ := recordedOptions(t)
	if ports, err := ActivePorts(o.RuntimeRoot); err != nil || ports != nil {
		t.Fatalf("without a release: ports=%v err=%v", ports, err)
	}
	sing := realmForward("sing", 30001)
	sing.Engine = model.ForwardSingBox
	node := model.Node{ID: "ss", Name: "SS", Protocol: model.ProtocolShadowsocks, ListenPort: 24443, Enabled: true,
		SS: &model.SSSpec{Method: "2022-blake3-aes-128-gcm", Password: "MDEyMzQ1Njc4OWFiY2RlZg=="}}
	release := filepath.Join(o.RuntimeRoot, "releases", "8")
	payload := agentproto.SyncPayload{Revision: 8, Nodes: []model.Node{node}, Forwards: []model.Forward{realmForward("edge", 30000), sing}}
	if err := writeRelease(release, payload, o); err != nil {
		t.Fatal(err)
	}
	if err := switchCurrent(o.RuntimeRoot, release); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ports, err := ActivePorts(o.RuntimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ports, []uint16{24443, 30000, 30001}) {
		t.Fatalf("ports = %v", ports)
	}
}

func TestInspectWithoutReleaseReportsNoUnits(t *testing.T) {
	o, _ := recordedOptions(t)
	o.RunCommand = func(context.Context, string, ...string) (string, error) { return "", errors.New("missing binary") }
	status, err := Inspect(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if status.AppliedRevision != 0 || status.SingBoxVersion != "" || status.RealmVersion != "" || len(status.Units) != 0 {
		t.Fatalf("status = %#v", status)
	}
}
