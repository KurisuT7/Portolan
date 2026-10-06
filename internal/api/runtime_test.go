package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KurisuT7/Portolan/internal/model"
)

func adminSession(t *testing.T, server *Server) (*http.Cookie, string) {
	t.Helper()
	login := httptest.NewRequest(http.MethodPost, "/api/v1/session", bytes.NewReader([]byte(`{"token":"this-is-a-long-random-admin-token"}`)))
	login.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, login)
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &session); err != nil || session.CSRF == "" {
		t.Fatalf("login failed: %d %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Result().Cookies()[0], session.CSRF
}

func TestAgentStatusReportIsValidatedAndExposedWithServer(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	created, enrollmentToken, err := server.store.CreateServer(context.Background(), model.Server{Name: "Runtime"})
	if err != nil {
		t.Fatal(err)
	}
	_, agentToken, err := server.store.Enroll(context.Background(), enrollmentToken, "198.51.100.70")
	if err != nil {
		t.Fatal(err)
	}
	report := func(units []model.UnitStatus) int {
		body, _ := json.Marshal(model.RuntimeStatus{AppliedRevision: 7, SingBoxVersion: "1.14.2", RealmVersion: "2.9.4", Units: units})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/status", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+agentToken)
		request.Header.Set("X-Portolan-Server-ID", created.ID)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder.Code
	}
	if code := report([]model.UnitStatus{{Name: "sshd.service", ActiveState: "active", SubState: "running"}}); code != http.StatusBadRequest {
		t.Fatalf("unmanaged unit accepted: %d", code)
	}
	if code := report(nil); code != http.StatusBadRequest {
		t.Fatalf("missing unit list accepted: %d", code)
	}
	if code := report([]model.UnitStatus{{Name: "portolan-realm@fwd_1.service", ActiveState: "failed", SubState: "failed"}}); code != http.StatusNoContent {
		t.Fatalf("runtime report returned %d", code)
	}

	cookie, _ := adminSession(t, server)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/servers", nil, cookie, ""))
	var listed struct {
		Items []model.Server `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 {
		t.Fatalf("server list: %s", recorder.Body.String())
	}
	runtime := listed.Items[0].Runtime
	if runtime == nil || runtime.RealmVersion != "2.9.4" || len(runtime.Units) != 1 || runtime.Units[0].ActiveState != "failed" {
		t.Fatalf("runtime = %#v", runtime)
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("core_channel")) {
		t.Fatalf("removed channel field is still exposed: %s", recorder.Body.String())
	}
}

func TestRealityCreationOffersOnlyClientCompatibleTransports(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	created, _, err := server.store.CreateServer(context.Background(), model.Server{Name: "Reality", Address: "203.0.113.20"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := adminSession(t, server)
	create := func(port int, flow, transport string, settings map[string]string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"server_id": created.ID, "name": "Reality " + transport, "protocol": "vless-reality", "listen_port": port,
			"reality": map[string]any{"flow": flow, "handshake_server": "aws.amazon.com", "transport": transport, "transport_settings": settings}})
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/v1/nodes", body, cookie, csrf))
		return recorder
	}
	if recorder := create(25001, "", "ws", map[string]string{"path": "/edge"}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("WebSocket Reality accepted: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := create(25002, "xtls-rprx-vision", "grpc", nil); recorder.Code != http.StatusBadRequest {
		t.Fatalf("Vision over gRPC accepted: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := create(25003, "", "grpc", map[string]string{"service_name": "edge"}); recorder.Code != http.StatusCreated ||
		!bytes.Contains(recorder.Body.Bytes(), []byte("serviceName=edge")) {
		t.Fatalf("gRPC Reality: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := create(25004, "xtls-rprx-vision", "tcp", nil); recorder.Code != http.StatusCreated {
		t.Fatalf("Vision over TCP: %d %s", recorder.Code, recorder.Body.String())
	}
}
