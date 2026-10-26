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

func TestShadowTLSNodesRequireAnAgentThatGeneratesThem(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()
	current, _, err := server.store.CreateServer(ctx, model.Server{Name: "Current", Address: "203.0.113.20"})
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := server.store.CreateServer(ctx, model.Server{Name: "Old", Address: "203.0.113.21"})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.store.SaveRuntimeStatus(ctx, old.ID, model.RuntimeStatus{AgentVersion: "v0.4.0", Units: []model.UnitStatus{}}); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := adminSession(t, server)
	create := func(serverID string, protocol model.Protocol, settings map[string]any) *httptest.ResponseRecorder {
		body := map[string]any{"server_id": serverID, "name": "Wrapped", "protocol": protocol,
			"shadow_tls": map[string]any{"handshake_server": "aws.amazon.com"}}
		for key, value := range settings {
			body[key] = value
		}
		data, _ := json.Marshal(body)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/v1/nodes", data, cookie, csrf))
		return recorder
	}
	shadowsocks := map[string]any{"shadowsocks": map[string]any{"method": "2022-blake3-aes-128-gcm"}}

	recorder := create(current.ID, model.ProtocolShadowsocks, shadowsocks)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("ShadowTLS node creation returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var created struct {
		ID     string `json:"id"`
		Client struct {
			URI string `json:"uri"`
		} `json:"client"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil || !strings.Contains(created.Client.URI, "?plugin=shadow-tls%3Bhost%3Daws.amazon.com%3Bpassword%3D") {
		t.Fatalf("ShadowTLS export is missing: %s", recorder.Body.String())
	}
	node, err := server.store.GetNode(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if node.ShadowTLS == nil || node.ShadowTLS.HandshakePort != 443 || len(node.ShadowTLS.Password) < 16 {
		t.Fatalf("ShadowTLS settings were not stored: %+v", node.ShadowTLS)
	}
	nodes, err := server.store.ListNodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].Profile != "2022-blake3-aes-128-gcm · ShadowTLS" {
		t.Fatalf("node profile does not mention ShadowTLS: %+v %v", nodes, err)
	}

	if recorder := create(old.ID, model.ProtocolSnell, map[string]any{"snell": map[string]any{"version": 6}}); recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), shadowTLSAgentSince) {
		t.Fatalf("an Agent without ShadowTLS support was accepted: %d %s", recorder.Code, recorder.Body.String())
	}
	reality := map[string]any{"reality": map[string]any{"handshake_server": "aws.amazon.com"}}
	if recorder := create(current.ID, model.ProtocolReality, reality); recorder.Code != http.StatusBadRequest {
		t.Fatalf("ShadowTLS around Reality was accepted: %d %s", recorder.Code, recorder.Body.String())
	}
}
