package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/KurisuT7/portolan/internal/model"
)

func (s *Store) CreateServer(ctx context.Context, server model.Server) (created model.Server, enrollmentToken string, err error) {
	if server.ID == "" {
		server.ID, err = randomID("srv")
		if err != nil {
			return model.Server{}, "", err
		}
	}
	if err := server.Validate(); err != nil {
		return model.Server{}, "", err
	}
	token, err := randomToken(32)
	if err != nil {
		return model.Server{}, "", err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO servers
			(id,name,address,ipv4_address,ipv6_address,egress_ipv4,egress_ipv6,region,
			 agent_status,enroll_token_hash,enroll_expires_at,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, server.ID, server.Name, server.Address, server.IPv4Address,
		server.IPv6Address, server.EgressIPv4, server.EgressIPv6, server.Region, "pending",
		tokenHash(token), now.Add(20*time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return model.Server{}, "", err
	}
	server.AgentStatus = "pending"
	server.CreatedAt = now
	return server, token, nil
}

func (s *Store) RotateEnrollmentToken(ctx context.Context, serverID string) (model.Server, string, error) {
	server, err := s.GetServer(ctx, serverID)
	if err != nil {
		return model.Server{}, "", err
	}
	token, err := randomToken(32)
	if err != nil {
		return model.Server{}, "", err
	}
	expires := time.Now().UTC().Add(20 * time.Minute).Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE servers SET enroll_token_hash=?,enroll_expires_at=? WHERE id=?`, tokenHash(token), expires, serverID)
	if err != nil {
		return model.Server{}, "", err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return model.Server{}, "", ErrNotFound
	}
	return server, token, nil
}

func (s *Store) Enroll(ctx context.Context, enrollmentToken, observedAddress string) (serverID, agentToken string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	hash := tokenHash(enrollmentToken)
	var expires string
	err = tx.QueryRowContext(ctx, `SELECT id,enroll_expires_at FROM servers WHERE enroll_token_hash=?`, hash).Scan(&serverID, &expires)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrNotFound
		}
		return "", "", err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || time.Now().UTC().After(expiresAt) {
		return "", "", errors.New("enrollment token has expired")
	}
	agentToken, err = randomToken(48)
	if err != nil {
		return "", "", err
	}
	result, err := tx.ExecContext(ctx, `UPDATE servers SET agent_token_hash=?, enroll_token_hash=NULL,
      enroll_expires_at=NULL, agent_status='online', last_seen_at=? WHERE id=? AND enroll_token_hash=?`,
		tokenHash(agentToken), time.Now().UTC().Format(time.RFC3339Nano), serverID, hash)
	if err != nil {
		return "", "", err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return "", "", errors.New("enrollment token was already used")
	}
	if observedAddress = model.NormalizeHost(observedAddress); model.IsPublicRoutableIP(observedAddress) {
		if _, err := tx.ExecContext(ctx, `UPDATE servers SET address=? WHERE id=? AND address=''`, observedAddress, serverID); err != nil {
			return "", "", err
		}
	}
	if err := s.commitSync(ctx, tx, serverID); err != nil {
		return "", "", err
	}
	return serverID, agentToken, nil
}

func (s *Store) UpdateServer(ctx context.Context, serverID, name, address, region string) (model.Server, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Server{}, err
	}
	defer tx.Rollback()
	current, err := s.getServerFrom(ctx, tx, serverID)
	if err != nil {
		return model.Server{}, err
	}
	if strings.TrimSpace(name) != "" {
		current.Name = strings.TrimSpace(name)
	}
	previousAddress := current.Address
	current.Address = model.NormalizeHost(address)
	if current.Address == "" {
		if current.IPv4Address != "" {
			current.Address = current.IPv4Address
		} else if current.IPv6Address != "" {
			current.Address = current.IPv6Address
		}
	}
	current.Region = strings.TrimSpace(region)
	if err := current.Validate(); err != nil {
		return model.Server{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE servers SET name=?,address=?,region=? WHERE id=?`, current.Name, current.Address, current.Region, serverID); err != nil {
		return model.Server{}, err
	}
	var changed []string
	if current.Address != previousAddress {
		changed, err = s.rerouteForwardsTx(ctx, tx, serverID)
		if err != nil {
			return model.Server{}, err
		}
	}
	updated, err := s.getServerFrom(ctx, tx, serverID)
	if err != nil {
		return model.Server{}, err
	}
	if err := s.commitAndNotify(tx, changed); err != nil {
		return model.Server{}, err
	}
	return updated, nil
}

// UpdateAgentNetwork stores separately reported public ingress addresses and
// outbound address-family capabilities. A blank or non-public legacy primary
// address is repaired automatically, while an explicit hostname or publicly
// routable address remains untouched.

func (s *Store) UpdateAgentNetwork(ctx context.Context, serverID string, update AgentNetworkUpdate) (model.Server, error) {
	update.IPv4Address = model.NormalizeHost(update.IPv4Address)
	update.IPv6Address = model.NormalizeHost(update.IPv6Address)
	if update.AddressesKnown && update.IPv4Address != "" &&
		(!model.IsPublicRoutableIP(update.IPv4Address) || net.ParseIP(update.IPv4Address).To4() == nil) {
		return model.Server{}, errors.New("Agent IPv4 address is not publicly routable")
	}
	if update.AddressesKnown && update.IPv6Address != "" &&
		(!model.IsPublicRoutableIP(update.IPv6Address) || net.ParseIP(update.IPv6Address).To4() != nil) {
		return model.Server{}, errors.New("Agent IPv6 address is not publicly routable")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Server{}, err
	}
	defer tx.Rollback()
	current, err := s.getServerFrom(ctx, tx, serverID)
	if err != nil {
		return model.Server{}, err
	}
	ipv4Address, ipv6Address := current.IPv4Address, current.IPv6Address
	egressIPv4, egressIPv6 := current.EgressIPv4, current.EgressIPv6
	if update.AddressesKnown {
		ipv4Address, ipv6Address = update.IPv4Address, update.IPv6Address
	}
	if update.EgressKnown {
		egressIPv4, egressIPv6 = update.EgressIPv4, update.EgressIPv6
	}
	if current.IPv4Address == ipv4Address && current.IPv6Address == ipv6Address &&
		current.EgressIPv4 == egressIPv4 && current.EgressIPv6 == egressIPv6 {
		return current, nil
	}

	primaryAddress := current.Address
	parsedPrimary := net.ParseIP(model.NormalizeHost(primaryAddress))
	if primaryAddress == "" || (parsedPrimary != nil && !model.IsPublicRoutableIP(primaryAddress)) {
		if ipv4Address != "" {
			primaryAddress = ipv4Address
		} else if ipv6Address != "" {
			primaryAddress = ipv6Address
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE servers SET address=?,ipv4_address=?,ipv6_address=?,egress_ipv4=?,egress_ipv6=? WHERE id=?`,
		primaryAddress, ipv4Address, ipv6Address, egressIPv4, egressIPv6, serverID); err != nil {
		return model.Server{}, err
	}
	changed, err := s.rerouteForwardsTx(ctx, tx, serverID)
	if err != nil {
		return model.Server{}, err
	}
	updated, err := s.getServerFrom(ctx, tx, serverID)
	if err != nil {
		return model.Server{}, err
	}
	if err := s.commitAndNotify(tx, changed); err != nil {
		return model.Server{}, err
	}
	return updated, nil
}

func (s *Store) rerouteForwardsTx(ctx context.Context, tx *sql.Tx, changedServerID string) ([]string, error) {
	servers, err := s.listServersFrom(ctx, tx)
	if err != nil {
		return nil, err
	}
	serversByID := make(map[string]model.Server, len(servers))
	for _, server := range servers {
		serversByID[server.ID] = server
	}

	nodeRows, err := tx.QueryContext(ctx, `SELECT id,server_id FROM nodes`)
	if err != nil {
		return nil, err
	}
	nodeServers := make(map[string]string)
	for nodeRows.Next() {
		var nodeID, serverID string
		if err := nodeRows.Scan(&nodeID, &serverID); err != nil {
			nodeRows.Close()
			return nil, err
		}
		nodeServers[nodeID] = serverID
	}
	if err := nodeRows.Err(); err != nil {
		nodeRows.Close()
		return nil, err
	}
	if err := nodeRows.Close(); err != nil {
		return nil, err
	}

	type update struct {
		id      string
		ingress string
		data    []byte
	}
	forwardRows, err := tx.QueryContext(ctx, `SELECT id,spec_json FROM forwards`)
	if err != nil {
		return nil, err
	}
	updates := []update{}
	for forwardRows.Next() {
		var id string
		var data []byte
		if err := forwardRows.Scan(&id, &data); err != nil {
			forwardRows.Close()
			return nil, err
		}
		var forward model.Forward
		if err := json.Unmarshal(data, &forward); err != nil {
			forwardRows.Close()
			return nil, err
		}
		targetServerID := forward.TargetServerID
		if targetServerID == "" {
			targetServerID = nodeServers[forward.TargetNodeID]
		}
		if targetServerID == "" {
			continue
		}
		if changedServerID != "" && forward.IngressServerID != changedServerID && targetServerID != changedServerID {
			continue
		}
		ingress, ingressExists := serversByID[forward.IngressServerID]
		target, targetExists := serversByID[targetServerID]
		if !ingressExists || !targetExists {
			continue
		}
		targetAddress := model.SelectServerTargetAddress(ingress, target)
		if targetAddress == "" || (forward.TargetHost == targetAddress && forward.TargetServerID == targetServerID) {
			continue
		}
		forward.TargetHost = targetAddress
		forward.TargetServerID = targetServerID
		updated, err := json.Marshal(forward)
		if err != nil {
			forwardRows.Close()
			return nil, err
		}
		updates = append(updates, update{id: id, ingress: forward.IngressServerID, data: updated})
	}
	if err := forwardRows.Err(); err != nil {
		forwardRows.Close()
		return nil, err
	}
	if err := forwardRows.Close(); err != nil {
		return nil, err
	}

	ingressServers := make(map[string]struct{})
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE forwards SET spec_json=? WHERE id=?`, item.data, item.id); err != nil {
			return nil, err
		}
		ingressServers[item.ingress] = struct{}{}
	}
	ids := make([]string, 0, len(ingressServers))
	for ingressServerID := range ingressServers {
		ids = append(ids, ingressServerID)
		if err := s.enqueueSyncTx(ctx, tx, ingressServerID); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func (s *Store) SetServerRegionIfBlank(ctx context.Context, serverID, region string) error {
	region = strings.TrimSpace(region)
	if region == "" {
		return nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE servers SET region=? WHERE id=? AND region=''`, region, serverID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM servers WHERE id=?`, serverID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	}
	return nil
}

// DeleteServer removes a server with its nodes and ingress forwards. Forwards
// of other servers that still lead to it must be changed or removed first.
func (s *Store) DeleteServer(ctx context.Context, serverID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	dependents, err := forwardNames(ctx, tx, `SELECT name FROM forwards WHERE ingress_server_id<>? AND
		(json_extract(spec_json,'$.target_server_id')=? OR json_extract(spec_json,'$.target_node_id') IN (SELECT id FROM nodes WHERE server_id=?))
		ORDER BY name`, serverID, serverID, serverID)
	if err != nil {
		return err
	}
	if len(dependents) > 0 {
		return fmt.Errorf("%w: 仍有 %d 条其他服务器的转发指向这台服务器（%s），请先修改或删除这些转发", ErrConflict, len(dependents), strings.Join(dependents, "、"))
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM servers WHERE id=?`, serverID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

func forwardNames(ctx context.Context, db queryer, query string, arguments ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (s *Store) AuthenticateAgent(ctx context.Context, serverID, token string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE id=? AND agent_token_hash=?`, serverID, tokenHash(token)).Scan(&count)
	return count == 1, err
}

func (s *Store) EnrollmentTokenValid(ctx context.Context, token string) (bool, error) {
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT enroll_expires_at FROM servers WHERE enroll_token_hash=?`, tokenHash(token)).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	return err == nil && time.Now().UTC().Before(expiresAt), nil
}

func (s *Store) TouchAgent(ctx context.Context, serverID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE servers SET agent_status='online', last_seen_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), serverID)
	return err
}

func (s *Store) ListServers(ctx context.Context) ([]model.Server, error) {
	return s.listServersFrom(ctx, s.db)
}

const serverQuery = `SELECT s.id,s.name,s.address,s.ipv4_address,s.ipv6_address,s.egress_ipv4,s.egress_ipv6,
      s.region,s.agent_status,COALESCE(s.last_seen_at,''),s.created_at,
      r.applied_revision,r.agent_version,r.sing_box_version,r.realm_version,r.units_json,r.reported_at
      FROM servers s LEFT JOIN agent_runtime r ON r.server_id=s.id`

func (s *Store) listServersFrom(ctx context.Context, db queryer) ([]model.Server, error) {
	rows, err := db.QueryContext(ctx, serverQuery+` ORDER BY s.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var servers []model.Server
	for rows.Next() {
		server, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	return servers, rows.Err()
}

func (s *Store) GetServer(ctx context.Context, serverID string) (model.Server, error) {
	return s.getServerFrom(ctx, s.db, serverID)
}

func (s *Store) getServerFrom(ctx context.Context, db queryer, serverID string) (model.Server, error) {
	server, err := scanServer(db.QueryRowContext(ctx, serverQuery+` WHERE s.id=?`, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Server{}, ErrNotFound
	}
	return server, err
}

func scanServer(row interface{ Scan(...any) error }) (model.Server, error) {
	var server model.Server
	var lastSeen, created string
	var applied sql.NullInt64
	var agentVersion, singBox, realm, units, reported sql.NullString
	if err := row.Scan(&server.ID, &server.Name, &server.Address, &server.IPv4Address, &server.IPv6Address,
		&server.EgressIPv4, &server.EgressIPv6, &server.Region, &server.AgentStatus, &lastSeen, &created,
		&applied, &agentVersion, &singBox, &realm, &units, &reported); err != nil {
		return model.Server{}, err
	}
	server.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	server.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeen)
	if applied.Valid {
		runtime := model.RuntimeStatus{AppliedRevision: applied.Int64, AgentVersion: agentVersion.String,
			SingBoxVersion: singBox.String, RealmVersion: realm.String}
		if err := json.Unmarshal([]byte(units.String), &runtime.Units); err != nil {
			return model.Server{}, err
		}
		runtime.ReportedAt, _ = time.Parse(time.RFC3339Nano, reported.String)
		server.Runtime = &runtime
	}
	return server, nil
}
