package configgen

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KurisuT7/Portolan/internal/model"
)

func TestEveryShadowsocksMethodRenders(t *testing.T) {
	for _, method := range model.ShadowsocksMethods {
		t.Run(method, func(t *testing.T) {
			password, err := ShadowsocksPassword(method)
			if err != nil {
				t.Fatal(err)
			}
			node := model.Node{
				ID: "ss-test", Name: "SS test", Protocol: model.ProtocolShadowsocks, ListenPort: 21001, Enabled: true,
				SS: &model.SSSpec{Method: method, Password: password, AllowInsecure: method == "none"},
			}
			data, err := NodeFragment(node)
			if err != nil {
				t.Fatal(err)
			}
			assertJSONContains(t, data, `"method": "`+method+`"`)
			if strings.HasPrefix(method, "2022-blake3-aes-128") {
				decoded, err := base64.StdEncoding.DecodeString(password)
				if err != nil || len(decoded) != 16 {
					t.Fatalf("2022 AES-128 key should decode to 16 bytes: len=%d err=%v", len(decoded), err)
				}
			}
		})
	}
}

func TestRealityRendersKeysTransportAndHandshake(t *testing.T) {
	privateKey, publicKey, err := RealityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	uuid, _ := NewUUID()
	node := model.Node{
		ID: "reality-test", Name: "Reality test", Protocol: model.ProtocolReality, ListenPort: 21002, Enabled: true,
		Reality: &model.RealitySpec{
			UUID: uuid, HandshakeServer: "aws.amazon.com", HandshakePort: 443,
			ServerName: "aws.amazon.com", PrivateKey: privateKey, PublicKey: publicKey,
			ShortIDs: []string{"0123456789abcdef"}, Fingerprint: "chrome", Transport: "grpc",
			TransportSettings: map[string]string{"service_name": "edge"},
		},
	}
	data, err := NodeFragment(node)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONContains(t, data, `"type": "vless"`, `"server_port": 443`, `"private_key":`, `"service_name": "edge"`)
}

func TestSnellV5AndV6RenderVersionSpecificFields(t *testing.T) {
	tests := []struct {
		name string
		spec model.SnellSpec
		want string
	}{
		{"v5-http", model.SnellSpec{Version: 5, PSK: "correct-horse-battery", ObfsMode: "http"}, `"obfs_mode": "http"`},
		{"v6-unshaped", model.SnellSpec{Version: 6, PSK: "correct-horse-battery", Mode: "unshaped"}, `"mode": "unshaped"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node := model.Node{ID: test.name, Name: test.name, Protocol: model.ProtocolSnell, ListenPort: 21003, Enabled: true, Snell: &test.spec}
			data, err := NodeFragment(node)
			if err != nil {
				t.Fatal(err)
			}
			assertJSONContains(t, data, `"type": "snell"`, test.want)
		})
	}
}

func TestUnsafeOptionsRequireExplicitOptIn(t *testing.T) {
	none := model.Node{ID: "none", Name: "none", Protocol: model.ProtocolShadowsocks, ListenPort: 1, SS: &model.SSSpec{Method: "none", Password: "x"}}
	if _, err := NodeFragment(none); err == nil {
		t.Fatal("expected method none to be rejected without allow_insecure")
	}
	unsafeRaw := model.Node{ID: "raw", Name: "raw", Protocol: model.ProtocolSnell, ListenPort: 2, Snell: &model.SnellSpec{Version: 6, PSK: "123456789012", Mode: "unsafe-raw"}}
	if _, err := NodeFragment(unsafeRaw); err == nil {
		t.Fatal("expected unsafe-raw to be rejected without allow_insecure")
	}
}

func TestForwardRendersTCPAndUDP(t *testing.T) {
	data, err := ForwardFragment(model.Forward{
		ID: "edge", Name: "Tokyo ingress", IngressServerID: "jp", ListenPort: 32000,
		Networks: []string{"tcp", "udp"}, TargetHost: "203.0.113.8", TargetPort: 443,
		Engine: model.ForwardSingBox, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONContains(t, data, `"type": "direct"`, `"override_address": "203.0.113.8"`, `"tcp"`, `"udp"`)
}

func TestForwardRendersCanonicalIPv6ForBothEngines(t *testing.T) {
	t.Parallel()
	forward := model.Forward{
		ID: "ipv6", Name: "IPv6 port target", IngressServerID: "entry", ListenPort: 32001,
		Networks: []string{"tcp", "udp"}, TargetHost: "[2001:0db8::8]", TargetPort: 443,
		Engine: model.ForwardSingBox, Enabled: true,
	}
	singBox, err := ForwardFragment(forward)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONContains(t, singBox, `"listen": "::"`, `"override_address": "2001:db8::8"`)

	forward.Engine = model.ForwardRealm
	realm, err := RealmConfig(forward)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`listen = "[::]:32001"`, `remote = "[2001:db8::8]:443"`, "ipv6_only = false"} {
		if !strings.Contains(string(realm), want) {
			t.Fatalf("missing %q in Realm config:\n%s", want, realm)
		}
	}
	if strings.Contains(string(realm), "[[2001:") {
		t.Fatalf("Realm IPv6 target was bracketed twice:\n%s", realm)
	}
}

func TestClientExportsUsePublishedEndpoint(t *testing.T) {
	node := model.Node{ID: "ss", Name: "origin", Protocol: model.ProtocolShadowsocks, ListenPort: 1000, SS: &model.SSSpec{Method: "aes-256-gcm", Password: "secret"}}
	export, err := ExportClient(node, PublishedEndpoint{Address: "2001:db8::8", Port: 2443, Name: "Hong Kong relay"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(export.URI, "@[2001:db8::8]:2443") {
		t.Fatalf("export should use the published relay endpoint: %s", export.URI)
	}
}

func assertJSONContains(t *testing.T, data []byte, wants ...string) {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}
	for _, want := range wants {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s in:\n%s", want, data)
		}
	}
}
