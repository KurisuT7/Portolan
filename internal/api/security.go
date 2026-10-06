package api

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KurisuT7/portolan/internal/totp"
	"rsc.io/qr"
)

const (
	loginFailureWindow = 15 * time.Minute
	// maxLoginFailures failed attempts from one client lock it for loginLockout.
	maxLoginFailures = 5
	loginLockout     = 15 * time.Minute
	// maxTOTPFailures invalid codes from all clients together lock two-step
	// verification. Reaching this step requires the administrator token, so the
	// lock only delays someone who already holds it.
	maxTOTPFailures   = 10
	maxTrackedClients = 4096
	pendingTOTPExpiry = 10 * time.Minute
)

type failureCount struct {
	count       int
	since       time.Time
	lockedUntil time.Time
}

// record counts one failure and starts a lockout once limit is reached.
func (f *failureCount) record(now time.Time, limit int) {
	if now.Sub(f.since) > loginFailureWindow {
		f.count, f.since = 0, now
	}
	f.count++
	if f.count >= limit {
		f.lockedUntil = now.Add(loginLockout)
		f.count, f.since = 0, now
	}
}

func (f *failureCount) expired(now time.Time) bool {
	return now.After(f.lockedUntil) && now.Sub(f.since) > loginFailureWindow
}

// loginGuard limits failed administrator logins per client and failed
// two-step verification codes across all clients.
type loginGuard struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]*failureCount
	totp    failureCount
}

func newLoginGuard() *loginGuard {
	return &loginGuard{now: time.Now, clients: map[string]*failureCount{}}
}

// clientKey groups IPv6 clients by /64, the smallest block a host usually controls.
func clientKey(client string) string {
	address, err := netip.ParseAddr(client)
	if err != nil {
		return client
	}
	address = address.Unmap()
	if address.Is6() {
		prefix, _ := address.Prefix(64)
		return prefix.String()
	}
	return address.String()
}

func (g *loginGuard) retryAfter(client string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if entry, ok := g.clients[clientKey(client)]; ok {
		return entry.lockedUntil.Sub(g.now())
	}
	return 0
}

func (g *loginGuard) fail(client string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	key := clientKey(client)
	entry, ok := g.clients[key]
	if !ok {
		if len(g.clients) >= maxTrackedClients {
			g.evict(now)
		}
		entry = &failureCount{since: now}
		g.clients[key] = entry
	}
	entry.record(now, maxLoginFailures)
}

// evict drops expired entries, then the entry with the oldest window.
func (g *loginGuard) evict(now time.Time) {
	oldest := ""
	for key, entry := range g.clients {
		if entry.expired(now) {
			delete(g.clients, key)
			continue
		}
		if oldest == "" || entry.since.Before(g.clients[oldest].since) {
			oldest = key
		}
	}
	if len(g.clients) >= maxTrackedClients && oldest != "" {
		delete(g.clients, oldest)
	}
}

func (g *loginGuard) succeed(client string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.clients, clientKey(client))
}

func (g *loginGuard) totpRetryAfter() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.totp.lockedUntil.Sub(g.now())
}

func (g *loginGuard) failTOTP() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.totp.record(g.now(), maxTOTPFailures)
}

func writeRetry(w http.ResponseWriter, wait time.Duration, code, message string) {
	seconds := int(wait.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": message, "code": code, "retry_after": seconds})
}

type qrMatrix struct {
	Size int      `json:"size"`
	Rows []string `json:"rows"`
}

// setupTOTP creates a secret for this session. It is stored only after the
// administrator confirms a code from the authenticator app.
func (s *Server) setupTOTP(w http.ResponseWriter, r *http.Request) {
	if _, _, enabled, err := s.store.AdminTOTP(r.Context()); err != nil {
		s.internalError(w, err)
		return
	} else if enabled {
		writeError(w, http.StatusConflict, "two-step verification is already enabled")
		return
	}
	secret, err := totp.NewSecret()
	if err != nil {
		s.internalError(w, err)
		return
	}
	uri := totp.URI("Portolan", s.totpAccount(r), secret)
	code, err := qr.Encode(uri, qr.M)
	if err != nil {
		s.internalError(w, err)
		return
	}
	matrix := qrMatrix{Size: code.Size, Rows: make([]string, code.Size)}
	for y := range code.Size {
		var row strings.Builder
		for x := range code.Size {
			if code.Black(x, y) {
				row.WriteByte('1')
			} else {
				row.WriteByte('0')
			}
		}
		matrix.Rows[y] = row.String()
	}
	token, active := r.Context().Value(sessionTokenContextKey{}).(string), r.Context().Value(sessionContextKey{}).(session)
	active.pendingTOTP, active.pendingTOTPExpires = secret, time.Now().UTC().Add(pendingTOTPExpiry)
	s.sessions.update(token, active)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret, "uri": uri, "qr": matrix})
}

// enableTOTP stores the pending secret once a code generated from it is valid,
// then ends every other session.
func (s *Server) enableTOTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, active := r.Context().Value(sessionTokenContextKey{}).(string), r.Context().Value(sessionContextKey{}).(session)
	if active.pendingTOTP == "" || time.Now().UTC().After(active.pendingTOTPExpires) {
		writeErrorCode(w, http.StatusConflict, "totp_setup_expired", "two-step verification setup expired; start again")
		return
	}
	step, valid := totp.Verify(active.pendingTOTP, request.Code, time.Now(), 0)
	if !valid {
		writeErrorCode(w, http.StatusBadRequest, "totp_invalid", "invalid two-step verification code")
		return
	}
	if err := s.store.EnableAdminTOTP(r.Context(), active.pendingTOTP, step); err != nil {
		s.internalError(w, err)
		return
	}
	active.pendingTOTP, active.pendingTOTPExpires = "", time.Time{}
	s.sessions.update(token, active)
	s.sessions.deleteOthers(token)
	s.logger.Info("administrator two-step verification enabled", "client", s.clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// disableTOTP requires a current code, so a stolen session alone cannot remove
// the second factor.
func (s *Server) disableTOTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	client := s.clientIP(r)
	if wait := s.guard.retryAfter(client); wait > 0 {
		writeRetry(w, wait, "login_locked", "too many failed attempts")
		return
	}
	if wait := s.guard.totpRetryAfter(); wait > 0 {
		writeRetry(w, wait, "totp_locked", "too many invalid two-step verification codes")
		return
	}
	secret, lastStep, enabled, err := s.store.AdminTOTP(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	if !enabled {
		w.WriteHeader(http.StatusNoContent)
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
		s.logger.Warn("two-step verification change refused", "client", client, "reason", "totp")
		writeErrorCode(w, http.StatusBadRequest, "totp_invalid", "invalid two-step verification code")
		return
	}
	if err := s.store.DisableAdminTOTP(r.Context()); err != nil {
		s.internalError(w, err)
		return
	}
	s.logger.Info("administrator two-step verification disabled", "client", client)
	w.WriteHeader(http.StatusNoContent)
}

// totpAccount names the panel in authenticator apps by its public host.
func (s *Server) totpAccount(r *http.Request) string {
	if parsed, err := url.Parse(s.externalURL(r)); err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	if host, _, err := net.SplitHostPort(r.Host); err == nil && host != "" {
		return host
	}
	if r.Host != "" {
		return r.Host
	}
	return "admin"
}
