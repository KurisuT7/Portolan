package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"

	"github.com/KurisuT7/Portolan/internal/model"
	"github.com/KurisuT7/Portolan/internal/vault"
)

func natStore(t *testing.T) *Store {
	t.Helper()
	secretVault, err := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{41}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	database, err := Open(filepath.Join(t.TempDir(), "nat.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func ssNode(serverID, name string, port uint16) model.Node {
	return model.Node{ServerID: serverID, Name: name, Protocol: model.ProtocolShadowsocks, ListenPort: port, Enabled: true,
		SS: &model.SSSpec{Method: "2022-blake3-aes-128-gcm", Password: "MDEyMzQ1Njc4OWFiY2RlZg=="}}
}

func setNATPorts(t *testing.T, s *Store, server model.Server, mappings ...model.PortMapping) (model.Server, error) {
	t.Helper()
	return s.UpdateServer(context.Background(), server.ID, server.Name, server.Address, server.Region, 0, &mappings)
}

func TestNATServerAllocatesAndChecksMappedPorts(t *testing.T) {
	t.Parallel()
	s := natStore(t)
	ctx := context.Background()
	server, _, err := s.CreateServer(ctx, model.Server{Name: "nat-hk", Address: "203.0.113.40"})
	if err != nil {
		t.Fatal(err)
	}
	server, err = setNATPorts(t, s, server, model.PortMapping{Public: 30010, Host: 20010, Count: 1}, model.PortMapping{Public: 30001, Host: 20001, Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(server.NATPorts) != 2 || server.NATPorts[0].Host != 20001 {
		t.Fatalf("mappings must be stored in host port order: %#v", server.NATPorts)
	}
	first, err := s.CreateNode(ctx, ssNode(server.ID, "first", 0), "ss")
	if err != nil || first.ListenPort != 20001 {
		t.Fatalf("first node must get the first mapped host port: %#v err=%v", first, err)
	}
	forward, err := s.CreateForward(ctx, model.Forward{IngressServerID: server.ID, Name: "relay", Networks: []string{"tcp"},
		TargetHost: "198.51.100.9", TargetPort: 443, Engine: model.ForwardRealm, Enabled: true})
	if err != nil || forward.ListenPort != 20002 {
		t.Fatalf("forward must get the next mapped host port: %#v err=%v", forward, err)
	}
	if _, err := s.CreateNode(ctx, ssNode(server.ID, "outside", 45678), "ss"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a port outside the mappings must be rejected: %v", err)
	}
	if _, err := s.CreateForward(ctx, model.Forward{IngressServerID: server.ID, Name: "outside", ListenPort: 45678, Networks: []string{"tcp"},
		TargetHost: "198.51.100.9", TargetPort: 443, Engine: model.ForwardRealm, Enabled: true}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a forward ingress outside the mappings must be rejected: %v", err)
	}
	if _, err := s.CreateNode(ctx, ssNode(server.ID, "last", 0), "ss"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNode(ctx, ssNode(server.ID, "full", 0), "ss"); !errors.Is(err, ErrConflict) {
		t.Fatalf("allocation must fail once every mapped port is used: %v", err)
	}

	// Mappings that would strand a managed listener are refused; discovered
	// services do not block them.
	if _, _, err := s.UpsertDiscoveredNodes(ctx, server.ID, []model.Node{{Name: "existing", Protocol: model.ProtocolShadowsocks,
		ListenPort: 8388, Source: "/etc/sing-box/config.json",
		SS: &model.SSSpec{Method: "2022-blake3-aes-128-gcm", Password: "MDEyMzQ1Njc4OWFiY2RlZg=="}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := setNATPorts(t, s, server, model.PortMapping{Public: 30001, Host: 20001, Count: 2}); !errors.Is(err, ErrConflict) {
		t.Fatalf("dropping the mapping of a used port must be refused: %v", err)
	}
	if _, err := setNATPorts(t, s, server, model.PortMapping{Public: 40001, Host: 20001, Count: 10}); err != nil {
		t.Fatalf("widened mappings rejected: %v", err)
	}
	cleared, err := setNATPorts(t, s, server)
	if err != nil || cleared.NAT() {
		t.Fatalf("an empty list must remove the mappings: %#v err=%v", cleared, err)
	}
}

func TestForwardsDialThePublicPortOfNATTargets(t *testing.T) {
	t.Parallel()
	s := natStore(t)
	ctx := context.Background()
	ingress, _, err := s.CreateServer(ctx, model.Server{Name: "edge", Address: "198.51.100.5", EgressIPv4: true, EgressIPv6: true})
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := s.CreateServer(ctx, model.Server{Name: "nat-sg", Address: "203.0.113.50", IPv6Address: "2001:db8::50"})
	if err != nil {
		t.Fatal(err)
	}
	target, err = setNATPorts(t, s, target, model.PortMapping{Public: 30001, Host: 10001, Count: 20})
	if err != nil {
		t.Fatal(err)
	}
	node, err := s.CreateNode(ctx, ssNode(target.ID, "landing", 10005), "ss")
	if err != nil {
		t.Fatal(err)
	}
	toNode, err := s.CreateForward(ctx, model.Forward{IngressServerID: ingress.ID, Name: "to node", Networks: []string{"tcp"},
		TargetNodeID: node.ID, Engine: model.ForwardRealm, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if toNode.TargetHost != "203.0.113.50" || toNode.TargetPort != 30005 || toNode.TargetServerPort != 10005 {
		t.Fatalf("node target must dial the mapped public port: %#v", toNode)
	}
	toPort, err := s.CreateForward(ctx, model.Forward{IngressServerID: ingress.ID, Name: "to port", Networks: []string{"tcp"},
		TargetServerID: target.ID, TargetServerPort: 10010, Engine: model.ForwardRealm, Enabled: true})
	if err != nil || toPort.TargetPort != 30010 || toPort.TargetServerPort != 10010 {
		t.Fatalf("server target must dial the mapped public port: %#v err=%v", toPort, err)
	}
	if _, err := s.CreateForward(ctx, model.Forward{IngressServerID: ingress.ID, Name: "unmapped", Networks: []string{"tcp"},
		TargetServerID: target.ID, TargetServerPort: 443, Engine: model.ForwardRealm, Enabled: true}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an unmapped target port must be rejected: %v", err)
	}

	// Editing a forward keeps the host port the operator chose.
	toPort.Name = "to port renamed"
	updated, err := s.UpdateForward(ctx, toPort.ID, toPort)
	if err != nil || updated.TargetPort != 30010 || updated.TargetServerPort != 10010 {
		t.Fatalf("an edited forward must keep its host port: %#v err=%v", updated, err)
	}

	// New mappings move the dialed ports and resend the ingress configuration.
	if err := drainJobs(ctx, s, ingress.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := setNATPorts(t, s, target, model.PortMapping{Public: 50001, Host: 10001, Count: 20}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{toNode.ID, toPort.ID} {
		forward, err := s.GetForward(ctx, id)
		if err != nil || forward.TargetPort != 50000+forward.TargetServerPort-10000 {
			t.Fatalf("forward %s did not follow the new mapping: %#v err=%v", id, forward, err)
		}
	}
	if job, err := s.NextJob(ctx, ingress.ID); err != nil || job == nil || job.Type != "sync" {
		t.Fatalf("the ingress must receive its updated configuration: %#v err=%v", job, err)
	}
	if _, err := setNATPorts(t, s, target, model.PortMapping{Public: 50005, Host: 10005, Count: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mappings that strand a forward target must be refused: %v", err)
	}

	// Over IPv6 the ingress reaches the host port directly.
	if _, err := s.UpdateServer(ctx, target.ID, target.Name, "2001:db8::50", target.Region, 0, nil); err != nil {
		t.Fatal(err)
	}
	forward, err := s.GetForward(ctx, toNode.ID)
	if err != nil || forward.TargetHost != "2001:db8::50" || forward.TargetPort != 10005 {
		t.Fatalf("an IPv6 target must dial the host port: %#v err=%v", forward, err)
	}
}

func drainJobs(ctx context.Context, s *Store, serverID string) error {
	for {
		job, err := s.NextJob(ctx, serverID)
		if err != nil || job == nil {
			return err
		}
		if err := s.CompleteJob(ctx, serverID, job.ID, true, "applied"); err != nil {
			return err
		}
	}
}
