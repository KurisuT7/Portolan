package store

import (
	"context"
	"database/sql"
)

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS servers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  address TEXT NOT NULL,
  ipv4_address TEXT NOT NULL DEFAULT '',
  ipv6_address TEXT NOT NULL DEFAULT '',
  egress_ipv4 INTEGER NOT NULL DEFAULT 0,
  egress_ipv6 INTEGER NOT NULL DEFAULT 0,
  region TEXT NOT NULL DEFAULT '',
  traffic_reset_day INTEGER NOT NULL DEFAULT 1,
  agent_status TEXT NOT NULL DEFAULT 'pending',
  enroll_token_hash TEXT,
  enroll_expires_at TEXT,
  agent_token_hash TEXT,
  last_seen_at TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  protocol TEXT NOT NULL,
  listen_port INTEGER NOT NULL,
  enabled INTEGER NOT NULL,
	managed INTEGER NOT NULL DEFAULT 1,
	source TEXT NOT NULL DEFAULT '',
	last_seen_at TEXT,
  profile TEXT NOT NULL,
  sealed_spec TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(server_id, listen_port)
);
CREATE TABLE IF NOT EXISTS forwards (
  id TEXT PRIMARY KEY,
  ingress_server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  listen_port INTEGER NOT NULL,
  engine TEXT NOT NULL,
  spec_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(ingress_server_id, listen_port)
);
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  type TEXT NOT NULL,
  sealed_payload TEXT NOT NULL,
  revision INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'pending',
  result TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT
);
CREATE TABLE IF NOT EXISTS core_targets (
  core TEXT PRIMARY KEY,
  version TEXT NOT NULL,
  sha256_amd64 TEXT NOT NULL,
  sha256_arm64 TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_runtime (
  server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
  applied_revision INTEGER NOT NULL,
  agent_version TEXT NOT NULL DEFAULT '',
  sing_box_version TEXT NOT NULL,
  realm_version TEXT NOT NULL,
  units_json TEXT NOT NULL,
  reported_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS admin_totp (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  sealed_secret TEXT NOT NULL,
  last_step INTEGER NOT NULL,
  enabled_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_server_state ON jobs(server_id, state, created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_server_type ON jobs(server_id, type, created_at DESC);
CREATE TABLE IF NOT EXISTS forward_probes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  forward_id TEXT NOT NULL REFERENCES forwards(id) ON DELETE CASCADE,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  checked_at TEXT NOT NULL,
  attempts INTEGER NOT NULL,
  successes INTEGER NOT NULL,
  latency_ms REAL NOT NULL,
  jitter_ms REAL NOT NULL,
  loss_percent REAL NOT NULL,
  status TEXT NOT NULL,
  last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_forward_probes_latest ON forward_probes(forward_id, checked_at DESC);
CREATE TABLE IF NOT EXISTS traffic_counters (
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  counter TEXT NOT NULL,
  epoch TEXT NOT NULL,
  rx INTEGER NOT NULL,
  tx INTEGER NOT NULL,
  rx_rate REAL NOT NULL DEFAULT 0,
  tx_rate REAL NOT NULL DEFAULT 0,
  observed_at TEXT NOT NULL,
  PRIMARY KEY (server_id, counter)
);
CREATE TABLE IF NOT EXISTS traffic_refs (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL,
  ref_id TEXT NOT NULL,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  UNIQUE (kind, ref_id)
);
CREATE INDEX IF NOT EXISTS idx_traffic_refs_server ON traffic_refs(server_id);
CREATE TABLE IF NOT EXISTS traffic_hourly (
  ref INTEGER NOT NULL REFERENCES traffic_refs(id) ON DELETE CASCADE,
  hour INTEGER NOT NULL,
  rx INTEGER NOT NULL,
  tx INTEGER NOT NULL,
  PRIMARY KEY (ref, hour)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS traffic_reports (
  server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
  reported_at TEXT NOT NULL,
  port_error TEXT NOT NULL DEFAULT ''
);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{table: "nodes", name: "managed", definition: "INTEGER NOT NULL DEFAULT 1"},
		{table: "nodes", name: "source", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "nodes", name: "last_seen_at", definition: "TEXT"},
		{table: "servers", name: "ipv4_address", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "servers", name: "ipv6_address", definition: "TEXT NOT NULL DEFAULT ''"},
		{table: "servers", name: "egress_ipv4", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "servers", name: "egress_ipv6", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "servers", name: "traffic_reset_day", definition: "INTEGER NOT NULL DEFAULT 1"},
		{table: "jobs", name: "revision", definition: "INTEGER NOT NULL DEFAULT 0"},
		{table: "agent_runtime", name: "agent_version", definition: "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	return s.repairLegacyDiscoveredProfiles(ctx)
}

func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition)
	return err
}
