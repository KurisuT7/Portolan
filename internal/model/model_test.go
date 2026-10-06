package model

import "testing"

func TestRealityTargetSafetyRules(t *testing.T) {
	valid := RealitySpec{
		UUID: "uuid", PrivateKey: "private", PublicKey: "public",
		Flow: "xtls-rprx-vision", HandshakeServer: "aws.amazon.com", HandshakePort: 443,
		ServerName: "aws.amazon.com", Fingerprint: "chrome", ShortIDs: []string{"aabbccdd"}, Transport: "tcp",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validated Reality target rejected: %v", err)
	}

	mismatch := valid
	mismatch.ServerName = "www.amazon.com"
	if err := mismatch.Validate(); err == nil {
		t.Fatal("mismatched Reality handshake target and SNI were accepted")
	}

	cloudflare := valid
	cloudflare.HandshakeServer = "www.cloudflare.com"
	cloudflare.ServerName = "www.cloudflare.com"
	if err := cloudflare.Validate(); err == nil {
		t.Fatal("Cloudflare Reality target was accepted")
	}
}

func TestForwardNormalizesIPv4AndIPv6Targets(t *testing.T) {
	t.Parallel()
	for _, target := range []struct {
		input string
		want  string
	}{
		{input: "203.0.113.8", want: "203.0.113.8"},
		{input: "2001:0db8::8", want: "2001:db8::8"},
		{input: "[2001:db8::8]", want: "2001:db8::8"},
		{input: "relay.example.com", want: "relay.example.com"},
	} {
		target := target
		t.Run(target.input, func(t *testing.T) {
			forward := Forward{
				IngressServerID: "entry", Name: "port target", ListenPort: 2443,
				Networks: []string{"tcp"}, TargetHost: target.input, TargetPort: 443,
				Engine: ForwardSingBox, Enabled: true,
			}
			if err := forward.Validate(); err != nil {
				t.Fatalf("valid target %q rejected: %v", target.input, err)
			}
			if got := NormalizeHost(target.input); got != target.want {
				t.Fatalf("NormalizeHost(%q) = %q, want %q", target.input, got, target.want)
			}
		})
	}
}

func TestForwardRejectsDirectSelfLoop(t *testing.T) {
	t.Parallel()
	forward := Forward{
		IngressServerID: "same", TargetServerID: "same", Name: "loop",
		ListenPort: 2443, TargetHost: "203.0.113.8", TargetPort: 2443,
		Networks: []string{"tcp"}, Engine: ForwardRealm, Enabled: true,
	}
	if err := forward.Validate(); err == nil {
		t.Fatal("forward targeting its own listening port was accepted")
	}
}

func TestPublicRoutableIPRejectsSharedAndPrivateAddresses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		address string
		want    bool
	}{
		{address: "100.88.13.252", want: false},
		{address: "100.64.0.1", want: false},
		{address: "100.127.255.254", want: false},
		{address: "10.0.0.1", want: false},
		{address: "203.0.113.8", want: true},
		{address: "2001:db8::8", want: true},
	} {
		if got := IsPublicRoutableIP(test.address); got != test.want {
			t.Errorf("IsPublicRoutableIP(%q) = %v, want %v", test.address, got, test.want)
		}
	}
}

func TestSelectServerTargetAddressSeparatesPublicIngressAndEgressFamilies(t *testing.T) {
	t.Parallel()
	dualEgress := Server{EgressIPv4: true, EgressIPv6: true}
	ipv4Egress := Server{EgressIPv4: true}
	ipv6Egress := Server{EgressIPv6: true}
	cgnatDualEgress := Server{
		Address: "2001:db8:100:dfd::", IPv6Address: "2001:db8:100:dfd::",
		EgressIPv4: true, EgressIPv6: true,
	}
	dualTarget := Server{
		Address: "203.0.113.20", IPv4Address: "203.0.113.20", IPv6Address: "2001:db8::20",
	}
	ipv6PublicIngressTarget := Server{
		Address: "100.88.13.252", IPv6Address: "2001:db8::30",
	}
	ipv4PublicIngressTarget := Server{
		Address: "203.0.113.31", IPv4Address: "203.0.113.31",
	}

	if got := SelectServerTargetAddress(dualEgress, ipv6PublicIngressTarget); got != "2001:db8::30" {
		t.Fatalf("dual-stack egress selected %q for IPv6-only public target", got)
	}
	if got := SelectServerTargetAddress(cgnatDualEgress, ipv4PublicIngressTarget); got != "203.0.113.31" {
		t.Fatalf("CGNAT server lost IPv4 egress and selected %q for IPv4 target", got)
	}
	if got := SelectServerTargetAddress(ipv4Egress, dualTarget); got != "203.0.113.20" {
		t.Fatalf("IPv4 egress selected %q for dual-stack target", got)
	}
	if got := SelectServerTargetAddress(ipv6Egress, dualTarget); got != "2001:db8::20" {
		t.Fatalf("IPv6 egress selected %q for dual-stack target", got)
	}
	if got := SelectServerTargetAddress(dualEgress, Server{
		Address: "2001:db8::21", IPv4Address: "203.0.113.21", IPv6Address: "2001:db8::21",
	}); got != "2001:db8::21" {
		t.Fatalf("configured IPv6 preference selected %q", got)
	}
	if got := SelectServerTargetAddress(dualEgress, Server{Address: "relay.example.com", IPv6Address: "2001:db8::40"}); got != "relay.example.com" {
		t.Fatalf("explicit hostname override selected %q", got)
	}
}
