package analyzer

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chitinhq/sentinel/internal/db"
)

// MCPToolPrefix identifies MCP tool invocations in governance events.
// MCP tool names follow the convention mcp__<server>__<tool>, emitted by
// the mcptrace package in octi-pulpo, atlas, and (in future) other MCP
// servers so they land alongside chitin governance events in events.jsonl.
const MCPToolPrefix = "mcp__"

// mcpServer extracts the server name from an MCP tool action.
// "mcp__octi__sprint_status" -> "octi". Returns "" if the action is not
// an MCP tool.
func mcpServer(action string) string {
	if !strings.HasPrefix(action, MCPToolPrefix) {
		return ""
	}
	rest := action[len(MCPToolPrefix):]
	i := strings.Index(rest, "__")
	if i <= 0 {
		return ""
	}
	return rest[:i]
}

// ProfileMCPUsage rolls up MCP tool invocations from action counts and
// emits findings for (1) high-volume tools — so we can see what's carrying
// weight, and (2) tools with elevated denial rates — which suggests the
// tool interface is confusing or the policy surface around it is wrong.
//
// Non-MCP actions are ignored; this pass complements hotspot/toolrisk
// rather than duplicating them.
func ProfileMCPUsage(counts []db.ActionCount) []Finding {
	type toolStats struct {
		server  string
		total   int
		denials int
	}
	byTool := make(map[string]*toolStats)

	for _, c := range counts {
		server := mcpServer(c.Action)
		if server == "" {
			continue
		}
		s, ok := byTool[c.Action]
		if !ok {
			s = &toolStats{server: server}
			byTool[c.Action] = s
		}
		s.total += c.Count
		if c.Outcome == "deny" {
			s.denials += c.Count
		}
	}

	var findings []Finding
	now := time.Now()
	for tool, s := range byTool {
		if s.total == 0 {
			continue
		}
		rate := float64(s.denials) / float64(s.total)
		findings = append(findings, Finding{
			ID:       fmt.Sprintf("mcpusage-%s-%d", tool, now.Unix()),
			Pass:     "mcp_usage",
			PolicyID: tool,
			Metrics: Metrics{
				Count:      s.total,
				Rate:       rate,
				SampleSize: s.total,
			},
			DetectedAt: now,
		})
	}

	// Order by raw call volume, highest first. That surfaces "what are
	// agents actually leaning on" before "what's failing" — failures are
	// covered by the existing hotspot pass.
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].Metrics.Count > findings[j].Metrics.Count
	})

	return findings
}
