package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/KurisuT7/Portolan/internal/agentproto"
	"github.com/KurisuT7/Portolan/internal/model"
)

func TestUpdateNodeMovesForwardsWithItsPort(t *testing.T) {
	s, a, b := forwardTestStore(t)
	ctx := context.Background()
	node, err := s.CreateNode(ctx, model.Node{Name: "Target", ServerID: a.ID, ListenPort: 24100, Protocol: model.ProtocolShadowsocks,
		Enabled: true, SS: &model.SSSpec{Method: "aes-256-gcm", Password: "test-password"}}, "aes-256-gcm")
	if err != nil {
		t.Fatal(err)
	}
	forward, err := s.CreateForward(ctx, model.Forward{Name: "To node", IngressServerID: b.ID, ListenPort: 24101, TargetNodeID: node.ID,
		Networks: []string{"tcp"}, Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		job, err := s.NextJob(ctx, id)
		if err != nil || job == nil {
			t.Fatalf("missing initial sync for %s: %v", id, err)
		}
		if err := s.CompleteJob(ctx, id, job.ID, true, "{}"); err != nil {
			t.Fatal(err)
		}
	}
	wakeA, cancelA := s.WatchJobs(a.ID)
	defer cancelA()
	wakeB, cancelB := s.WatchJobs(b.ID)
	defer cancelB()

	edited := node
	edited.Name, edited.ListenPort = "Renamed", 24102
	edited.ServerID, edited.CreatedAt, edited.Enabled = b.ID, time.Time{}, false
	edited.SS = &model.SSSpec{Method: "aes-128-gcm", Password: "new-password"}
	got, updatedAt, err := s.UpdateNode(ctx, edited, "aes-128-gcm")
	if err != nil {
		t.Fatal(err)
	}
	if updatedAt.IsZero() || got.ServerID != a.ID || !got.Enabled || !got.CreatedAt.Equal(node.CreatedAt) || got.ListenPort != 24102 {
		t.Fatalf("identity changed or update not reported: %#v %v", got, updatedAt)
	}
	for _, wake := range []<-chan struct{}{wakeA, wakeB} {
		select {
		case <-wake:
		default:
			t.Fatal("affected server was not notified")
		}
	}
	retargeted, err := s.GetForward(ctx, forward.ID)
	if err != nil || retargeted.TargetPort != 24102 || retargeted.TargetServerPort != 24102 {
		t.Fatalf("forward did not follow the node port: %#v %v", retargeted, err)
	}
	for _, id := range []string{a.ID, b.ID} {
		job, err := s.NextJob(ctx, id)
		if err != nil || job == nil {
			t.Fatalf("missing sync for %s: %v", id, err)
		}
		var payload agentproto.SyncPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if id == a.ID && (len(payload.Nodes) != 1 || payload.Nodes[0].ListenPort != 24102 || payload.Nodes[0].SS.Method != "aes-128-gcm") {
			t.Fatalf("node server got stale settings: %#v", payload.Nodes)
		}
		if id == b.ID && (len(payload.Forwards) != 1 || payload.Forwards[0].TargetPort != 24102) {
			t.Fatalf("ingress got a stale forward: %#v", payload.Forwards)
		}
		if err := s.CompleteJob(ctx, id, job.ID, true, "{}"); err != nil {
			t.Fatal(err)
		}
	}
	nodes, err := s.ListNodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].Name != "Renamed" || nodes[0].ListenPort != 24102 || nodes[0].Profile != "aes-128-gcm" {
		t.Fatalf("summary not updated: %#v %v", nodes, err)
	}

	before, _ := s.LatestSyncs(ctx)
	unchanged, updatedAt, err := s.UpdateNode(ctx, got, "aes-128-gcm")
	if err != nil || !updatedAt.IsZero() || unchanged.ListenPort != 24102 {
		t.Fatalf("no-op reported a change: %v %v", updatedAt, err)
	}
	after, _ := s.LatestSyncs(ctx)
	if len(after) != len(before) {
		t.Fatal("no-op queued a sync")
	}
	for index := range after {
		if after[index].ID != before[index].ID {
			t.Fatal("no-op queued a sync")
		}
	}
}

func TestUpdateNodeRejectsTakenPortsAndDiscoveredNodes(t *testing.T) {
	s, a, _ := forwardTestStore(t)
	ctx := context.Background()
	spec := func() *model.SSSpec { return &model.SSSpec{Method: "aes-256-gcm", Password: "test-password"} }
	node, err := s.CreateNode(ctx, model.Node{Name: "Edited", ServerID: a.ID, ListenPort: 24200, Protocol: model.ProtocolShadowsocks,
		Enabled: true, SS: spec()}, "aes-256-gcm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNode(ctx, model.Node{Name: "Neighbour", ServerID: a.ID, ListenPort: 24201, Protocol: model.ProtocolShadowsocks,
		Enabled: true, SS: spec()}, "aes-256-gcm"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateForward(ctx, model.Forward{Name: "Rule", IngressServerID: a.ID, ListenPort: 24202, TargetHost: "example.com",
		TargetPort: 443, Networks: []string{"tcp"}, Engine: model.ForwardSingBox, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, port := range []uint16{24201, 24202} {
		candidate := node
		candidate.ListenPort = port
		if _, _, err := s.UpdateNode(ctx, candidate, "aes-256-gcm"); !errors.Is(err, ErrConflict) {
			t.Fatalf("port %d: expected conflict, got %v", port, err)
		}
	}
	stored, err := s.GetNode(ctx, node.ID)
	if err != nil || stored.ListenPort != 24200 {
		t.Fatalf("rejected update leaked: %#v %v", stored, err)
	}

	if _, _, err := s.UpsertDiscoveredNodes(ctx, a.ID, []model.Node{{Name: "External", ListenPort: 24210, Protocol: model.ProtocolShadowsocks,
		Source: "/etc/sing-box/config.json", SS: spec()}}); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range nodes {
		if item.Managed {
			continue
		}
		external, err := s.GetNode(ctx, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		external.Name = "Taken over"
		if _, _, err := s.UpdateNode(ctx, external, "aes-256-gcm"); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("discovered node was edited: %v", err)
		}
		return
	}
	t.Fatal("discovered node missing")
}
