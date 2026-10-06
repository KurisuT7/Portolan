package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	summary := get("/api/v1/traffic?tz=" + url.QueryEscape("Asia/Shanghai"))
	if summary.Code != http.StatusOK {
		t.Fatalf("summary returned %d: %s", summary.Code, summary.Body.String())
	}
	var body struct {
		Items []struct {
			Kind  string    `json:"kind"`
			ID    string    `json:"id"`
			Since time.Time `json:"since"`
			RX    int64     `json:"rx_bytes"`
			TX    int64     `json:"tx_bytes"`
		} `json:"items"`
	}
	if err := json.Unmarshal(summary.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	monthStart := time.Date(time.Now().In(shanghai).Year(), time.Now().In(shanghai).Month(), 1, 0, 0, 0, 0, shanghai)
	totals := map[string][2]int64{}
	for _, item := range body.Items {
		totals[item.Kind+":"+item.ID] = [2]int64{item.RX, item.TX}
		if !item.Since.Equal(monthStart) {
			t.Fatalf("%s:%s counts since %v, want %v", item.Kind, item.ID, item.Since, monthStart)
		}
	}
	if totals["server:"+created.ID] != [2]int64{4_000, 2_000} || totals["node:"+node.ID] != [2]int64{600, 1_200} {
		t.Fatalf("summary = %s", summary.Body.String())
	}
	for _, target := range []string{"/api/v1/traffic", "/api/v1/traffic?tz=Local", "/api/v1/traffic?tz=Mars%2FOlympus"} {
		if code := get(target).Code; code != http.StatusBadRequest {
			t.Fatalf("%s returned %d", target, code)
		}
	}

	history := get("/api/v1/nodes/" + node.ID + "/traffic?range=24h&tz=UTC")
	var series struct {
		Points []struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
			RX    int64     `json:"rx_bytes"`
		} `json:"points"`
	}
	if err := json.Unmarshal(history.Body.Bytes(), &series); err != nil || history.Code != http.StatusOK || len(series.Points) != 24 {
		t.Fatalf("history returned %d: %s", history.Code, history.Body.String())
	}
	var received int64
	for _, point := range series.Points {
		received += point.RX
	}
	if last := series.Points[23]; received != 600 || !last.Start.Equal(time.Now().UTC().Truncate(time.Hour)) || last.End.Sub(last.Start) != time.Hour {
		t.Fatalf("history = %s", history.Body.String())
	}
	for target, want := range map[string]int{
		"/api/v1/nodes/" + node.ID + "/traffic?range=7d&tz=UTC": http.StatusBadRequest,
		"/api/v1/nodes/" + node.ID + "/traffic?range=12m":       http.StatusBadRequest,
		"/api/v1/forwards/fwd_missing/traffic?range=24h&tz=UTC": http.StatusNotFound,
	} {
		if code := get(target).Code; code != want {
			t.Fatalf("%s returned %d, want %d", target, code, want)
		}
	}
}
