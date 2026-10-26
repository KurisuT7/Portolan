package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KurisuT7/Portolan/internal/model"
)

func TestReorderServersEndpoint(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	ctx := context.Background()
	var ids []string
	for _, name := range []string{"A", "B", "C"} {
		created, _, err := server.store.CreateServer(ctx, model.Server{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, created.ID)
	}
	cookie, csrf := adminSession(t, server)
	reorder := func(order []string) int {
		body, _ := json.Marshal(map[string]any{"ids": order})
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPut, "/api/v1/servers/order", body, cookie, csrf))
		return recorder.Code
	}
	if code := reorder([]string{ids[2], ids[0], ids[1]}); code != http.StatusNoContent {
		t.Fatalf("reorder returned %d", code)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet, "/api/v1/servers", nil, cookie, ""))
	var listed struct {
		Items []model.Server `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil || len(listed.Items) != 3 ||
		listed.Items[0].ID != ids[2] || listed.Items[1].ID != ids[0] || listed.Items[2].ID != ids[1] {
		t.Fatalf("servers are not listed in the stored order: %s", recorder.Body.String())
	}
	if code := reorder(ids[:2]); code != http.StatusConflict {
		t.Fatalf("an order that leaves out a server returned %d", code)
	}
}
