package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

func (s *Store) CreateForward(ctx context.Context, forward model.Forward) (model.Forward, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Forward{}, err
	}
	defer tx.Rollback()
	if forward.ID == "" {
		forward.ID, err = randomID("fwd")
		if err != nil {
			return model.Forward{}, err
		}
	}
	forward.CreatedAt = time.Now().UTC()
	forward.UpdatedAt = forward.CreatedAt
	if err := s.prepareForward(ctx, tx, &forward, ""); err != nil {
		return model.Forward{}, err
	}
	data, err := json.Marshal(forward)
	if err != nil {
		return model.Forward{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO forwards(id,ingress_server_id,name,listen_port,engine,spec_json,created_at)
		VALUES(?,?,?,?,?,?,?)`, forward.ID, forward.IngressServerID, forward.Name, forward.ListenPort, string(forward.Engine), data,
		forward.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return model.Forward{}, err
	}
	return forward, s.commitSync(ctx, tx, forward.IngressServerID)
}

// UpdateForward replaces a rule without changing its identity. Moving an ingress
// schedules both removal from the old server and application on the new server
// in the same transaction. No remote service is touched by this method.
func (s *Store) UpdateForward(ctx context.Context, id string, forward model.Forward) (model.Forward, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Forward{}, err
	}
	defer tx.Rollback()
	previous, err := s.getForwardFrom(ctx, tx, id)
	if err != nil {
		return model.Forward{}, err
	}
	forward.ID, forward.CreatedAt, forward.UpdatedAt = previous.ID, previous.CreatedAt, previous.UpdatedAt
	if err := s.prepareForward(ctx, tx, &forward, id); err != nil {
		return model.Forward{}, err
	}
	before, err := json.Marshal(previous)
	if err != nil {
		return model.Forward{}, err
	}
	after, err := json.Marshal(forward)
	if err != nil {
		return model.Forward{}, err
	}
	if bytes.Equal(before, after) {
		return previous, tx.Commit()
	}
	forward.UpdatedAt = time.Now().UTC()
	after, err = json.Marshal(forward)
	if err != nil {
		return model.Forward{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE forwards SET ingress_server_id=?,name=?,listen_port=?,engine=?,spec_json=? WHERE id=?`,
		forward.IngressServerID, forward.Name, forward.ListenPort, string(forward.Engine), after, id); err != nil {
		return model.Forward{}, err
	}
	servers := []string{previous.IngressServerID}
	if forward.IngressServerID != previous.IngressServerID {
		servers = append(servers, forward.IngressServerID)
	}
	for _, serverID := range servers {
		if err := s.enqueueSyncTx(ctx, tx, serverID); err != nil {
			return model.Forward{}, err
		}
	}
	return forward, s.commitAndNotify(tx, servers)
}

func (s *Store) GetForward(ctx context.Context, id string) (model.Forward, error) {
	return s.getForwardFrom(ctx, s.db, id)
}

func (s *Store) getForwardFrom(ctx context.Context, db queryer, id string) (model.Forward, error) {
	var data []byte
	if err := db.QueryRowContext(ctx, `SELECT spec_json FROM forwards WHERE id=?`, id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Forward{}, ErrNotFound
		}
		return model.Forward{}, err
	}
	var forward model.Forward
	if err := json.Unmarshal(data, &forward); err != nil {
		return model.Forward{}, err
	}
	return forward, nil
}

// Resolve targets and reserve ports under the mutation transaction. A target
// node may be external; selecting it does not grant ownership of its service.
func (s *Store) prepareForward(ctx context.Context, tx *sql.Tx, forward *model.Forward, excludingID string) error {
	ingress, err := s.getServerFrom(ctx, tx, forward.IngressServerID)
	if err != nil {
		return err
	}
	if forward.TargetNodeID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT server_id,listen_port FROM nodes WHERE id=?`, forward.TargetNodeID).
			Scan(&forward.TargetServerID, &forward.TargetPort); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	}
	if forward.TargetServerID != "" {
		target, err := s.getServerFrom(ctx, tx, forward.TargetServerID)
		if err != nil {
			return err
		}
		forward.TargetHost = model.SelectServerTargetAddress(ingress, target)
		if forward.TargetHost == "" {
			return fmt.Errorf("%w: 目标服务器尚未识别到可用地址", ErrConflict)
		}
	}
	forward.TargetHost = model.NormalizeHost(forward.TargetHost)
	if forward.ListenPort == 0 {
		port, err := availablePortFrom(ctx, tx, forward.IngressServerID, excludingID)
		if err != nil {
			return err
		}
		forward.ListenPort = port
	}
	if err := forward.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	var used int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM nodes WHERE server_id=? AND listen_port=?) +
		(SELECT COUNT(*) FROM forwards WHERE ingress_server_id=? AND listen_port=? AND id<>?)`,
		forward.IngressServerID, forward.ListenPort, forward.IngressServerID, forward.ListenPort, excludingID).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return fmt.Errorf("%w: 入口端口 %d 已被节点或转发占用", ErrConflict, forward.ListenPort)
	}
	return nil
}

// AvailablePort only considers the control-plane inventory. Real service
// activation remains authoritative for conflicts with independently owned apps.
func (s *Store) AvailablePort(ctx context.Context, serverID string) (uint16, error) {
	if _, err := s.GetServer(ctx, serverID); err != nil {
		return 0, err
	}
	return availablePortFrom(ctx, s.db, serverID, "")
}

func availablePortFrom(ctx context.Context, db queryer, serverID, excludingID string) (uint16, error) {
	for attempt := 0; attempt < 256; attempt++ {
		buffer := make([]byte, 2)
		if _, err := rand.Read(buffer); err != nil {
			return 0, err
		}
		candidate := uint16(20000 + (uint32(buffer[0])<<8|uint32(buffer[1]))%40001)
		var used int
		err := db.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM nodes WHERE server_id=? AND listen_port=?) +
			(SELECT COUNT(*) FROM forwards WHERE ingress_server_id=? AND listen_port=? AND id<>?)`,
			serverID, candidate, serverID, candidate, excludingID).Scan(&used)
		if err != nil {
			return 0, err
		}
		if used == 0 {
			return candidate, nil
		}
	}
	return 0, errors.New("could not allocate an unused high port")
}

func (s *Store) ListForwards(ctx context.Context) ([]model.Forward, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT spec_json FROM forwards ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readForwards(rows)
}

func (s *Store) ListForwardsForServer(ctx context.Context, serverID string) ([]model.Forward, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT spec_json FROM forwards WHERE ingress_server_id=? ORDER BY created_at DESC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readForwards(rows)
}

func readForwards(rows *sql.Rows) ([]model.Forward, error) {
	forwards := []model.Forward{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var forward model.Forward
		if err := json.Unmarshal(data, &forward); err != nil {
			return nil, err
		}
		forwards = append(forwards, forward)
	}
	return forwards, rows.Err()
}

func (s *Store) DeleteForward(ctx context.Context, forwardID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var serverID string
	if err := tx.QueryRowContext(ctx, `SELECT ingress_server_id FROM forwards WHERE id=?`, forwardID).Scan(&serverID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM forwards WHERE id=?`, forwardID); err != nil {
		return err
	}
	return s.commitSync(ctx, tx, serverID)
}
