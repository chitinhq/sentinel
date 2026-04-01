package db

import "time"

// Event mirrors a row from governance_events in Neon.
type Event struct {
	ID            string
	Timestamp     time.Time
	AgentID       string
	SessionID     string
	EventType     string // e.g. "tool_call"
	Action        string // tool name: "Bash", "Edit", "Read", etc.
	Resource      string
	Outcome       string // "allow", "deny"
	RiskLevel     string // "low", "medium", "high", "critical"
	PolicyVersion string
	Metadata      map[string]any // parsed JSON metadata
}

// MatchedPolicy extracts the policy identifier from event metadata.
// Returns the action name as fallback if no policy is recorded.
func (e Event) MatchedPolicy() string {
	if e.Metadata != nil {
		if p, ok := e.Metadata["matched_policy"].(string); ok && p != "" {
			return p
		}
		if p, ok := e.Metadata["policy_id"].(string); ok && p != "" {
			return p
		}
		if p, ok := e.Metadata["invariant"].(string); ok && p != "" {
			return p
		}
	}
	return e.Action
}
