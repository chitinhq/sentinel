package router_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"
	"github.com/AgentGuardHQ/sentinel/internal/router"
)

func testFindings() ([]analyzer.InterpretedFinding, []analyzer.RoutingDecision, map[string]string) {
	findings := []analyzer.InterpretedFinding{
		{
			Finding: analyzer.Finding{
				ID:       "f1",
				Pass:     "hotspot",
				PolicyID: "Bash",
				Metrics:  analyzer.Metrics{Count: 50, Rate: 0.3, SampleSize: 200},
				DetectedAt: time.Now(),
			},
			Confidence: 0.85,
			Actionable: true,
		},
		{
			Finding: analyzer.Finding{
				ID:       "f2",
				Pass:     "false_positive",
				PolicyID: "Edit",
				Metrics:  analyzer.Metrics{Count: 10, Rate: 0.15, SampleSize: 60},
				DetectedAt: time.Now(),
			},
			Confidence:  0.65,
			Remediation: "Loosen Edit policy",
		},
		{
			Finding: analyzer.Finding{
				ID:         "f3",
				Pass:       "anomaly",
				PolicyID:   "volume_spike",
				DetectedAt: time.Now(),
			},
			Confidence: 0.3,
		},
	}
	decisions := []analyzer.RoutingDecision{
		{Qdrant: true, GitHubIssue: true},
		{Qdrant: true, WeeklyDigest: true},
		{Qdrant: true},
	}
	issues := map[string]string{
		"f1": "https://github.com/test/repo/issues/42",
	}
	return findings, decisions, issues
}

func TestDigest_RenderMarkdown(t *testing.T) {
	findings, decisions, issues := testFindings()

	md := router.RenderDigest(findings, decisions, issues, 7, time.Now().AddDate(0, 0, -7), time.Now())

	if !strings.Contains(md, "Sentinel Research Digest") {
		t.Error("missing title")
	}
	if !strings.Contains(md, "Bash") {
		t.Error("missing Bash reference")
	}
	if !strings.Contains(md, "Edit") {
		t.Error("missing Edit reference")
	}
}

func TestDigest_RenderMarkdown_ContainsIssueURL(t *testing.T) {
	findings, decisions, issues := testFindings()

	md := router.RenderDigest(findings, decisions, issues, 7, time.Now().AddDate(0, 0, -7), time.Now())

	if !strings.Contains(md, "https://github.com/test/repo/issues/42") {
		t.Error("issue URL missing from digest")
	}
}

func TestDigest_RenderMarkdown_ContainsDateRange(t *testing.T) {
	findings, decisions, issues := testFindings()
	start := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	md := router.RenderDigest(findings, decisions, issues, 7, start, end)

	if !strings.Contains(md, "2026-03-25") {
		t.Error("start date missing")
	}
	if !strings.Contains(md, "2026-04-01") {
		t.Error("end date missing")
	}
}

func TestDigest_RenderMarkdown_EmptyFindings(t *testing.T) {
	md := router.RenderDigest(nil, nil, nil, 0, time.Now(), time.Now())
	if md == "" {
		t.Error("RenderDigest should return non-empty string even with no findings")
	}
}

func TestDigest_WriteDigest_CreatesFile(t *testing.T) {
	findings, decisions, issues := testFindings()
	md := router.RenderDigest(findings, decisions, issues, 3, time.Now().AddDate(0, 0, -3), time.Now())

	tmpDir := t.TempDir()
	if err := router.WriteDigest(nil, md, tmpDir, ""); err != nil { //nolint:staticcheck
		t.Fatalf("WriteDigest() error: %v", err)
	}

	entries, err := filepath.Glob(filepath.Join(tmpDir, "sentinel-digest-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 digest file, found %d", len(entries))
	}

	data, err := os.ReadFile(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Sentinel Research Digest") {
		t.Error("written file missing digest title")
	}
}
