package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/KurisuT7/Portolan/internal/store"
)

func (s *Server) withAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(s.sessionCookieName())
		if err != nil {
			writeError(w, http.StatusUnauthorized, "administrator session required")
			return
		}
		active, ok := s.sessions.get(cookie.Value)
		if !ok {
			writeError(w, http.StatusUnauthorized, "administrator session expired")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(active.csrf)) != 1 {
				writeError(w, http.StatusForbidden, "valid CSRF token required")
				return
			}
		}
		ctx := context.WithValue(r.Context(), sessionContextKey{}, active)
		next(w, r.WithContext(context.WithValue(ctx, sessionTokenContextKey{}, cookie.Value)))
	}
}

func (s *Server) withAgent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serverID := r.Header.Get("X-Portolan-Server-ID")
		authorization := r.Header.Get("Authorization")
		if serverID == "" || !strings.HasPrefix(authorization, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "agent authentication required")
			return
		}
		valid, err := s.store.AuthenticateAgent(r.Context(), serverID, strings.TrimPrefix(authorization, "Bearer "))
		if err != nil {
			s.internalError(w, err)
			return
		}
		if !valid {
			writeError(w, http.StatusUnauthorized, "invalid agent credentials")
			return
		}
		addressHeader, addressesKnown := agentHeader(r, agentAddressesHeader)
		egressHeader, egressKnown := agentHeader(r, agentEgressFamiliesHeader)
		addressCandidates := splitAgentHeader(addressHeader)
		egressCandidates := splitAgentHeader(egressHeader)
		if addressesKnown || egressKnown {
			if len(addressCandidates) > 32 {
				writeError(w, http.StatusBadRequest, "too many Agent public addresses")
				return
			}
			if len(egressCandidates) > 4 {
				writeError(w, http.StatusBadRequest, "too many Agent egress families")
				return
			}
			ipv4Address, ipv6Address := agentPublicAddresses(addressCandidates)
			egressIPv4, egressIPv6 := agentEgressFamilies(egressCandidates)
			if _, err := s.store.UpdateAgentNetwork(r.Context(), serverID, store.AgentNetworkUpdate{
				AddressesKnown: addressesKnown,
				IPv4Address:    ipv4Address,
				IPv6Address:    ipv6Address,
				EgressKnown:    egressKnown,
				EgressIPv4:     egressIPv4,
				EgressIPv6:     egressIPv6,
			}); err != nil {
				s.internalError(w, err)
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), agentContextKey{}, serverID)))
	}
}

func agentHeader(r *http.Request, name string) (string, bool) {
	values, exists := r.Header[http.CanonicalHeaderKey(name)]
	if !exists {
		return "", false
	}
	return strings.Join(values, ","), true
}

func splitAgentHeader(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "none") {
		return nil
	}
	return strings.Split(value, ",")
}

// securityHeaders applies to every response. The console handler replaces the
// restrictive Content-Security-Policy with one that allows its own assets.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("http panic", "error", recovered)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/v1/agent/bootstrap/") || strings.HasPrefix(path, "/api/v1/agent/downloads/") {
			path = "/api/v1/agent/downloads/[redacted]"
		}
		s.logger.Info("http request", "method", r.Method, "path", path, "status", recorder.status,
			"client", s.clientIP(r), "duration", time.Since(started))
	})
}

// statusRecorder keeps the response status for the access log. Unwrap lets
// http.ResponseController reach the underlying writer for deadlines.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status, r.wroteHeader = status, true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	r.wroteHeader = true
	return r.ResponseWriter.Write(data)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
