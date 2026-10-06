package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

const (
	legacyExternalPrefix      = "\u6fb6\u682d\u5134 \u8def "
	legacyRealityPrefix       = legacyExternalPrefix + "REALITY \u8def "
	legacyExternalNodeProfile = "\u6fb6\u682d\u5134\u947a\u509c\u5063"
)

func (s *Store) CreateNode(ctx context.Context, node model.Node, profile string) (model.Node, error) {
	if node.ListenPort == 0 {
		port, err := s.AvailablePort(ctx, node.ServerID)
		if err != nil {
			return model.Node{}, err
		}
		node.ListenPort = port
	}
	node.Managed = true
	node.Source = ""
	if err := node.Validate(); err != nil {
		return model.Node{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	var err error
	if node.ID == "" {
		node.ID, err = randomID("node")
		if err != nil {
			return model.Node{}, err
		}
	}
	node.CreatedAt = time.Now().UTC()
	data, err := json.Marshal(node)
	if err != nil {
		return model.Node{}, err
	}
	sealed, err := s.vault.Seal(data, "node:"+node.ID)
	if err != nil {
		return model.Node{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Node{}, err
	}
	defer tx.Rollback()
	var usedByForward int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM forwards WHERE ingress_server_id=? AND listen_port=?`, node.ServerID, node.ListenPort).Scan(&usedByForward); err != nil {
		return model.Node{}, err
	}
	if usedByForward > 0 {
		return model.Node{}, fmt.Errorf("%w: 监听端口 %d 已被转发占用", ErrConflict, node.ListenPort)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO nodes(id,server_id,name,protocol,listen_port,enabled,managed,source,last_seen_at,profile,sealed_spec,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, node.ID, node.ServerID, node.Name, string(node.Protocol), node.ListenPort, node.Enabled,
		true, "", nil, profile, sealed, node.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return model.Node{}, err
	}
	return node, s.commitSync(ctx, tx, node.ServerID)
}

func (s *Store) ListNodes(ctx context.Context) ([]NodeSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,server_id,name,protocol,listen_port,enabled,managed,source,
		COALESCE(last_seen_at,''),profile,created_at FROM nodes ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []NodeSummary
	for rows.Next() {
		var node NodeSummary
		var protocol, lastSeen, created string
		if err := rows.Scan(&node.ID, &node.ServerID, &node.Name, &protocol, &node.ListenPort, &node.Enabled,
			&node.Managed, &node.Source, &lastSeen, &node.Profile, &created); err != nil {
			return nil, err
		}
		node.Protocol = model.Protocol(protocol)
		node.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeen)
		node.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func (s *Store) GetNode(ctx context.Context, nodeID string) (model.Node, error) {
	var sealed, source, lastSeen string
	var managed bool
	if err := s.db.QueryRowContext(ctx, `SELECT sealed_spec,managed,source,COALESCE(last_seen_at,'') FROM nodes WHERE id=?`, nodeID).
		Scan(&sealed, &managed, &source, &lastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Node{}, ErrNotFound
		}
		return model.Node{}, err
	}
	node, err := s.openNode(nodeID, sealed)
	if err != nil {
		return model.Node{}, err
	}
	node.Managed = managed
	node.Source = source
	node.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeen)
	return node, nil
}

func (s *Store) DeleteNode(ctx context.Context, nodeID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var serverID string
	var managed bool
	if err := tx.QueryRowContext(ctx, `SELECT server_id,managed FROM nodes WHERE id=?`, nodeID).Scan(&serverID, &managed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !managed {
		return ErrReadOnly
	}
	dependents, err := forwardNames(ctx, tx, `SELECT name FROM forwards WHERE json_extract(spec_json,'$.target_node_id')=? ORDER BY name`, nodeID)
	if err != nil {
		return err
	}
	if len(dependents) > 0 {
		return fmt.Errorf("%w: 仍有 %d 条转发指向这个节点（%s），请先修改或删除这些转发", ErrConflict, len(dependents), strings.Join(dependents, "、"))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, nodeID); err != nil {
		return err
	}
	return s.commitSync(ctx, tx, serverID)
}

func (s *Store) UpsertDiscoveredNodes(ctx context.Context, serverID string, items []model.Node) (created, updated int, err error) {
	if len(items) > 100 {
		return 0, 0, errors.New("too many discovered nodes")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for _, item := range items {
		item.ID = ""
		item.ServerID = serverID
		item.Managed = false
		item.Enabled = true
		item.CreatedAt = now
		item.LastSeenAt = now
		item.Source = strings.TrimSpace(item.Source)
		if item.Source == "" || len(item.Source) > 512 {
			return 0, 0, errors.New("discovered node source is invalid")
		}
		if err := item.Validate(); err != nil {
			return 0, 0, fmt.Errorf("invalid discovered node on port %d: %w", item.ListenPort, err)
		}
		var forwardConflict int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM forwards WHERE ingress_server_id=? AND listen_port=?`, serverID, item.ListenPort).Scan(&forwardConflict); err != nil {
			return 0, 0, err
		}
		if forwardConflict > 0 {
			continue
		}
		var existingID, createdAt string
		var existingManaged bool
		err := tx.QueryRowContext(ctx, `SELECT id,managed,created_at FROM nodes WHERE server_id=? AND listen_port=?`, serverID, item.ListenPort).
			Scan(&existingID, &existingManaged, &createdAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, 0, err
		}
		if existingManaged {
			continue
		}
		if errors.Is(err, sql.ErrNoRows) {
			item.ID, err = randomID("node")
			if err != nil {
				return 0, 0, err
			}
			createdAt = now.Format(time.RFC3339Nano)
		} else {
			item.ID = existingID
			item.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		}
		data, err := json.Marshal(item)
		if err != nil {
			return 0, 0, err
		}
		sealed, err := s.vault.Seal(data, "node:"+item.ID)
		if err != nil {
			return 0, 0, err
		}
		profile := discoveredProfile(item)
		if existingID == "" {
			_, err = tx.ExecContext(ctx, `INSERT INTO nodes(id,server_id,name,protocol,listen_port,enabled,managed,source,last_seen_at,profile,sealed_spec,created_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, serverID, item.Name, string(item.Protocol), item.ListenPort, true, false,
				item.Source, now.Format(time.RFC3339Nano), profile, sealed, createdAt)
			if err != nil {
				return 0, 0, err
			}
			created++
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE nodes SET name=?,protocol=?,enabled=1,source=?,last_seen_at=?,profile=?,sealed_spec=?
				WHERE id=? AND managed=0`, item.Name, string(item.Protocol), item.Source, now.Format(time.RFC3339Nano), profile, sealed, item.ID)
			if err != nil {
				return 0, 0, err
			}
			updated++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return created, updated, nil
}

func discoveredProfile(node model.Node) string {
	switch node.Protocol {
	case model.ProtocolReality:
		flow := node.Reality.Flow
		if flow == "" {
			flow = "standard"
		}
		return "外部 · REALITY · " + flow
	case model.ProtocolShadowsocks:
		return "外部 · " + node.SS.Method
	case model.ProtocolSnell:
		return "外部 · Snell v" + strconv.Itoa(node.Snell.Version)
	default:
		return "外部节点"
	}
}

func repairLegacyDiscoveredProfile(profile string) string {
	switch {
	case profile == legacyExternalNodeProfile:
		return "外部节点"
	case strings.HasPrefix(profile, legacyRealityPrefix):
		return "外部 · REALITY · " + strings.TrimPrefix(profile, legacyRealityPrefix)
	case strings.HasPrefix(profile, legacyExternalPrefix):
		return "外部 · " + strings.TrimPrefix(profile, legacyExternalPrefix)
	default:
		return profile
	}
}

func (s *Store) repairLegacyDiscoveredProfiles(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id,profile FROM nodes WHERE managed=0`)
	if err != nil {
		return err
	}
	type repair struct {
		id      string
		profile string
	}
	var repairs []repair
	for rows.Next() {
		var item repair
		if err := rows.Scan(&item.id, &item.profile); err != nil {
			rows.Close()
			return err
		}
		fixed := repairLegacyDiscoveredProfile(item.profile)
		if fixed != item.profile {
			item.profile = fixed
			repairs = append(repairs, item)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range repairs {
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET profile=? WHERE id=? AND managed=0`, item.profile, item.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) openNode(id, sealed string) (model.Node, error) {
	data, err := s.vault.Open(sealed, "node:"+id)
	if err != nil {
		return model.Node{}, err
	}
	var node model.Node
	if err := json.Unmarshal(data, &node); err != nil {
		return model.Node{}, err
	}
	return node, nil
}
