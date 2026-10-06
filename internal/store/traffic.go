package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

// Traffic is stored per hour for a whole server (its default-route
// interfaces), a node or a forward.
const (
	TrafficServer  = "server"
	TrafficNode    = "node"
	TrafficForward = "forward"
)

const (
	trafficRetention = 400 * 24 * time.Hour
	// Counters observed further apart than trafficMaxGap only set a new
	// baseline: the traffic in between can no longer be placed in time.
	trafficMaxGap = 31 * 24 * time.Hour
	// Rates come from consecutive observations at most trafficRateGap apart
	// and are current for trafficRateWindow.
	trafficRateGap    = 10 * time.Minute
	trafficRateWindow = 2 * time.Minute
	maxTrafficEdges   = 401
)

// TrafficItem is the traffic of one server, node or forward since the start
// of a summary, with its rates from the latest report. Server items also carry
// the time of the latest report and why port counters are missing, if they are.
type TrafficItem struct {
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	ServerID   string    `json:"server_id"`
	RX         int64     `json:"rx_bytes"`
	TX         int64     `json:"tx_bytes"`
	RXRate     float64   `json:"rx_rate"`
	TXRate     float64   `json:"tx_rate"`
	ReportedAt time.Time `json:"reported_at,omitzero"`
	PortError  string    `json:"port_error,omitempty"`
}

type TrafficSummary struct {
	Since time.Time     `json:"since"`
	Items []TrafficItem `json:"items"`
}

// TrafficPoint is the traffic between Start and the start of the next point.
type TrafficPoint struct {
	Start time.Time `json:"start"`
	RX    int64     `json:"rx_bytes"`
	TX    int64     `json:"tx_bytes"`
}

type trafficRef struct{ kind, id string }

type counterState struct {
	known    bool
	epoch    string
	rx, tx   int64
	observed time.Time
}

// SaveTraffic turns an Agent's cumulative counters into hourly traffic.
// Interface counters add to the server; a port's counters add to the node or
// forward that listens on it on that server.
func (s *Store) SaveTraffic(ctx context.Context, serverID string, report model.TrafficReport) error {
	return s.saveTrafficAt(ctx, serverID, report, time.Now().UTC())
}

func (s *Store) saveTrafficAt(ctx context.Context, serverID string, report model.TrafficReport, now time.Time) error {
	if err := report.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	previous, err := trafficCounters(ctx, tx, serverID)
	if err != nil {
		return err
	}
	owners, err := portOwners(ctx, tx, serverID)
	if err != nil {
		return err
	}
	type bucket struct {
		ref  trafficRef
		hour int64
	}
	hours := map[bucket][2]int64{}
	observe := func(counter, epoch string, receivedTotal, sentTotal uint64, ref *trafficRef) error {
		received, sent := int64(receivedTotal), int64(sentTotal)
		state := previous[counter]
		deltaRX, deltaTX, from, counted := counterDelta(state, epoch, received, sent, now)
		// A rate needs two close observations of a counter that did not restart.
		var rateRX, rateTX float64
		if elapsed := now.Sub(state.observed); counted && from.Equal(state.observed) && elapsed >= time.Second && elapsed <= trafficRateGap {
			rateRX, rateTX = float64(deltaRX)/elapsed.Seconds(), float64(deltaTX)/elapsed.Seconds()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO traffic_counters(server_id,counter,epoch,rx,tx,rx_rate,tx_rate,observed_at)
			VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(server_id,counter) DO UPDATE SET epoch=excluded.epoch,rx=excluded.rx,tx=excluded.tx,
			rx_rate=excluded.rx_rate,tx_rate=excluded.tx_rate,observed_at=excluded.observed_at`,
			serverID, counter, epoch, received, sent, rateRX, rateTX, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if !counted || ref == nil {
			return nil
		}
		for _, share := range spreadOverHours(deltaRX, deltaTX, from, now) {
			key := bucket{ref: *ref, hour: share.Start.Unix()}
			value := hours[key]
			hours[key] = [2]int64{value[0] + share.RX, value[1] + share.TX}
		}
		return nil
	}
	server := trafficRef{kind: TrafficServer, id: serverID}
	for _, item := range report.Interfaces {
		if err := observe("if:"+item.Name, report.BootID, item.RX, item.TX, &server); err != nil {
			return err
		}
	}
	for _, item := range report.Ports {
		var owner *trafficRef
		if ref, ok := owners[item.Port]; ok {
			owner = &ref
		}
		if err := observe("port:"+strconv.Itoa(int(item.Port)), report.PortEpoch, item.RX, item.TX, owner); err != nil {
			return err
		}
	}
	for key, value := range hours {
		if value[0] == 0 && value[1] == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO traffic_hours(kind,ref_id,hour,server_id,rx,tx) VALUES(?,?,?,?,?,?)
			ON CONFLICT(kind,ref_id,hour) DO UPDATE SET rx=rx+excluded.rx,tx=tx+excluded.tx`,
			key.ref.kind, key.ref.id, key.hour, serverID, value[0], value[1]); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO traffic_reports(server_id,reported_at,port_error) VALUES(?,?,?)
		ON CONFLICT(server_id) DO UPDATE SET reported_at=excluded.reported_at,port_error=excluded.port_error`,
		serverID, now.Format(time.RFC3339Nano), report.PortError); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM traffic_hours WHERE hour<?`, now.Add(-trafficRetention).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// counterDelta returns the bytes a counter gained since it was last observed
// and when that period began. The first observation only records a baseline,
// as does one after a gap too long to place the traffic in time. A changed
// epoch or a counter that went backwards restarted from zero shortly before
// this report (the host booted or the nftables table was recreated), so its
// whole value is new and recent.
func counterDelta(previous counterState, epoch string, rx, tx int64, now time.Time) (int64, int64, time.Time, bool) {
	if !previous.known || now.Sub(previous.observed) > trafficMaxGap {
		return 0, 0, now, false
	}
	if previous.epoch != epoch || rx < previous.rx || tx < previous.tx {
		return rx, tx, now, true
	}
	from := previous.observed
	if from.After(now) {
		from = now
	}
	return rx - previous.rx, tx - previous.tx, from, true
}

// spreadOverHours divides traffic counted between from and to over the hours
// it spans, in proportion to time.
func spreadOverHours(rx, tx int64, from, to time.Time) []TrafficPoint {
	span := to.Sub(from)
	if span <= 0 {
		return []TrafficPoint{{Start: to.Truncate(time.Hour), RX: rx, TX: tx}}
	}
	var shares []TrafficPoint
	var doneRX, doneTX int64
	for hour := from.Truncate(time.Hour); hour.Before(to); hour = hour.Add(time.Hour) {
		end := hour.Add(time.Hour)
		cumulativeRX, cumulativeTX := rx, tx
		if end.Before(to) {
			fraction := float64(end.Sub(from)) / float64(span)
			cumulativeRX, cumulativeTX = int64(float64(rx)*fraction), int64(float64(tx)*fraction)
		}
		shares = append(shares, TrafficPoint{Start: hour, RX: cumulativeRX - doneRX, TX: cumulativeTX - doneTX})
		doneRX, doneTX = cumulativeRX, cumulativeTX
	}
	return shares
}

func trafficCounters(ctx context.Context, db queryer, serverID string) (map[string]counterState, error) {
	rows, err := db.QueryContext(ctx, `SELECT counter,epoch,rx,tx,observed_at FROM traffic_counters WHERE server_id=?`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]counterState{}
	for rows.Next() {
		var counter, observed string
		state := counterState{known: true}
		if err := rows.Scan(&counter, &state.epoch, &state.rx, &state.tx, &observed); err != nil {
			return nil, err
		}
		state.observed, _ = time.Parse(time.RFC3339Nano, observed)
		states[counter] = state
	}
	return states, rows.Err()
}

// portOwners maps the listening ports of a server to its nodes and the
// forwards it is the entrance of. Ports are unique on a server.
func portOwners(ctx context.Context, db queryer, serverID string) (map[uint16]trafficRef, error) {
	rows, err := db.QueryContext(ctx, `SELECT 'node',id,listen_port FROM nodes WHERE server_id=?
		UNION ALL SELECT 'forward',id,listen_port FROM forwards WHERE ingress_server_id=?`, serverID, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := map[uint16]trafficRef{}
	for rows.Next() {
		var ref trafficRef
		var port uint16
		if err := rows.Scan(&ref.kind, &ref.id, &port); err != nil {
			return nil, err
		}
		owners[port] = ref
	}
	return owners, rows.Err()
}

// TrafficSummary returns the traffic of every server, node and forward since
// the given time, with current rates and the state of each server's reports.
func (s *Store) TrafficSummary(ctx context.Context, since time.Time) (TrafficSummary, error) {
	return s.trafficSummaryAt(ctx, since, time.Now().UTC())
}

func (s *Store) trafficSummaryAt(ctx context.Context, since, now time.Time) (TrafficSummary, error) {
	summary := TrafficSummary{Since: since.UTC(), Items: []TrafficItem{}}
	servers := map[string]bool{}
	entities := map[trafficRef]string{}
	ports := map[string]map[uint16]trafficRef{}
	rows, err := s.db.QueryContext(ctx, `SELECT 'server',id,id,0 FROM servers
		UNION ALL SELECT 'node',id,server_id,listen_port FROM nodes
		UNION ALL SELECT 'forward',id,ingress_server_id,listen_port FROM forwards`)
	if err != nil {
		return TrafficSummary{}, err
	}
	for rows.Next() {
		var ref trafficRef
		var serverID string
		var port uint16
		if err := rows.Scan(&ref.kind, &ref.id, &serverID, &port); err != nil {
			rows.Close()
			return TrafficSummary{}, err
		}
		entities[ref] = serverID
		if ref.kind == TrafficServer {
			servers[ref.id] = true
			continue
		}
		if ports[serverID] == nil {
			ports[serverID] = map[uint16]trafficRef{}
		}
		ports[serverID][port] = ref
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return TrafficSummary{}, err
	}

	items := map[trafficRef]*TrafficItem{}
	item := func(ref trafficRef) *TrafficItem {
		if items[ref] == nil {
			items[ref] = &TrafficItem{Kind: ref.kind, ID: ref.id, ServerID: entities[ref]}
		}
		return items[ref]
	}

	rows, err = s.db.QueryContext(ctx, `SELECT kind,ref_id,SUM(rx),SUM(tx) FROM traffic_hours WHERE hour>=? GROUP BY kind,ref_id`, since.Unix())
	if err != nil {
		return TrafficSummary{}, err
	}
	for rows.Next() {
		var ref trafficRef
		var received, sent int64
		if err := rows.Scan(&ref.kind, &ref.id, &received, &sent); err != nil {
			rows.Close()
			return TrafficSummary{}, err
		}
		if _, exists := entities[ref]; exists {
			current := item(ref)
			current.RX, current.TX = received, sent
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return TrafficSummary{}, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT server_id,counter,rx_rate,tx_rate FROM traffic_counters WHERE observed_at>=?`,
		now.Add(-trafficRateWindow).Format(time.RFC3339Nano))
	if err != nil {
		return TrafficSummary{}, err
	}
	for rows.Next() {
		var serverID, counter string
		var receiveRate, sendRate float64
		if err := rows.Scan(&serverID, &counter, &receiveRate, &sendRate); err != nil {
			rows.Close()
			return TrafficSummary{}, err
		}
		var ref trafficRef
		var found bool
		if strings.HasPrefix(counter, "if:") {
			ref, found = trafficRef{kind: TrafficServer, id: serverID}, servers[serverID]
		} else if port, err := strconv.ParseUint(strings.TrimPrefix(counter, "port:"), 10, 16); err == nil {
			ref, found = ports[serverID][uint16(port)]
		}
		if found {
			current := item(ref)
			current.RXRate += receiveRate
			current.TXRate += sendRate
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return TrafficSummary{}, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT server_id,reported_at,port_error FROM traffic_reports`)
	if err != nil {
		return TrafficSummary{}, err
	}
	for rows.Next() {
		var serverID, reported, portError string
		if err := rows.Scan(&serverID, &reported, &portError); err != nil {
			rows.Close()
			return TrafficSummary{}, err
		}
		if servers[serverID] {
			current := item(trafficRef{kind: TrafficServer, id: serverID})
			current.ReportedAt, _ = time.Parse(time.RFC3339Nano, reported)
			current.PortError = portError
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return TrafficSummary{}, err
	}

	for _, current := range items {
		summary.Items = append(summary.Items, *current)
	}
	slices.SortFunc(summary.Items, func(a, b TrafficItem) int {
		return strings.Compare(a.Kind+"\x00"+a.ID, b.Kind+"\x00"+b.ID)
	})
	return summary, nil
}

// TrafficSeries sums the hourly traffic of one server, node or forward into
// the periods between consecutive edges, which the caller chooses so that
// days and months follow its own time zone.
func (s *Store) TrafficSeries(ctx context.Context, kind, id string, edges []time.Time) ([]TrafficPoint, error) {
	table := map[string]string{TrafficServer: "servers", TrafficNode: "nodes", TrafficForward: "forwards"}[kind]
	if table == "" || len(edges) < 2 || len(edges) > maxTrafficEdges {
		return nil, fmt.Errorf("%w: traffic history needs 2 to %d period edges", ErrInvalidInput, maxTrafficEdges)
	}
	for index := 1; index < len(edges); index++ {
		if !edges[index].After(edges[index-1]) {
			return nil, fmt.Errorf("%w: traffic history edges must increase", ErrInvalidInput)
		}
	}
	if edges[len(edges)-1].Sub(edges[0]) > trafficRetention+trafficMaxGap {
		return nil, fmt.Errorf("%w: traffic history range is too long", ErrInvalidInput)
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id=?`, id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	points := make([]TrafficPoint, len(edges)-1)
	starts := make([]int64, len(edges))
	for index, edge := range edges {
		starts[index] = edge.Unix()
		if index < len(points) {
			points[index].Start = edge.UTC()
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT hour,rx,tx FROM traffic_hours WHERE kind=? AND ref_id=? AND hour>=? AND hour<?`,
		kind, id, starts[0], starts[len(starts)-1])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hour, received, sent int64
		if err := rows.Scan(&hour, &received, &sent); err != nil {
			return nil, err
		}
		index := sort.Search(len(starts), func(position int) bool { return starts[position] > hour }) - 1
		if index >= 0 && index < len(points) {
			points[index].RX += received
			points[index].TX += sent
		}
	}
	return points, rows.Err()
}

// deleteTraffic removes the history of a node or forward with the resource.
func deleteTraffic(ctx context.Context, tx *sql.Tx, kind, id string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM traffic_hours WHERE kind=? AND ref_id=?`, kind, id)
	return err
}
