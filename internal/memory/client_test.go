package memory_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AgentGuardHQ/sentinel/internal/memory"
)

func TestMemoryClient_Store(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/memory" { t.Errorf("path = %s, want /api/memory", r.URL.Path) }
		if r.Method != http.MethodPost { t.Errorf("method = %s, want POST", r.Method) }
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]string{"id": "mem-123", "status": "stored"})
	}))
	defer server.Close()

	client := memory.NewClient(server.URL)
	id, err := client.Store(context.Background(), "test finding content", []string{"policy_pattern"}, "sentinel")
	if err != nil { t.Fatalf("Store() error: %v", err) }
	if id != "mem-123" { t.Errorf("id = %s, want mem-123", id) }
	if gotBody["content"] != "test finding content" { t.Errorf("content = %v", gotBody["content"]) }
	if gotBody["agent_id"] != "sentinel" { t.Errorf("agent_id = %v", gotBody["agent_id"]) }
}

func TestMemoryClient_RecallReturnsEmpty(t *testing.T) {
	client := memory.NewClient("http://localhost:8080")
	entries, err := client.Recall(context.Background(), "Bash denial patterns", 5)
	if err != nil { t.Fatalf("Recall() error: %v", err) }
	if len(entries) != 0 { t.Errorf("expected 0 entries in Phase 1, got %d", len(entries)) }
}

func TestMemoryClient_StoreHandlesServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := memory.NewClient(server.URL)
	_, err := client.Store(context.Background(), "content", nil, "sentinel")
	if err == nil { t.Fatal("expected error for 500 response") }
}
