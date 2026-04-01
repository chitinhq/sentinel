package interpreter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"
	"github.com/AgentGuardHQ/sentinel/internal/config"
	"github.com/AgentGuardHQ/sentinel/internal/memory"
)

const systemPrompt = `You are a governance telemetry analyst for AgentGuard, an execution governance kernel for AI coding agents.

You are given a set of statistical findings from today's telemetry analysis, along with related past findings from the knowledge store.

For each finding, assess:

1. ACTIONABILITY - Is this something that should change in the kernel? (policy adjustment, new invariant, configuration change, or nothing)

2. REMEDIATION - If actionable, what specifically should change? Be concrete: "Add /tmp/** to shell_write_guard allowlist" not "consider adjusting the policy"

3. NOVELTY - Has this been seen before? Is it getting worse, stable, or improving? (Use the past findings for context)

4. CONFIDENCE - Score 0.0 to 1.0 based on:
   - signal_strength (0.35): frequency, sample size, statistical deviation
   - impact (0.25): how much agent friction does this cause?
   - actionability (0.25): is the remediation clear and safe?
   - novelty (0.15): new patterns score higher than known ones

Respond ONLY with a JSON array (no markdown fencing):
[{"finding_id": "...", "actionable": true/false, "remediation": "...", "novelty": "new|recurring|worsening|improving", "confidence": 0.0-1.0, "reasoning": "...", "suggested_title": "..."}]`

// Interpreter calls the Anthropic Messages API to enrich findings with LLM analysis.
type Interpreter struct {
	apiURL     string
	apiKey     string
	mem        memory.MemoryClient
	cfg        config.InterpreterConfig
	httpClient *http.Client
}

// New creates a new Interpreter.
func New(apiURL, apiKey string, mem memory.MemoryClient, cfg config.InterpreterConfig) *Interpreter {
	return &Interpreter{
		apiURL: apiURL,
		apiKey: apiKey,
		mem:    mem,
		cfg:    cfg,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Interpret enriches a slice of findings with LLM analysis, batching as needed.
func (i *Interpreter) Interpret(ctx context.Context, findings []analyzer.Finding) ([]analyzer.InterpretedFinding, error) {
	if len(findings) == 0 {
		return nil, nil
	}

	batchSize := i.cfg.MaxFindingsPerBatch
	if batchSize <= 0 {
		batchSize = 20
	}

	var all []analyzer.InterpretedFinding
	for start := 0; start < len(findings); start += batchSize {
		end := start + batchSize
		if end > len(findings) {
			end = len(findings)
		}
		batch := findings[start:end]

		interpreted, err := i.interpretBatch(ctx, batch)
		if err != nil {
			// On batch failure, fall back to zero-confidence results.
			all = append(all, i.fallbackFindings(batch)...)
			continue
		}
		all = append(all, interpreted...)
	}
	return all, nil
}

// interpretBatch processes a single batch of findings.
func (i *Interpreter) interpretBatch(ctx context.Context, findings []analyzer.Finding) ([]analyzer.InterpretedFinding, error) {
	pastContext, err := i.recallContext(ctx, findings)
	if err != nil {
		// Non-fatal: proceed without memory context.
		pastContext = ""
	}

	userMsg, err := i.buildUserMessage(findings, pastContext)
	if err != nil {
		return nil, fmt.Errorf("build user message: %w", err)
	}

	rawResponse, err := i.callAPI(ctx, userMsg)
	if err != nil {
		return nil, fmt.Errorf("call API: %w", err)
	}

	interpreted, err := i.parseAndValidate(rawResponse, findings)
	if err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return interpreted, nil
}

// recallContext queries memory for context related to each finding.
func (i *Interpreter) recallContext(ctx context.Context, findings []analyzer.Finding) (string, error) {
	var parts []string
	for _, f := range findings {
		query := fmt.Sprintf("%s %s", f.Pass, f.PolicyID)
		entries, err := i.mem.Recall(ctx, query, 3)
		if err != nil {
			continue
		}
		for _, e := range entries {
			parts = append(parts, e.Content)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "Past findings from knowledge store:\n" + strings.Join(parts, "\n---\n"), nil
}

// buildUserMessage serializes findings (and optional past context) as a user message.
func (i *Interpreter) buildUserMessage(findings []analyzer.Finding, pastContext string) (string, error) {
	type findingSummary struct {
		ID         string          `json:"id"`
		Pass       string          `json:"pass"`
		PolicyID   string          `json:"policy_id"`
		Count      int             `json:"count"`
		Rate       float64         `json:"rate"`
		Baseline   float64         `json:"baseline_rate"`
		Deviation  float64         `json:"deviation"`
		SampleSize int             `json:"sample_size"`
		DetectedAt string          `json:"detected_at"`
	}

	summaries := make([]findingSummary, 0, len(findings))
	for _, f := range findings {
		summaries = append(summaries, findingSummary{
			ID:         f.ID,
			Pass:       f.Pass,
			PolicyID:   f.PolicyID,
			Count:      f.Metrics.Count,
			Rate:       f.Metrics.Rate,
			Baseline:   f.Metrics.BaselineRate,
			Deviation:  f.Metrics.Deviation,
			SampleSize: f.Metrics.SampleSize,
			DetectedAt: f.DetectedAt.UTC().Format(time.RFC3339),
		})
	}

	data, err := json.MarshalIndent(summaries, "", "  ")
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	if pastContext != "" {
		sb.WriteString(pastContext)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Findings to analyze:\n")
	sb.Write(data)
	return sb.String(), nil
}

// anthropicRequest is the request body for the Anthropic Messages API.
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicResponse is the (minimal) response from the Anthropic Messages API.
type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// callAPI sends findings to the Anthropic Messages API and returns the raw text response.
func (i *Interpreter) callAPI(ctx context.Context, userMsg string) (string, error) {
	model := i.cfg.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	reqBody := anthropicRequest{
		Model:     model,
		MaxTokens: 4096,
		System:    systemPrompt,
		Messages: []anthropicMessage{
			{Role: "user", Content: userMsg},
		},
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	endpoint := strings.TrimRight(i.apiURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", i.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := i.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var apiResp anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	for _, block := range apiResp.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("no text content in response")
}

// llmFinding is the expected shape of each element in the LLM's JSON array response.
type llmFinding struct {
	FindingID      string  `json:"finding_id"`
	Actionable     bool    `json:"actionable"`
	Remediation    string  `json:"remediation"`
	Novelty        string  `json:"novelty"`
	Confidence     float64 `json:"confidence"`
	Reasoning      string  `json:"reasoning"`
	SuggestedTitle string  `json:"suggested_title"`
}

// parseAndValidate unmarshals the LLM JSON array, validates each result, and merges
// back with the source findings indexed by finding_id. Findings not returned by the
// LLM fall back to zero-confidence.
func (i *Interpreter) parseAndValidate(raw string, findings []analyzer.Finding) ([]analyzer.InterpretedFinding, error) {
	// Build lookup for source findings.
	findingByID := make(map[string]analyzer.Finding, len(findings))
	for _, f := range findings {
		findingByID[f.ID] = f
	}

	var llmResults []llmFinding
	if err := json.Unmarshal([]byte(raw), &llmResults); err != nil {
		return nil, fmt.Errorf("unmarshal LLM JSON: %w", err)
	}

	// Index validated results by finding_id.
	resultByID := make(map[string]analyzer.InterpretedFinding, len(llmResults))
	for _, r := range llmResults {
		f, ok := findingByID[r.FindingID]
		if !ok {
			// LLM hallucinated an ID — skip.
			continue
		}

		interpreted := analyzer.InterpretedFinding{
			Finding:        f,
			Actionable:     r.Actionable,
			Remediation:    r.Remediation,
			Novelty:        r.Novelty,
			Confidence:     r.Confidence,
			Reasoning:      r.Reasoning,
			SuggestedTitle: r.SuggestedTitle,
		}

		// Validate confidence is in [0.0, 1.0].
		if r.Confidence < 0.0 || r.Confidence > 1.0 {
			interpreted.Confidence = 0.0
		}

		// Validate actionable findings have a remediation.
		if r.Actionable && strings.TrimSpace(r.Remediation) == "" {
			interpreted.Actionable = false
		}

		resultByID[r.FindingID] = interpreted
	}

	// Build final slice in original finding order, falling back for missing results.
	out := make([]analyzer.InterpretedFinding, 0, len(findings))
	for _, f := range findings {
		if interp, ok := resultByID[f.ID]; ok {
			out = append(out, interp)
		} else {
			out = append(out, analyzer.InterpretedFinding{
				Finding:    f,
				Confidence: 0.0,
			})
		}
	}
	return out, nil
}

// fallbackFindings returns zero-confidence InterpretedFindings for all input findings.
// Called when the LLM API call or parsing fails entirely.
func (i *Interpreter) fallbackFindings(findings []analyzer.Finding) []analyzer.InterpretedFinding {
	out := make([]analyzer.InterpretedFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, analyzer.InterpretedFinding{
			Finding:    f,
			Confidence: 0.0,
		})
	}
	return out
}
