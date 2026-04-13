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

func TestLoadExpandsEnvPlaceholders(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("TEST_WS", tmp)
	defer os.Unsetenv("TEST_WS")

	yml := `
ingestion:
  chitin_governance:
    workspaces:
      - ${TEST_WS}/chitin
      - ${TEST_WS}/octi
`
	path := tmp + "/sentinel.yaml"
	if err := os.WriteFile(path, []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Ingestion.ChitinGovernance.Workspaces
	if len(got) != 2 {
		t.Fatalf("got %d workspaces, want 2", len(got))
	}
	if got[0] != tmp+"/chitin" {
		t.Errorf("workspace[0] = %q, want %q", got[0], tmp+"/chitin")
	}
}

func TestLoadRejectsUnresolvedPlaceholders(t *testing.T) {
	tmp := t.TempDir()
	os.Unsetenv("WORKSPACE") // ensure unresolved

	yml := `
ingestion:
  chitin_governance:
    workspaces:
      - ${WORKSPACE}/chitin
`
	path := tmp + "/sentinel.yaml"
	if err := os.WriteFile(path, []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}
	// ExpandEnv replaces unset vars with "" so the literal "${" disappears.
	// The guard catches the OTHER leftover pattern: $(...) or mistyped ${.
	// Use a $(...) form that ExpandEnv leaves alone to exercise the guard.
	yml2 := `
ingestion:
  chitin_governance:
    workspaces:
      - $(unresolved)/chitin
`
	if err := os.WriteFile(path, []byte(yml2), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Error("expected load to fail on $(unresolved) placeholder, got nil")
	}
}
