package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/chitinhq/sentinel/internal/config"
)

func TestLoadFromFile(t *testing.T) {
	cfg, err := config.Load("../../sentinel.yaml")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Analysis.Lookback != 24*time.Hour {
		t.Errorf("Lookback = %v, want 24h", cfg.Analysis.Lookback)
	}
	if cfg.Detection.FalsePositive.MinSampleSize != 20 {
		t.Errorf("MinSampleSize = %d, want 20", cfg.Detection.FalsePositive.MinSampleSize)
	}
	if cfg.Routing.HighConfidence != 0.8 {
		t.Errorf("HighConfidence = %f, want 0.8", cfg.Routing.HighConfidence)
	}
	if cfg.GitHub.Repo != "chitinhq/agent-guard" {
		t.Errorf("Repo = %s, want chitinhq/agent-guard", cfg.GitHub.Repo)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	os.Setenv("NEON_DATABASE_URL", "postgres://test:5432/db")
	defer os.Unsetenv("NEON_DATABASE_URL")

	cfg, err := config.Load("../../sentinel.yaml")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.NeonDatabaseURL != "postgres://test:5432/db" {
		t.Errorf("NeonDatabaseURL = %s, want postgres://test:5432/db", cfg.NeonDatabaseURL)
	}
}
