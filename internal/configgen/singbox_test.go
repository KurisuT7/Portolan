package configgen

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
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

func TestShadowTLSWrapsShadowsocksAndSnell(t *testing.T) {
	stls := &model.ShadowTLSSpec{Password: "stls-password", HandshakeServer: "aws.amazon.com", HandshakePort: 443}
	for _, node := range []model.Node{
		{ID: "ss", Name: "SS", Protocol: model.ProtocolShadowsocks, ListenPort: 21004, ShadowTLS: stls,
			SS: &model.SSSpec{Method: "2022-blake3-aes-128-gcm", Password: "8JCsPssfgS8tiRwiMlhARg=="}},
		{ID: "snell", Name: "Snell", Protocol: model.ProtocolSnell, ListenPort: 21005, ShadowTLS: stls,
			Snell: &model.SnellSpec{Version: 6, PSK: "correct-horse-battery"}},
	} {
		t.Run(node.ID, func(t *testing.T) {
			data, err := NodeFragment(node)
			if err != nil {
				t.Fatal(err)
			}
			var fragment struct {
				Inbounds []map[string]any `json:"inbounds"`
			}
			if err := json.Unmarshal(data, &fragment); err != nil || len(fragment.Inbounds) != 2 {
				t.Fatalf("expected a ShadowTLS and a protocol inbound: %s", data)
			}
			wrapper, inner := fragment.Inbounds[0], fragment.Inbounds[1]
			if wrapper["type"] != "shadowtls" || wrapper["tag"] != "node-"+node.ID || wrapper["listen"] != "::" ||
				wrapper["listen_port"] != float64(node.ListenPort) || wrapper["version"] != float64(3) || wrapper["strict_mode"] != true ||
				wrapper["detour"] != inner["tag"] || inner["tag"] != "node-"+node.ID+"-inner" || inner["type"] != string(node.Protocol) {
				t.Fatalf("unexpected ShadowTLS wiring: %s", data)
			}
			assertJSONContains(t, data, `"password": "stls-password"`, `"server": "aws.amazon.com"`, `"server_port": 443`)
			if node.Protocol == model.ProtocolShadowsocks {
				// Shadowsocks UDP stays on the node's port; TCP arrives only through ShadowTLS.
				if inner["listen"] != "::" || inner["listen_port"] != float64(node.ListenPort) || !reflect.DeepEqual(inner["network"], []any{"udp"}) {
					t.Fatalf("Shadowsocks behind ShadowTLS should listen only on UDP: %s", data)
				}
			} else if _, public := inner["listen_port"]; public || inner["listen"] != "127.0.0.1" {
				t.Fatalf("Snell behind ShadowTLS must not listen publicly: %s", data)
			}
		})
	}
	reality := model.Node{ID: "reality", Name: "Reality", Protocol: model.ProtocolReality, ListenPort: 21006, ShadowTLS: stls,
		Reality: &model.RealitySpec{UUID: "6d1f0d6e-2f1c-4c55-9b5f-6a0c1e7b8f10", HandshakeServer: "aws.amazon.com", HandshakePort: 443,
			ServerName: "aws.amazon.com", PrivateKey: "private", PublicKey: "public", ShortIDs: []string{"01"}, Fingerprint: "chrome"}}
	if _, err := NodeFragment(reality); err == nil {
		t.Fatal("ShadowTLS must not wrap Reality")
	}
	for _, spec := range []model.ShadowTLSSpec{
		{HandshakeServer: "aws.amazon.com", HandshakePort: 443},
		{Password: "x", HandshakeServer: "192.0.2.1", HandshakePort: 443},
		{Password: "x", HandshakeServer: "aws.amazon.com"},
	} {
		node := model.Node{ID: "ss", Name: "SS", Protocol: model.ProtocolShadowsocks, ListenPort: 21007, ShadowTLS: &spec,
			SS: &model.SSSpec{Method: "aes-128-gcm", Password: "secret"}}
		if _, err := NodeFragment(node); err == nil {
			t.Fatalf("incomplete ShadowTLS settings were accepted: %+v", spec)
		}
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
