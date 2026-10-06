package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/KurisuT7/portolan/internal/model"
)

// lostSyncAfter is how long a sync may stay running without a result before
// a runtime report that shows an older release counts it as lost. An Agent
// never reports its runtime while it is still executing a job.
const lostSyncAfter = 2 * time.Minute

const lostSyncResult = `{"message":"Agent 未回报应用结果，已重新下发当前配置。"}`

// SaveRuntimeStatus records what an Agent observed on its host and converges a
// server whose active release is older than the latest snapshot that was
// confirmed or lost. A failed snapshot is not retried automatically. A release
// newer than the latest snapshot means the panel state is older than the
// server, for example after a database restore, and is never overwritten.
func (s *Store) SaveRuntimeStatus(ctx context.Context, serverID string, status model.RuntimeStatus) error {
	units, err := json.Marshal(status.Units)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runtime(server_id,applied_revision,agent_version,sing_box_version,realm_version,units_json,reported_at)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET applied_revision=excluded.applied_revision,
		agent_version=excluded.agent_version,sing_box_version=excluded.sing_box_version,realm_version=excluded.realm_version,
		units_json=excluded.units_json,reported_at=excluded.reported_at`,
		serverID, status.AppliedRevision, status.AgentVersion, status.SingBoxVersion, status.RealmVersion, string(units), now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	var jobID, state, started string
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT id,state,revision,COALESCE(started_at,'') FROM jobs
		WHERE server_id=? AND type='sync' ORDER BY created_at DESC,id DESC LIMIT 1`, serverID).Scan(&jobID, &state, &revision, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	resend := false
	switch {
	case revision == 0:
	case status.AppliedRevision == revision && state == "running":
		// The release is active; only the completion report was lost.
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET state='succeeded',finished_at=? WHERE id=? AND state='running'`,
			now.Format(time.RFC3339Nano), jobID)
	case status.AppliedRevision >= revision:
	case state == "succeeded":
		resend = true
	case state == "running":
		startedAt, parseErr := time.Parse(time.RFC3339Nano, started)
		if parseErr != nil || now.Sub(startedAt) >= lostSyncAfter {
			resend = true
			_, err = tx.ExecContext(ctx, `UPDATE jobs SET state='failed',result=?,finished_at=? WHERE id=? AND state='running'`,
				lostSyncResult, now.Format(time.RFC3339Nano), jobID)
		}
	}
	if err != nil {
		return err
	}
	if !resend {
		return tx.Commit()
	}
	if err := s.enqueueSyncTx(ctx, tx, serverID); err != nil {
		return err
	}
	return s.commitAndNotify(tx, []string{serverID})
}
