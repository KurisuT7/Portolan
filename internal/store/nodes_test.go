package store

import (
	"testing"

	"github.com/KurisuT7/portolan/internal/model"
)

func TestDiscoveredProfileUsesReadableChinese(t *testing.T) {
	tests := []struct {
		name string
		node model.Node
		want string
	}{
		{
			name: "reality",
			node: model.Node{
				Protocol: model.ProtocolReality,
				Reality:  &model.RealitySpec{Flow: "xtls-rprx-vision"},
			},
			want: "外部 · REALITY · xtls-rprx-vision",
		},
		{
			name: "shadowsocks",
			node: model.Node{
				Protocol: model.ProtocolShadowsocks,
				SS:       &model.SSSpec{Method: "2022-blake3-aes-256-gcm"},
			},
			want: "外部 · 2022-blake3-aes-256-gcm",
		},
		{
			name: "snell",
			node: model.Node{
				Protocol: model.ProtocolSnell,
				Snell:    &model.SnellSpec{Version: 6},
			},
			want: "外部 · Snell v6",
		},
		{
			name: "unknown",
			node: model.Node{Protocol: model.Protocol("unknown")},
			want: "外部节点",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := discoveredProfile(test.node); got != test.want {
				t.Fatalf("discoveredProfile() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRepairLegacyDiscoveredProfile(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		want    string
	}{
		{
			name:    "reality",
			profile: legacyRealityPrefix + "xtls-rprx-vision",
			want:    "外部 · REALITY · xtls-rprx-vision",
		},
		{
			name:    "shadowsocks",
			profile: legacyExternalPrefix + "2022-blake3-aes-256-gcm",
			want:    "外部 · 2022-blake3-aes-256-gcm",
		},
		{
			name:    "snell",
			profile: legacyExternalPrefix + "Snell v6",
			want:    "外部 · Snell v6",
		},
		{
			name:    "unknown",
			profile: legacyExternalNodeProfile,
			want:    "外部节点",
		},
		{
			name:    "unrelated",
			profile: "自定义节点",
			want:    "自定义节点",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := repairLegacyDiscoveredProfile(test.profile); got != test.want {
				t.Fatalf("repairLegacyDiscoveredProfile() = %q, want %q", got, test.want)
			}
		})
	}
}
