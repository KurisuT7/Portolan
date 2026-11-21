package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KurisuT7/Portolan/internal/model"
)

func TestBatchNodeCreationAllocatesPortsAndQueuesOneSync(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()
	entry, _, err := server.store.CreateServer(ctx, model.Server{Name: "Entry", Address: "203.0.113.40"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := adminSession(t, server)

	callBatch := func(serverID string, nodes []map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"server_id": serverID, "nodes": nodes})
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/v1/nodes/batch", data, cookie, csrf))
		return recorder
	}

	recorder := callBatch(entry.ID, []map[string]any{
		{
			"protocol":    model.ProtocolReality,
			"name":        "Entry Reality",
			"listen_port": 0,
			"reality":     map[string]any{"handshake_server": "www.example.com", "flow": "xtls-rprx-vision"},
		},
		{
			"protocol":    model.ProtocolShadowsocks,
			"name":        "Entry SS",
			"listen_port": 24410,
			"shadowsocks": map[string]any{"method": "2022-blake3-aes-256-gcm"},
			"shadow_tls":  map[string]any{"handshake_server": "www.example.com"},
		},
		{
			"protocol":    model.ProtocolSnell,
			"name":        "Entry Snell",
			"listen_port": 0,
			"snell":       map[string]any{"version": 6, "mode": "default"},
		},
	})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("batch create returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var resp struct {
		Items []struct {
			ID         string         `json:"id"`
			Name       string         `json:"name"`
			Protocol   model.Protocol `json:"protocol"`
			ListenPort uint16         `json:"listen_port"`
			Client     struct {
				URI       string `json:"uri"`
				SurgeLine string `json:"surge_line"`
			} `json:"client"`
		} `json:"items"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 3 || resp.UpdatedAt == "" {
		t.Fatalf("unexpected batch response: %s", recorder.Body.String())
	}
	if resp.Items[0].ListenPort == 0 || resp.Items[1].ListenPort != 24410 || resp.Items[2].ListenPort == 0 || resp.Items[0].ListenPort == resp.Items[2].ListenPort {
		t.Fatalf("unexpected allocated ports: %+v", resp.Items)
	}
	if !strings.HasPrefix(resp.Items[0].Client.URI, "vless://") ||
		!strings.HasPrefix(resp.Items[1].Client.URI, "ss://") ||
		!strings.Contains(resp.Items[1].Client.SurgeLine, "shadow-tls-version=3") ||
		!strings.Contains(resp.Items[2].Client.SurgeLine, "= snell,") {
		t.Fatalf("unexpected client exports: %+v", resp.Items)
	}

	jobs, err := server.store.ListJobs(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	syncCount := 0
	for _, job := range jobs {
		if job.ServerID == entry.ID && job.Type == "sync" {
			syncCount++
		}
	}
	if syncCount != 1 {
		t.Fatalf("expected 1 queued sync job for batch create, got %d (%+v)", syncCount, jobs)
	}

	// Failure in the second item must roll back the whole transaction.
	beforeNodes, err := server.store.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conflict := callBatch(entry.ID, []map[string]any{
		{
			"protocol":    model.ProtocolReality,
			"name":        "Should Rollback",
			"listen_port": 24420,
			"reality":     map[string]any{"handshake_server": "www.example.com"},
		},
		{
			"protocol":    model.ProtocolSnell,
			"name":        "Duplicate Port",
			"listen_port": 24410,
			"snell":       map[string]any{"version": 6},
		},
	})
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "第 2 个节点") {
		t.Fatalf("expected conflict referencing node 2, got %d: %s", conflict.Code, conflict.Body.String())
	}
	afterNodes, err := server.store.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterNodes) != len(beforeNodes) {
		t.Fatalf("failed batch left partial nodes: before=%d after=%d", len(beforeNodes), len(afterNodes))
	}

	// NAT servers allocate sequential ports from their mapped ranges and roll back when full.
	natServer, _, err := server.store.CreateServer(ctx, model.Server{
		Name:     "NAT",
		Address:  "203.0.113.41",
		NATPorts: []model.PortMapping{{Public: 31001, Host: 21001, Count: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	natTooMany := callBatch(natServer.ID, []map[string]any{
		{"protocol": model.ProtocolSnell, "name": "N1", "snell": map[string]any{"version": 6}},
		{"protocol": model.ProtocolSnell, "name": "N2", "snell": map[string]any{"version": 6}},
		{"protocol": model.ProtocolSnell, "name": "N3", "snell": map[string]any{"version": 6}},
	})
	if natTooMany.Code != http.StatusConflict || !strings.Contains(natTooMany.Body.String(), "第 3 个节点") {
		t.Fatalf("expected NAT overflow conflict on node 3, got %d: %s", natTooMany.Code, natTooMany.Body.String())
	}
	natList, err := server.store.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range natList {
		if node.ServerID == natServer.ID {
			t.Fatalf("NAT overflow batch left partial node: %+v", node)
		}
	}
	natOK := callBatch(natServer.ID, []map[string]any{
		{"protocol": model.ProtocolSnell, "name": "N1", "snell": map[string]any{"version": 6}},
		{"protocol": model.ProtocolSnell, "name": "N2", "snell": map[string]any{"version": 6}},
	})
	if natOK.Code != http.StatusCreated {
		t.Fatalf("NAT batch create returned %d: %s", natOK.Code, natOK.Body.String())
	}
}
