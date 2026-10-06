package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/KurisuT7/Portolan/internal/agentproto"
	"github.com/KurisuT7/Portolan/internal/model"
	"github.com/KurisuT7/Portolan/internal/vault"
)

func forwardTestStore(t *testing.T) (*Store, model.Server, model.Server) {
	t.Helper()
	v, err := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(t.TempDir(), "state.db"), v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a, _, err := s.CreateServer(context.Background(), model.Server{Name: "Original entry", Address: "203.0.113.1"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.CreateServer(context.Background(), model.Server{Name: "Original target", Address: "2001:db8::2"})
	if err != nil {
		t.Fatal(err)
	}
	return s, a, b
}

func TestUpdateForwardRetainsIdentityAndSchedulesBothIngresses(t *testing.T) {
	s, a, b := forwardTestStore(t)
	ctx := context.Background()
	original, err := s.CreateForward(ctx, model.Forward{Name: "Rule", IngressServerID: a.ID, ListenPort: 23001,
		TargetHost: "example.com", TargetPort: 443, Networks: []string{"tcp"}, Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		job, err := s.NextJob(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if job != nil {
			if err := s.CompleteJob(ctx, id, job.ID, true, "{}"); err != nil {
				t.Fatal(err)
			}
		}
	}
	wakeA, cancelA := s.WatchJobs(a.ID)
	defer cancelA()
	wakeB, cancelB := s.WatchJobs(b.ID)
	defer cancelB()
	updated := original
	updated.ID = "must-not-be-used"
	updated.CreatedAt = time.Time{}
	updated.IngressServerID = b.ID
	updated.TargetServerID = a.ID
	updated.TargetHost = "ignored.invalid"
	updated.TargetPort = 8443
	updated.ListenPort = 23002
	updated.Name = "Edited rule"
	updated.Networks = []string{"tcp", "udp"}
	updated.Engine = model.ForwardRealm
	got, err := s.UpdateForward(ctx, original.ID, updated)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != original.ID || !got.CreatedAt.Equal(original.CreatedAt) || got.TargetHost != a.Address || !got.UpdatedAt.After(original.UpdatedAt) {
		t.Fatalf("identity or resolution changed incorrectly: %#v", got)
	}
	for _, wake := range []<-chan struct{}{wakeA, wakeB} {
		select {
		case <-wake:
		default:
			t.Fatal("committed ingress was not notified")
		}
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
		if id == a.ID && len(payload.Forwards) != 0 {
			t.Fatal("old ingress retained rule")
		}
		if id == b.ID && (len(payload.Forwards) != 1 || payload.Forwards[0].ID != original.ID) {
			t.Fatal("new ingress missing rule")
		}
	}
	storedA, _ := s.GetServer(ctx, a.ID)
	storedB, _ := s.GetServer(ctx, b.ID)
	if storedA.Name != a.Name || storedB.Name != b.Name {
		t.Fatal("server names changed")
	}
}

func TestUpdateForwardConflictAndQueueFailureAreAtomic(t *testing.T) {
	s, a, b := forwardTestStore(t)
	ctx := context.Background()
	original, err := s.CreateForward(ctx, model.Forward{Name: "Rule", IngressServerID: a.ID, ListenPort: 24001,
		TargetHost: "example.com", TargetPort: 443, Networks: []string{"tcp"}, Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateNode(ctx, model.Node{Name: "Unchanged node", ServerID: b.ID, ListenPort: 24002,
		Protocol: model.ProtocolShadowsocks, Enabled: true, SS: &model.SSSpec{Method: "aes-256-gcm", Password: "test-password"}}, "AEAD")
	if err != nil {
		t.Fatal(err)
	}
	candidate := original
	candidate.IngressServerID, candidate.ListenPort = b.ID, 24002
	if _, err := s.UpdateForward(ctx, original.ID, candidate); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
	candidate.ListenPort = 24003
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER reject_new_ingress BEFORE INSERT ON jobs WHEN NEW.server_id='`+b.ID+`' BEGIN SELECT RAISE(ABORT, 'queue unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	beforeJobs, _ := s.ListJobs(ctx, 200)
	if _, err := s.UpdateForward(ctx, original.ID, candidate); err == nil {
		t.Fatal("queue failure hidden")
	}
	stored, err := s.GetForward(ctx, original.ID)
	if err != nil || stored.IngressServerID != original.IngressServerID || stored.ListenPort != original.ListenPort {
		t.Fatalf("mutation leaked: %#v %v", stored, err)
	}
	afterJobs, _ := s.ListJobs(ctx, 200)
	if len(beforeJobs) != len(afterJobs) {
		t.Fatal("partial jobs committed")
	}
}

func TestUpdateForwardNoOpAndDisabledRule(t *testing.T) {
	s, a, _ := forwardTestStore(t)
	ctx := context.Background()
	original, err := s.CreateForward(ctx, model.Forward{Name: "Rule", IngressServerID: a.ID, ListenPort: 24001,
		TargetHost: "example.com", TargetPort: 443, Networks: []string{"tcp"}, Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.LatestSyncs(ctx)
	unchanged, err := s.UpdateForward(ctx, original.ID, original)
	if err != nil || !unchanged.UpdatedAt.Equal(original.UpdatedAt) {
		t.Fatalf("no-op changed: %v", err)
	}
	after, _ := s.LatestSyncs(ctx)
	if len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatal("no-op queued a sync")
	}
	original.Enabled = false
	if _, err := s.UpdateForward(ctx, original.ID, original); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueForwardProbe(ctx, original.ID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("disabled rule probed: %v", err)
	}
}

func TestLatestSyncsIncludesOldServerOutsideActivityWindow(t *testing.T) {
	s, a, b := forwardTestStore(t)
	ctx := context.Background()
	if err := s.EnqueueSync(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueSync(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		id, err := randomID("probe")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO jobs(id,server_id,type,sealed_payload,state,created_at) VALUES(?,?,?,?,?,?)`, id, b.ID, "probe", "unused", "succeeded", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.LatestSyncs(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("latest syncs lost to log limit: %#v %v", items, err)
	}
}

func TestLatestProbeUsesMeasurementTimeNotArrivalOrder(t *testing.T) {
	s, a, _ := forwardTestStore(t)
	ctx := context.Background()
	f, err := s.CreateForward(ctx, model.Forward{Name: "Rule", IngressServerID: a.ID, ListenPort: 24001,
		TargetHost: "example.com", TargetPort: 443, Networks: []string{"tcp"}, Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, at := range []time.Time{now, now.Add(-time.Minute)} {
		if err := s.SaveForwardProbes(ctx, a.ID, []model.ForwardProbe{{ForwardID: f.ID, CheckedAt: at, Attempts: 3, Successes: 3, LatencyMS: 10, Status: "stable"}}); err != nil {
			t.Fatal(err)
		}
	}
	probes, err := s.LatestForwardProbes(ctx)
	if err != nil || len(probes) != 1 || !probes[0].CheckedAt.Equal(now) {
		t.Fatalf("older sample replaced latest: %#v %v", probes, err)
	}
}
