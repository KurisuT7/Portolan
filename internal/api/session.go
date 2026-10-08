package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/KurisuT7/Portolan/internal/buildinfo"
	"github.com/KurisuT7/Portolan/internal/totp"
)

const (
	sessionLifetime = 12 * time.Hour
	maxSessions     = 32
)

type session struct {
	csrf      string
	expiresAt time.Time
	// pendingTOTP holds a generated secret until the administrator confirms it.
	pendingTOTP        string
	pendingTOTPExpires time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]session
}

// sessionCookieName uses the __Host- prefix whenever cookies are Secure, so the
// browser only accepts the cookie from this origin over HTTPS with Path=/.
func (s *Server) sessionCookieName() string {
	if s.secureCookies {
		return "__Host-portolan_session"
	}
	return "portolan_session"
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token string `json:"token"`
		Code  string `json:"code"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	client := s.clientIP(r)
	if wait := s.guard.retryAfter(client); wait > 0 {
		s.logger.Warn("administrator login refused", "client", client, "reason", "locked", "retry_after", wait.Round(time.Second).String())
		writeRetry(w, wait, "login_locked", "too many failed login attempts")
		return
	}
	candidate := sha256.Sum256([]byte(request.Token))
	if subtle.ConstantTimeCompare(candidate[:], s.adminHash[:]) != 1 {
		s.guard.fail(client)
		s.logger.Warn("administrator login failed", "client", client, "reason", "token")
		time.Sleep(250 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "invalid administrator token")
		return
	}
	secret, lastStep, totpEnabled, err := s.store.AdminTOTP(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	if totpEnabled {
		if strings.TrimSpace(request.Code) == "" {
			writeErrorCode(w, http.StatusUnauthorized, "totp_required", "two-step verification code required")
			return
		}
		if wait := s.guard.totpRetryAfter(); wait > 0 {
			s.logger.Warn("administrator login refused", "client", client, "reason", "totp_locked", "retry_after", wait.Round(time.Second).String())
			writeRetry(w, wait, "totp_locked", "too many invalid two-step verification codes")
			return
		}
		step, valid := totp.Verify(secret, request.Code, time.Now(), lastStep)
		if valid {
			if valid, err = s.store.ConsumeAdminTOTPStep(r.Context(), step); err != nil {
				s.internalError(w, err)
				return
			}
		}
		if !valid {
			s.guard.fail(client)
			s.guard.failTOTP()
			s.logger.Warn("administrator login failed", "client", client, "reason", "totp")
			writeErrorCode(w, http.StatusUnauthorized, "totp_invalid", "invalid two-step verification code")
			return
		}
	}
	s.guard.succeed(client)
	sessionToken, err := secureToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	csrf, err := secureToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	active := session{csrf: csrf, expiresAt: time.Now().UTC().Add(sessionLifetime)}
	s.sessions.put(sessionToken, active)
	http.SetCookie(w, &http.Cookie{Name: s.sessionCookieName(), Value: sessionToken, Path: "/", Expires: active.expiresAt,
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteStrictMode})
	s.logger.Info("administrator logged in", "client", client, "two_step", totpEnabled)
	writeJSON(w, http.StatusOK, s.sessionInfo(active, totpEnabled))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(s.sessionCookieName()); err == nil {
		s.sessions.delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: s.sessionCookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) {
	active := r.Context().Value(sessionContextKey{}).(session)
	_, _, totpEnabled, err := s.store.AdminTOTP(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.sessionInfo(active, totpEnabled))
}

func (s *Server) sessionInfo(active session, totpEnabled bool) map[string]any {
	return map[string]any{
		"csrf_token": active.csrf, "expires_at": active.expiresAt, "version": buildinfo.Version,
		"agent_version": buildinfo.AgentVersion, "totp_enabled": totpEnabled, "geoip_provider": s.geoIPProvider,
	}
}

// put stores a new session after dropping expired ones. When the limit is
// reached, the session closest to expiry is replaced.
func (s *sessionStore) put(token string, value session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	oldest := ""
	for key, existing := range s.sessions {
		if now.After(existing.expiresAt) {
			delete(s.sessions, key)
			continue
		}
		if oldest == "" || existing.expiresAt.Before(s.sessions[oldest].expiresAt) {
			oldest = key
		}
	}
	if len(s.sessions) >= maxSessions && oldest != "" {
		delete(s.sessions, oldest)
	}
	s.sessions[token] = value
}

func (s *sessionStore) get(token string) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.sessions[token]
	if !ok || time.Now().UTC().After(value.expiresAt) {
		delete(s.sessions, token)
		return session{}, false
	}
	return value, true
}

// update replaces a session that still exists.
func (s *sessionStore) update(token string, value session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[token]; ok {
		s.sessions[token] = value
	}
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// deleteOthers ends every session except keep.
func (s *sessionStore) deleteOthers(keep string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.sessions {
		if key != keep {
			delete(s.sessions, key)
		}
	}
}
