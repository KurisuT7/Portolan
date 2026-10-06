package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
	"github.com/KurisuT7/Portolan/internal/vault"
)

type trafficFixture struct {
	store    *Store
	server   string
	node     string
	forward  string
	baseTime time.Time
}

func newTrafficFixture(t *testing.T) trafficFixture {
	t.Helper()
	secretVault, err := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	database, err := Open(filepath.Join(t.TempDir(), "traffic.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	server, _, err := database.CreateServer(ctx, model.Server{Name: "Edge", Address: "203.0.113.20"})
	if err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode(ctx, model.Node{
		ServerID: server.ID, Name: "SS", Protocol: model.ProtocolShadowsocks, ListenPort: 24443, Enabled: true,
		SS: &model.SSSpec{Method: "2022-blake3-aes-128-gcm", Password: "MDEyMzQ1Njc4OWFiY2RlZg=="},
	}, "2022-blake3-aes-128-gcm")
	if err != nil {
		t.Fatal(err)
	}
	forward, err := database.CreateForward(ctx, model.Forward{
		IngressServerID: server.ID, Name: "Entrance", ListenPort: 30443, Networks: []string{"tcp"},
		TargetHost: "198.51.100.30", TargetPort: 443, Engine: model.ForwardRealm, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return trafficFixture{store: database, server: server.ID, node: node.ID, forward: forward.ID,
		baseTime: time.Date(2026, 10, 6, 9, 59, 40, 0, time.UTC)}
}

func report(boot string, interfaceRX, interfaceTX uint64, ports ...model.PortTraffic) model.TrafficReport {
	if ports == nil {
		ports = []model.PortTraffic{}
	}
	return model.TrafficReport{
		BootID: boot, Interfaces: []model.InterfaceTraffic{{Name: "eth0", RX: interfaceRX, TX: interfaceTX}},
		PortEpoch: boot + ":7", Ports: ports,
	}
}

func (f trafficFixture) summary(t *testing.T, now time.Time) map[string]TrafficItem {
	t.Helper()
	summary, err := f.store.trafficSummaryAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]TrafficItem{}
	for _, item := range summary.Items {
		items[item.Kind+":"+item.ID] = item
	}
	return items
}

func TestTrafficCountsDeltasPerServerNodeAndForward(t *testing.T) {
	t.Parallel()
	f := newTrafficFixture(t)
	ctx := context.Background()
	first := f.baseTime
	if err := f.store.saveTrafficAt(ctx, f.server, report("boot-a", 1_000_000, 2_000_000,
		model.PortTraffic{Port: 24443, RX: 10_000, TX: 50_000},
		model.PortTraffic{Port: 30443, RX: 0, TX: 0},
		model.PortTraffic{Port: 9999, RX: 7, TX: 7}), first); err != nil {
		t.Fatal(err)
	}
	// The first report is only a baseline: earlier traffic cannot be placed in time.
	if items := f.summary(t, first); items["server:"+f.server].RX != 0 || items["node:"+f.node].RX != 0 {
		t.Fatalf("baseline counted traffic: %#v", items)
	}
	second := first.Add(30 * time.Second)
	if err := f.store.saveTrafficAt(ctx, f.server, report("boot-a", 1_300_000, 2_600_000,
		model.PortTraffic{Port: 24443, RX: 40_000, TX: 110_000},
		model.PortTraffic{Port: 30443, RX: 3_000, TX: 9_000},
		model.PortTraffic{Port: 9999, RX: 70, TX: 70}), second); err != nil {
		t.Fatal(err)
	}
	items := f.summary(t, second)
	server := items["server:"+f.server]
	if server.RX != 300_000 || server.TX != 600_000 || server.RXRate != 10_000 || server.TXRate != 20_000 || !server.ReportedAt.Equal(second) ||
		!server.Since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("server item = %#v", server)
	}
	if node := items["node:"+f.node]; node.RX != 30_000 || node.TX != 60_000 || node.RXRate != 1_000 || node.ServerID != f.server {
		t.Fatalf("node item = %#v", node)
	}
	if forward := items["forward:"+f.forward]; forward.RX != 3_000 || forward.TX != 9_000 {
		t.Fatalf("forward item = %#v", forward)
	}
	if len(items) != 3 {
		t.Fatalf("a port without a node or forward must not be reported: %#v", items)
	}
	// The report crossed 10:00 UTC; the traffic is split by time.
	points, err := f.store.trafficSeries(ctx, TrafficServer, f.server, []time.Time{first.Truncate(time.Hour), first.Truncate(time.Hour).Add(time.Hour), first.Truncate(time.Hour).Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if points[0].RX+points[1].RX != 300_000 || points[0].RX != 300_000*20/30 {
		t.Fatalf("hourly split = %#v", points)
	}
	// Rates are only current shortly after a report.
	if stale := f.summary(t, second.Add(3*time.Minute))["server:"+f.server]; stale.RXRate != 0 || stale.RX != 300_000 {
		t.Fatalf("stale rate = %#v", stale)
	}
}

func TestTrafficCounterRestartsAndGaps(t *testing.T) {
	t.Parallel()
	f := newTrafficFixture(t)
	ctx := context.Background()
	at := f.baseTime
	save := func(value model.TrafficReport, when time.Time) {
		t.Helper()
		if err := f.store.saveTrafficAt(ctx, f.server, value, when); err != nil {
			t.Fatal(err)
		}
	}
	total := func(until time.Time) int64 {
		return f.summary(t, until)["server:"+f.server].RX
	}
	save(report("boot-a", 5_000, 0), at)
	// The host rebooted: the counter restarted and its whole value is new.
	save(report("boot-b", 800, 0), at.Add(time.Minute))
	if got := total(at.Add(time.Minute)); got != 800 {
		t.Fatalf("after reboot = %d", got)
	}
	// A counter that goes backwards within an epoch restarted as well.
	save(report("boot-b", 100, 0), at.Add(2*time.Minute))
	if got := total(at.Add(2 * time.Minute)); got != 900 {
		t.Fatalf("after counter reset = %d", got)
	}
	// Six hours without reports are spread over those hours.
	later := at.Add(2*time.Minute + 6*time.Hour)
	save(report("boot-b", 600_100, 0), later)
	edges := []time.Time{}
	for hour := at.Truncate(time.Hour); !hour.After(later.Truncate(time.Hour).Add(time.Hour)); hour = hour.Add(time.Hour) {
		edges = append(edges, hour)
	}
	points, err := f.store.trafficSeries(ctx, TrafficServer, f.server, edges)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	busy := 0
	for _, point := range points {
		sum += point.RX
		if point.RX >= 90_000 {
			busy++
		}
	}
	if sum != 600_900 || busy < 5 {
		t.Fatalf("spread points = %#v", points)
	}
	// After more than a month without reports the counter only sets a new baseline.
	resumed := later.Add(32 * 24 * time.Hour)
	save(report("boot-b", 9_000_000, 0), resumed)
	points, err = f.store.trafficSeries(ctx, TrafficServer, f.server, []time.Time{at.Add(-48 * time.Hour), resumed.Add(time.Hour)})
	if err != nil || points[0].RX != 600_900 {
		t.Fatalf("after a long gap = %#v err=%v", points, err)
	}
}

func TestTrafficHistoryValidationAndCleanup(t *testing.T) {
	t.Parallel()
	f := newTrafficFixture(t)
	ctx := context.Background()
	at := f.baseTime
	if err := f.store.saveTrafficAt(ctx, f.server, report("boot-a", 0, 0, model.PortTraffic{Port: 24443}), at); err != nil {
		t.Fatal(err)
	}
	if err := f.store.saveTrafficAt(ctx, f.server, report("boot-a", 0, 0, model.PortTraffic{Port: 24443, RX: 500, TX: 900}), at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	edges := []time.Time{at.Add(-24 * time.Hour), at.Add(24 * time.Hour)}
	points, err := f.store.trafficSeries(ctx, TrafficNode, f.node, edges)
	if err != nil || !reflect.DeepEqual(points, []TrafficPoint{{Start: edges[0], End: edges[1], RX: 500, TX: 900}}) {
		t.Fatalf("node series = %#v err=%v", points, err)
	}
	for _, span := range []string{"", "7d"} {
		if _, err := f.store.trafficHistoryAt(ctx, TrafficNode, f.node, span, at); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("range %q accepted: %v", span, err)
		}
	}
	if _, err := f.store.trafficHistoryAt(ctx, "user", f.node, TrafficDay, at); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown kind accepted: %v", err)
	}
	if _, err := f.store.trafficHistoryAt(ctx, TrafficForward, "fwd_missing", TrafficDay, at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing forward = %v", err)
	}
	if err := f.store.saveTrafficAt(ctx, f.server, model.TrafficReport{BootID: "boot-a", Interfaces: []model.InterfaceTraffic{}, Ports: []model.PortTraffic{{Port: 1, RX: 1}}}, at); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("port counters without an epoch accepted: %v", err)
	}
	if err := f.store.DeleteNode(ctx, f.node); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := f.store.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM traffic_refs WHERE kind='node')+(SELECT COUNT(*) FROM traffic_hourly h
		JOIN traffic_refs r ON r.id=h.ref WHERE r.kind='node')`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("node traffic left after deletion: %d err=%v", rows, err)
	}
	if err := f.store.DeleteServer(ctx, f.server); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM traffic_refs)+(SELECT COUNT(*) FROM traffic_hourly)
		+(SELECT COUNT(*) FROM traffic_counters)+(SELECT COUNT(*) FROM traffic_reports)`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("traffic left after server deletion: %d err=%v", rows, err)
	}
}

func TestTrafficReportsMissingPortCounters(t *testing.T) {
	t.Parallel()
	f := newTrafficFixture(t)
	value := report("boot-a", 1, 1)
	value.PortEpoch = ""
	value.PortError = model.TrafficPortsUnavailable
	if err := f.store.saveTrafficAt(context.Background(), f.server, value, f.baseTime); err != nil {
		t.Fatal(err)
	}
	if item := f.summary(t, f.baseTime)["server:"+f.server]; item.PortError != model.TrafficPortsUnavailable {
		t.Fatalf("server item = %#v", item)
	}
}

func TestTrafficCycleStartsAndPeriods(t *testing.T) {
	t.Parallel()
	zone := time.FixedZone("UTC+8", 8*60*60)
	day := func(year int, month time.Month, day int) time.Time {
		return time.Date(year, month, day, 0, 0, 0, 0, zone)
	}
	for _, test := range []struct {
		now      time.Time
		resetDay int
		want     time.Time
	}{
		{time.Date(2026, 10, 7, 9, 30, 0, 0, zone), 1, day(2026, 10, 1)},
		{time.Date(2026, 10, 7, 9, 30, 0, 0, zone), 15, day(2026, 9, 15)},
		{day(2026, 10, 15), 15, day(2026, 10, 15)},
		{time.Date(2026, 1, 10, 23, 0, 0, 0, zone), 20, day(2025, 12, 20)},
		// A month without the reset day starts its cycle on its last day.
		{time.Date(2026, 3, 30, 12, 0, 0, 0, zone), 31, day(2026, 2, 28)},
		{day(2026, 3, 31), 31, day(2026, 3, 31)},
	} {
		if got := CycleStart(test.now, test.resetDay); !got.Equal(test.want) {
			t.Errorf("CycleStart(%v, %d) = %v, want %v", test.now, test.resetDay, got, test.want)
		}
	}

	now := time.Date(2026, 10, 7, 9, 30, 0, 0, zone)
	hours, err := trafficPeriods(TrafficDay, now, 1)
	if err != nil || len(hours) != 25 || !hours[0].Equal(time.Date(2026, 10, 6, 10, 0, 0, 0, zone)) || !hours[24].Equal(time.Date(2026, 10, 7, 10, 0, 0, 0, zone)) {
		t.Fatalf("hours = %v err=%v", hours, err)
	}
	days, err := trafficPeriods(TrafficMonth, now, 1)
	if err != nil || len(days) != 31 || !days[0].Equal(day(2026, 9, 8)) || !days[30].Equal(day(2026, 10, 8)) {
		t.Fatalf("days = %v err=%v", days, err)
	}
	cycles, err := trafficPeriods(TrafficCycles, now, 31)
	want := map[int]time.Time{0: day(2025, 10, 31), 4: day(2026, 2, 28), 5: day(2026, 3, 31), 11: day(2026, 9, 30), 12: day(2026, 10, 31)}
	if err != nil || len(cycles) != 13 {
		t.Fatalf("cycles = %v err=%v", cycles, err)
	}
	for index, edge := range want {
		if !cycles[index].Equal(edge) {
			t.Fatalf("cycle edge %d = %v, want %v", index, cycles[index], edge)
		}
	}
}

func TestTrafficFollowsEachServersCycle(t *testing.T) {
	t.Parallel()
	f := newTrafficFixture(t)
	ctx := context.Background()
	zone := time.FixedZone("UTC+8", 8*60*60)
	// 1000 bytes on 14 September, 2000 on 16 September and 4000 on 2 October,
	// with idle gaps in between.
	var total uint64
	for _, step := range []struct {
		at    time.Time
		bytes uint64
	}{
		{time.Date(2026, 9, 14, 12, 0, 0, 0, zone), 0},
		{time.Date(2026, 9, 14, 12, 1, 0, 0, zone), 1_000},
		{time.Date(2026, 9, 16, 12, 0, 0, 0, zone), 0},
		{time.Date(2026, 9, 16, 12, 1, 0, 0, zone), 2_000},
		{time.Date(2026, 10, 2, 12, 0, 0, 0, zone), 0},
		{time.Date(2026, 10, 2, 12, 1, 0, 0, zone), 4_000},
	} {
		total += step.bytes
		if err := f.store.saveTrafficAt(ctx, f.server, report("boot-a", total, 0, model.PortTraffic{Port: 24443, RX: total}), step.at.UTC()); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, zone)
	items := f.summary(t, now)
	if server, node := items["server:"+f.server], items["node:"+f.node]; server.RX != 4_000 || node.RX != 4_000 || !node.Since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, zone)) {
		t.Fatalf("calendar month summary = %#v", items)
	}
	if _, err := f.store.UpdateServer(ctx, f.server, "Edge", "203.0.113.20", "", 15); err != nil {
		t.Fatal(err)
	}
	items = f.summary(t, now)
	if server, node := items["server:"+f.server], items["node:"+f.node]; server.RX != 6_000 || node.RX != 6_000 || !server.Since.Equal(time.Date(2026, 9, 15, 0, 0, 0, 0, zone)) {
		t.Fatalf("summary from the 15th = %#v", items)
	}
	points, err := f.store.trafficHistoryAt(ctx, TrafficNode, f.node, TrafficCycles, now)
	if err != nil || len(points) != 12 {
		t.Fatalf("cycles = %#v err=%v", points, err)
	}
	last, previous := points[11], points[10]
	if last.RX != 6_000 || !last.Start.Equal(time.Date(2026, 9, 15, 0, 0, 0, 0, zone)) || !last.End.Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, zone)) || previous.RX != 1_000 {
		t.Fatalf("cycles = %#v", points)
	}
	if _, err := f.store.UpdateServer(ctx, f.server, "Edge", "203.0.113.20", "", 32); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reset day 32 accepted: %v", err)
	}
}

func TestLegacyHourlyTrafficIsMoved(t *testing.T) {
	t.Parallel()
	secretVault, err := vault.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "legacy.db")
	ctx := context.Background()
	database, err := Open(path, secretVault)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := database.CreateServer(ctx, model.Server{Name: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := database.CreateServer(ctx, model.Server{Name: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	hour := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	// The first release kept one row per resource and hour; this forward
	// moved its entrance from the first server to the second.
	if _, err := database.db.ExecContext(ctx, `CREATE TABLE traffic_hours (
		kind TEXT NOT NULL, ref_id TEXT NOT NULL, hour INTEGER NOT NULL,
		server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
		rx INTEGER NOT NULL, tx INTEGER NOT NULL, PRIMARY KEY (kind, ref_id, hour))`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `INSERT INTO traffic_hours VALUES ('server', ?, ?, ?, 100, 200), ('server', ?, ?, ?, 300, 400),
			('forward', 'fwd_moved', ?, ?, 5, 6), ('forward', 'fwd_moved', ?, ?, 7, 8)`,
		first.ID, hour.Unix(), first.ID, first.ID, hour.Add(time.Hour).Unix(), first.ID,
		hour.Unix(), first.ID, hour.Add(time.Hour).Unix(), second.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		database, err = Open(path, secretVault)
		if err != nil {
			t.Fatal(err)
		}
		points, err := database.trafficSeries(ctx, TrafficServer, first.ID, []time.Time{hour, hour.Add(time.Hour), hour.Add(2 * time.Hour)})
		if err != nil || points[0].RX != 100 || points[1].TX != 400 {
			t.Fatalf("moved server traffic = %#v err=%v", points, err)
		}
		var owner string
		var legacy int
		if err := database.db.QueryRowContext(ctx, `SELECT server_id,(SELECT COUNT(*) FROM sqlite_master WHERE name='traffic_hours')
			FROM traffic_refs WHERE kind='forward' AND ref_id='fwd_moved'`).Scan(&owner, &legacy); err != nil || owner != second.ID || legacy != 0 {
			t.Fatalf("forward owner = %q legacy table = %d err=%v", owner, legacy, err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
