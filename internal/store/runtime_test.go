package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/KurisuT7/portolan/internal/agentproto"
	"github.com/KurisuT7/portolan/internal/model"
)

// deliverSync hands the pending sync to the Agent and returns its revision.
func deliverSync(t *testing.T, s *Store, serverID string) (string, int64) {
	t.Helper()
	job, err := s.NextJob(context.Background(), serverID)
	if err != nil || job == nil || job.Type != "sync" {
		t.Fatalf("expected a pending sync: %#v err=%v", job, err)
	}
	var payload agentproto.SyncPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return job.ID, payload.Revision
}

func latestSync(t *testing.T, s *Store, serverID string) JobSummary {
	t.Helper()
	items, err := s.LatestSyncs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ServerID == serverID {
			return item
		}
	}
	t.Fatalf("no sync for %s", serverID)
	return JobSummary{}
}

func runtimeAt(revision int64) model.RuntimeStatus {
	return model.RuntimeStatus{AppliedRevision: revision, SingBoxVersion: "1.14.2", RealmVersion: "2.9.4",
		Units: []model.UnitStatus{{Name: "portolan-sing-box.service", ActiveState: "active", SubState: "running"}}}
}

func TestRuntimeStatusComparesAndConvergesReleases(t *testing.T) {
	ctx := context.Background()
	s, server, _ := forwardTestStore(t)
	if err := s.EnqueueSync(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	jobID, revision := deliverSync(t, s, server.ID)
	if err := s.CompleteJob(ctx, server.ID, jobID, true, "{}"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRuntimeStatus(ctx, server.ID, runtimeAt(revision)); err != nil {
		t.Fatal(err)
	}
	if got := latestSync(t, s, server.ID); got.ID != jobID || got.Applied != "current" {
		t.Fatalf("confirmed release: %#v", got)
	}
	stored, err := s.GetServer(ctx, server.ID)
	if err != nil || stored.Runtime == nil || stored.Runtime.SingBoxVersion != "1.14.2" || len(stored.Runtime.Units) != 1 || stored.Runtime.ReportedAt.IsZero() {
		t.Fatalf("runtime not exposed: %#v err=%v", stored.Runtime, err)
	}

	// The runtime fell behind a confirmed snapshot, for example after the
	// release directory was replaced on the host.
	if err := s.SaveRuntimeStatus(ctx, server.ID, runtimeAt(revision-1)); err != nil {
		t.Fatal(err)
	}
	resent := latestSync(t, s, server.ID)
	if resent.ID == jobID || resent.State != "pending" || resent.Applied != "behind" {
		t.Fatalf("drift was not resent: %#v", resent)
	}
	if err := s.SaveRuntimeStatus(ctx, server.ID, runtimeAt(revision-1)); err != nil {
		t.Fatal(err)
	}
	if again := latestSync(t, s, server.ID); again.ID != resent.ID {
		t.Fatalf("a pending snapshot was replaced: %#v", again)
	}

	// A runtime newer than every snapshot means the panel state is older.
	_, newest := deliverSync(t, s, server.ID)
	if err := s.SaveRuntimeStatus(ctx, server.ID, runtimeAt(newest+1)); err != nil {
		t.Fatal(err)
	}
	if ahead := latestSync(t, s, server.ID); ahead.ID != resent.ID || ahead.Applied != "ahead" {
		t.Fatalf("newer runtime must not be overwritten: %#v", ahead)
	}
}

func TestSyncRevisionsStrictlyIncrease(t *testing.T) {
	ctx := context.Background()
	s, server, _ := forwardTestStore(t)
	var previous int64
	for range 20 {
		if err := s.EnqueueSync(ctx, server.ID); err != nil {
			t.Fatal(err)
		}
		_, revision := deliverSync(t, s, server.ID)
		if revision <= previous {
			t.Fatalf("revision %d does not follow %d", revision, previous)
		}
		previous = revision
	}
	// A clock that steps back still produces a newer revision.
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET revision=? WHERE server_id=?`, previous+int64(time.Hour), server.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueSync(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if _, revision := deliverSync(t, s, server.ID); revision != previous+int64(time.Hour)+1 {
		t.Fatalf("revision after a clock step = %d", revision)
	}
}

func TestRuntimeStatusResolvesLostAndFailedSyncs(t *testing.T) {
	ctx := context.Background()
	s, server, _ := forwardTestStore(t)
	if err := s.EnqueueSync(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	jobID, revision := deliverSync(t, s, server.ID)

	// Applied, but the completion report never arrived.
	if err := s.SaveRuntimeStatus(ctx, server.ID, runtimeAt(revision)); err != nil {
		t.Fatal(err)
	}
	if got := latestSync(t, s, server.ID); got.ID != jobID || got.State != "succeeded" {
		t.Fatalf("lost completion: %#v", got)
	}

	// Delivered but never applied: a recent sync may still be in flight.
	if err := s.EnqueueSync(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	lostID, _ := deliverSync(t, s, server.ID)
	if err := s.SaveRuntimeStatus(ctx, server.ID, runtimeAt(revision)); err != nil {
		t.Fatal(err)
	}
	if got := latestSync(t, s, server.ID); got.ID != lostID || got.State != "running" {
		t.Fatalf("recent sync was treated as lost: %#v", got)
	}
	stale := time.Now().UTC().Add(-lostSyncAfter - time.Second).Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET started_at=? WHERE id=?`, stale, lostID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRuntimeStatus(ctx, server.ID, runtimeAt(revision)); err != nil {
		t.Fatal(err)
	}
	reissued := latestSync(t, s, server.ID)
	lost, err := s.GetJobSummary(ctx, lostID)
	if err != nil || lost.State != "failed" || lost.Result != lostSyncResult || reissued.ID == lostID || reissued.State != "pending" {
		t.Fatalf("lost sync: lost=%#v reissued=%#v err=%v", lost, reissued, err)
	}

	// A snapshot the Agent rejected is not retried automatically.
	failedID, _ := deliverSync(t, s, server.ID)
	if err := s.CompleteJob(ctx, server.ID, failedID, false, "{}"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRuntimeStatus(ctx, server.ID, runtimeAt(revision)); err != nil {
		t.Fatal(err)
	}
	if got := latestSync(t, s, server.ID); got.ID != failedID || got.State != "failed" || got.Applied != "behind" {
		t.Fatalf("failed sync was retried: %#v", got)
	}
}

func TestDeletingAForwardTargetIsRefused(t *testing.T) {
	ctx := context.Background()
	s, entry, target := forwardTestStore(t)
	node, err := s.CreateNode(ctx, model.Node{ServerID: target.ID, Name: "target", Protocol: model.ProtocolShadowsocks,
		ListenPort: 24000, Enabled: true, SS: &model.SSSpec{Method: "aes-256-gcm", Password: "example-password"}}, "AEAD")
	if err != nil {
		t.Fatal(err)
	}
	forward, err := s.CreateForward(ctx, model.Forward{IngressServerID: entry.ID, Name: "via entry", ListenPort: 24001,
		Networks: []string{"tcp"}, TargetNodeID: node.ID, Engine: model.ForwardRealm, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode(ctx, node.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("referenced node deleted: %v", err)
	}
	if err := s.DeleteServer(ctx, target.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("targeted server deleted: %v", err)
	}
	if err := s.DeleteNode(ctx, "node_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing node: %v", err)
	}

	// The ingress server owns its forwards; deleting it removes them.
	if err := s.DeleteServer(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetForward(ctx, forward.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ingress forward survived: %v", err)
	}
	if err := s.DeleteNode(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteServer(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
}
