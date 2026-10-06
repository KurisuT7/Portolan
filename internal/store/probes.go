package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/KurisuT7/portolan/internal/model"
)

func (s *Store) SaveForwardProbes(ctx context.Context, serverID string, probes []model.ForwardProbe) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, probe := range probes {
		if err := probe.Validate(); err != nil {
			return err
		}
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT ingress_server_id FROM forwards WHERE id=?`, probe.ForwardID).Scan(&owner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if owner != serverID {
			return errors.New("forward does not belong to authenticated agent")
		}
		checkedAt := probe.CheckedAt.UTC()
		if checkedAt.IsZero() || checkedAt.After(time.Now().UTC().Add(2*time.Minute)) {
			checkedAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO forward_probes
		  (forward_id,server_id,checked_at,attempts,successes,latency_ms,jitter_ms,loss_percent,status,last_error)
		  VALUES(?,?,?,?,?,?,?,?,?,?)`, probe.ForwardID, serverID, checkedAt.Format(time.RFC3339Nano), probe.Attempts,
			probe.Successes, probe.LatencyMS, probe.JitterMS, probe.LossPct, probe.Status, probe.LastError); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM forward_probes WHERE checked_at < ?`, time.Now().UTC().Add(-7*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LatestForwardProbes(ctx context.Context) ([]model.ForwardProbe, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.forward_id,p.server_id,p.checked_at,p.attempts,p.successes,
	  p.latency_ms,p.jitter_ms,p.loss_percent,p.status,p.last_error
	  FROM forwards f JOIN forward_probes p ON p.id=(SELECT latest.id FROM forward_probes latest
	  WHERE latest.forward_id=f.id ORDER BY latest.checked_at DESC,latest.id DESC LIMIT 1)
	  ORDER BY p.checked_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	probes := []model.ForwardProbe{}
	for rows.Next() {
		var probe model.ForwardProbe
		var checked string
		if err := rows.Scan(&probe.ForwardID, &probe.ServerID, &checked, &probe.Attempts, &probe.Successes, &probe.LatencyMS,
			&probe.JitterMS, &probe.LossPct, &probe.Status, &probe.LastError); err != nil {
			return nil, err
		}
		probe.CheckedAt, _ = time.Parse(time.RFC3339Nano, checked)
		probes = append(probes, probe)
	}
	return probes, rows.Err()
}

func (s *Store) GetForwardProbeHistory(ctx context.Context, forwardID string, from, to time.Time, bucket time.Duration) (ForwardProbeHistory, error) {
	history := ForwardProbeHistory{
		Summary: ForwardProbeHistorySummary{From: from.UTC(), To: to.UTC()},
		Points:  []ForwardProbeHistoryPoint{},
	}
	if strings.TrimSpace(forwardID) == "" || !from.Before(to) || bucket <= 0 {
		return ForwardProbeHistory{}, errors.New("invalid forward probe history range")
	}
	if bucketCount := int(math.Ceil(float64(to.Sub(from)) / float64(bucket))); bucketCount > 1000 {
		return ForwardProbeHistory{}, errors.New("forward probe history range contains too many buckets")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM forwards WHERE id=?`, forwardID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ForwardProbeHistory{}, ErrNotFound
		}
		return ForwardProbeHistory{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT checked_at,attempts,successes,latency_ms,jitter_ms,status
	  FROM forward_probes WHERE forward_id=? AND checked_at>=? AND checked_at<?
	  ORDER BY checked_at`,
		forwardID, from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return ForwardProbeHistory{}, err
	}
	defer rows.Close()

	type probeBucket struct {
		attempts    int
		successes   int
		jitterTotal float64
		latencies   []float64
		sawDown     bool
	}
	buckets := make(map[int]*probeBucket)
	summaryLatencies := []float64{}
	summaryJitterTotal := 0.0
	summaryMetricSamples := 0
	summaryAttempts := 0
	summarySuccesses := 0
	inIncident := false

	for rows.Next() {
		var (
			checkedAtText string
			attempts      int
			successes     int
			latencyMS     float64
			jitterMS      float64
			status        string
		)
		if err := rows.Scan(&checkedAtText, &attempts, &successes, &latencyMS, &jitterMS, &status); err != nil {
			return ForwardProbeHistory{}, err
		}
		checkedAt, err := time.Parse(time.RFC3339Nano, checkedAtText)
		if err != nil {
			return ForwardProbeHistory{}, err
		}
		index := int(checkedAt.Sub(from) / bucket)
		item := buckets[index]
		if item == nil {
			item = &probeBucket{}
			buckets[index] = item
		}
		history.Summary.SampleCount++

		if status == "unsupported" {
			continue
		}
		if status == "down" {
			item.sawDown = true
		}
		item.attempts += attempts
		item.successes += successes
		summaryAttempts += attempts
		summarySuccesses += successes
		if successes > 0 {
			item.latencies = append(item.latencies, latencyMS)
			item.jitterTotal += jitterMS
			summaryLatencies = append(summaryLatencies, latencyMS)
			summaryJitterTotal += jitterMS
			summaryMetricSamples++
		}
		unhealthy := status == "degraded" || status == "down"
		if unhealthy && !inIncident {
			history.Summary.Incidents++
		}
		inIncident = unhealthy
	}
	if err := rows.Err(); err != nil {
		return ForwardProbeHistory{}, err
	}

	if summaryAttempts > 0 {
		availability := 100 * float64(summarySuccesses) / float64(summaryAttempts)
		history.Summary.AvailabilityPercent = &availability
		history.Summary.LossPercent = 100 - availability
	}
	if summaryMetricSamples > 0 {
		history.Summary.AverageLatencyMS = averageProbeMetric(summaryLatencies)
		history.Summary.P95LatencyMS = percentileProbeMetric(summaryLatencies, 0.95)
		history.Summary.AverageJitterMS = summaryJitterTotal / float64(summaryMetricSamples)
	}

	bucketCount := int(math.Ceil(float64(to.Sub(from)) / float64(bucket)))
	for index := 0; index < bucketCount; index++ {
		item := buckets[index]
		if item == nil {
			history.Points = append(history.Points, ForwardProbeHistoryPoint{
				CheckedAt: from.UTC().Add(time.Duration(index) * bucket),
				Status:    "unknown",
			})
			continue
		}
		point := ForwardProbeHistoryPoint{
			CheckedAt: from.UTC().Add(time.Duration(index) * bucket),
			Status:    "unsupported",
			Attempts:  item.attempts,
			Successes: item.successes,
		}
		averageLatency := 0.0
		averageJitter := 0.0
		if len(item.latencies) > 0 {
			averageLatency = averageProbeMetric(item.latencies)
			averageJitter = item.jitterTotal / float64(len(item.latencies))
			point.LatencyMS = averageLatency
			point.JitterMS = averageJitter
		}
		if item.attempts > 0 {
			availability := 100 * float64(item.successes) / float64(item.attempts)
			point.Availability = &availability
			point.LossPercent = 100 - availability
			if item.successes == 0 {
				point.Status = "down"
				point.Reasons = append(point.Reasons, "unreachable")
			} else {
				if item.sawDown {
					point.Reasons = append(point.Reasons, "unreachable")
				}
				if item.successes < item.attempts {
					point.Reasons = append(point.Reasons, "packet_loss")
				}
				if averageLatency > 500 {
					point.Reasons = append(point.Reasons, "high_latency")
				}
				if averageJitter > 30 {
					point.Reasons = append(point.Reasons, "high_jitter")
				}
				if len(point.Reasons) > 0 {
					point.Status = "degraded"
				} else {
					point.Status = "stable"
				}
			}
		}
		history.Points = append(history.Points, point)
	}
	return history, nil
}

func averageProbeMetric(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func percentileProbeMetric(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
