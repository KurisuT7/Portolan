package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/KurisuT7/portolan/internal/agentproto"
	"github.com/KurisuT7/portolan/internal/model"
)

func (s *Store) SetCoreTarget(ctx context.Context, target model.CoreTarget) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO core_targets(core,version,sha256_amd64,sha256_arm64,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(core) DO UPDATE SET version=excluded.version,sha256_amd64=excluded.sha256_amd64,
		sha256_arm64=excluded.sha256_arm64,updated_at=excluded.updated_at`,
		string(target.Core), target.Version, target.SHA256["amd64"], target.SHA256["arm64"], time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) CoreTargets(ctx context.Context) (map[model.Core]model.CoreTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT core,version,sha256_amd64,sha256_arm64,updated_at FROM core_targets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := map[model.Core]model.CoreTarget{}
	for rows.Next() {
		var target model.CoreTarget
		var amd64, arm64, updated string
		if err := rows.Scan(&target.Core, &target.Version, &amd64, &arm64, &updated); err != nil {
			return nil, err
		}
		target.SHA256 = map[string]string{"amd64": amd64, "arm64": arm64}
		target.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		targets[target.Core] = target
	}
	return targets, rows.Err()
}

// EnqueueCoreUpdate asks a server's Agent to install the target release of a
// core. A newer request replaces an update the Agent has not picked up yet.
func (s *Store) EnqueueCoreUpdate(ctx context.Context, serverID string, core model.Core) (JobSummary, error) {
	targets, err := s.CoreTargets(ctx)
	if err != nil {
		return JobSummary{}, err
	}
	target, ok := targets[core]
	if !ok {
		return JobSummary{}, fmt.Errorf("%w: 尚未选择 %s 的目标版本", ErrConflict, core)
	}
	data, err := json.Marshal(agentproto.CoreUpdatePayload{Version: target.Version, SHA256: target.SHA256})
	if err != nil {
		return JobSummary{}, err
	}
	jobID, err := randomID("job")
	if err != nil {
		return JobSummary{}, err
	}
	sealed, err := s.vault.Seal(data, "job:"+jobID)
	if err != nil {
		return JobSummary{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return JobSummary{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM servers WHERE id=?`, serverID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return JobSummary{}, ErrNotFound
		}
		return JobSummary{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE server_id=? AND type=? AND state='pending'`, serverID, core.JobType()); err != nil {
		return JobSummary{}, err
	}
	createdAt := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,server_id,type,sealed_payload,state,created_at) VALUES(?,?,?,?,?,?)`,
		jobID, serverID, core.JobType(), sealed, "pending", createdAt.Format(time.RFC3339Nano)); err != nil {
		return JobSummary{}, err
	}
	if err := s.commitAndNotify(tx, []string{serverID}); err != nil {
		return JobSummary{}, err
	}
	return JobSummary{ID: jobID, ServerID: serverID, Type: core.JobType(), State: "pending", CreatedAt: createdAt}, nil
}

// LatestCoreUpdates returns each server's most recent update job per core.
func (s *Store) LatestCoreUpdates(ctx context.Context) ([]JobSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT j.id,j.server_id,j.type,j.state,j.result,j.created_at,
		COALESCE(j.started_at,''),COALESCE(j.finished_at,'') FROM jobs j
		WHERE j.type IN (?,?) AND j.id=(SELECT id FROM jobs
		WHERE server_id=j.server_id AND type=j.type ORDER BY created_at DESC,id DESC LIMIT 1)
		ORDER BY j.created_at DESC,j.id DESC`, model.CoreSingBox.JobType(), model.CoreRealm.JobType())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []JobSummary{}
	for rows.Next() {
		var item JobSummary
		var created, started, finished string
		if err := rows.Scan(&item.ID, &item.ServerID, &item.Type, &item.State, &item.Result, &created, &started, &finished); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		item.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		item.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
		items = append(items, item)
	}
	return items, rows.Err()
}
