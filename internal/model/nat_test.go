package model

import (
	"strings"
	"testing"
)

func TestPortMappingValidation(t *testing.T) {
	server := Server{Name: "nat"}
	server.NATPorts = []PortMapping{{Public: 30001, Host: 10001, Count: 20}, {Public: 30022, Host: 22, Count: 1}}
	if err := server.Validate(); err != nil {
		t.Fatalf("valid mappings rejected: %v", err)
	}
	for name, mappings := range map[string][]PortMapping{
		"empty range":      {{Public: 30001, Host: 10001}},
		"zero port":        {{Public: 0, Host: 10001, Count: 1}},
		"past 65535":       {{Public: 65530, Host: 10001, Count: 10}},
		"host overlap":     {{Public: 30001, Host: 10001, Count: 20}, {Public: 40001, Host: 10020, Count: 5}},
		"public overlap":   {{Public: 30001, Host: 10001, Count: 20}, {Public: 30020, Host: 20001, Count: 5}},
		"too many entries": make([]PortMapping, maxPortMappings+1),
	} {
		t.Run(name, func(t *testing.T) {
			server.NATPorts = mappings
			if err := server.Validate(); err == nil {
				t.Fatal("invalid mappings accepted")
			}
		})
	}
}

func TestPublicPortFollowsAddressFamily(t *testing.T) {
	plain := Server{}
	if port, ok := plain.PublicPort("203.0.113.10", 45678); !ok || port != 45678 || !plain.MapsHostPort(45678) {
		t.Fatalf("a server without mappings must publish every host port: %d %v", port, ok)
	}

	nat := Server{NATPorts: []PortMapping{{Public: 10001, Host: 10001, Count: 20}, {Public: 30001, Host: 20001, Count: 10}}}
	cases := []struct {
		address string
		host    uint16
		public  uint16
		ok      bool
	}{
		{"203.0.113.10", 10005, 10005, true},
		{"203.0.113.10", 20003, 30003, true},
		{"nat.example.com", 20009, 30009, true},
		{"203.0.113.10", 20011, 0, false},
		{"2001:db8::10", 20011, 20011, true},
		{"[2001:db8::10]", 45678, 45678, true},
	}
	for _, item := range cases {
		port, ok := nat.PublicPort(item.address, item.host)
		if port != item.public || ok != item.ok {
			t.Fatalf("PublicPort(%q, %d) = %d %v, want %d %v", item.address, item.host, port, ok, item.public, item.ok)
		}
		if !item.ok {
			continue
		}
		if back, ok := nat.HostPort(item.address, port); !ok || back != item.host {
			t.Fatalf("HostPort(%q, %d) = %d %v, want %d", item.address, port, back, ok, item.host)
		}
	}
	if nat.MapsHostPort(20011) || !nat.MapsHostPort(20001) {
		t.Fatal("MapsHostPort must follow the mapped host ranges")
	}
	if _, ok := nat.HostPort("203.0.113.10", 30011); ok {
		t.Fatal("an unmapped public port must not reach a host port")
	}
	nat.NATPorts = append(nat.NATPorts, PortMapping{Public: 30022, Host: 22, Count: 1})
	if got := nat.HostPortRanges(); !strings.Contains(got, "10001-10020") || !strings.HasSuffix(got, "、22") {
		t.Fatalf("host port ranges = %q", got)
	}
}

func TestForwardServicePort(t *testing.T) {
	legacy := Forward{TargetServerID: "target", TargetPort: 443}
	if legacy.ServicePort() != 443 {
		t.Fatal("a forward without a recorded host port dials the host port directly")
	}
	nat := Forward{IngressServerID: "nat", TargetServerID: "nat", TargetPort: 30001, TargetServerPort: 10001, ListenPort: 10001,
		Name: "loop", TargetHost: "203.0.113.10", Engine: ForwardRealm, Networks: []string{"tcp"}}
	if err := nat.Validate(); err == nil {
		t.Fatal("a forward to its own host port must be rejected")
	}
}
