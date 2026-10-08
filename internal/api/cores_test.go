package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KurisuT7/Portolan/internal/cores/corestest"
	"github.com/KurisuT7/Portolan/internal/model"
)

func TestCoreTargetIsVerifiedAndServedToAgents(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	cookie, csrf := adminSession(t, server)
	admin := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(method, path, []byte(body), cookie, csrf))
		return recorder
	}

	releases := admin(http.MethodGet, "/api/v1/cores/sing-box/releases", "")
	if releases.Code != http.StatusOK || !bytes.Contains(releases.Body.Bytes(), []byte(`"version":"1.14.3"`)) {
		t.Fatalf("releases: %d %s", releases.Code, releases.Body.String())
	}
	if recorder := admin(http.MethodPut, "/api/v1/cores/sing-box", `{"version":"1.13.0"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unsupported version accepted: %d", recorder.Code)
	}
	if recorder := admin(http.MethodPut, "/api/v1/cores/unknown", `{"version":"1.14.3"}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown core accepted: %d", recorder.Code)
	}
	selected := admin(http.MethodPut, "/api/v1/cores/sing-box", `{"version":"1.14.3"}`)
	if selected.Code != http.StatusOK {
		t.Fatalf("select target: %d %s", selected.Code, selected.Body.String())
	}
	listed := admin(http.MethodGet, "/api/v1/cores", "")
	var cores struct {
		Targets []model.CoreTarget `json:"targets"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &cores); err != nil || len(cores.Targets) != 1 || cores.Targets[0].Version != "1.14.3" {
		t.Fatalf("cores: %s", listed.Body.String())
	}

	created, enrollmentToken, err := server.store.CreateServer(context.Background(), model.Server{Name: "Core agent"})
	if err != nil {
		t.Fatal(err)
	}
	_, agentToken, err := server.store.Enroll(context.Background(), enrollmentToken, "198.51.100.71")
	if err != nil {
		t.Fatal(err)
	}
	download := func(path, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-Portolan-Server-ID", created.ID)
		}
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	archive := download("/api/v1/agent/cores/sing-box/1.14.3/arm64", agentToken)
	want := corestest.Archive(model.CoreSingBox, "1.14.3", []byte("sing-box 1.14.3"))
	if archive.Code != http.StatusOK || !bytes.Equal(archive.Body.Bytes(), want) {
		t.Fatalf("agent archive: %d (%d bytes)", archive.Code, archive.Body.Len())
	}
	if recorder := download("/api/v1/agent/cores/sing-box/1.14.3/arm64", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous archive download: %d", recorder.Code)
	}
	if recorder := download("/api/v1/agent/cores/sing-box/1.14.2/arm64", agentToken); recorder.Code != http.StatusNotFound {
		t.Fatalf("unselected version served: %d", recorder.Code)
	}

	_, installToken, err := server.store.RotateEnrollmentToken(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recorder := download("/api/v1/agent/downloads/"+installToken+"/cores/sing-box/amd64", ""); recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), want) {
		t.Fatalf("installer archive: %d", recorder.Code)
	}
	if recorder := download("/api/v1/agent/downloads/expired/cores/sing-box/amd64", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("installer archive without enrollment: %d", recorder.Code)
	}
}

func TestCoreUpdatesReachOnlyAgentsThatReportRuntime(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()
	if err := server.store.SetCoreTarget(ctx, model.CoreTarget{Core: model.CoreRealm, Version: "2.9.6",
		SHA256: map[string]string{"amd64": strings.Repeat("a", 64), "arm64": strings.Repeat("b", 64)}}); err != nil {
		t.Fatal(err)
	}
	enroll := func(name, realmVersion string) model.Server {
		created, token, err := server.store.CreateServer(ctx, model.Server{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := server.store.Enroll(ctx, token, "198.51.100.72"); err != nil {
			t.Fatal(err)
		}
		if realmVersion != "" {
			status := model.RuntimeStatus{SingBoxVersion: "1.14.2", RealmVersion: realmVersion, Units: []model.UnitStatus{}}
			if err := server.store.SaveRuntimeStatus(ctx, created.ID, status); err != nil {
				t.Fatal(err)
			}
		}
		return created
	}
	legacy := enroll("Legacy agent", "")
	outdated := enroll("Outdated", "2.9.4")
	enroll("Current", "2.9.6")
	cookie, csrf := adminSession(t, server)
	post := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, path, nil, cookie, csrf))
		return recorder
	}
	if recorder := post("/api/v1/servers/" + legacy.ID + "/cores/realm"); recorder.Code != http.StatusConflict {
		t.Fatalf("legacy Agent update: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := post("/api/v1/servers/" + outdated.ID + "/cores/sing-box"); recorder.Code != http.StatusConflict {
		t.Fatalf("update without a target: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := post("/api/v1/servers/" + outdated.ID + "/cores/realm"); recorder.Code != http.StatusAccepted {
		t.Fatalf("single update: %d %s", recorder.Code, recorder.Body.String())
	}
	rollout := post("/api/v1/cores/realm/rollout")
	if rollout.Code != http.StatusAccepted || strings.TrimSpace(rollout.Body.String()) != `{"queued":1}` {
		t.Fatalf("rollout: %d %s", rollout.Code, rollout.Body.String())
	}
	jobs, err := server.store.LatestUpdates(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].ServerID != outdated.ID || jobs[0].Type != "update-realm" {
		t.Fatalf("queued jobs = %#v err=%v", jobs, err)
	}
}

func TestPublicUpdateResultOnlyReturnsFixedMessages(t *testing.T) {
	if got := publicUpdateResult("update-realm", "failed", `{"message":"新核心没有正常运行，已换回原版本。","detail":"secret"}`); got != `{"message":"新核心没有正常运行，已换回原版本。"}` {
		t.Fatalf("known result = %s", got)
	}
	if got := publicUpdateResult("update-sing-box", "failed", `{"message":"psk=secret"}`); strings.Contains(got, "secret") {
		t.Fatalf("unsafe result exposed: %s", got)
	}
	if got := publicUpdateResult("update-agent", "failed", `{"message":"新版本 Agent 没有正常连接面板，已换回原版本。"}`); got != `{"message":"新版本 Agent 没有正常连接面板，已换回原版本。"}` {
		t.Fatalf("known Agent result = %s", got)
	}
	if got := publicUpdateResult("update-agent", "failed", `{"message":"新核心没有正常运行，已换回原版本。"}`); !strings.Contains(got, "Agent 更新失败") {
		t.Fatalf("core message accepted for an Agent update: %s", got)
	}
	if got := publicUpdateResult("update-sing-box", "succeeded", `{"message":""}`); got != "" {
		t.Fatalf("success result = %s", got)
	}
}
