package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/KurisuT7/Portolan/internal/model"
	"github.com/KurisuT7/Portolan/internal/vault"
)

func TestQueueFailureRollsBackResourceMutation(t *testing.T) {
	ctx := context.Background()
	v, err := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server, token, err := s.CreateServer(ctx, model.Server{Name: "server", Address: "203.0.113.1"})
	if err != nil {
		t.Fatal(err)
	}
	node := model.Node{ID: "node", ServerID: server.ID, Name: "node", Protocol: model.ProtocolShadowsocks,
		ListenPort: 20001, Enabled: true, SS: &model.SSSpec{Method: "aes-256-gcm", Password: "example-password"}}
	forward := model.Forward{ID: "forward", IngressServerID: server.ID, Name: "forward", ListenPort: 20002,
		Networks: []string{"tcp"}, TargetHost: server.Address, TargetServerID: server.ID, TargetPort: 443, Engine: model.ForwardRealm, Enabled: true}
	if _, err := s.CreateNode(ctx, node, "AEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateForward(ctx, forward); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER reject_jobs BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'test queue failure'); END`); err != nil {
		t.Fatal(err)
	}
	changed, unwatch := s.WatchJobs(server.ID)
	defer unwatch()
	for _, run := range []func() error{
		func() error { _, err := s.UpdateServer(ctx, server.ID, "changed", "198.51.100.1", "", 0); return err },
		func() error { _, _, err := s.Enroll(ctx, token, "198.51.100.1"); return err },
		func() error { return s.DeleteNode(ctx, node.ID) },
		func() error { return s.DeleteForward(ctx, forward.ID) },
		func() error {
			node.ID = "new-node"
			node.ListenPort = 20003
			_, err := s.CreateNode(ctx, node, "AEAD")
			return err
		},
		func() error {
			forward.ID = "new-forward"
			forward.ListenPort = 20004
			_, err := s.CreateForward(ctx, forward)
			return err
		},
	} {
		if err := run(); err == nil {
			t.Fatal("queue failure hidden")
		}
	}
	unchanged, err := s.GetServer(ctx, server.ID)
	if err != nil || unchanged.Address != server.Address || unchanged.Name != server.Name {
		t.Fatalf("server mutation leaked: %#v, %v", unchanged, err)
	}
	if valid, err := s.EnrollmentTokenValid(ctx, token); err != nil || !valid {
		t.Fatalf("failed enrollment consumed token: %v, %v", valid, err)
	}
	for _, table := range []string{"nodes", "forwards"} {
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count = %d, %v", table, count, err)
		}
	}
	select {
	case <-changed:
		t.Fatal("failed transaction notified Agent")
	default:
	}
}

func TestJobNotificationIsScopedAndUnregistered(t *testing.T) {
	s := &Store{}
	a, releaseA := s.WatchJobs("a")
	b, releaseB := s.WatchJobs("b")
	s.notifyJobs("a")
	select {
	case <-a:
	default:
		t.Fatal("waiter missed committed job")
	}
	select {
	case <-b:
		t.Fatal("unrelated server woke")
	default:
	}
	releaseA()
	releaseB()
	if len(s.waiters) != 0 {
		t.Fatalf("wait registrations leaked: %v", s.waiters)
	}
}
