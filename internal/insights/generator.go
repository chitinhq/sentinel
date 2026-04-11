package insights

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Generator orchestrates insight generation from health scores and telemetry.
type Generator struct {
	pool       *pgxpool.Pool
	redis      *redis.Client
	apiURL     string
	apiKey     string
	model      string
	store      *Store
	maxFreq    time.Duration
	lastGenAt  time.Time
	scoreDelta int     // threshold for score delta to trigger generation
	volumeSpike float64 // multiplier for volume spike detection
	httpClient *http.Client
}

// GeneratorConfig holds config for the generator.
type GeneratorConfig struct {
	APIKey               string
	Model                string
	MaxFrequencyMinutes  int
	ScoreDeltaThreshold  int
	VolumeSpikeThreshold float64
	NtfyTopic            string
}

// NewGenerator constructs a Generator.
func NewGenerator(pool *pgxpool.Pool, redisClient *redis.Client, cfg GeneratorConfig) *Generator {
	maxFreq := time.Duration(cfg.MaxFrequencyMinutes) * time.Minute
	if maxFreq == 0 {
		maxFreq = 60 * time.Minute
	}
	scoreDelta := cfg.ScoreDeltaThreshold
	if scoreDelta == 0 {
		scoreDelta = 5
	}
	volumeSpike := cfg.VolumeSpikeThreshold
	if volumeSpike == 0 {
		volumeSpike = 3.0
	}
	model := cfg.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	return &Generator{
		pool:        pool,
		redis:       redisClient,
		apiURL:      "https://api.anthropic.com",
		apiKey:      cfg.APIKey,
		model:       model,
		store:       NewStore(pool, redisClient, cfg.NtfyTopic),
		maxFreq:     maxFreq,
		scoreDelta:  scoreDelta,
		volumeSpike: volumeSpike,
		httpClient:  &http.Client{Timeout: 60 * time.Second},
	}
}

// MaybeGenerate checks for signals and generates insights if warranted.
func (g *Generator) MaybeGenerate(ctx context.Context) ([]Insight, error) {
	// Rate cap check.
	if !g.lastGenAt.IsZero() && time.Since(g.lastGenAt) < g.maxFreq {
		return nil, nil
	}

	// Gather inputs.
	inputs, err := g.gatherInputs(ctx)
	if err != nil {
		return nil, fmt.Errorf("gather inputs: %w", err)
	}

	// Determine which categories have signal.
	categories := g.detectSignals(inputs)
	if len(categories) == 0 {
		return nil, nil
	}

	var allInsights []Insight

	for _, cat := range categories {
		insights, err := g.generateCategory(ctx, cat, inputs)
		if err != nil {
			log.Printf("insights: %s generation failed: %v", cat, err)
			continue
		}
		allInsights = append(allInsights, insights...)
	}

	if len(allInsights) == 0 {
		return nil, nil
	}

	// Store.
	if err := g.store.Write(ctx, allInsights); err != nil {
		return nil, fmt.Errorf("store insights: %w", err)
	}

	g.lastGenAt = time.Now()
	return allInsights, nil
}

// gatherInputs collects data from Neon and Redis.
func (g *Generator) gatherInputs(ctx context.Context) (*GeneratorInputs, error) {
	inputs := &GeneratorInputs{
		FailureCounts:  make(map[string]int),
		PlatformStats:  make(map[string]int),
		DispatchCounts: make(map[string]int),
		BudgetPcts:     make(map[string]int),
	}

	// Health score deltas: compare latest vs prior.
	rows, err := g.pool.Query(ctx, `
		WITH latest AS (
			SELECT scope_type, scope_value, score, dimensions,
			       ROW_NUMBER() OVER (PARTITION BY scope_type, scope_value ORDER BY timestamp DESC) as rn
			FROM health_scores
		)
		SELECT l1.scope_type, l1.scope_value, l1.score, COALESCE(l2.score, l1.score), l1.dimensions
		FROM latest l1
		LEFT JOIN latest l2 ON l1.scope_type = l2.scope_type AND l1.scope_value = l2.scope_value AND l2.rn = 2
		WHERE l1.rn = 1
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d HealthScoreDelta
			var dimsJSON []byte
			if err := rows.Scan(&d.ScopeType, &d.ScopeValue, &d.Score, &d.Prior, &dimsJSON); err == nil {
				d.Delta = d.Score - d.Prior
				if dimsJSON != nil {
					_ = json.Unmarshal(dimsJSON, &d.Dimensions)
				}
				inputs.ScoreDeltas = append(inputs.ScoreDeltas, d)
			}
		}
	}

	// Failure counts by repo (24h).
	fRows, err := g.pool.Query(ctx, `
		SELECT COALESCE(repository, 'unknown'), COUNT(*)::int
		FROM execution_events
		WHERE timestamp >= NOW() - INTERVAL '24 hours'
		  AND (has_error OR (exit_code IS NOT NULL AND exit_code != 0))
		GROUP BY repository
		ORDER BY COUNT(*) DESC
		LIMIT 20
	`)
	if err == nil {
		defer fRows.Close()
		for fRows.Next() {
			var repo string
			var count int
			if fRows.Scan(&repo, &count) == nil {
				inputs.FailureCounts[repo] = count
			}
		}
	}

	// Platform failure stats (24h).
	pRows, err := g.pool.Query(ctx, `
		SELECT COALESCE(agent_id, 'unknown'), COUNT(*)::int
		FROM execution_events
		WHERE timestamp >= NOW() - INTERVAL '24 hours'
		  AND (has_error OR (exit_code IS NOT NULL AND exit_code != 0))
		GROUP BY agent_id
	`)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var platform string
			var count int
			if pRows.Scan(&platform, &count) == nil {
				inputs.PlatformStats[platform] = count
			}
		}
	}

	// Event volume (24h).
	g.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM execution_events
		WHERE timestamp >= NOW() - INTERVAL '24 hours'
	`).Scan(&inputs.EventVolume)

	// Redis state.
	if g.redis != nil {
		if skipList, err := g.redis.HGetAll(ctx, "octi:skip-list").Result(); err == nil {
			inputs.SkipListSize = len(skipList)
		}
		for _, platform := range []string{"claude", "copilot", "gemini", "codex"} {
			if count, err := g.redis.Get(ctx, "octi:dispatch-count:"+platform).Int(); err == nil {
				inputs.DispatchCounts[platform] = count
			}
			if capStr, err := g.redis.HGet(ctx, "octi:budget:"+platform, "daily_cap").Result(); err == nil {
				cap := 0
				fmt.Sscanf(capStr, "%d", &cap)
				if cap > 0 {
					dispatches := inputs.DispatchCounts[platform]
					inputs.BudgetPcts[platform] = (dispatches * 100) / cap
				}
			}
		}
	}

	return inputs, nil
}

// detectSignals determines which insight categories have enough signal.
func (g *Generator) detectSignals(inputs *GeneratorInputs) []InsightCategory {
	var cats []InsightCategory

	// Health: any score moved > threshold.
	for _, d := range inputs.ScoreDeltas {
		if d.Delta > g.scoreDelta || d.Delta < -g.scoreDelta {
			cats = append(cats, CategoryHealth)
			break
		}
	}

	// Pattern: failures exist.
	totalFailures := 0
	for _, c := range inputs.FailureCounts {
		totalFailures += c
	}
	if totalFailures > 0 {
		cats = append(cats, CategoryPattern)
	}

	// Recommendation: any platform has data.
	if len(inputs.DispatchCounts) > 0 {
		cats = append(cats, CategoryRecommendation)
	}

	// Anomaly: volume spike — compare to 7d average.
	// Simple heuristic: if event volume > 0, always include for first run.
	// In practice, compare against historical average.
	if inputs.EventVolume > 100 {
		cats = append(cats, CategoryAnomaly)
	}

	return cats
}

// generateCategory calls the LLM for a single insight category.
func (g *Generator) generateCategory(ctx context.Context, cat InsightCategory, inputs *GeneratorInputs) ([]Insight, error) {
	var systemPrompt, userPrompt string

	switch cat {
	case CategoryHealth:
		systemPrompt = healthNarrativeSystemPrompt()
		userPrompt = buildHealthPrompt(inputs.ScoreDeltas, inputs.FailureCounts)
	case CategoryPattern:
		systemPrompt = patternDetectionSystemPrompt()
		userPrompt = buildPatternPrompt(inputs.FailureCounts, inputs.PlatformStats)
	case CategoryRecommendation:
		systemPrompt = dispatchRecommendationSystemPrompt()
		userPrompt = buildRecommendationPrompt(inputs.DispatchCounts, inputs.BudgetPcts, inputs.PlatformStats)
	case CategoryAnomaly:
		systemPrompt = anomalyAlertSystemPrompt()
		userPrompt = buildAnomalyPrompt(inputs.EventVolume, float64(inputs.EventVolume))
	default:
		return nil, fmt.Errorf("unknown category: %s", cat)
	}

	raw, err := g.callLLM(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, err
	}

	var llmResults []llmInsight
	if err := json.Unmarshal([]byte(raw), &llmResults); err != nil {
		return nil, fmt.Errorf("parse LLM response: %w", err)
	}

	now := time.Now().UTC()
	insights := make([]Insight, 0, len(llmResults))
	for i, r := range llmResults {
		ins := Insight{
			ID:              fmt.Sprintf("ins-%s-%d-%d", cat, now.Unix(), i),
			Timestamp:       now,
			Category:        InsightCategory(r.Category),
			Severity:        InsightSeverity(r.Severity),
			Narrative:       r.Narrative,
			Evidence:        r.Evidence,
			SuggestedAction: r.SuggestedAction,
			ScopeType:       r.ScopeType,
			ScopeValue:      r.ScopeValue,
		}
		// Validate severity.
		switch ins.Severity {
		case SeverityInfo, SeverityWarning, SeverityHigh, SeverityCritical:
			// valid
		default:
			ins.Severity = SeverityInfo
		}
		insights = append(insights, ins)
	}

	return insights, nil
}

// callLLM sends a prompt to the Anthropic Messages API.
func (g *Generator) callLLM(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	type anthropicMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type anthropicRequest struct {
		Model     string             `json:"model"`
		MaxTokens int                `json:"max_tokens"`
		System    string             `json:"system"`
		Messages  []anthropicMessage `json:"messages"`
	}
	type anthropicResponse struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}

	reqBody := anthropicRequest{
		Model:     g.model,
		MaxTokens: 1024,
		System:    systemPrompt,
		Messages: []anthropicMessage{
			{Role: "user", Content: userPrompt},
		},
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	endpoint := strings.TrimRight(g.apiURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", g.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := g.httpClient.Do(req)
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
