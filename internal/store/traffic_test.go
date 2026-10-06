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

func (f trafficFixture) summary(t *testing.T, since, now time.Time) map[string]TrafficItem {
	t.Helper()
	summary, err := f.store.trafficSummaryAt(context.Background(), since, now)
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
	if items := f.summary(t, first.Add(-time.Hour), first); items["server:"+f.server].RX != 0 || items["node:"+f.node].RX != 0 {
		t.Fatalf("baseline counted traffic: %#v", items)
	}
	second := first.Add(30 * time.Second)
	if err := f.store.saveTrafficAt(ctx, f.server, report("boot-a", 1_300_000, 2_600_000,
		model.PortTraffic{Port: 24443, RX: 40_000, TX: 110_000},
		model.PortTraffic{Port: 30443, RX: 3_000, TX: 9_000},
		model.PortTraffic{Port: 9999, RX: 70, TX: 70}), second); err != nil {
		t.Fatal(err)
	}
	items := f.summary(t, first.Add(-time.Hour), second)
	server := items["server:"+f.server]
	if server.RX != 300_000 || server.TX != 600_000 || server.RXRate != 10_000 || server.TXRate != 20_000 || !server.ReportedAt.Equal(second) {
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
	points, err := f.store.TrafficSeries(ctx, TrafficServer, f.server, []time.Time{first.Truncate(time.Hour), first.Truncate(time.Hour).Add(time.Hour), first.Truncate(time.Hour).Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if points[0].RX+points[1].RX != 300_000 || points[0].RX != 300_000*20/30 {
		t.Fatalf("hourly split = %#v", points)
	}
	// Rates are only current shortly after a report.
	if stale := f.summary(t, first.Add(-time.Hour), second.Add(3*time.Minute))["server:"+f.server]; stale.RXRate != 0 || stale.RX != 300_000 {
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
		return f.summary(t, at.Add(-48*time.Hour), until)["server:"+f.server].RX
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
	points, err := f.store.TrafficSeries(ctx, TrafficServer, f.server, edges)
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
	save(report("boot-b", 9_000_000, 0), later.Add(32*24*time.Hour))
	if got := f.summary(t, at.Add(-48*time.Hour), later.Add(32*24*time.Hour))["server:"+f.server].RX; got != 600_900 {
		t.Fatalf("after a long gap = %d", got)
	}
}

func TestTrafficSeriesValidationAndCleanup(t *testing.T) {
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
	points, err := f.store.TrafficSeries(ctx, TrafficNode, f.node, edges)
	if err != nil || !reflect.DeepEqual(points, []TrafficPoint{{Start: edges[0], RX: 500, TX: 900}}) {
		t.Fatalf("node series = %#v err=%v", points, err)
	}
	for _, bad := range [][]time.Time{{at}, {at, at}, {at.Add(time.Hour), at}, {at, at.Add(500 * 24 * time.Hour)}} {
		if _, err := f.store.TrafficSeries(ctx, TrafficNode, f.node, bad); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("edges %v accepted: %v", bad, err)
		}
	}
	if _, err := f.store.TrafficSeries(ctx, "user", f.node, edges); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown kind accepted: %v", err)
	}
	if _, err := f.store.TrafficSeries(ctx, TrafficForward, "fwd_missing", edges); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing forward = %v", err)
	}
	if err := f.store.saveTrafficAt(ctx, f.server, model.TrafficReport{BootID: "boot-a", Interfaces: []model.InterfaceTraffic{}, Ports: []model.PortTraffic{{Port: 1, RX: 1}}}, at); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("port counters without an epoch accepted: %v", err)
	}
	if err := f.store.DeleteNode(ctx, f.node); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := f.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM traffic_hours WHERE kind='node'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("node traffic left after deletion: %d err=%v", rows, err)
	}
	if err := f.store.DeleteServer(ctx, f.server); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM traffic_hours)+(SELECT COUNT(*) FROM traffic_counters)+(SELECT COUNT(*) FROM traffic_reports)`).Scan(&rows); err != nil || rows != 0 {
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
	if item := f.summary(t, f.baseTime, f.baseTime)["server:"+f.server]; item.PortError != model.TrafficPortsUnavailable {
		t.Fatalf("server item = %#v", item)
	}
}
