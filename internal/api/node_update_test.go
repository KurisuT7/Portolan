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

func TestNodeUpdateKeepsCredentialsUnlessRotated(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()
	entry, _, err := server.store.CreateServer(ctx, model.Server{Name: "Entry", Address: "203.0.113.30"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := adminSession(t, server)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(method, path, data, cookie, csrf))
		return recorder
	}
	create := func(body map[string]any) model.Node {
		t.Helper()
		body["server_id"] = entry.ID
		recorder := call(http.MethodPost, "/api/v1/nodes", body)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create returned %d: %s", recorder.Code, recorder.Body.String())
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		node, err := server.store.GetNode(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		return node
	}
	update := func(id string, body map[string]any) (*httptest.ResponseRecorder, model.Node) {
		t.Helper()
		recorder := call(http.MethodPut, "/api/v1/nodes/"+id, body)
		node, err := server.store.GetNode(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return recorder, node
	}

	reality := create(map[string]any{"name": "Reality", "protocol": model.ProtocolReality, "listen_port": 24300,
		"reality": map[string]any{"handshake_server": "www.example.com", "flow": "xtls-rprx-vision", "fingerprint": "firefox"}})
	recorder := call(http.MethodGet, "/api/v1/nodes/"+reality.ID, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("get returned %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, secret := range []string{reality.Reality.UUID, reality.Reality.PrivateKey, reality.Reality.ShortIDs[0]} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("node settings expose credentials: %s", recorder.Body.String())
		}
	}
	var view struct {
		Reality struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"reality"`
		Shadowsocks *struct{} `json:"shadowsocks"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil || view.Reality.Fingerprint != "firefox" || view.Shadowsocks != nil {
		t.Fatalf("node settings are incomplete: %s", recorder.Body.String())
	}

	recorder, edited := update(reality.ID, map[string]any{"name": "Reality gRPC", "listen_port": 24301, "reality": map[string]any{
		"handshake_server": "www.example.com", "transport": "grpc", "transport_settings": map[string]string{"service_name": "edge"}}})
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"updated_at"`) || !strings.Contains(recorder.Body.String(), ":24301") {
		t.Fatalf("update returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if edited.Reality.UUID != reality.Reality.UUID || edited.Reality.PrivateKey != reality.Reality.PrivateKey ||
		edited.Reality.Transport != "grpc" || edited.Reality.Flow != "" || edited.ListenPort != 24301 || edited.Name != "Reality gRPC" {
		t.Fatalf("update did not keep the credentials or apply the settings: %#v", edited.Reality)
	}
	if recorder, _ := update(reality.ID, map[string]any{"name": "Moved", "server_id": "elsewhere"}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("moving a node to another server was accepted: %d", recorder.Code)
	}
	recorder, rotated := update(reality.ID, map[string]any{"name": "Reality gRPC", "listen_port": 24301, "rotate_credentials": true,
		"reality": map[string]any{"handshake_server": "www.example.com", "transport": "grpc"}})
	if recorder.Code != http.StatusOK || rotated.Reality.UUID == reality.Reality.UUID || rotated.Reality.PrivateKey == reality.Reality.PrivateKey {
		t.Fatalf("rotation kept the credentials: %d %s", recorder.Code, recorder.Body.String())
	}

	shadowsocks := create(map[string]any{"name": "SS", "protocol": model.ProtocolShadowsocks, "listen_port": 24310,
		"shadowsocks": map[string]any{"method": "2022-blake3-aes-128-gcm"}})
	settings := func(method string, wrapped bool) map[string]any {
		body := map[string]any{"name": "SS", "listen_port": 24310, "shadowsocks": map[string]any{"method": method}}
		if wrapped {
			body["shadow_tls"] = map[string]any{"handshake_server": "www.example.com"}
		}
		return body
	}
	recorder, wrapped := update(shadowsocks.ID, settings("2022-blake3-aes-128-gcm", true))
	if recorder.Code != http.StatusOK || wrapped.SS.Password != shadowsocks.SS.Password || wrapped.ShadowTLS == nil || wrapped.ShadowTLS.HandshakePort != 443 {
		t.Fatalf("ShadowTLS was not added around the same key: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder, resized := update(shadowsocks.ID, settings("2022-blake3-aes-256-gcm", true))
	if recorder.Code != http.StatusOK || resized.SS.Password == shadowsocks.SS.Password || len(resized.SS.Password) != 44 ||
		resized.ShadowTLS.Password != wrapped.ShadowTLS.Password {
		t.Fatalf("a new method did not get a key of its own: %d %#v", recorder.Code, resized.SS)
	}
	recorder, bare := update(shadowsocks.ID, settings("2022-blake3-aes-256-gcm", false))
	if recorder.Code != http.StatusOK || bare.ShadowTLS != nil {
		t.Fatalf("ShadowTLS was not removed: %d %s", recorder.Code, recorder.Body.String())
	}
	nodes, err := server.store.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.ID == shadowsocks.ID && node.Profile != "2022-blake3-aes-256-gcm" {
			t.Fatalf("profile was not recomputed: %q", node.Profile)
		}
	}

	if _, _, err := server.store.UpsertDiscoveredNodes(ctx, entry.ID, []model.Node{{Name: "External", ListenPort: 24320,
		Protocol: model.ProtocolShadowsocks, Source: "/etc/sing-box/config.json", SS: &model.SSSpec{Method: "aes-256-gcm", Password: "external-password"}}}); err != nil {
		t.Fatal(err)
	}
	nodes, err = server.store.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if !node.Managed {
			if recorder := call(http.MethodPut, "/api/v1/nodes/"+node.ID, settings("aes-256-gcm", false)); recorder.Code != http.StatusConflict {
				t.Fatalf("discovered node was edited: %d %s", recorder.Code, recorder.Body.String())
			}
		}
	}
}
