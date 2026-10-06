package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KurisuT7/portolan/internal/agentproto"
	"github.com/KurisuT7/portolan/internal/cores/corestest"
	"github.com/KurisuT7/portolan/internal/model"
	"github.com/KurisuT7/portolan/internal/store"
	"github.com/KurisuT7/portolan/internal/vault"
)

func TestAdminSessionServerAndNodeFlow(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	loginBody := []byte(`{"token":"this-is-a-long-random-admin-token"}`)
	login := httptest.NewRequest(http.MethodPost, "/api/v1/session", bytes.NewReader(loginBody))
	login.Header.Set("Content-Type", "application/json")
	loginRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(loginRecorder, login)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login returned %d: %s", loginRecorder.Code, loginRecorder.Body.String())
	}
	var sessionResponse struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginRecorder.Body.Bytes(), &sessionResponse); err != nil {
		t.Fatal(err)
	}
	cookies := loginRecorder.Result().Cookies()
	if len(cookies) != 1 || sessionResponse.CSRF == "" {
		t.Fatal("login did not return a session cookie and CSRF token")
	}

	serverJSON := []byte(`{"name":"Edge A","address":"203.0.113.9","region":"IN"}`)
	withoutCSRF := authenticatedRequest(http.MethodPost, "/api/v1/servers", serverJSON, cookies[0], "")
	withoutCSRFRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(withoutCSRFRecorder, withoutCSRF)
	if withoutCSRFRecorder.Code != http.StatusForbidden {
		t.Fatalf("mutation without CSRF returned %d", withoutCSRFRecorder.Code)
	}

	createServer := authenticatedRequest(http.MethodPost, "/api/v1/servers", serverJSON, cookies[0], sessionResponse.CSRF)
	createServerRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(createServerRecorder, createServer)
	if createServerRecorder.Code != http.StatusCreated {
		t.Fatalf("server creation returned %d: %s", createServerRecorder.Code, createServerRecorder.Body.String())
	}
	var created struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
		EnrollmentToken string `json:"enrollment_token"`
	}
	if err := json.Unmarshal(createServerRecorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Server.ID == "" || created.EnrollmentToken == "" {
		t.Fatalf("incomplete server response: %s", createServerRecorder.Body.String())
	}
	rotate := authenticatedRequest(http.MethodPost, "/api/v1/servers/"+created.Server.ID+"/enrollment", nil, cookies[0], sessionResponse.CSRF)
	rotateRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(rotateRecorder, rotate)
	if rotateRecorder.Code != http.StatusCreated || !bytes.Contains(rotateRecorder.Body.Bytes(), []byte(`"enrollment_hint"`)) {
		t.Fatalf("enrollment regeneration returned %d: %s", rotateRecorder.Code, rotateRecorder.Body.String())
	}

	nodeJSON, _ := json.Marshal(map[string]any{
		"server_id": created.Server.ID, "name": "SS 2022", "protocol": "shadowsocks", "listen_port": 24443,
		"shadowsocks": map[string]any{"method": "2022-blake3-aes-256-gcm", "allow_insecure": false},
	})
	createNode := authenticatedRequest(http.MethodPost, "/api/v1/nodes", nodeJSON, cookies[0], sessionResponse.CSRF)
	createNodeRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(createNodeRecorder, createNode)
	if createNodeRecorder.Code != http.StatusCreated {
		t.Fatalf("node creation returned %d: %s", createNodeRecorder.Code, createNodeRecorder.Body.String())
	}
	if !bytes.Contains(createNodeRecorder.Body.Bytes(), []byte(`"ss://`)) {
		t.Fatalf("node response did not contain a client export: %s", createNodeRecorder.Body.String())
	}

	snellJSON, _ := json.Marshal(map[string]any{
		"server_id": created.Server.ID, "name": "Snell", "protocol": "snell", "listen_port": 24444,
		"snell": map[string]any{"version": 6, "mode": "default", "allow_insecure": false},
	})
	createSnell := authenticatedRequest(http.MethodPost, "/api/v1/nodes", snellJSON, cookies[0], sessionResponse.CSRF)
	createSnellRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(createSnellRecorder, createSnell)
	if createSnellRecorder.Code != http.StatusCreated {
		t.Fatalf("Snell capability should be managed automatically, got %d: %s", createSnellRecorder.Code, createSnellRecorder.Body.String())
	}
}

func TestAgentBootstrapReturnsToShellForTemporaryFileCleanup(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	downloads := t.TempDir()
	for _, name := range []string{"portolan-agent-linux-amd64", "portolan-agent-linux-arm64"} {
		if err := os.WriteFile(filepath.Join(downloads, name), []byte("agent-binary"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	server.publicURL = "https://panel.example.com"
	server.downloadsDir = downloads
	_, token, err := server.store.CreateServer(context.Background(), model.Server{Name: "Bootstrap cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	unready := httptest.NewRecorder()
	server.Handler().ServeHTTP(unready, httptest.NewRequest(http.MethodGet, "/api/v1/agent/bootstrap/"+token, nil))
	if unready.Code != http.StatusServiceUnavailable {
		t.Fatalf("bootstrap without selected cores returned %d", unready.Code)
	}
	for _, target := range []model.CoreTarget{
		{Core: model.CoreSingBox, Version: "1.14.3", SHA256: map[string]string{"amd64": strings.Repeat("a", 64), "arm64": strings.Repeat("b", 64)}},
		{Core: model.CoreRealm, Version: "2.9.6", SHA256: map[string]string{"amd64": strings.Repeat("c", 64), "arm64": strings.Repeat("d", 64)}},
	} {
		if err := server.store.SetCoreTarget(context.Background(), target); err != nil {
			t.Fatal(err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/bootstrap/"+token, nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("bootstrap returned %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.Bytes()
	if !bytes.Contains(body, []byte(`trap 'rm -f -- "$tmp"' EXIT HUP INT TERM`)) {
		t.Fatal("bootstrap is missing temporary file cleanup trap")
	}
	if bytes.Contains(body, []byte(`exec sh "$tmp"`)) {
		t.Fatal("bootstrap must not replace the shell before its cleanup trap runs")
	}
	if !bytes.Contains(body, []byte(`sh "$tmp" --panel`)) {
		t.Fatal("bootstrap does not invoke the downloaded installer")
	}
	for _, want := range []string{"sing_box_sha=" + strings.Repeat("b", 64), "realm_sha=" + strings.Repeat("c", 64),
		`--sing-box-url "https://panel.example.com/api/v1/agent/downloads/` + token + `/cores/sing-box/$arch"`} {
		if !bytes.Contains(body, []byte(want)) {
			t.Fatalf("bootstrap is missing %q:\n%s", want, body)
		}
	}
}

func TestCreateForwardToServerPortResolvesIPv6Address(t *testing.T) {
	t.Parallel()
	control := newTestServer(t)
	entry, _, err := control.store.CreateServer(context.Background(), model.Server{
		Name: "Entry", Address: "203.0.113.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := control.store.CreateServer(context.Background(), model.Server{
		Name: "Middle", Address: "2001:0db8::20",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"ingress_server_id": entry.ID,
		"target_server_id":  target.ID,
		"name":              "Entry to middle port",
		"listen_port":       2443,
		"target_port":       3443,
		"networks":          []string{"tcp", "udp"},
		"engine":            "sing-box",
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/forwards", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	control.createForward(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("server-port forward creation returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var created model.Forward
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.TargetServerID != target.ID || created.TargetNodeID != "" || created.TargetHost != "2001:db8::20" || created.TargetPort != 3443 {
		t.Fatalf("unexpected server-port target: %#v", created)
	}
}

func TestAgentReportsExistingNodesAsReadOnly(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	created, enrollmentToken, err := server.store.CreateServer(context.Background(), model.Server{Name: "Existing VPS"})
	if err != nil {
		t.Fatal(err)
	}
	_, agentToken, err := server.store.Enroll(context.Background(), enrollmentToken, "198.51.100.91")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"items": []model.Node{{
		Name: "existing ss", Protocol: model.ProtocolShadowsocks, ListenPort: 24443, Enabled: true,
		Source: "sing-box:/etc/sing-box/conf/ss.json", SS: &model.SSSpec{Method: "aes-256-gcm", Password: "existing-secret"},
	}}})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/discovered-nodes", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+agentToken)
	request.Header.Set("X-Portolan-Server-ID", created.ID)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("discovery report returned %d: %s", recorder.Code, recorder.Body.String())
	}
	items, err := server.store.ListNodes(context.Background())
	if err != nil || len(items) != 1 || items[0].Managed {
		t.Fatalf("unexpected discovered nodes: %#v err=%v", items, err)
	}
}

func TestAgentEnrollmentAutoDetectsAddressAndRegion(t *testing.T) {
	t.Parallel()
	server := newTestServerWithRegionLookup(t, func(address string) (string, error) {
		if address != "198.51.100.16" {
			t.Fatalf("region lookup received %q", address)
		}
		return "SG · 新加坡", nil
	})
	created, enrollmentToken, err := server.store.CreateServer(context.Background(), model.Server{Name: "Auto VPS"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"enrollment_token": enrollmentToken,
		"public_addresses": []string{"2001:db8:9:1001::88", "198.51.100.16"},
		"egress_families":  []string{"ipv4", "ipv6"},
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/enroll", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "[2001:db8:9:1001::88]:41234"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("enrollment returned %d: %s", recorder.Code, recorder.Body.String())
	}
	stored, err := server.store.GetServer(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Address != "198.51.100.16" || stored.IPv4Address != "198.51.100.16" ||
		stored.IPv6Address != "2001:db8:9:1001::88" || !stored.EgressIPv4 || !stored.EgressIPv6 ||
		stored.Region != "SG · 新加坡" {
		t.Fatalf("auto-detected server = %#v", stored)
	}
}

func TestAgentEnrollmentExcludesSharedIPv4FromPublicIngress(t *testing.T) {
	t.Parallel()
	server := newTestServerWithRegionLookup(t, func(address string) (string, error) {
		if address != "2001:db8:100:dfd::" {
			t.Fatalf("region lookup received %q", address)
		}
		return "HK", nil
	})
	created, enrollmentToken, err := server.store.CreateServer(context.Background(), model.Server{Name: "relay-cgnat"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"enrollment_token": enrollmentToken,
		"public_addresses": []string{"100.88.13.252", "2001:db8:100:dfd::"},
		"egress_families":  []string{"ipv4", "ipv6"},
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/enroll", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.90:41234"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("enrollment returned %d: %s", recorder.Code, recorder.Body.String())
	}
	stored, err := server.store.GetServer(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Address != "2001:db8:100:dfd::" || stored.IPv4Address != "" ||
		stored.IPv6Address != "2001:db8:100:dfd::" || !stored.EgressIPv4 || !stored.EgressIPv6 {
		t.Fatalf("shared IPv4 was accepted as public ingress: %#v", stored)
	}
}

func TestAuthenticatedAgentAddressReportRepairsExistingManagedForward(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ingress, _, err := server.store.CreateServer(context.Background(), model.Server{
		Name: "edge-hk", Address: "203.0.113.45", IPv4Address: "203.0.113.45", IPv6Address: "2001:db8:e0ce:4b::2",
		EgressIPv4: true, EgressIPv6: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, enrollmentToken, err := server.store.CreateServer(context.Background(), model.Server{
		Name: "relay-cgnat", Address: "100.88.13.252",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, agentToken, err := server.store.Enroll(context.Background(), enrollmentToken, "198.51.100.90")
	if err != nil {
		t.Fatal(err)
	}
	forward, err := server.store.CreateForward(context.Background(), model.Forward{
		IngressServerID: ingress.ID, TargetServerID: target.ID, Name: "hk-to-relay",
		ListenPort: 47383, TargetHost: "100.88.13.252", TargetPort: 32891,
		Networks: []string{"tcp", "udp"}, Engine: model.ForwardSingBox, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/jobs/next?wait=0", nil)
	request.Header.Set("Authorization", "Bearer "+agentToken)
	request.Header.Set("X-Portolan-Server-ID", target.ID)
	request.Header.Set(agentAddressesHeader, "100.88.13.252,2001:db8:100:dfd::")
	request.Header.Set(agentEgressFamiliesHeader, "ipv4,ipv6")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK && recorder.Code != http.StatusNoContent {
		t.Fatalf("address report returned %d: %s", recorder.Code, recorder.Body.String())
	}

	storedTarget, err := server.store.GetServer(context.Background(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTarget.Address != "2001:db8:100:dfd::" || storedTarget.IPv4Address != "" ||
		storedTarget.IPv6Address != "2001:db8:100:dfd::" || !storedTarget.EgressIPv4 || !storedTarget.EgressIPv6 {
		t.Fatalf("target address was not repaired: %#v", storedTarget)
	}
	forwards, err := server.store.ListForwards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range forwards {
		if item.ID == forward.ID && item.TargetHost == "2001:db8:100:dfd::" {
			return
		}
	}
	t.Fatalf("managed forward was not rerouted: %#v", forwards)
}

func TestImmediateForwardProbeRoundTrip(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	created, enrollmentToken, err := server.store.CreateServer(context.Background(), model.Server{Name: "Entry"})
	if err != nil {
		t.Fatal(err)
	}
	_, agentToken, err := server.store.Enroll(context.Background(), enrollmentToken, "198.51.100.80")
	if err != nil {
		t.Fatal(err)
	}
	forward, err := server.store.CreateForward(context.Background(), model.Forward{IngressServerID: created.ID, Name: "now", ListenPort: 25002,
		Networks: []string{"tcp"}, TargetHost: "target.example", TargetPort: 443, Engine: model.ForwardRealm, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	syncJob, err := server.store.NextJob(context.Background(), created.ID)
	if err != nil || syncJob == nil {
		t.Fatalf("missing setup sync job: %#v err=%v", syncJob, err)
	}
	if err := server.store.CompleteJob(context.Background(), created.ID, syncJob.ID, true, "ok"); err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRequest(http.MethodPost, "/api/v1/session", bytes.NewReader([]byte(`{"token":"this-is-a-long-random-admin-token"}`)))
	login.Header.Set("Content-Type", "application/json")
	loginRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(loginRecorder, login)
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginRecorder.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	cookie := loginRecorder.Result().Cookies()[0]
	adminRecorder := httptest.NewRecorder()
	adminDone := make(chan struct{})
	go func() {
		defer close(adminDone)
		request := authenticatedRequest(http.MethodPost, "/api/v1/forwards/"+forward.ID+"/probe", nil, cookie, session.CSRF)
		server.Handler().ServeHTTP(adminRecorder, request)
	}()

	next := httptest.NewRequest(http.MethodGet, "/api/v1/agent/jobs/next?wait=2", nil)
	next.Header.Set("Authorization", "Bearer "+agentToken)
	next.Header.Set("X-Portolan-Server-ID", created.ID)
	nextRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(nextRecorder, next)
	if nextRecorder.Code != http.StatusOK {
		t.Fatalf("long poll did not return probe job: %d %s", nextRecorder.Code, nextRecorder.Body.String())
	}
	var probeJob agentproto.Job
	if err := json.Unmarshal(nextRecorder.Body.Bytes(), &probeJob); err != nil || probeJob.Type != "probe" {
		t.Fatalf("unexpected immediate probe job: %#v err=%v", probeJob, err)
	}
	probe := model.ForwardProbe{ForwardID: forward.ID, CheckedAt: time.Now().UTC(), Attempts: 3, Successes: 3, LatencyMS: 12.4, JitterMS: 0.7, Status: "stable"}
	reportBody, _ := json.Marshal(map[string]any{"items": []model.ForwardProbe{probe}})
	report := httptest.NewRequest(http.MethodPost, "/api/v1/agent/forward-probes", bytes.NewReader(reportBody))
	report.Header.Set("Content-Type", "application/json")
	report.Header.Set("Authorization", "Bearer "+agentToken)
	report.Header.Set("X-Portolan-Server-ID", created.ID)
	reportRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(reportRecorder, report)
	if reportRecorder.Code != http.StatusNoContent {
		t.Fatalf("probe report failed: %d %s", reportRecorder.Code, reportRecorder.Body.String())
	}
	probeResult, _ := json.Marshal(probe)
	completeBody, _ := json.Marshal(map[string]any{"success": true, "result": string(probeResult)})
	complete := httptest.NewRequest(http.MethodPost, "/api/v1/agent/jobs/"+probeJob.ID+"/complete", bytes.NewReader(completeBody))
	complete.Header.Set("Content-Type", "application/json")
	complete.Header.Set("Authorization", "Bearer "+agentToken)
	complete.Header.Set("X-Portolan-Server-ID", created.ID)
	completeRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(completeRecorder, complete)
	if completeRecorder.Code != http.StatusNoContent {
		t.Fatalf("probe job completion failed: %d %s", completeRecorder.Code, completeRecorder.Body.String())
	}
	select {
	case <-adminDone:
	case <-time.After(3 * time.Second):
		t.Fatal("administrator probe request did not receive the fresh result")
	}
	if adminRecorder.Code != http.StatusOK || !bytes.Contains(adminRecorder.Body.Bytes(), []byte(`"latency_ms":12.4`)) {
		t.Fatalf("unexpected administrator probe response: %d %s", adminRecorder.Code, adminRecorder.Body.String())
	}
}

func TestForwardProbeHistoryEndpoint(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	created, _, err := server.store.CreateServer(context.Background(), model.Server{Name: "History entry"})
	if err != nil {
		t.Fatal(err)
	}
	forward, err := server.store.CreateForward(context.Background(), model.Forward{IngressServerID: created.ID, Name: "History route", ListenPort: 27000,
		Networks: []string{"tcp"}, TargetHost: "target.example", TargetPort: 443, Engine: model.ForwardSingBox, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	probe := model.ForwardProbe{ForwardID: forward.ID, CheckedAt: time.Now().UTC().Add(-10 * time.Minute),
		Attempts: 3, Successes: 3, LatencyMS: 18.5, JitterMS: 1.2, Status: "stable"}
	if err := server.store.SaveForwardProbes(context.Background(), created.ID, []model.ForwardProbe{probe}); err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRequest(http.MethodPost, "/api/v1/session", bytes.NewReader([]byte(`{"token":"this-is-a-long-random-admin-token"}`)))
	login.Header.Set("Content-Type", "application/json")
	loginRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(loginRecorder, login)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login returned %d: %s", loginRecorder.Code, loginRecorder.Body.String())
	}
	cookie := loginRecorder.Result().Cookies()[0]

	request := authenticatedRequest(http.MethodGet, "/api/v1/forwards/"+forward.ID+"/probe-history?range=1h", nil, cookie, "")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("history returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var history store.ForwardProbeHistory
	if err := json.Unmarshal(recorder.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if history.Summary.SampleCount != 1 || history.Summary.AvailabilityPercent == nil ||
		*history.Summary.AvailabilityPercent != 100 || len(history.Points) != 60 {
		t.Fatalf("unexpected history response: %#v", history)
	}
	stablePoints := 0
	unknownPoints := 0
	for _, point := range history.Points {
		switch point.Status {
		case "stable":
			stablePoints++
			if point.LatencyMS != 18.5 {
				t.Fatalf("stable point latency = %v, want 18.5", point.LatencyMS)
			}
		case "unknown":
			unknownPoints++
		}
	}
	if stablePoints != 1 || unknownPoints != 59 {
		t.Fatalf("unexpected bucket states: stable=%d unknown=%d", stablePoints, unknownPoints)
	}
	if span := history.Summary.To.Sub(history.Summary.From); span != time.Hour {
		t.Fatalf("history span = %v, want 1h", span)
	}

	invalid := authenticatedRequest(http.MethodGet, "/api/v1/forwards/"+forward.ID+"/probe-history?range=30d", nil, cookie, "")
	invalidRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidRecorder, invalid)
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid range returned %d: %s", invalidRecorder.Code, invalidRecorder.Body.String())
	}

	missing := authenticatedRequest(http.MethodGet, "/api/v1/forwards/missing/probe-history?range=24h", nil, cookie, "")
	missingRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(missingRecorder, missing)
	if missingRecorder.Code != http.StatusNotFound {
		t.Fatalf("missing forward returned %d: %s", missingRecorder.Code, missingRecorder.Body.String())
	}
}

func TestAgentJobWaitIsBounded(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw  string
		want time.Duration
	}{
		{raw: "", want: 0},
		{raw: "invalid", want: 0},
		{raw: "-1", want: 0},
		{raw: "2", want: 2 * time.Second},
		{raw: "60", want: 60 * time.Second},
		{raw: "61", want: 60 * time.Second},
		{raw: "9223372036854775807", want: 60 * time.Second},
	} {
		if got := agentJobWait(test.raw); got != test.want {
			t.Errorf("agentJobWait(%q) = %s, want %s", test.raw, got, test.want)
		}
	}
}

func newTestServer(t *testing.T) *Server {
	return newTestServerWithRegionLookup(t, nil)
}

func newTestServerWithRegionLookup(t *testing.T, regionLookup func(string) (string, error)) *Server {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	secretVault, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(t.TempDir(), "api.db"), secretVault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	github := corestest.GitHub(t,
		corestest.Release{Core: model.CoreSingBox, Version: "1.14.3", Binary: []byte("sing-box 1.14.3")},
		corestest.Release{Core: model.CoreSingBox, Version: "1.14.2", Binary: []byte("sing-box 1.14.2")},
		corestest.Release{Core: model.CoreRealm, Version: "2.9.6", Binary: []byte("realm 2.9.6")},
	)
	server, err := New(Config{Store: database, AdminToken: "this-is-a-long-random-admin-token", SecureCookies: false,
		RegionLookup: regionLookup, Cores: corestest.Client(t, github)})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func authenticatedRequest(method, target string, body []byte, cookie *http.Cookie, csrf string) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	return request
}

func TestConsoleRoutesLeaveUnknownAPIPathsAsJSON(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.web = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		_, _ = w.Write([]byte("console"))
	})
	handler := server.Handler()
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || page.Body.String() != "console" || page.Header().Get("Content-Security-Policy") != "default-src 'self'" {
		t.Fatalf("console returned %d %q with policy %q", page.Code, page.Body.String(), page.Header().Get("Content-Security-Policy"))
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil))
	if missing.Code != http.StatusNotFound || !strings.HasPrefix(missing.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unknown API path returned %d %s", missing.Code, missing.Header().Get("Content-Type"))
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || health.Header().Get("Content-Security-Policy") != "default-src 'none'; frame-ancestors 'none'" {
		t.Fatalf("health returned %d with policy %q", health.Code, health.Header().Get("Content-Security-Policy"))
	}
}
