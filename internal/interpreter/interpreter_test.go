package interpreter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"
	"github.com/AgentGuardHQ/sentinel/internal/config"
	"github.com/AgentGuardHQ/sentinel/internal/interpreter"
	"github.com/AgentGuardHQ/sentinel/internal/memory"
)

func TestInterpreter_InterpretFindings(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmResponse := []map[string]any{{
			"finding_id": "hotspot-Bash-1", "actionable": true,
			"remediation": "Add /tmp/** to shell_write_guard allowlist",
			"novelty": "new", "confidence": 0.85,
			"reasoning": "High denial rate with clear pattern",
			"suggested_title": "shell_write_guard false positives on /tmp writes",
		}}
		respJSON, _ := json.Marshal(llmResponse)
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(respJSON)}},
		})
	}))
	defer apiServer.Close()

	memServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"entries": []any{}})
	}))
	defer memServer.Close()

	findings := []analyzer.Finding{{
		ID: "hotspot-Bash-1", Pass: "hotspot", PolicyID: "Bash",
		Metrics: analyzer.Metrics{Count: 50, Rate: 0.3, SampleSize: 200},
		DetectedAt: time.Now(),
	}}

	cfg := config.InterpreterConfig{Model: "claude-sonnet-4-6", MaxFindingsPerBatch: 20}
	memClient := memory.NewClient(memServer.URL)
	interp := interpreter.New(apiServer.URL, "test-key", memClient, cfg)

	interpreted, err := interp.Interpret(context.Background(), findings)
	if err != nil { t.Fatalf("Interpret() error: %v", err) }
	if len(interpreted) != 1 { t.Fatalf("expected 1, got %d", len(interpreted)) }
	if !interpreted[0].Actionable { t.Error("expected actionable") }
	if interpreted[0].Confidence != 0.85 { t.Errorf("confidence = %f, want 0.85", interpreted[0].Confidence) }
}

func TestInterpreter_ValidationRejectsInvalid(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmResponse := []map[string]any{{
			"finding_id": "f1", "actionable": true, "remediation": "",
			"novelty": "new", "confidence": 1.5, "reasoning": "test", "suggested_title": "test",
		}}
		respJSON, _ := json.Marshal(llmResponse)
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(respJSON)}},
		})
	}))
	defer apiServer.Close()

	memServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"entries": []any{}})
	}))
	defer memServer.Close()

	findings := []analyzer.Finding{{ID: "f1", Pass: "hotspot", PolicyID: "Bash", DetectedAt: time.Now()}}
	cfg := config.InterpreterConfig{Model: "claude-sonnet-4-6", MaxFindingsPerBatch: 20}
	interp := interpreter.New(apiServer.URL, "test-key", memory.NewClient(memServer.URL), cfg)

	interpreted, err := interp.Interpret(context.Background(), findings)
	if err != nil { t.Fatalf("Interpret() error: %v", err) }
	if len(interpreted) != 1 { t.Fatalf("expected 1, got %d", len(interpreted)) }
	if interpreted[0].Confidence != 0.0 { t.Errorf("invalid confidence = %f, want 0.0", interpreted[0].Confidence) }
}
