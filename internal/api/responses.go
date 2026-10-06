package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/KurisuT7/Portolan/internal/store"
)

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListJobs(r.Context(), 100)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.logger.Error("request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, context.Canceled):
		writeError(w, http.StatusNotFound, "resource not found")
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), store.ErrInvalidInput.Error()+": "))
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, strings.TrimPrefix(err.Error(), store.ErrConflict.Error()+": "))
	case store.IsConstraintError(err):
		writeError(w, http.StatusConflict, "listen port is already managed on this server")
	case errors.Is(err, store.ErrReadOnly):
		writeError(w, http.StatusConflict, "discovered nodes are read-only and remain managed by their original service")
	case errors.Is(err, store.ErrAgentOffline):
		writeError(w, http.StatusServiceUnavailable, "入口 Agent 当前离线，无法执行即时探测")
	default:
		s.internalError(w, err)
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if decoder.Decode(&struct{}{}) == nil {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

// writeErrorCode adds a stable code that the console uses to choose its next step.
func writeErrorCode(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": message, "code": code})
}

func secureToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
