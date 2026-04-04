package analyzer

import (
	"testing"

	"github.com/AgentGuardHQ/sentinel/internal/config"
	"github.com/AgentGuardHQ/sentinel/internal/db"
)

func TestDetectCommandFailures(t *testing.T) {
	cfg := config.CommandFailureConfig{
		MinOccurrences:       10,
		FailureRateThreshold: 0.5,
	}

	tests := []struct {
		name        string
		rates       []db.CommandFailureRate
		wantCount   int
		wantCommand string // only checked when wantCount == 1
	}{
		{
			name: "high failure rate qualifies",
			rates: []db.CommandFailureRate{
				{Command: "cargo build", TotalCount: 20, FailureCount: 14, FailureRate: 0.7},
			},
			wantCount:   1,
			wantCommand: "cargo build",
		},
		{
			name: "low failure rate skipped",
			rates: []db.CommandFailureRate{
				{Command: "git status", TotalCount: 50, FailureCount: 5, FailureRate: 0.1},
			},
			wantCount: 0,
		},
		{
			name: "below min occurrences skipped",
			rates: []db.CommandFailureRate{
				{Command: "rm -rf /tmp", TotalCount: 3, FailureCount: 3, FailureRate: 1.0},
			},
			wantCount: 0,
		},
		{
			name:      "empty input",
			rates:     []db.CommandFailureRate{},
			wantCount: 0,
		},
		{
			name: "mixed — only one qualifies",
			rates: []db.CommandFailureRate{
				{Command: "make test", TotalCount: 15, FailureCount: 10, FailureRate: 0.67},
				{Command: "make lint", TotalCount: 5, FailureCount: 4, FailureRate: 0.8},  // below min
				{Command: "make build", TotalCount: 30, FailureCount: 9, FailureRate: 0.3}, // below rate
			},
			wantCount:   1,
			wantCommand: "make test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := DetectCommandFailures(tt.rates, cfg)
			if len(findings) != tt.wantCount {
				t.Fatalf("want %d findings, got %d", tt.wantCount, len(findings))
			}
			if tt.wantCount == 1 {
				f := findings[0]
				if f.Pass != "command_failure" {
					t.Errorf("pass: want command_failure, got %s", f.Pass)
				}
				if f.PolicyID != tt.wantCommand {
					t.Errorf("policyID: want %q, got %q", tt.wantCommand, f.PolicyID)
				}
				if f.Metrics.SampleSize == 0 {
					t.Errorf("expected non-zero SampleSize")
				}
			}
		})
	}
}

func TestSanitizeID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"cargo build", "cargo-build"},
		{"make test", "make-test"},
		{"rm -rf /tmp", "rm-rf-tmp"},
		{"UPPER CASE", "upper-case"},
		{"git!!status", "git-status"},
	}
	for _, tt := range tests {
		got := sanitizeID(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeID(%q): want %q, got %q", tt.input, tt.want, got)
		}
	}
}
