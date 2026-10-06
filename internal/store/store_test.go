package store

import (
	"bytes"
	"context"
	"database/sql"
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

func TestEnrollmentDesiredStateAndJobLifecycle(t *testing.T) {
	t.Parallel()
	secretVault, err := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	database, err := Open(filepath.Join(t.TempDir(), "state.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server, enrollmentToken, err := database.CreateServer(ctx, model.Server{Name: "Edge A", Address: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}
	serverID, agentToken, err := database.Enroll(ctx, enrollmentToken, "198.51.100.23")
	if err != nil {
		t.Fatal(err)
	}
	if serverID != server.ID {
		t.Fatalf("enrolled wrong server: got %s want %s", serverID, server.ID)
	}
	enrolled, err := database.GetServer(ctx, serverID)
	if err != nil || enrolled.Address != "203.0.113.10" {
		t.Fatalf("an explicit address must not be overwritten during enrollment: %#v err=%v", enrolled, err)
	}
	authenticated, err := database.AuthenticateAgent(ctx, serverID, agentToken)
	if err != nil || !authenticated {
		t.Fatalf("agent authentication failed: ok=%v err=%v", authenticated, err)
	}
	_, err = database.CreateNode(ctx, model.Node{
		ServerID: serverID, Name: "SS 2022", Protocol: model.ProtocolShadowsocks, ListenPort: 22443, Enabled: true,
		SS: &model.SSSpec{Method: "2022-blake3-aes-128-gcm", Password: "MDEyMzQ1Njc4OWFiY2RlZg=="},
	}, "2022-blake3-aes-128-gcm")
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateForward(ctx, model.Forward{
		IngressServerID: serverID, Name: "HK entrance", ListenPort: 32443, Networks: []string{"tcp", "udp"},
		TargetHost: "203.0.113.10", TargetPort: 22443, Engine: model.ForwardSingBox, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := database.NextJob(ctx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.Type != "sync" {
		t.Fatalf("expected a sync job, got %#v", job)
	}
	var payload agentproto.SyncPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Nodes) != 1 || len(payload.Forwards) != 1 {
		t.Fatalf("unexpected desired state: %#v", payload)
	}
	if err := database.CompleteJob(ctx, serverID, job.ID, true, "validated"); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentAutoAddressAndForwardProbeOwnership(t *testing.T) {
	secretVault, _ := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{13}, 32)))
	database, err := Open(filepath.Join(t.TempDir(), "probe.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server, token, err := database.CreateServer(ctx, model.Server{Name: "Auto address"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.Enroll(ctx, token, "198.51.100.44"); err != nil {
		t.Fatal(err)
	}
	enrolled, _ := database.GetServer(ctx, server.ID)
	if enrolled.Address != "198.51.100.44" {
		t.Fatalf("Agent address was not detected: %#v", enrolled)
	}
	forward, err := database.CreateForward(ctx, model.Forward{IngressServerID: server.ID, Name: "probe", ListenPort: 25000,
		Networks: []string{"tcp"}, TargetHost: "target.example", TargetPort: 443, Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	probe := model.ForwardProbe{ForwardID: forward.ID, Attempts: 3, Successes: 3, LatencyMS: 12.3, JitterMS: 0.8, Status: "stable"}
	if err := database.SaveForwardProbes(ctx, server.ID, []model.ForwardProbe{probe}); err != nil {
		t.Fatal(err)
	}
	items, err := database.LatestForwardProbes(ctx)
	if err != nil || len(items) != 1 || items[0].ForwardID != forward.ID {
		t.Fatalf("unexpected probe history: %#v err=%v", items, err)
	}
}

func TestForwardProbeHistoryAggregatesBucketsAndIgnoresUnsupportedAvailability(t *testing.T) {
	t.Parallel()
	secretVault, _ := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{21}, 32)))
	database, err := Open(filepath.Join(t.TempDir(), "probe-history.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server, _, err := database.CreateServer(ctx, model.Server{Name: "Probe history"})
	if err != nil {
		t.Fatal(err)
	}
	forward, err := database.CreateForward(ctx, model.Forward{IngressServerID: server.ID, Name: "TCP history", ListenPort: 26000,
		Networks: []string{"tcp"}, TargetHost: "target.example", TargetPort: 443, Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	unsupportedForward, err := database.CreateForward(ctx, model.Forward{IngressServerID: server.ID, Name: "UDP history", ListenPort: 26001,
		Networks: []string{"udp"}, TargetHost: "target.example", TargetPort: 53, Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	from := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	to := from.Add(time.Hour)
	probes := []model.ForwardProbe{
		{ForwardID: forward.ID, CheckedAt: from.Add(5 * time.Minute), Attempts: 3, Successes: 3, LatencyMS: 10, JitterMS: 1, Status: "stable"},
		{ForwardID: forward.ID, CheckedAt: from.Add(10 * time.Minute), Attempts: 3, Successes: 2, LatencyMS: 30, JitterMS: 4, LossPct: 100.0 / 3, Status: "degraded"},
		{ForwardID: forward.ID, CheckedAt: from.Add(35 * time.Minute), Attempts: 3, Successes: 0, LossPct: 100, Status: "down"},
		{ForwardID: forward.ID, CheckedAt: from.Add(40 * time.Minute), Attempts: 3, Successes: 0, LossPct: 100, Status: "down"},
		{ForwardID: forward.ID, CheckedAt: from.Add(50 * time.Minute), Attempts: 3, Successes: 3, LatencyMS: 15, JitterMS: 2, Status: "stable"},
		{ForwardID: forward.ID, CheckedAt: from.Add(55 * time.Minute), Attempts: 3, Successes: 0, Status: "unsupported"},
		{ForwardID: unsupportedForward.ID, CheckedAt: from.Add(20 * time.Minute), Attempts: 3, Successes: 0, Status: "unsupported"},
	}
	if err := database.SaveForwardProbes(ctx, server.ID, probes); err != nil {
		t.Fatal(err)
	}

	history, err := database.GetForwardProbeHistory(ctx, forward.ID, from, to, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if history.Summary.SampleCount != 6 || history.Summary.Incidents != 1 {
		t.Fatalf("unexpected history counts: %#v", history.Summary)
	}
	assertClose(t, "availability", pointerValue(history.Summary.AvailabilityPercent), 100*8.0/15)
	assertClose(t, "loss", history.Summary.LossPercent, 100*7.0/15)
	assertClose(t, "average latency", history.Summary.AverageLatencyMS, 55.0/3)
	assertClose(t, "p95 latency", history.Summary.P95LatencyMS, 30)
	assertClose(t, "average jitter", history.Summary.AverageJitterMS, 7.0/3)
	if len(history.Points) != 2 {
		t.Fatalf("expected two populated buckets, got %#v", history.Points)
	}
	if !history.Points[0].CheckedAt.Equal(from) || history.Points[0].Status != "degraded" {
		t.Fatalf("unexpected first bucket: %#v", history.Points[0])
	}
	if history.Points[0].Attempts != 6 || history.Points[0].Successes != 5 ||
		len(history.Points[0].Reasons) != 1 || history.Points[0].Reasons[0] != "packet_loss" {
		t.Fatalf("first bucket should explain its packet loss: %#v", history.Points[0])
	}
	assertClose(t, "first bucket availability", pointerValue(history.Points[0].Availability), 100*5.0/6)
	assertClose(t, "first bucket latency", history.Points[0].LatencyMS, 20)
	if history.Points[1].Status != "degraded" {
		t.Fatalf("unexpected second bucket: %#v", history.Points[1])
	}
	if history.Points[1].Attempts != 9 || history.Points[1].Successes != 3 ||
		len(history.Points[1].Reasons) != 2 || history.Points[1].Reasons[0] != "unreachable" ||
		history.Points[1].Reasons[1] != "packet_loss" {
		t.Fatalf("second bucket should explain outage and packet loss: %#v", history.Points[1])
	}
	assertClose(t, "second bucket availability", pointerValue(history.Points[1].Availability), 100.0/3)

	unsupported, err := database.GetForwardProbeHistory(ctx, unsupportedForward.ID, from, to, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if unsupported.Summary.AvailabilityPercent != nil || unsupported.Summary.SampleCount != 1 ||
		len(unsupported.Points) != 2 || unsupported.Points[0].Availability != nil || unsupported.Points[0].Status != "unsupported" ||
		unsupported.Points[1].Availability != nil || unsupported.Points[1].Status != "unknown" {
		t.Fatalf("unsupported probes affected availability: %#v", unsupported)
	}
}

func assertClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	const tolerance = 0.0001
	if got < want-tolerance || got > want+tolerance {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func pointerValue(value *float64) float64 {
	if value == nil {
		return -1
	}
	return *value
}

func TestImmediateProbeJobRequiresOnlineEntryAgent(t *testing.T) {
	t.Parallel()
	secretVault, _ := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{18}, 32)))
	database, err := Open(filepath.Join(t.TempDir(), "probe-job.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server, enrollmentToken, err := database.CreateServer(ctx, model.Server{Name: "Entry"})
	if err != nil {
		t.Fatal(err)
	}
	forward, err := database.CreateForward(ctx, model.Forward{IngressServerID: server.ID, Name: "probe now", ListenPort: 25001,
		Networks: []string{"tcp"}, TargetHost: "target.example", TargetPort: 443, Engine: model.ForwardRealm, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnqueueForwardProbe(ctx, forward.ID); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("offline entry Agent should reject an immediate probe, got %v", err)
	}
	if _, _, err := database.Enroll(ctx, enrollmentToken, "198.51.100.55"); err != nil {
		t.Fatal(err)
	}
	probeJob, err := database.EnqueueForwardProbe(ctx, forward.ID)
	if err != nil {
		t.Fatal(err)
	}
	if probeJob.Type != "probe" || probeJob.ServerID != server.ID {
		t.Fatalf("unexpected probe job: %#v", probeJob)
	}
	// Forward creation queues a sync first; the immediate probe remains ordered behind it.
	syncJob, err := database.NextJob(ctx, server.ID)
	if err != nil || syncJob == nil || syncJob.Type != "sync" {
		t.Fatalf("expected sync job before probe: %#v err=%v", syncJob, err)
	}
	if err := database.CompleteJob(ctx, server.ID, syncJob.ID, true, "ok"); err != nil {
		t.Fatal(err)
	}
	job, err := database.NextJob(ctx, server.ID)
	if err != nil || job == nil || job.Type != "probe" {
		t.Fatalf("expected immediate probe job: %#v err=%v", job, err)
	}
	var payload agentproto.ProbeJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.ForwardID != forward.ID {
		t.Fatalf("unexpected probe payload: %#v err=%v", payload, err)
	}
	if err := database.CompleteJob(ctx, server.ID, job.ID, true, `{"forward_id":"`+forward.ID+`","status":"stable"}`); err != nil {
		t.Fatal(err)
	}
	finished, err := database.GetJobSummary(ctx, probeJob.ID)
	if err != nil || finished.State != "succeeded" {
		t.Fatalf("probe job was not completed: %#v err=%v", finished, err)
	}
}

func TestPortUniquenessIsScopedPerServer(t *testing.T) {
	t.Parallel()
	secretVault, _ := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	database, err := Open(filepath.Join(t.TempDir(), "state.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	serverA, _, _ := database.CreateServer(ctx, model.Server{Name: "A", Address: "a.example"})
	serverB, _, _ := database.CreateServer(ctx, model.Server{Name: "B", Address: "b.example"})
	newNode := func(serverID, name string) model.Node {
		return model.Node{ServerID: serverID, Name: name, Protocol: model.ProtocolShadowsocks, ListenPort: 443, Enabled: true,
			SS: &model.SSSpec{Method: "aes-256-gcm", Password: "secret"}}
	}
	if _, err := database.CreateNode(ctx, newNode(serverA.ID, "A1"), "aes-256-gcm"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateNode(ctx, newNode(serverB.ID, "B1"), "aes-256-gcm"); err != nil {
		t.Fatalf("same port on another server should be valid: %v", err)
	}
	if _, err := database.CreateNode(ctx, newNode(serverA.ID, "A2"), "aes-256-gcm"); !IsConstraintError(err) {
		t.Fatalf("duplicate port on same server should be rejected: %v", err)
	}
}

func TestAutomaticPortAndCrossEngineConflict(t *testing.T) {
	t.Parallel()
	secretVault, _ := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{11}, 32)))
	database, err := Open(filepath.Join(t.TempDir(), "state.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server, _, err := database.CreateServer(ctx, model.Server{Name: "Auto", Address: "auto.example"})
	if err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode(ctx, model.Node{ServerID: server.ID, Name: "auto node", Protocol: model.ProtocolShadowsocks, Enabled: true,
		SS: &model.SSSpec{Method: "aes-256-gcm", Password: "secret"}}, "aes-256-gcm")
	if err != nil {
		t.Fatal(err)
	}
	if node.ListenPort < 20000 || node.ListenPort > 60000 {
		t.Fatalf("automatic port is outside the managed range: %d", node.ListenPort)
	}
	_, err = database.CreateForward(ctx, model.Forward{IngressServerID: server.ID, Name: "collision", ListenPort: node.ListenPort,
		Networks: []string{"tcp"}, TargetHost: "target.example", TargetPort: 443, Engine: model.ForwardSingBox, Enabled: true})
	if err == nil {
		t.Fatal("forward should not be allowed to share a node port")
	}
}

func TestDiscoveredNodesStayReadOnlyAndOutOfDesiredState(t *testing.T) {
	t.Parallel()
	secretVault, _ := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{15}, 32)))
	database, err := Open(filepath.Join(t.TempDir(), "discovery.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server, _, err := database.CreateServer(ctx, model.Server{Name: "Existing", Address: "existing.example"})
	if err != nil {
		t.Fatal(err)
	}
	discovered := model.Node{
		Name: "existing ss", Protocol: model.ProtocolShadowsocks, ListenPort: 24443, Enabled: true,
		Source: "sing-box:/etc/sing-box/conf/ss.json", SS: &model.SSSpec{Method: "aes-256-gcm", Password: "existing-secret"},
	}
	created, updated, err := database.UpsertDiscoveredNodes(ctx, server.ID, []model.Node{discovered})
	if err != nil || created != 1 || updated != 0 {
		t.Fatalf("first discovery = created %d updated %d err %v", created, updated, err)
	}
	items, err := database.ListNodes(ctx)
	if err != nil || len(items) != 1 || items[0].Managed || items[0].Source == "" {
		t.Fatalf("unexpected discovered summary: %#v err=%v", items, err)
	}
	payload, err := database.desiredState(ctx, server.ID)
	if err != nil || len(payload.Nodes) != 0 {
		t.Fatalf("external node leaked into desired state: %#v err=%v", payload.Nodes, err)
	}
	if err := database.DeleteNode(ctx, items[0].ID); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("discovered node deletion returned %v", err)
	}
	discovered.Name = "existing ss renamed"
	created, updated, err = database.UpsertDiscoveredNodes(ctx, server.ID, []model.Node{discovered})
	if err != nil || created != 0 || updated != 1 {
		t.Fatalf("refresh discovery = created %d updated %d err %v", created, updated, err)
	}
}

func TestRotateEnrollmentTokenKeepsServerAndInvalidatesPreviousHint(t *testing.T) {
	t.Parallel()
	secretVault, _ := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32)))
	database, err := Open(filepath.Join(t.TempDir(), "rotate.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server, oldToken, err := database.CreateServer(ctx, model.Server{Name: "Recover command"})
	if err != nil {
		t.Fatal(err)
	}
	rotated, newToken, err := database.RotateEnrollmentToken(ctx, server.ID)
	if err != nil || rotated.ID != server.ID || newToken == "" || newToken == oldToken {
		t.Fatalf("unexpected rotation: server=%#v token=%q err=%v", rotated, newToken, err)
	}
	oldValid, _ := database.EnrollmentTokenValid(ctx, oldToken)
	newValid, _ := database.EnrollmentTokenValid(ctx, newToken)
	if oldValid || !newValid {
		t.Fatalf("token validity after rotation: old=%v new=%v", oldValid, newValid)
	}
}

func TestServerAddressUpdateRetargetsExistingForwards(t *testing.T) {
	t.Parallel()
	secretVault, _ := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{20}, 32)))
	database, err := Open(filepath.Join(t.TempDir(), "retarget.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	ingress, _, err := database.CreateServer(ctx, model.Server{Name: "edge-jp", Address: "203.0.113.77"})
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := database.CreateServer(ctx, model.Server{Name: "origin-us", Address: "2001:db8:9:1001::88"})
	if err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode(ctx, model.Node{ServerID: target.ID, Name: "origin-us Reality", Protocol: model.ProtocolReality,
		ListenPort: 52539, Enabled: true, Reality: &model.RealitySpec{UUID: "01890e7a-28f0-7c5c-9c29-62ce1d3c5500", HandshakeServer: "aws.amazon.com",
			HandshakePort: 443, ServerName: "aws.amazon.com", PrivateKey: "private", PublicKey: "public", ShortIDs: []string{"0123456789abcdef"}, Fingerprint: "chrome", Transport: "tcp"}}, "Reality")
	if err != nil {
		t.Fatal(err)
	}
	nodeForward, err := database.CreateForward(ctx, model.Forward{IngressServerID: ingress.ID, Name: "jp-to-us", ListenPort: 21345,
		Networks: []string{"tcp", "udp"}, TargetHost: target.Address, TargetPort: node.ListenPort, TargetNodeID: node.ID,
		Engine: model.ForwardRealm, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	portForward, err := database.CreateForward(ctx, model.Forward{IngressServerID: ingress.ID, Name: "jp-to-us port", ListenPort: 21346,
		Networks: []string{"tcp"}, TargetHost: target.Address, TargetPort: 443, TargetServerID: target.ID,
		Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateServer(ctx, target.ID, target.Name, "2001:0db8::168", target.Region); err != nil {
		t.Fatal(err)
	}
	forwards, err := database.ListForwards(ctx)
	if err != nil || len(forwards) != 2 {
		t.Fatalf("forward target was not updated: %#v err=%v", forwards, err)
	}
	byID := make(map[string]model.Forward, len(forwards))
	for _, forward := range forwards {
		byID[forward.ID] = forward
	}
	for _, id := range []string{nodeForward.ID, portForward.ID} {
		if byID[id].TargetHost != "2001:db8::168" || byID[id].TargetServerID != target.ID {
			t.Fatalf("forward %s did not follow the target server address: %#v", id, byID[id])
		}
	}
}

func TestMigrationAddsDiscoveryAndAddressColumnsToExistingDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE servers (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, address TEXT NOT NULL, region TEXT NOT NULL DEFAULT '',
		core_channel TEXT NOT NULL DEFAULT 'stable', agent_status TEXT NOT NULL DEFAULT 'pending',
		enroll_token_hash TEXT, enroll_expires_at TEXT, agent_token_hash TEXT, last_seen_at TEXT, created_at TEXT NOT NULL
	);
	CREATE TABLE nodes (
		id TEXT PRIMARY KEY, server_id TEXT NOT NULL, name TEXT NOT NULL, protocol TEXT NOT NULL,
		listen_port INTEGER NOT NULL, enabled INTEGER NOT NULL, profile TEXT NOT NULL,
		sealed_spec TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(server_id, listen_port)
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	secretVault, _ := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{19}, 32)))
	database, err := Open(path, secretVault)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rows, err := database.db.Query(`PRAGMA table_info(nodes)`)
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	for _, name := range []string{"managed", "source", "last_seen_at"} {
		if !columns[name] {
			t.Fatalf("migration did not add %s: %#v", name, columns)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	serverRows, err := database.db.Query(`PRAGMA table_info(servers)`)
	if err != nil {
		t.Fatal(err)
	}
	serverColumns := map[string]bool{}
	for serverRows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := serverRows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		serverColumns[name] = true
	}
	if err := serverRows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ipv4_address", "ipv6_address", "egress_ipv4", "egress_ipv6"} {
		if !serverColumns[name] {
			t.Fatalf("migration did not add %s: %#v", name, serverColumns)
		}
	}
}

func TestMigrationRepairsLegacyDiscoveredProfiles(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "profile-migration.db")
	secretVault, _ := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{23}, 32)))
	database, err := Open(path, secretVault)
	if err != nil {
		t.Fatal(err)
	}
	server, _, err := database.CreateServer(context.Background(), model.Server{Name: "Legacy profile", Address: "198.51.100.10"})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	_, err = database.db.Exec(`INSERT INTO nodes(id,server_id,name,protocol,listen_port,enabled,managed,source,last_seen_at,profile,sealed_spec,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "node-legacy", server.ID, "Legacy SS", string(model.ProtocolShadowsocks), 23456, true, false,
		"agent", time.Now().UTC().Format(time.RFC3339Nano), legacyExternalPrefix+"2022-blake3-aes-128-gcm", "sealed", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(path, secretVault)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var profile string
	if err := database.db.QueryRow(`SELECT profile FROM nodes WHERE id=?`, "node-legacy").Scan(&profile); err != nil {
		t.Fatal(err)
	}
	if profile != "外部 · 2022-blake3-aes-128-gcm" {
		t.Fatalf("legacy discovered profile was not repaired: %q", profile)
	}
}
