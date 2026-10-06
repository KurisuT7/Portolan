package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KurisuT7/Portolan/internal/totp"
)

const testAdminToken = "this-is-a-long-random-admin-token"

type loginResult struct {
	code   int
	cookie *http.Cookie
	csrf   string
	body   map[string]any
}

func loginAs(t *testing.T, handler http.Handler, remote, token, code string) loginResult {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token, "code": code})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/session", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = remote
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	result := loginResult{code: recorder.Code}
	_ = json.Unmarshal(recorder.Body.Bytes(), &result.body)
	if cookies := recorder.Result().Cookies(); len(cookies) == 1 {
		result.cookie = cookies[0]
	}
	if csrf, ok := result.body["csrf_token"].(string); ok {
		result.csrf = csrf
	}
	return result
}

func TestFailedLoginsLockOnlyTheClient(t *testing.T) {
	t.Parallel()
	handler := newTestServer(t).Handler()
	for range maxLoginFailures {
		if result := loginAs(t, handler, "203.0.113.50:40000", "wrong-token", ""); result.code != http.StatusUnauthorized {
			t.Fatalf("failed login returned %d", result.code)
		}
	}
	locked := loginAs(t, handler, "203.0.113.50:40001", testAdminToken, "")
	if locked.code != http.StatusTooManyRequests || locked.body["code"] != "login_locked" || locked.cookie != nil {
		t.Fatalf("locked client was not refused: %d %v", locked.code, locked.body)
	}
	if other := loginAs(t, handler, "198.51.100.60:40000", testAdminToken, ""); other.code != http.StatusOK {
		t.Fatalf("another client was refused: %d %v", other.code, other.body)
	}
}

func TestLoginGuardUnlocksAfterLockout(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	guard := newLoginGuard()
	guard.now = func() time.Time { return now }
	for range maxLoginFailures {
		guard.fail("2001:db8:1:2::10")
	}
	if guard.retryAfter("2001:db8:1:2::99") <= 0 {
		t.Fatal("an address in the same IPv6 /64 was not locked")
	}
	now = now.Add(loginLockout + time.Second)
	if wait := guard.retryAfter("2001:db8:1:2::10"); wait > 0 {
		t.Fatalf("client still locked %s after the lockout", wait)
	}
	guard.fail("203.0.113.7")
	guard.succeed("203.0.113.7")
	if _, tracked := guard.clients["203.0.113.7"]; tracked {
		t.Fatal("a successful login kept the failure count")
	}
}

func TestTwoStepVerificationLifecycle(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	handler := server.Handler()
	first := loginAs(t, handler, "203.0.113.20:40000", testAdminToken, "")
	if first.code != http.StatusOK || first.body["totp_enabled"] != false {
		t.Fatalf("login returned %d %v", first.code, first.body)
	}

	setup := httptest.NewRecorder()
	handler.ServeHTTP(setup, authenticatedRequest(http.MethodPost, "/api/v1/security/totp/setup", nil, first.cookie, first.csrf))
	var pending struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
		QR     struct {
			Size int      `json:"size"`
			Rows []string `json:"rows"`
		} `json:"qr"`
	}
	if err := json.Unmarshal(setup.Body.Bytes(), &pending); err != nil || setup.Code != http.StatusOK {
		t.Fatalf("setup returned %d: %s", setup.Code, setup.Body.String())
	}
	if !strings.HasPrefix(pending.URI, "otpauth://totp/Portolan:") || pending.QR.Size < 21 || len(pending.QR.Rows) != pending.QR.Size {
		t.Fatalf("unexpected setup response %+v", pending)
	}

	step := totp.Step(time.Now())
	code := func(offset int64) string {
		value, err := totp.Code(pending.Secret, step+offset)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	enable := httptest.NewRecorder()
	handler.ServeHTTP(enable, authenticatedRequest(http.MethodPost, "/api/v1/security/totp",
		[]byte(`{"code":"`+code(-1)+`"}`), first.cookie, first.csrf))
	if enable.Code != http.StatusNoContent {
		t.Fatalf("enable returned %d: %s", enable.Code, enable.Body.String())
	}

	if required := loginAs(t, handler, "203.0.113.20:40000", testAdminToken, ""); required.code != http.StatusUnauthorized || required.body["code"] != "totp_required" {
		t.Fatalf("login without a code returned %d %v", required.code, required.body)
	}
	if reused := loginAs(t, handler, "203.0.113.20:40000", testAdminToken, code(-1)); reused.code != http.StatusUnauthorized || reused.body["code"] != "totp_invalid" {
		t.Fatalf("the enabling code was accepted again: %d %v", reused.code, reused.body)
	}
	second := loginAs(t, handler, "203.0.113.20:40000", testAdminToken, code(0))
	if second.code != http.StatusOK || second.body["totp_enabled"] != true {
		t.Fatalf("login with a valid code returned %d %v", second.code, second.body)
	}
	if replay := loginAs(t, handler, "203.0.113.20:40000", testAdminToken, code(0)); replay.code != http.StatusUnauthorized {
		t.Fatalf("a code was accepted twice: %d", replay.code)
	}
	// The session that enabled two-step verification stays valid.
	current := httptest.NewRecorder()
	handler.ServeHTTP(current, authenticatedRequest(http.MethodGet, "/api/v1/session", nil, first.cookie, ""))
	if current.Code != http.StatusOK {
		t.Fatalf("the session that enabled two-step verification was ended: %d", current.Code)
	}

	wrong := httptest.NewRecorder()
	handler.ServeHTTP(wrong, authenticatedRequest(http.MethodDelete, "/api/v1/security/totp", []byte(`{"code":"000000"}`), second.cookie, second.csrf))
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("disable with a wrong code returned %d", wrong.Code)
	}
	disable := httptest.NewRecorder()
	handler.ServeHTTP(disable, authenticatedRequest(http.MethodDelete, "/api/v1/security/totp",
		[]byte(`{"code":"`+code(1)+`"}`), second.cookie, second.csrf))
	if disable.Code != http.StatusNoContent {
		t.Fatalf("disable returned %d: %s", disable.Code, disable.Body.String())
	}
	if plain := loginAs(t, handler, "203.0.113.20:40000", testAdminToken, ""); plain.code != http.StatusOK {
		t.Fatalf("login after disabling returned %d %v", plain.code, plain.body)
	}
}

func TestEnablingTwoStepVerificationEndsOtherSessions(t *testing.T) {
	t.Parallel()
	handler := newTestServer(t).Handler()
	owner := loginAs(t, handler, "203.0.113.21:40000", testAdminToken, "")
	other := loginAs(t, handler, "203.0.113.22:40000", testAdminToken, "")
	setup := httptest.NewRecorder()
	handler.ServeHTTP(setup, authenticatedRequest(http.MethodPost, "/api/v1/security/totp/setup", nil, owner.cookie, owner.csrf))
	var pending struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(setup.Body.Bytes(), &pending)
	code, _ := totp.Code(pending.Secret, totp.Step(time.Now()))
	enable := httptest.NewRecorder()
	handler.ServeHTTP(enable, authenticatedRequest(http.MethodPost, "/api/v1/security/totp", []byte(`{"code":"`+code+`"}`), owner.cookie, owner.csrf))
	if enable.Code != http.StatusNoContent {
		t.Fatalf("enable returned %d: %s", enable.Code, enable.Body.String())
	}
	check := httptest.NewRecorder()
	handler.ServeHTTP(check, authenticatedRequest(http.MethodGet, "/api/v1/session", nil, other.cookie, ""))
	if check.Code != http.StatusUnauthorized {
		t.Fatalf("another session survived enabling two-step verification: %d", check.Code)
	}
}

func TestSecureCookiesUseHostPrefix(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.secureCookies = true
	result := loginAs(t, server.Handler(), "203.0.113.23:40000", testAdminToken, "")
	if result.code != http.StatusOK || result.cookie == nil || result.cookie.Name != "__Host-portolan_session" ||
		!result.cookie.Secure || !result.cookie.HttpOnly || result.cookie.Path != "/" {
		t.Fatalf("unexpected session cookie %+v", result.cookie)
	}
}

func TestWeakAdminTokensAreRejected(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"short-token", strings.Repeat("a", 40), "passwordpasswordpasswordpassword"} {
		if err := ValidateAdminToken(token); err == nil {
			t.Errorf("token %q was accepted", token)
		}
	}
	if err := ValidateAdminToken("q4Jx0ZtKw8mVb2cH7nLr5sYd1gPf9aEu3iTo6+/="); err != nil {
		t.Fatalf("a random token was rejected: %v", err)
	}
}

func TestClientIPTrustsOnlyConfiguredProxies(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.RemoteAddr = "127.0.0.1:50000"
	request.Header.Set("X-Forwarded-For", "192.0.2.66, 203.0.113.9")
	if got := server.clientIP(request); got != "203.0.113.9" {
		t.Fatalf("client behind the local proxy = %q, want the address the proxy appended", got)
	}
	request.RemoteAddr = "198.51.100.4:50000"
	if got := server.clientIP(request); got != "198.51.100.4" {
		t.Fatalf("an untrusted peer chose its address: %q", got)
	}
	proxies, err := ParseTrustedProxies("172.16.0.0/12, 198.51.100.4")
	if err != nil {
		t.Fatal(err)
	}
	server.trustedProxies = proxies
	if got := server.clientIP(request); got != "203.0.113.9" {
		t.Fatalf("client behind a configured proxy = %q", got)
	}
	if _, err := ParseTrustedProxies("not-an-address"); err == nil {
		t.Fatal("an invalid trusted proxy was accepted")
	}
}
