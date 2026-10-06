package configgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/KurisuT7/Portolan/internal/model"
)

// Run with SING_BOX_BIN set to a real sing-box binary. The normal test suite
// remains hermetic; CI opts into this compatibility check with a pinned release.
func TestGeneratedFragmentsPassSingBoxCheck(t *testing.T) {
	binary := os.Getenv("SING_BOX_BIN")
	if binary == "" {
		t.Skip("SING_BOX_BIN is not set")
	}
	root := t.TempDir()
	confDir := filepath.Join(root, "conf.d")
	if err := os.MkdirAll(confDir, 0o700); err != nil {
		t.Fatal(err)
	}
	base, err := BaseConfig()
	if err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(root, "00-base.json")
	if err := os.WriteFile(basePath, base, 0o600); err != nil {
		t.Fatal(err)
	}
	port := uint16(22000)
	for _, method := range model.ShadowsocksMethods {
		password, err := ShadowsocksPassword(method)
		if err != nil {
			t.Fatal(err)
		}
		node := model.Node{ID: "ss_" + strconv.Itoa(int(port)), Name: method, Protocol: model.ProtocolShadowsocks, ListenPort: port, Enabled: true,
			SS: &model.SSSpec{Method: method, Password: password, AllowInsecure: method == "none"}}
		writeNodeFixture(t, confDir, node)
		port++
	}
	privateKey, publicKey, err := RealityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	uuid, _ := NewUUID()
	shortID, _ := RandomShortID()
	for _, transport := range []string{"tcp", "http", "ws", "grpc", "httpupgrade"} {
		settings := map[string]string{}
		flow := ""
		switch transport {
		case "tcp":
			flow = "xtls-rprx-vision"
		case "http", "ws", "httpupgrade":
			settings["path"] = "/edge"
			settings["host"] = "aws.amazon.com"
		case "grpc":
			settings["service_name"] = "edge"
		}
		node := model.Node{ID: "reality_" + transport, Name: "Reality " + transport, Protocol: model.ProtocolReality, ListenPort: port, Enabled: true,
			Reality: &model.RealitySpec{UUID: uuid, Flow: flow, HandshakeServer: "aws.amazon.com", HandshakePort: 443,
				ServerName: "aws.amazon.com", PrivateKey: privateKey, PublicKey: publicKey, ShortIDs: []string{shortID},
				Fingerprint: "chrome", Transport: transport, TransportSettings: settings}}
		writeNodeFixture(t, confDir, node)
		port++
	}
	writeNodeFixture(t, confDir, model.Node{ID: "snell_5", Name: "Snell 5", Protocol: model.ProtocolSnell, ListenPort: port, Enabled: true,
		Snell: &model.SnellSpec{Version: 5, PSK: "correct-horse-battery", ObfsMode: "http"}})
	port++
	writeNodeFixture(t, confDir, model.Node{ID: "snell_6", Name: "Snell 6", Protocol: model.ProtocolSnell, ListenPort: port, Enabled: true,
		Snell: &model.SnellSpec{Version: 6, PSK: "correct-horse-battery", Mode: "default"}})
	port++
	forward, err := ForwardFragment(model.Forward{ID: "forward", Name: "forward", ListenPort: port, Networks: []string{"tcp", "udp"},
		TargetHost: "[2001:0db8::20]", TargetPort: 443, Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "20-forward.json"), forward, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "check", "-c", basePath, "-C", confDir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sing-box rejected generated compatibility matrix: %v\n%s", err, output)
	}
}

func writeNodeFixture(t *testing.T, directory string, node model.Node) {
	t.Helper()
	data, err := NodeFragment(node)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "10-"+node.ID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
