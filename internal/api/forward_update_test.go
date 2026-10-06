package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

func TestForwardUpdateRequiresSessionAndCSRFAndRetainsID(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	entry, _, err := s.store.CreateServer(ctx, model.Server{Name: "Original server", Address: "203.0.113.5"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.store.CreateForward(ctx, model.Forward{Name: "Original rule", IngressServerID: entry.ID, ListenPort: 25001,
		TargetHost: "example.com", TargetPort: 443, Networks: []string{"tcp"}, Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	s.sessions.put("test-session", session{csrf: "test-csrf", expiresAt: time.Now().Add(time.Hour)})
	cookie := &http.Cookie{Name: "portolan_session", Value: "test-session"}
	f.Name, f.TargetPort, f.Engine = "Edited rule", 8443, model.ForwardRealm
	body, _ := json.Marshal(f)
	for _, tc := range []struct {
		csrf string
		want int
	}{{"", http.StatusForbidden}, {"test-csrf", http.StatusOK}} {
		r := authenticatedRequest(http.MethodPut, "/api/v1/forwards/"+f.ID, body, cookie, tc.csrf)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("update status=%d body=%s", w.Code, w.Body.String())
		}
		if tc.want == http.StatusOK {
			var updated model.Forward
			if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
				t.Fatal(err)
			}
			if updated.ID != f.ID || updated.Name != f.Name || updated.TargetPort != 8443 || !updated.CreatedAt.Equal(f.CreatedAt) {
				t.Fatalf("wrong update: %#v", updated)
			}
		}
	}
	job, err := s.store.NextJob(ctx, entry.ID)
	if err != nil || job == nil {
		t.Fatalf("missing sync job: %#v %v", job, err)
	}
	if err := s.store.CompleteJob(ctx, entry.ID, job.ID, false, `private-key=do-not-display`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/config-status", "/api/v1/servers"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, authenticatedRequest(http.MethodGet, path, nil, cookie, ""))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if path == "/api/v1/config-status" {
			if bytes.Contains(w.Body.Bytes(), []byte("private-key")) || !bytes.Contains(w.Body.Bytes(), []byte("配置未能应用")) {
				t.Fatalf("config status exposed an unsafe result: %s", w.Body.String())
			}
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(http.MethodPost, "/api/v1/servers/"+entry.ID+"/sync", nil, cookie, "test-csrf"))
	if w.Code != http.StatusAccepted {
		t.Fatalf("sync: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/forwards/"+f.ID, bytes.NewReader(body)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated PUT accepted: %d", w.Code)
	}
}

func TestObservedAgentStatusUsesHeartbeatFreshness(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		label, state string
		at           time.Time
		want         string
	}{
		{"fresh", "online", now.Add(-time.Minute), "online"},
		{"expired", "online", now.Add(-2 * time.Minute), "offline"},
		{"future", "online", now.Add(time.Minute), "offline"},
		{"missing", "online", time.Time{}, "offline"},
		{"unenrolled", "pending", time.Time{}, "pending"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			if got := observedAgentStatus(model.Server{AgentStatus: tc.state, LastSeenAt: tc.at}, now); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestPublicConfigResultOnlyReturnsFixedMessages(t *testing.T) {
	known := `{"message":"核心配置校验失败，未切换到新配置。","private":"do-not-display"}`
	if got := publicConfigResult("failed", known); got != `{"message":"核心配置校验失败，未切换到新配置。"}` {
		t.Fatalf("known result = %s", got)
	}
	for _, result := range []string{`private-key=do-not-display`, `{"message":"private-key=do-not-display"}`} {
		got := publicConfigResult("failed", result)
		if bytes.Contains([]byte(got), []byte("private-key")) || !bytes.Contains([]byte(got), []byte("配置未能应用")) {
			t.Fatalf("unsafe result was exposed: %s", got)
		}
	}
	if got := publicConfigResult("succeeded", `{"message":"private-key=do-not-display"}`); got != "" {
		t.Fatalf("success result should be empty: %s", got)
	}
}
