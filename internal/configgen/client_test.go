package configgen

import (
	"net/url"
	"strings"
	"testing"

	"github.com/KurisuT7/Portolan/internal/model"
)

func TestRealityExportUsesShareLinkParameterNames(t *testing.T) {
	privateKey, publicKey, err := RealityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	node := model.Node{ID: "grpc", Name: "gRPC", Protocol: model.ProtocolReality, ListenPort: 443, Reality: &model.RealitySpec{
		UUID: "6d1f0d6e-2f1c-4c55-9b5f-6a0c1e7b8f10", HandshakeServer: "aws.amazon.com", HandshakePort: 443,
		ServerName: "aws.amazon.com", PrivateKey: privateKey, PublicKey: publicKey, ShortIDs: []string{"0123456789abcdef"},
		Fingerprint: "chrome", Transport: "grpc", TransportSettings: map[string]string{"service_name": "edge"},
	}}
	export, err := ExportClient(node, PublishedEndpoint{Address: "203.0.113.9", Port: 443, Name: "Tokyo"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(export.URI)
	if err != nil {
		t.Fatal(err)
	}
	query := link.Query()
	if query.Get("serviceName") != "edge" || query.Has("service_name") || query.Get("type") != "grpc" {
		t.Fatalf("unexpected gRPC share link: %s", export.URI)
	}
}

func TestShadowsocksExportEncodingFollowsSIP002(t *testing.T) {
	endpoint := PublishedEndpoint{Address: "203.0.113.9", Port: 8388, Name: "edge"}
	modern := model.Node{ID: "ss2022", Name: "ss2022", Protocol: model.ProtocolShadowsocks, ListenPort: 8388,
		SS: &model.SSSpec{Method: "2022-blake3-aes-256-gcm", Password: "6dTK/AU6smBCNgj3ixv9B7kiCIDI2v+KGeqI2LPo++w="}}
	export, err := ExportClient(modern, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	want := "ss://2022-blake3-aes-256-gcm:6dTK%2FAU6smBCNgj3ixv9B7kiCIDI2v%2BKGeqI2LPo%2B%2Bw%3D@203.0.113.9:8388#edge"
	if export.URI != want {
		t.Fatalf("AEAD-2022 link = %s, want %s", export.URI, want)
	}

	classic := model.Node{ID: "aead", Name: "aead", Protocol: model.ProtocolShadowsocks, ListenPort: 8388,
		SS: &model.SSSpec{Method: "aes-256-gcm", Password: "secret"}}
	export, err = ExportClient(classic, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if export.URI != "ss://YWVzLTI1Ni1nY206c2VjcmV0@203.0.113.9:8388#edge" {
		t.Fatalf("AEAD link should keep Base64URL user info: %s", export.URI)
	}
}

func TestSnellExportUsesSurgeParameters(t *testing.T) {
	tests := []struct {
		name   string
		spec   model.SnellSpec
		want   string
		reject string
	}{
		{"v5 http obfuscation", model.SnellSpec{Version: 5, PSK: "correct-horse-battery", ObfsMode: "http"}, ", obfs=http", "mode="},
		{"v5 without obfuscation", model.SnellSpec{Version: 5, PSK: "correct-horse-battery", ObfsMode: "none"}, "version=5", "obfs="},
		{"v6 unshaped", model.SnellSpec{Version: 6, PSK: "correct-horse-battery", Mode: "unshaped"}, ", mode=unshaped", "obfs="},
		{"v6 default", model.SnellSpec{Version: 6, PSK: "correct-horse-battery", Mode: "default"}, "version=6", "mode="},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node := model.Node{ID: "snell", Name: "Snell", Protocol: model.ProtocolSnell, ListenPort: 6160, Snell: &test.spec}
			export, err := ExportClient(node, PublishedEndpoint{Address: "203.0.113.9", Port: 6160, Name: "Snell"})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(export.SurgeLine, "Snell = snell, 203.0.113.9, 6160, psk=correct-horse-battery, ") ||
				!strings.Contains(export.SurgeLine, test.want) || strings.Contains(export.SurgeLine, test.reject) {
				t.Fatalf("Surge line = %q", export.SurgeLine)
			}
		})
	}
}
