package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type MemoryEntry struct {
	ID      string   `json:"id"`
	Content string   `json:"content"`
	Topics  []string `json:"topics"`
	AgentID string   `json:"agent_id"`
}

type MemoryClient interface {
	Store(ctx context.Context, content string, topics []string, agentID string) (string, error)
	Recall(ctx context.Context, query string, limit int) ([]MemoryEntry, error)
}

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) Store(ctx context.Context, content string, topics []string, agentID string) (string, error) {
	body := map[string]any{"content": content, "topics": topics, "agent_id": agentID}
	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal store request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/memory", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create store request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("store request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("store: status %d", resp.StatusCode)
	}

	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode store response: %w", err)
	}
	return result.ID, nil
}

// Recall is a no-op in Phase 1. Octi Pulpo's /api/memory only supports store;
// recall requires MCP tools (available in Phase 2).
func (c *Client) Recall(ctx context.Context, query string, limit int) ([]MemoryEntry, error) {
	return nil, nil
}
