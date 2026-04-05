package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Analysis    AnalysisConfig    `yaml:"analysis"`
	Detection   DetectionConfig   `yaml:"detection"`
	Scoring     ScoringConfig     `yaml:"scoring"`
	Routing     RoutingConfig     `yaml:"routing"`
	Interpreter InterpreterConfig `yaml:"interpreter"`
	Digest      DigestConfig      `yaml:"digest"`
	GitHub      GitHubConfig      `yaml:"github"`

	// Environment variable overrides (not in YAML)
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
	Drift         DriftConfig         `yaml:"drift"`
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

type DriftConfig struct {
	ActionDistributionThreshold   float64 `yaml:"action_distribution_threshold"`
	OutcomeDistributionThreshold  float64 `yaml:"outcome_distribution_threshold"`
	TemporalDistributionThreshold float64 `yaml:"temporal_distribution_threshold"`
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

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Environment variable overrides
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
