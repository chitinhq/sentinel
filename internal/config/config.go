package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Analysis    AnalysisConfig    `yaml:"analysis"`
	Detection   DetectionConfig   `yaml:"detection"`
	Scoring     ScoringConfig     `yaml:"scoring"`
	Routing     RoutingConfig     `yaml:"routing"`
	Interpreter InterpreterConfig `yaml:"interpreter"`
	Digest          DigestConfig          `yaml:"digest"`
	GitHub          GitHubConfig          `yaml:"github"`
	Ingestion       IngestionConfig       `yaml:"ingestion"`
	ExecutionPasses ExecutionPassesConfig `yaml:"execution_passes"`
	Health          HealthConfig          `yaml:"health"`
	Insights        InsightsConfig        `yaml:"insights"`

	// Environment variable overrides (not in YAML)
	RedisURL        string `yaml:"-"`
	NeonDatabaseURL string `yaml:"-"`
	AnthropicAPIKey string `yaml:"-"`
	QdrantURL       string `yaml:"-"`
	GitHubToken     string `yaml:"-"`
	SlackWebhookURL string `yaml:"-"`
	OctiPulpoURL    string `yaml:"-"`
}

type AnalysisConfig struct {
	Lookback    time.Duration `yaml:"lookback"`
	TrendWindow time.Duration `yaml:"trend_window"`
}

type DetectionConfig struct {
	FalsePositive FalsePositiveConfig `yaml:"false_positive"`
	Bypass        BypassConfig        `yaml:"bypass"`
	Anomaly       AnomalyConfig       `yaml:"anomaly"`
}

type FalsePositiveConfig struct {
	MinSampleSize         int     `yaml:"min_sample_size"`
	DeviationThreshold    float64 `yaml:"deviation_threshold"`
	AbsoluteRateThreshold float64 `yaml:"absolute_rate_threshold"`
}

type BypassConfig struct {
	Window     time.Duration `yaml:"window"`
	MinRetries int           `yaml:"min_retries"`
}

type AnomalyConfig struct {
	VolumeSpikeThreshold float64 `yaml:"volume_spike_threshold"`
}

type ScoringConfig struct {
	Weights WeightsConfig `yaml:"weights"`
}

type WeightsConfig struct {
	SignalStrength float64 `yaml:"signal_strength"`
	Impact        float64 `yaml:"impact"`
	Actionability float64 `yaml:"actionability"`
	Novelty       float64 `yaml:"novelty"`
}

type RoutingConfig struct {
	HighConfidence   float64       `yaml:"high_confidence"`
	MediumConfidence float64       `yaml:"medium_confidence"`
	DedupSimilarity  float64       `yaml:"dedup_similarity"`
	DedupLookback    time.Duration `yaml:"dedup_lookback"`
}

type InterpreterConfig struct {
	Model               string `yaml:"model"`
	MaxFindingsPerBatch int    `yaml:"max_findings_per_batch"`
}

type DigestConfig struct {
	SlackEnabled bool `yaml:"slack_enabled"`
}

type GitHubConfig struct {
	Repo   string   `yaml:"repo"`
	Labels []string `yaml:"labels"`
}

type IngestionConfig struct {
	Enabled           bool                   `yaml:"enabled"`
	GitHubActions     GitHubActionsConfig    `yaml:"github_actions"`
	ChitinGovernance  ChitinGovernanceConfig `yaml:"chitin_governance"`
	SwarmDispatch     SwarmDispatchConfig    `yaml:"swarm_dispatch"`
	BrainState        BrainStateConfig       `yaml:"brain_state"`
}

type GitHubActionsConfig struct {
	Repos         []string            `yaml:"repos"`
	Since         time.Duration       `yaml:"since"`
	PollInterval  time.Duration       `yaml:"poll_interval"`
	ActorPatterns []ActorPatternConfig `yaml:"actor_patterns"`
}

type ActorPatternConfig struct {
	Pattern string `yaml:"pattern"`
	AgentID string `yaml:"agent_id"`
}

type HealthConfig struct {
	WindowHours             int            `yaml:"window_hours"`
	Weights                 HealthWeights  `yaml:"weights"`
	BrainHealthThresholdSkip  int          `yaml:"brain_health_threshold_skip"`
	BrainHealthThresholdLimit int          `yaml:"brain_health_threshold_limit"`
}

type HealthWeights struct {
	SuccessRate          float64 `yaml:"success_rate"`
	GovernanceCompliance float64 `yaml:"governance_compliance"`
	Latency              float64 `yaml:"latency"`
	BudgetHealth         float64 `yaml:"budget_health"`
	Stability            float64 `yaml:"stability"`
}

type InsightsConfig struct {
	Enabled              bool    `yaml:"enabled"`
	MaxFrequencyMinutes  int     `yaml:"max_frequency_minutes"`
	VolumeSpikeThreshold float64 `yaml:"volume_spike_threshold"`
	ScoreDeltaThreshold  int     `yaml:"score_delta_threshold"`
}

type ChitinGovernanceConfig struct {
	Workspaces []string `yaml:"workspaces"`
}

type SwarmDispatchConfig struct {
	TelemetryPath string `yaml:"telemetry_path"`
}

type BrainStateConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
}

type ExecutionPassesConfig struct {
	CommandFailure    CommandFailureConfig    `yaml:"command_failure"`
	SequenceDetection SequenceDetectionConfig `yaml:"sequence_detection"`
}

type CommandFailureConfig struct {
	MinOccurrences       int     `yaml:"min_occurrences"`
	FailureRateThreshold float64 `yaml:"failure_rate_threshold"`
}

type SequenceDetectionConfig struct {
	NgramRange           [2]int  `yaml:"ngram_range"`
	MinFrequency         int     `yaml:"min_frequency"`
	FailureRateThreshold float64 `yaml:"failure_rate_threshold"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Expand ${VAR}/$VAR against process env before parsing. Fixes the
	// long-standing trap where sentinel.yaml used placeholders like
	// ${WORKSPACE}/octi and the loader passed the literal string to
	// ingesters, which then silently skipped every path.
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := validateNoUnresolvedPlaceholders(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	// Environment variable overrides
	cfg.RedisURL = os.Getenv("SENTINEL_REDIS_URL")
	if cfg.RedisURL == "" {
		cfg.RedisURL = "redis://localhost:6379"
	}
	cfg.NeonDatabaseURL = os.Getenv("NEON_DATABASE_URL")
	cfg.AnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	cfg.QdrantURL = os.Getenv("QDRANT_URL")
	cfg.GitHubToken = os.Getenv("GITHUB_TOKEN")
	cfg.SlackWebhookURL = os.Getenv("SLACK_WEBHOOK_URL")
	cfg.OctiPulpoURL = os.Getenv("OCTI_PULPO_URL")
	if cfg.OctiPulpoURL == "" {
		cfg.OctiPulpoURL = "http://localhost:8080"
	}

	return &cfg, nil
}

// validateNoUnresolvedPlaceholders walks the config paths that historically
// used ${VAR} placeholders and rejects any literal "${" survivor. Empty
// strings are allowed (the ingester will warn and skip); a lingering "${"
// indicates the env var was missing at load time, which is the silent-break
// trap we want to catch loudly.
func validateNoUnresolvedPlaceholders(cfg *Config) error {
	var bad []string
	for _, ws := range cfg.Ingestion.ChitinGovernance.Workspaces {
		if strings.Contains(ws, "${") || strings.Contains(ws, "$(") {
			bad = append(bad, fmt.Sprintf("ingestion.chitin_governance.workspaces: %q", ws))
		}
	}
	if p := cfg.Ingestion.SwarmDispatch.TelemetryPath; strings.Contains(p, "${") || strings.Contains(p, "$(") {
		bad = append(bad, fmt.Sprintf("ingestion.swarm_dispatch.telemetry_path: %q", p))
	}
	if len(bad) > 0 {
		return fmt.Errorf("unresolved env placeholders (set the env var or use an absolute path): %s", strings.Join(bad, "; "))
	}
	return nil
}
