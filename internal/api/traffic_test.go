package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/KurisuT7/Portolan/internal/model"
)

func TestAgentTrafficIsSummarizedForTheConsole(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()
	created, enrollmentToken, err := server.store.CreateServer(ctx, model.Server{Name: "Traffic"})
	if err != nil {
		t.Fatal(err)
	}
	_, agentToken, err := server.store.Enroll(ctx, enrollmentToken, "198.51.100.80")
	if err != nil {
		t.Fatal(err)
	}
	node, err := server.store.CreateNode(ctx, model.Node{
		ServerID: created.ID, Name: "SS", Protocol: model.ProtocolShadowsocks, ListenPort: 24443, Enabled: true,
		SS: &model.SSSpec{Method: "2022-blake3-aes-128-gcm", Password: "MDEyMzQ1Njc4OWFiY2RlZg=="},
	}, "2022-blake3-aes-128-gcm")
	if err != nil {
		t.Fatal(err)
	}
	send := func(body []byte) int {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/traffic", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+agentToken)
		request.Header.Set("X-Portolan-Server-ID", created.ID)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder.Code
	}
	report := func(interfaceRX, portRX uint64) []byte {
		body, _ := json.Marshal(model.TrafficReport{
			BootID: "boot-1", Interfaces: []model.InterfaceTraffic{{Name: "eth0", RX: interfaceRX, TX: interfaceRX / 2}},
			PortEpoch: "boot-1:3", Ports: []model.PortTraffic{{Port: 24443, RX: portRX, TX: portRX * 2}},
		})
		return body
	}
	if code := send([]byte(`{"boot_id":"boot-1","interfaces":[{"name":"eth0/../x","rx_bytes":1,"tx_bytes":1}],"ports":[]}`)); code != http.StatusBadRequest {
		t.Fatalf("invalid interface name accepted: %d", code)
	}
	if code := send(report(1_000, 100)); code != http.StatusNoContent {
		t.Fatalf("first report returned %d", code)
	}
	if code := send(report(5_000, 700)); code != http.StatusNoContent {
		t.Fatalf("second report returned %d", code)
	}

	cookie, _ := adminSession(t, server)
	get := func(target string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet, target, nil, cookie, ""))
		return recorder
	}
	since := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	summary := get("/api/v1/traffic?since=" + url.QueryEscape(since))
	if summary.Code != http.StatusOK {
		t.Fatalf("summary returned %d: %s", summary.Code, summary.Body.String())
	}
	var body struct {
		Items []struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
			RX   int64  `json:"rx_bytes"`
			TX   int64  `json:"tx_bytes"`
		} `json:"items"`
	}
	if err := json.Unmarshal(summary.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	totals := map[string][2]int64{}
	for _, item := range body.Items {
		totals[item.Kind+":"+item.ID] = [2]int64{item.RX, item.TX}
	}
	if totals["server:"+created.ID] != [2]int64{4_000, 2_000} || totals["node:"+node.ID] != [2]int64{600, 1_200} {
		t.Fatalf("summary = %s", summary.Body.String())
	}
	for _, target := range []string{"/api/v1/traffic", "/api/v1/traffic?since=yesterday", "/api/v1/traffic?since=" + url.QueryEscape(time.Now().UTC().Add(time.Hour).Format(time.RFC3339))} {
		if code := get(target).Code; code != http.StatusBadRequest {
			t.Fatalf("%s returned %d", target, code)
		}
	}

	hour := time.Now().UTC().Truncate(time.Hour)
	edges := strconv.FormatInt(hour.Add(-time.Hour).Unix(), 10) + "," + strconv.FormatInt(hour.Add(time.Hour).Unix(), 10)
	history := get("/api/v1/nodes/" + node.ID + "/traffic?edges=" + edges)
	var series struct {
		Points []struct {
			RX int64 `json:"rx_bytes"`
		} `json:"points"`
	}
	if err := json.Unmarshal(history.Body.Bytes(), &series); err != nil || history.Code != http.StatusOK || len(series.Points) != 1 || series.Points[0].RX != 600 {
		t.Fatalf("history returned %d: %s", history.Code, history.Body.String())
	}
	if code := get("/api/v1/nodes/" + node.ID + "/traffic?edges=1,x").Code; code != http.StatusBadRequest {
		t.Fatalf("invalid edges returned %d", code)
	}
	if code := get("/api/v1/forwards/fwd_missing/traffic?edges=" + edges).Code; code != http.StatusNotFound {
		t.Fatalf("missing forward returned %d", code)
	}
}
