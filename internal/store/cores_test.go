package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/KurisuT7/portolan/internal/agentproto"
	"github.com/KurisuT7/portolan/internal/model"
)

func TestCoreUpdatesUseTheTargetAndReplacePendingRequests(t *testing.T) {
	ctx := context.Background()
	s, server, _ := forwardTestStore(t)
	if _, err := s.EnqueueCoreUpdate(ctx, server.ID, model.CoreSingBox); !errors.Is(err, ErrConflict) {
		t.Fatalf("update without a target: %v", err)
	}
	digests := map[string]string{"amd64": "a", "arm64": "b"}
	for _, version := range []string{"1.14.2", "1.14.3"} {
		if err := s.SetCoreTarget(ctx, model.CoreTarget{Core: model.CoreSingBox, Version: version, SHA256: digests}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EnqueueCoreUpdate(ctx, server.ID, model.CoreSingBox); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := s.CoreTargets(ctx)
	if err != nil || targets[model.CoreSingBox].Version != "1.14.3" || !reflect.DeepEqual(targets[model.CoreSingBox].SHA256, digests) {
		t.Fatalf("targets = %#v err=%v", targets, err)
	}
	job, err := s.NextJob(ctx, server.ID)
	if err != nil || job == nil || job.Type != "update-sing-box" {
		t.Fatalf("job = %#v err=%v", job, err)
	}
	var payload agentproto.CoreUpdatePayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.Version != "1.14.3" || payload.SHA256["arm64"] != "b" {
		t.Fatalf("payload = %#v err=%v", payload, err)
	}
	if next, err := s.NextJob(ctx, server.ID); err != nil || next != nil {
		t.Fatalf("superseded update still queued: %#v err=%v", next, err)
	}
	if err := s.CompleteJob(ctx, server.ID, job.ID, false, `{"message":"x"}`); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestCoreUpdates(ctx)
	if err != nil || len(latest) != 1 || latest[0].ID != job.ID || latest[0].State != "failed" {
		t.Fatalf("latest = %#v err=%v", latest, err)
	}
	if _, err := s.EnqueueCoreUpdate(ctx, "srv_missing", model.CoreSingBox); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing server: %v", err)
	}
}
