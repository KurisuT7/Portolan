package discovery

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/KurisuT7/Portolan/internal/model"
)

func TestScanSingBoxRealityAndShadowsocks(t *testing.T) {
	privateKey := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	config := fmt.Sprintf(`{
		// Comments are accepted by sing-box and should not break discovery.
		"inbounds": [
			{
				"type": "vless", "tag": "reality-existing", "listen_port": 443,
				"users": [{"uuid": "2f021724-15a1-4e7f-b98a-3e40b7016f26", "flow": "xtls-rprx-vision"}],
				"tls": {"enabled": true, "server_name": "aws.amazon.com", "reality": {
					"enabled": true, "handshake": {"server": "aws.amazon.com", "server_port": 443},
					"private_key": %q, "short_id": ["01234567"]
				}}
			},
			{
				"type": "shadowsocks", "tag": "ss-existing", "listen_port": 24443,
				"method": "2022-blake3-aes-256-gcm", "password": "server-key",
				"users": [{"name": "primary", "password": "user-key"}]
			}
		]
	}`, privateKey)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Scan(context.Background(), Options{Paths: []string{path}})
	if len(report.Items) != 2 {
		t.Fatalf("discovered %d nodes, warnings=%v", len(report.Items), report.Warnings)
	}
	if report.Items[0].Protocol != model.ProtocolReality || report.Items[0].Reality.PublicKey == "" || report.Items[0].Managed {
		t.Fatalf("unexpected Reality discovery: %#v", report.Items[0])
	}
	if report.Items[1].Protocol != model.ProtocolShadowsocks || report.Items[1].SS.Password != "server-key:user-key" {
		t.Fatalf("unexpected Shadowsocks discovery: %#v", report.Items[1])
	}
}

func TestScanStandaloneSnellV5(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snell-server.conf")
	config := "[snell-server]\nlisten = ::0:60735\npsk = 0123456789abcdef\nipv6 = true\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Scan(context.Background(), Options{Paths: []string{path}, SnellVersion: 5})
	if len(report.Items) != 1 || len(report.Warnings) != 0 {
		t.Fatalf("unexpected Snell report: %#v", report)
	}
	node := report.Items[0]
	if node.Protocol != model.ProtocolSnell || node.ListenPort != 60735 || node.Snell.Version != 5 || node.Snell.PSK != "0123456789abcdef" {
		t.Fatalf("unexpected Snell node: %#v", node)
	}
}
