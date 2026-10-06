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

func (s *Store) ListJobs(ctx context.Context, limit int) ([]JobSummary, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,server_id,type,state,result,created_at,COALESCE(started_at,''),COALESCE(finished_at,'')
	  FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
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

// LatestSyncs is independent of the bounded activity log. Every server gets its
// latest configuration result even when newer probe jobs fill that log, and
// how that snapshot compares with the release the Agent reports as active.
func (s *Store) LatestSyncs(ctx context.Context) ([]JobSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT j.id,j.server_id,j.type,j.state,j.result,j.created_at,
		COALESCE(j.started_at,''),COALESCE(j.finished_at,''),j.revision,r.applied_revision FROM jobs j
		LEFT JOIN agent_runtime r ON r.server_id=j.server_id
		WHERE j.type='sync' AND j.id=(SELECT id FROM jobs
		WHERE server_id=j.server_id AND type='sync' ORDER BY created_at DESC,id DESC LIMIT 1)
		ORDER BY j.created_at DESC,j.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []JobSummary{}
	for rows.Next() {
		var item JobSummary
		var created, started, finished string
		var applied sql.NullInt64
		if err := rows.Scan(&item.ID, &item.ServerID, &item.Type, &item.State, &item.Result, &created, &started, &finished,
			&item.Revision, &applied); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		item.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		item.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
		switch {
		case !applied.Valid || item.Revision == 0:
		case applied.Int64 == item.Revision:
			item.Applied = "current"
		case applied.Int64 < item.Revision:
			item.Applied = "behind"
		default:
			item.Applied = "ahead"
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) EnqueueSync(ctx context.Context, serverID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return s.commitSync(ctx, tx, serverID)
}

func (s *Store) enqueueSyncTx(ctx context.Context, tx *sql.Tx, serverID string) error {
	payload, err := s.desiredStateFrom(ctx, tx, serverID)
	if err != nil {
		return err
	}
	// Runtime convergence compares revisions, so they must strictly increase
	// per server even when the clock is coarse or steps back.
	var last int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM jobs WHERE server_id=? AND type='sync'`, serverID).Scan(&last); err != nil {
		return err
	}
	payload.Revision = max(payload.Revision, last+1)
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	jobID, err := randomID("job")
	if err != nil {
		return err
	}
	sealed, err := s.vault.Seal(data, "job:"+jobID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE server_id=? AND type='sync' AND state='pending'`, serverID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,server_id,type,sealed_payload,revision,state,created_at) VALUES(?,?,?,?,?,?,?)`,
		jobID, serverID, "sync", sealed, payload.Revision, "pending", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return nil
}

func (s *Store) EnqueueForwardProbe(ctx context.Context, forwardID string) (JobSummary, error) {
	forward, err := s.GetForward(ctx, forwardID)
	if err != nil {
		return JobSummary{}, err
	}
	if !forward.Enabled {
		return JobSummary{}, fmt.Errorf("%w: 已停用的规则不执行探测", ErrInvalidInput)
	}
	var serverID, status, lastSeen string
	err = s.db.QueryRowContext(ctx, `SELECT f.ingress_server_id,s.agent_status,COALESCE(s.last_seen_at,'')
	  FROM forwards f JOIN servers s ON s.id=f.ingress_server_id WHERE f.id=?`, forwardID).
		Scan(&serverID, &status, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return JobSummary{}, ErrNotFound
	}
	if err != nil {
		return JobSummary{}, err
	}
	seenAt, seenErr := time.Parse(time.RFC3339Nano, lastSeen)
	if status != "online" || seenErr != nil || time.Since(seenAt) > 2*time.Minute {
		return JobSummary{}, ErrAgentOffline
	}
	payload, err := json.Marshal(agentproto.ProbeJobPayload{ForwardID: forwardID})
	if err != nil {
		return JobSummary{}, err
	}
	jobID, err := randomID("job")
	if err != nil {
		return JobSummary{}, err
	}
	sealed, err := s.vault.Seal(payload, "job:"+jobID)
	if err != nil {
		return JobSummary{}, err
	}
	createdAt := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO jobs(id,server_id,type,sealed_payload,state,created_at) VALUES(?,?,?,?,?,?)`,
		jobID, serverID, "probe", sealed, "pending", createdAt.Format(time.RFC3339Nano)); err != nil {
		return JobSummary{}, err
	}
	s.notifyJobs(serverID)
	return JobSummary{ID: jobID, ServerID: serverID, Type: "probe", State: "pending", CreatedAt: createdAt}, nil
}

func (s *Store) GetJobSummary(ctx context.Context, jobID string) (JobSummary, error) {
	var item JobSummary
	var created, started, finished string
	err := s.db.QueryRowContext(ctx, `SELECT id,server_id,type,state,result,created_at,COALESCE(started_at,''),COALESCE(finished_at,'')
	  FROM jobs WHERE id=?`, jobID).Scan(&item.ID, &item.ServerID, &item.Type, &item.State, &item.Result, &created, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return JobSummary{}, ErrNotFound
	}
	if err != nil {
		return JobSummary{}, err
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	item.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	item.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
	return item, nil
}

func (s *Store) NextJob(ctx context.Context, serverID string) (*agentproto.Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var job agentproto.Job
	var sealed, created string
	err = tx.QueryRowContext(ctx, `SELECT id,type,sealed_payload,created_at FROM jobs WHERE server_id=? AND state='pending' ORDER BY created_at LIMIT 1`, serverID).
		Scan(&job.ID, &job.Type, &sealed, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET state='running',started_at=? WHERE id=? AND state='pending'`, time.Now().UTC().Format(time.RFC3339Nano), job.ID)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, nil
	}
	plaintext, err := s.vault.Open(sealed, "job:"+job.ID)
	if err != nil {
		return nil, err
	}
	job.ServerID = serverID
	job.Payload = plaintext
	job.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (s *Store) CompleteJob(ctx context.Context, serverID, jobID string, success bool, result string) error {
	state := "failed"
	if success {
		state = "succeeded"
	}
	if len(result) > 4096 {
		result = result[:4096]
	}
	changed, err := s.db.ExecContext(ctx, `UPDATE jobs SET state=?,result=?,finished_at=? WHERE id=? AND server_id=? AND state='running'`,
		state, result, time.Now().UTC().Format(time.RFC3339Nano), jobID, serverID)
	if err != nil {
		return err
	}
	count, _ := changed.RowsAffected()
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) desiredState(ctx context.Context, serverID string) (agentproto.SyncPayload, error) {
	return s.desiredStateFrom(ctx, s.db, serverID)
}

func (s *Store) desiredStateFrom(ctx context.Context, db queryer, serverID string) (agentproto.SyncPayload, error) {
	payload := agentproto.SyncPayload{Revision: time.Now().UTC().UnixNano(), Nodes: []model.Node{}, Forwards: []model.Forward{}}
	rows, err := db.QueryContext(ctx, `SELECT id,sealed_spec FROM nodes WHERE server_id=? AND managed=1 ORDER BY id`, serverID)
	if err != nil {
		return payload, err
	}
	for rows.Next() {
		var id, sealed string
		if err := rows.Scan(&id, &sealed); err != nil {
			rows.Close()
			return payload, err
		}
		node, err := s.openNode(id, sealed)
		if err != nil {
			rows.Close()
			return payload, err
		}
		payload.Nodes = append(payload.Nodes, node)
	}
	if err := rows.Close(); err != nil {
		return payload, err
	}
	forwardRows, err := db.QueryContext(ctx, `SELECT spec_json FROM forwards WHERE ingress_server_id=? ORDER BY id`, serverID)
	if err != nil {
		return payload, err
	}
	defer forwardRows.Close()
	for forwardRows.Next() {
		var data []byte
		if err := forwardRows.Scan(&data); err != nil {
			return payload, err
		}
		var forward model.Forward
		if err := json.Unmarshal(data, &forward); err != nil {
			return payload, err
		}
		payload.Forwards = append(payload.Forwards, forward)
	}
	return payload, forwardRows.Err()
}
