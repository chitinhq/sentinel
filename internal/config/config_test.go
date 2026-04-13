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
	if cfg.GitHub.Repo != "chitinhq/chitin" {
		t.Errorf("Repo = %s, want chitinhq/chitin", cfg.GitHub.Repo)
	}
}

func TestLoadHeartbeatDefaults(t *testing.T) {
	// sentinel.yaml does not set heartbeat — defaults should apply.
	cfg, err := config.Load("../../sentinel.yaml")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Heartbeat.MinEvents24h != 10 {
		t.Errorf("MinEvents24h default = %d, want 10", cfg.Heartbeat.MinEvents24h)
	}
	if cfg.Heartbeat.NtfyTopic != "ganglia" {
		t.Errorf("NtfyTopic default = %q, want ganglia", cfg.Heartbeat.NtfyTopic)
	}
}

func TestLoadHeartbeatOverrides(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "sentinel-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	if _, err := tmp.WriteString("heartbeat:\n  min_events_24h: 500\n  ntfy_topic: test-topic\n"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(tmp.Name())
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Heartbeat.MinEvents24h != 500 {
		t.Errorf("MinEvents24h = %d, want 500", cfg.Heartbeat.MinEvents24h)
	}
	if cfg.Heartbeat.NtfyTopic != "test-topic" {
		t.Errorf("NtfyTopic = %q, want test-topic", cfg.Heartbeat.NtfyTopic)
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
