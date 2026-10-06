package api

import (
	"errors"
	"net/http"
	"time"
	// The panel resolves the console's time zone itself, also on hosts and in
	// images without a zoneinfo database.
	_ "time/tzdata"

	"github.com/KurisuT7/Portolan/internal/model"
)

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

// trafficSummary returns the traffic of every server, node and forward in
// the current cycle of its server; the console passes its own time zone.
func (s *Server) trafficSummary(w http.ResponseWriter, r *http.Request) {
	location, err := trafficLocation(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	summary, err := s.store.TrafficSummary(r.Context(), location)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// trafficHistory returns the traffic of one resource over the last 24 hours
// (range=24h), 30 days (30d) or 12 cycles (12m) in the console's time zone.
func (s *Server) trafficHistory(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		location, err := trafficLocation(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		points, err := s.store.TrafficHistory(r.Context(), kind, r.PathValue("id"), r.URL.Query().Get("range"), location)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"points": points})
	}
}

// trafficLocation reads the IANA time zone in which days and cycles start.
func trafficLocation(r *http.Request) (*time.Location, error) {
	name := r.URL.Query().Get("tz")
	location, err := time.LoadLocation(name)
	if name == "" || name == "Local" || err != nil {
		return nil, errors.New("tz must be an IANA time zone such as Asia/Shanghai")
	}
	return location, nil
}
