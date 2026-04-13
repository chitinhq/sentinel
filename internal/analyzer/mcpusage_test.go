package analyzer

import (
	"testing"

	"github.com/chitinhq/sentinel/internal/db"
)

func TestMCPServer(t *testing.T) {
	cases := []struct {
		action, want string
	}{
		{"mcp__octi__sprint_status", "octi"},
		{"mcp__atlas__wiki_search", "atlas"},
		{"Bash", ""},
		{"mcp__", ""},
		{"mcp__bad", ""},            // no trailing __<tool>
		{"mcp____tool", ""},         // empty server segment
		{"something_else", ""},
	}
	for _, c := range cases {
		if got := mcpServer(c.action); got != c.want {
			t.Errorf("mcpServer(%q): want %q, got %q", c.action, c.want, got)
		}
	}
}

func TestProfileMCPUsage(t *testing.T) {
	counts := []db.ActionCount{
		{Action: "mcp__octi__sprint_status", Outcome: "allow", Count: 42},
		{Action: "mcp__octi__sprint_status", Outcome: "deny", Count: 2},
		{Action: "mcp__atlas__wiki_read", Outcome: "allow", Count: 10},
		{Action: "mcp__atlas__wiki_edit", Outcome: "deny", Count: 3}, // all-deny tool
		{Action: "Bash", Outcome: "allow", Count: 500},              // ignored
		{Action: "Read", Outcome: "allow", Count: 200},              // ignored
	}

	findings := ProfileMCPUsage(counts)

	if len(findings) != 3 {
		t.Fatalf("want 3 findings (one per MCP tool), got %d", len(findings))
	}

	// Expect highest-volume tool first.
	if findings[0].PolicyID != "mcp__octi__sprint_status" {
		t.Errorf("top finding should be sprint_status, got %q", findings[0].PolicyID)
	}
	if findings[0].Metrics.Count != 44 {
		t.Errorf("sprint_status total count: want 44, got %d", findings[0].Metrics.Count)
	}
	if rate := findings[0].Metrics.Rate; rate < 0.04 || rate > 0.05 {
		t.Errorf("sprint_status denial rate should be ~0.045, got %f", rate)
	}

	// All-deny tool: rate == 1.0.
	var editFinding *Finding
	for i := range findings {
		if findings[i].PolicyID == "mcp__atlas__wiki_edit" {
			editFinding = &findings[i]
		}
	}
	if editFinding == nil {
		t.Fatal("wiki_edit finding not emitted")
	}
	if editFinding.Metrics.Rate != 1.0 {
		t.Errorf("wiki_edit denial rate: want 1.0, got %f", editFinding.Metrics.Rate)
	}

	// Ensure every finding is tagged as the mcp_usage pass.
	for _, f := range findings {
		if f.Pass != "mcp_usage" {
			t.Errorf("Pass field: want mcp_usage, got %q", f.Pass)
		}
	}
}

func TestProfileMCPUsage_NoMCPEventsReturnsEmpty(t *testing.T) {
	counts := []db.ActionCount{
		{Action: "Bash", Outcome: "allow", Count: 100},
		{Action: "Edit", Outcome: "deny", Count: 5},
	}
	if got := ProfileMCPUsage(counts); len(got) != 0 {
		t.Errorf("want no findings for non-MCP input, got %d", len(got))
	}
}
