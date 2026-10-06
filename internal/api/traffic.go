package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

// maxTrafficLookback bounds summaries to the hourly history the panel keeps.
const maxTrafficLookback = 401 * 24 * time.Hour

func (s *Server) saveAgentTraffic(w http.ResponseWriter, r *http.Request) {
	serverID := r.Context().Value(agentContextKey{}).(string)
	var report model.TrafficReport
	if err := decodeJSON(w, r, &report); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := report.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SaveTraffic(r.Context(), serverID, report); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// trafficSummary returns the traffic of every server, node and forward since
// the given time; the console asks from the start of the month in its own
// time zone.
func (s *Server) trafficSummary(w http.ResponseWriter, r *http.Request) {
	since, err := time.Parse(time.RFC3339, r.URL.Query().Get("since"))
	now := time.Now().UTC()
	if err != nil || since.After(now) || now.Sub(since) > maxTrafficLookback {
		writeError(w, http.StatusBadRequest, "since must be an RFC 3339 time within the kept traffic history")
		return
	}
	summary, err := s.store.TrafficSummary(r.Context(), since)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// trafficHistory returns the traffic of one resource between consecutive
// edges, given as comma-separated Unix seconds.
func (s *Server) trafficHistory(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		edges, err := trafficEdges(r.URL.Query().Get("edges"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		points, err := s.store.TrafficSeries(r.Context(), kind, r.PathValue("id"), edges)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"points": points})
	}
}

func trafficEdges(raw string) ([]time.Time, error) {
	parts := strings.Split(raw, ",")
	if raw == "" || len(parts) > 401 {
		return nil, errors.New("edges must list 2 to 401 Unix times")
	}
	edges := make([]time.Time, len(parts))
	for index, part := range parts {
		seconds, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, errors.New("edges must list 2 to 401 Unix times")
		}
		edges[index] = time.Unix(seconds, 0).UTC()
	}
	return edges, nil
}
