package recovery

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KurisuT7/Portolan/internal/configgen"
	"github.com/KurisuT7/Portolan/internal/model"
	"github.com/KurisuT7/Portolan/internal/store"
	"github.com/KurisuT7/Portolan/internal/vault"
)

func TestLoadAndWriteDatabasePreservesRuntimeCredentials(t *testing.T) {
	planPath, expected := writeFixture(t, nil)
	recovered, err := Load(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Servers) != 1 || len(recovered.Nodes) != 3 || len(recovered.Forwards) != 2 {
		t.Fatalf("unexpected recovery counts: %#v", recovered)
	}
	byID := map[string]model.Node{}
	for _, node := range recovered.Nodes {
		byID[node.Node.ID] = node.Node
	}
	if byID[expected.reality.ID].Reality.PrivateKey != expected.reality.Reality.PrivateKey ||
		byID[expected.reality.ID].Reality.PublicKey != expected.reality.Reality.PublicKey ||
		byID[expected.reality.ID].Reality.UUID != expected.reality.Reality.UUID ||
		byID[expected.reality.ID].Reality.Fingerprint != expected.reality.Reality.Fingerprint ||
		byID[expected.reality.ID].Reality.TransportSettings["host"] != expected.reality.Reality.TransportSettings["host"] {
		t.Fatal("Reality credentials or client-only transport metadata changed")
	}
	if byID[expected.shadowsocks.ID].SS.Password != expected.shadowsocks.SS.Password {
		t.Fatal("Shadowsocks password changed")
	}
	if byID[expected.snell.ID].Snell.PSK != expected.snell.Snell.PSK {
		t.Fatal("Snell PSK changed")
	}

	masterKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	databasePath := filepath.Join(t.TempDir(), "recovered.db")
	if err := WriteDatabase(context.Background(), databasePath, masterKey, recovered); err != nil {
		t.Fatal(err)
	}
	databaseInfo, err := os.Lstat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !databaseInfo.Mode().IsRegular() || (databaseModeEnforced() && databaseInfo.Mode().Perm() != 0o600) {
		t.Fatalf("recovered database mode is %v, expected a regular 0600 file", databaseInfo.Mode())
	}
	for _, suffix := range []string{"-wal", "-shm", ".importing", ".importing-wal", ".importing-shm"} {
		if _, err := os.Lstat(databasePath + suffix); !os.IsNotExist(err) {
			t.Fatalf("unexpected SQLite residue: %s", databasePath+suffix)
		}
	}
	secretVault, err := vault.New(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(databasePath, secretVault)
	if err != nil {
		t.Fatal(err)
	}
	databaseClosed := false
	t.Cleanup(func() {
		if !databaseClosed {
			_ = database.Close()
		}
	})
	stored, err := database.GetNode(context.Background(), expected.reality.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Reality.PrivateKey != expected.reality.Reality.PrivateKey || stored.Reality.PublicKey != expected.reality.Reality.PublicKey {
		t.Fatal("sealed Reality key pair changed after database import")
	}
	storedSS, err := database.GetNode(context.Background(), expected.shadowsocks.ID)
	if err != nil || storedSS.SS.Password != expected.shadowsocks.SS.Password {
		t.Fatalf("sealed Shadowsocks password changed after database import: %v", err)
	}
	storedSnell, err := database.GetNode(context.Background(), expected.snell.ID)
	if err != nil || storedSnell.Snell.PSK != expected.snell.Snell.PSK || storedSnell.Snell.ObfsMode != expected.snell.Snell.ObfsMode {
		t.Fatalf("sealed Snell settings changed after database import: %v", err)
	}
	forwards, err := database.ListForwards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(forwards) != 2 {
		t.Fatalf("unexpected stored forward count: %d", len(forwards))
	}
	forwardByID := map[string]model.Forward{}
	for _, forward := range forwards {
		forwardByID[forward.ID] = forward
	}
	for _, expectedForward := range []model.Forward{expected.singForward, expected.realmForward} {
		storedForward := forwardByID[expectedForward.ID]
		if storedForward.ListenPort != expectedForward.ListenPort || storedForward.TargetHost != expectedForward.TargetHost ||
			storedForward.TargetPort != expectedForward.TargetPort || storedForward.Engine != expectedForward.Engine ||
			strings.Join(storedForward.Networks, ",") != strings.Join(expectedForward.Networks, ",") {
			t.Fatalf("forward %s changed after database import: %#v", expectedForward.ID, storedForward)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	databaseClosed = true
	databaseBytes, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{expected.reality.Reality.PrivateKey, expected.reality.Reality.UUID, expected.shadowsocks.SS.Password, expected.snell.Snell.PSK} {
		if strings.Contains(string(databaseBytes), secret) {
			t.Fatal("protocol secret is present in plaintext in the recovered database")
		}
	}
}

func TestLoadRejectsMissingClientOnlyMetadata(t *testing.T) {
	planPath, _ := writeFixture(t, func(plan map[string]any) {
		server := plan["servers"].([]any)[0].(map[string]any)
		nodes := server["nodes"].(map[string]NodeMetadata)
		metadata := nodes["reality-one"]
		metadata.Fingerprint = nil
		nodes["reality-one"] = metadata
	})
	if _, err := Load(planPath); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("expected missing fingerprint failure, got %v", err)
	}
}

func TestLoadRejectsMismatchedRealityKeyPair(t *testing.T) {
	planPath, _ := writeFixture(t, func(plan map[string]any) {
		server := plan["servers"].([]any)[0].(map[string]any)
		nodes := server["nodes"].(map[string]NodeMetadata)
		metadata := nodes["reality-one"]
		wrong := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		metadata.PublicKey = &wrong
		nodes["reality-one"] = metadata
	})
	if _, err := Load(planPath); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected Reality key mismatch failure, got %v", err)
	}
}

func TestLoadRejectsRealityTransportMetadataNotPresentInRuntime(t *testing.T) {
	planPath, _ := writeFixture(t, func(plan map[string]any) {
		server := plan["servers"].([]any)[0].(map[string]any)
		nodes := server["nodes"].(map[string]NodeMetadata)
		metadata := nodes["reality-one"]
		settings := map[string]string{}
		for key, value := range *metadata.TransportSettings {
			settings[key] = value
		}
		settings["service_name"] = "not-present-in-runtime"
		metadata.TransportSettings = &settings
		nodes["reality-one"] = metadata
	})
	if _, err := Load(planPath); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("expected unverifiable transport metadata failure, got %v", err)
	}
}

func TestLoadRejectsUnrecoverableDisabledObjects(t *testing.T) {
	planPath, _ := writeFixture(t, func(plan map[string]any) {
		plan["manifest_nodes"] = 4
	})
	if _, err := Load(planPath); err == nil || !strings.Contains(err.Error(), "cannot be guessed") {
		t.Fatalf("expected disabled object failure, got %v", err)
	}
}

func TestValidateForwardAssociationRejectsRuntimeDrift(t *testing.T) {
	servers := map[string]model.Server{
		"ingress": {ID: "ingress", Name: "Ingress", Address: "198.51.100.10", EgressIPv4: true},
		"target":  {ID: "target", Name: "Target", Address: "target.example.com", EgressIPv4: true},
	}
	nodes := map[string]model.Node{
		"target-node": {ID: "target-node", ServerID: "target", Name: "Target node", ListenPort: 24443},
	}
	valid := model.Forward{
		ID: "forward", IngressServerID: "ingress", TargetServerID: "target", TargetNodeID: "target-node",
		TargetHost: "target.example.com", TargetPort: 24443,
	}
	if err := validateForwardAssociation(valid, servers, nodes); err != nil {
		t.Fatalf("valid association rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*model.Forward)
		want   string
	}{
		{"missing explicit target server", func(forward *model.Forward) { forward.TargetServerID = "" }, "belongs to server"},
		{"conflicting target server", func(forward *model.Forward) { forward.TargetServerID = "ingress" }, "belongs to server"},
		{"target node port drift", func(forward *model.Forward) { forward.TargetPort++ }, "does not match target node"},
		{"target host drift", func(forward *model.Forward) { forward.TargetHost = "203.0.113.99" }, "does not match target server"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			forward := valid
			test.mutate(&forward)
			if err := validateForwardAssociation(forward, servers, nodes); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q failure, got %v", test.want, err)
			}
		})
	}
}

type fixtureExpected struct {
	reality      model.Node
	shadowsocks  model.Node
	snell        model.Node
	singForward  model.Forward
	realmForward model.Forward
}

func writeFixture(t *testing.T, mutate func(map[string]any)) (string, fixtureExpected) {
	t.Helper()
	root := t.TempDir()
	release := filepath.Join(root, "runtime-release")
	confDir := filepath.Join(release, "sing-box", "conf.d")
	realmDir := filepath.Join(release, "realm")
	if err := os.MkdirAll(confDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(realmDir, 0o750); err != nil {
		t.Fatal(err)
	}

	privateBytes := make([]byte, 32)
	for index := range privateBytes {
		privateBytes[index] = byte(index + 1)
	}
	private := base64.RawURLEncoding.EncodeToString(privateBytes)
	privateKey, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes())
	reality := model.Node{
		ID: "reality-one", ServerID: "srv-one", Name: "Reality One", Protocol: model.ProtocolReality,
		ListenPort: 24443, Enabled: true,
		Reality: &model.RealitySpec{
			UUID: "4c20c398-ef17-4d4f-8810-73c879f240cb", Flow: "xtls-rprx-vision",
			HandshakeServer: "www.microsoft.com", HandshakePort: 443, ServerName: "www.microsoft.com",
			PrivateKey: private, PublicKey: public, ShortIDs: []string{"aabbccdd"}, Fingerprint: "chrome",
			Transport: "http", TransportSettings: map[string]string{"path": "/recovery", "host": "front.example.com"},
		},
	}
	shadowsocks := model.Node{
		ID: "ss-one", ServerID: "srv-one", Name: "SS One", Protocol: model.ProtocolShadowsocks,
		ListenPort: 24444, Enabled: true,
		SS: &model.SSSpec{Method: "aes-256-gcm", Password: "preserved-ss-password"},
	}
	snell := model.Node{
		ID: "snell-one", ServerID: "srv-one", Name: "Snell One", Protocol: model.ProtocolSnell,
		ListenPort: 24445, Enabled: true,
		Snell: &model.SnellSpec{Version: 5, PSK: "preserved-snell-psk", ObfsMode: "http"},
	}
	for _, node := range []model.Node{reality, shadowsocks, snell} {
		data, err := configgen.NodeFragment(node)
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(confDir, "10-node-"+node.ID+".json"), data)
	}
	singForward := model.Forward{
		ID: "sing-forward", IngressServerID: "srv-one", Name: "Sing Forward", ListenPort: 25551,
		Networks: []string{"tcp", "udp"}, TargetHost: "203.0.113.10", TargetPort: 443,
		Engine: model.ForwardSingBox, Enabled: true,
	}
	realmForward := model.Forward{
		ID: "realm-forward", IngressServerID: "srv-one", Name: "Realm Forward", ListenPort: 25552,
		Networks: []string{"tcp"}, TargetHost: "example.org", TargetPort: 8443,
		Engine: model.ForwardRealm, Enabled: true,
	}
	singData, err := configgen.ForwardFragment(singForward)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(confDir, "20-forward-"+singForward.ID+".json"), singData)
	realmData, err := configgen.RealmConfig(realmForward)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(realmDir, realmForward.ID+".toml"), realmData)

	manifestNodes := 3
	plan := map[string]any{
		"schema":         1,
		"manifest_nodes": manifestNodes,
	}
	nameReality, profileReality, fingerprint, transport := reality.Name, "REALITY · xtls-rprx-vision", reality.Reality.Fingerprint, reality.Reality.Transport
	settings := reality.Reality.TransportSettings
	nameSS, profileSS, allowSS := shadowsocks.Name, shadowsocks.SS.Method, false
	nameSnell, profileSnell, allowSnell := snell.Name, "Snell v5", false
	nameSing, empty := singForward.Name, ""
	nameRealm := realmForward.Name
	nodes := map[string]NodeMetadata{
		reality.ID:     {Name: &nameReality, Profile: &profileReality, PublicKey: &public, Fingerprint: &fingerprint, Transport: &transport, TransportSettings: &settings},
		shadowsocks.ID: {Name: &nameSS, Profile: &profileSS, AllowInsecure: &allowSS},
		snell.ID:       {Name: &nameSnell, Profile: &profileSnell, AllowInsecure: &allowSnell},
	}
	forwards := map[string]ForwardMetadata{
		singForward.ID:  {Name: &nameSing, TargetServerID: &empty, TargetNodeID: &empty},
		realmForward.ID: {Name: &nameRealm, TargetServerID: &empty, TargetNodeID: &empty},
	}
	serverDocument := map[string]any{
		"agent_config": "agent.json", "runtime_release": "runtime-release",
		"server": map[string]any{
			"id": "srv-one", "name": "Server One", "address": "203.0.113.1",
			"ipv4_address": "", "ipv6_address": "", "egress_ipv4": true, "egress_ipv6": false,
			"region": "",
		},
		"nodes": nodes, "forwards": forwards,
	}
	plan["servers"] = []any{serverDocument}
	if mutate != nil {
		mutate(plan)
	}
	manifestNodes = plan["manifest_nodes"].(int)
	delete(plan, "manifest_nodes")
	manifest := map[string]any{"revision": 42, "nodes": manifestNodes, "forwards": 2, "created_at": time.Now().UTC()}
	writeJSON(t, filepath.Join(release, "manifest.json"), manifest)
	writeJSON(t, filepath.Join(root, "agent.json"), map[string]any{"server_id": "srv-one", "agent_token": "this-old-token-must-never-be-printed"})
	planPath := filepath.Join(root, "plan.json")
	writeJSON(t, planPath, plan)
	return planPath, fixtureExpected{
		reality: reality, shadowsocks: shadowsocks, snell: snell,
		singForward: singForward, realmForward: realmForward,
	}
}

func TestPublishNoReplacePreservesExistingTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	target := filepath.Join(root, "target.db")
	writeTestFile(t, source, []byte("new database"))
	writeTestFile(t, target, []byte("existing database"))
	if err := publishNoReplace(source, target); err == nil {
		t.Fatal("expected no-replace publish to reject an existing target")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "existing database" {
		t.Fatal("existing target was modified")
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, data)
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
