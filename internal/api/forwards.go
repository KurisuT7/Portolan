package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

func (s *Server) listForwards(w http.ResponseWriter, r *http.Request) {
	forwards, err := s.store.ListForwards(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": forwards})
}

func (s *Server) createForward(w http.ResponseWriter, r *http.Request) {
	var forward model.Forward
	if err := decodeJSON(w, r, &forward); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	forward.Enabled = true
	created, err := s.store.CreateForward(r.Context(), forward)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// PUT replaces the editable rule; server-owned identity and timestamps are retained.
func (s *Server) updateForward(w http.ResponseWriter, r *http.Request) {
	var forward model.Forward
	if err := decodeJSON(w, r, &forward); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.store.UpdateForward(r.Context(), r.PathValue("id"), forward)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteForward(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteForward(r.Context(), r.PathValue("id")); err != nil {
		s.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) triggerForwardProbe(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.EnqueueForwardProbe(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			writeError(w, http.StatusGatewayTimeout, "入口 Agent 未在 8 秒内返回本次探测结果")
			return
		case <-ticker.C:
			current, lookupErr := s.store.GetJobSummary(r.Context(), job.ID)
			if lookupErr != nil {
				s.internalError(w, lookupErr)
				return
			}
			switch current.State {
			case "succeeded":
				var probe model.ForwardProbe
				if err := json.Unmarshal([]byte(current.Result), &probe); err != nil {
					s.internalError(w, fmt.Errorf("decode immediate probe result: %w", err))
					return
				}
				writeJSON(w, http.StatusOK, probe)
				return
			case "failed":
				writeError(w, http.StatusBadGateway, "入口 Agent 探测失败："+current.Result)
				return
			}
		}
	}
}

func (s *Server) listForwardProbes(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.LatestForwardProbes(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) forwardProbeHistory(w http.ResponseWriter, r *http.Request) {
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "24h"
	}
	var window, bucket time.Duration
	switch rangeName {
	case "1h":
		window, bucket = time.Hour, time.Minute
	case "6h":
		window, bucket = 6*time.Hour, 5*time.Minute
	case "24h":
		window, bucket = 24*time.Hour, 15*time.Minute
	case "7d":
		window, bucket = 7*24*time.Hour, 2*time.Hour
	default:
		writeError(w, http.StatusBadRequest, "range must be one of 1h, 6h, 24h or 7d")
		return
	}
	to := time.Now().UTC()
	history, err := s.store.GetForwardProbeHistory(r.Context(), r.PathValue("id"), to.Add(-window), to, bucket)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, history)
}
