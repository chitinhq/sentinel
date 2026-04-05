package router_test

import (
	"strings"
	"testing"
	"time"

	"github.com/chitinhq/sentinel/internal/analyzer"
	"github.com/chitinhq/sentinel/internal/router"
)

func TestGhClient_FormatIssueBody(t *testing.T) {
	finding := analyzer.InterpretedFinding{
		Finding: analyzer.Finding{
			ID:   "fp-Bash-1",
			Pass: "false_positive",
			PolicyID: "Bash",
			Metrics: analyzer.Metrics{
				Count:        40,
				Rate:         0.35,
				BaselineRate: 0.1,
				Deviation:    3.2,
				SampleSize:   120,
			},
			DetectedAt: time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC),
		},
		Actionable:     true,
		Remediation:    "Add /tmp/** to shell_write_guard allowlist",
		Novelty:        "new",
		Confidence:     0.87,
		Reasoning:      "High denial rate",
		SuggestedTitle: "shell_write_guard false positives on /tmp writes",
	}

	title, body := router.FormatIssue(finding)

	if title != "[Sentinel] shell_write_guard false positives on /tmp writes" {
		t.Errorf("title = %s", title)
	}
	if body == "" {
		t.Error("body is empty")
	}
}

func TestGhClient_FormatIssueBody_ContainsKeyFields(t *testing.T) {
	finding := analyzer.InterpretedFinding{
		Finding: analyzer.Finding{
			ID:       "by-Edit-1",
			Pass:     "bypass",
			PolicyID: "Edit",
			Metrics:  analyzer.Metrics{Count: 5, Rate: 0.95, SampleSize: 50},
			DetectedAt: time.Now(),
		},
		Actionable:     true,
		Remediation:    "Review Edit policy scope",
		Novelty:        "recurring",
		Confidence:     0.91,
		Reasoning:      "Repeated retry pattern detected",
		SuggestedTitle: "Edit policy bypass via retry loop",
	}

	title, body := router.FormatIssue(finding)

	if !strings.HasPrefix(title, "[Sentinel]") {
		t.Errorf("title should start with [Sentinel], got: %s", title)
	}
	if !strings.Contains(body, "Edit") {
		t.Error("body should contain the PolicyID (Edit)")
	}
	if !strings.Contains(body, "bypass") {
		t.Error("body should contain the Pass (bypass)")
	}
	if !strings.Contains(body, "Repeated retry pattern") {
		t.Error("body should contain the Reasoning")
	}
	if !strings.Contains(body, "Review Edit policy scope") {
		t.Error("body should contain the Remediation")
	}
}

func TestGhClient_FormatIssue_NoRemediation(t *testing.T) {
	finding := analyzer.InterpretedFinding{
		Finding: analyzer.Finding{
			ID:         "an-vol-1",
			Pass:       "anomaly",
			PolicyID:   "volume_spike",
			DetectedAt: time.Now(),
		},
		Confidence:     0.72,
		SuggestedTitle: "Unusual volume spike in Bash calls",
	}

	title, body := router.FormatIssue(finding)

	if title == "" {
		t.Error("title should not be empty")
	}
	// No remediation — body should still be non-empty and not crash.
	if body == "" {
		t.Error("body should not be empty even with no remediation")
	}
}
