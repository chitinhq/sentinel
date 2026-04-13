package ingestion

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeGovernanceWriter captures every row the adapter emits so tests can
// assert on the final shape without needing a live Postgres.
type fakeGovernanceWriter struct {
	mu   sync.Mutex
	rows []GovernanceEventRow
	err  error
}

func (f *fakeGovernanceWriter) InsertGovernance(_ context.Context, row GovernanceEventRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, row)
	return nil
}

const testTenantID = "00000000-0000-0000-0000-000000000001"

func TestChitinGovernanceAdapter_Ingest(t *testing.T) {
	dir := t.TempDir()
	chitinDir := filepath.Join(dir, ".chitin")
	os.MkdirAll(chitinDir, 0755)

	eventsFile := filepath.Join(chitinDir, "events.jsonl")
	data := `{"ts":"2026-04-11T19:00:00Z","sid":"sess-1","agent":"claude-code","tool":"Bash","action":"exec","path":"","command":"git push origin main","outcome":"deny","reason":"Direct push to protected branch","source":"policy","latency_us":1200}
{"ts":"2026-04-11T19:01:00Z","sid":"sess-1","agent":"claude-code","tool":"Read","action":"read","path":"src/main.go","command":"","outcome":"allow","reason":"","source":"policy","latency_us":500}
`
	os.WriteFile(eventsFile, []byte(data), 0644)

	w := &fakeGovernanceWriter{}
	adapter := NewChitinGovernanceAdapter([]string{dir}, testTenantID, w)
	n, cp, err := adapter.Ingest(context.Background(), nil)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 rows inserted, got %d", n)
	}
	if len(w.rows) != 2 {
		t.Fatalf("expected 2 rows captured, got %d", len(w.rows))
	}

	r0 := w.rows[0]
	if r0.TenantID != testTenantID {
		t.Errorf("expected tenant_id propagated, got %q", r0.TenantID)
	}
	if r0.Outcome != "deny" {
		t.Errorf("expected outcome=deny, got %q", r0.Outcome)
	}
	if r0.RiskLevel != "medium" {
		t.Errorf("expected policy-deny → medium risk, got %q", r0.RiskLevel)
	}
	if r0.Action != "Bash" {
		t.Errorf("expected action=Bash, got %q", r0.Action)
	}
	if r0.Resource != "git push origin main" {
		t.Errorf("expected resource fallback to command, got %q", r0.Resource)
	}
	if r0.EventType != "tool_call" {
		t.Errorf("expected event_type=tool_call, got %q", r0.EventType)
	}
	if r0.AgentID != "claude-code" {
		t.Errorf("expected agent_id=claude-code, got %q", r0.AgentID)
	}
	if r0.Metadata["reason"] != "Direct push to protected branch" {
		t.Errorf("expected reason in metadata, got %v", r0.Metadata["reason"])
	}

	r1 := w.rows[1]
	if r1.Outcome != "allow" {
		t.Errorf("expected outcome=allow, got %q", r1.Outcome)
	}
	if r1.RiskLevel != "low" {
		t.Errorf("expected allow → low risk, got %q", r1.RiskLevel)
	}
	if r1.Resource != "src/main.go" {
		t.Errorf("expected resource=path, got %q", r1.Resource)
	}

	// Checkpoint should be set.
	if cp == nil {
		t.Fatal("expected non-nil checkpoint")
	}
	if cp.Adapter != "chitin_governance" {
		t.Errorf("expected adapter chitin_governance, got %q", cp.Adapter)
	}

	// Second ingest with checkpoint should return 0 rows.
	n2, _, err := adapter.Ingest(context.Background(), cp)
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if n2 != 0 {
		t.Errorf("expected 0 rows on re-ingest, got %d", n2)
	}
}

// TestChitinGovernanceAdapter_TrustTelemetry verifies that trust_score /
// trust_level flow from chitin events into governance_events.metadata. The
// zero-score case is important: chitin emits trust_score as *int precisely
// so "score = 0" (lowest-trust agent) survives omitempty serialization,
// and Sentinel must not drop that signal when forwarding to metadata.
func TestChitinGovernanceAdapter_TrustTelemetry(t *testing.T) {
	dir := t.TempDir()
	chitinDir := filepath.Join(dir, ".chitin")
	os.MkdirAll(chitinDir, 0755)

	eventsFile := filepath.Join(chitinDir, "events.jsonl")
	data := `{"ts":"2026-04-12T09:00:00Z","sid":"s","agent":"a1","tool":"Bash","action":"exec","outcome":"allow","source":"policy","latency_us":100,"trust_score":500,"trust_level":"baseline"}
{"ts":"2026-04-12T09:01:00Z","sid":"s","agent":"a2","tool":"Bash","action":"exec","outcome":"deny","source":"policy","latency_us":100,"trust_score":0,"trust_level":"restricted"}
{"ts":"2026-04-12T09:02:00Z","sid":"s","agent":"a3","tool":"Bash","action":"exec","outcome":"allow","source":"policy","latency_us":100}
`
	os.WriteFile(eventsFile, []byte(data), 0644)

	w := &fakeGovernanceWriter{}
	adapter := NewChitinGovernanceAdapter([]string{dir}, testTenantID, w)
	if _, _, err := adapter.Ingest(context.Background(), nil); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(w.rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(w.rows))
	}

	if got := w.rows[0].Metadata["trust_score"]; got != 500 {
		t.Errorf("baseline trust_score metadata = %v, want 500", got)
	}
	if got := w.rows[0].Metadata["trust_level"]; got != "baseline" {
		t.Errorf("baseline trust_level metadata = %v, want baseline", got)
	}

	// The keystone assertion: score=0 must survive.
	if got := w.rows[1].Metadata["trust_score"]; got != 0 {
		t.Errorf("restricted trust_score metadata = %v, want 0 (score=0 must be preserved)", got)
	}
	if got := w.rows[1].Metadata["trust_level"]; got != "restricted" {
		t.Errorf("restricted trust_level metadata = %v, want restricted", got)
	}

	// Event without trust fields must not gain empty metadata entries.
	if _, ok := w.rows[2].Metadata["trust_score"]; ok {
		t.Errorf("untagged event should not have trust_score metadata")
	}
	if _, ok := w.rows[2].Metadata["trust_level"]; ok {
		t.Errorf("untagged event should not have trust_level metadata")
	}
}

func TestChitinGovernanceAdapter_IncrementalRead(t *testing.T) {
	dir := t.TempDir()
	chitinDir := filepath.Join(dir, ".chitin")
	os.MkdirAll(chitinDir, 0755)

	eventsFile := filepath.Join(chitinDir, "events.jsonl")

	// Write 3 initial events.
	initial := `{"ts":"2026-04-11T19:00:00Z","sid":"s1","agent":"claude-code","tool":"Bash","action":"exec","outcome":"allow","reason":"","source":"policy","latency_us":100}
{"ts":"2026-04-11T19:01:00Z","sid":"s1","agent":"claude-code","tool":"Edit","action":"write","outcome":"allow","reason":"","source":"policy","latency_us":200}
{"ts":"2026-04-11T19:02:00Z","sid":"s1","agent":"claude-code","tool":"Bash","action":"exec","outcome":"deny","reason":"blocked","source":"invariant","latency_us":300}
`
	os.WriteFile(eventsFile, []byte(initial), 0644)

	w := &fakeGovernanceWriter{}
	adapter := NewChitinGovernanceAdapter([]string{dir}, testTenantID, w)
	n1, cp1, err := adapter.Ingest(context.Background(), nil)
	if err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	if n1 != 3 {
		t.Fatalf("expected 3 rows, got %d", n1)
	}

	// Invariant-sourced deny should escalate to high risk.
	if w.rows[2].RiskLevel != "high" {
		t.Errorf("expected invariant deny → high risk, got %q", w.rows[2].RiskLevel)
	}

	// Append 2 more events.
	f, _ := os.OpenFile(eventsFile, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString(`{"ts":"2026-04-11T19:03:00Z","sid":"s2","agent":"copilot","tool":"Write","action":"write","outcome":"allow","reason":"","source":"policy","latency_us":150}
{"ts":"2026-04-11T19:04:00Z","sid":"s2","agent":"copilot","tool":"Bash","action":"exec","outcome":"deny","reason":"risky","source":"policy","latency_us":250}
`)
	f.Close()

	// Second ingest should return only the 2 new rows.
	n2, _, err := adapter.Ingest(context.Background(), cp1)
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if n2 != 2 {
		t.Errorf("expected 2 new rows, got %d", n2)
	}
	if len(w.rows) != 5 {
		t.Fatalf("expected 5 total rows captured, got %d", len(w.rows))
	}
	if w.rows[3].AgentID != "copilot" {
		t.Errorf("expected copilot agent on row 4, got %q", w.rows[3].AgentID)
	}
}

// TestMapChitinSourceToEventSource locks in the ce.Source -> event_source
// mapping documented on issue #41. A regression here will silently re-break
// downstream filters (e.g. `sentinel flows` uses event_source IN (...)).
func TestMapChitinSourceToEventSource(t *testing.T) {
	cases := []struct {
		source string
		want   string
	}{
		{"flow", "flow"},
		{"heartbeat", "heartbeat"},
		{"policy", "agent"},
		{"invariant", "agent"},
		{"fail-open", "agent"},
		{"fail-closed", "agent"},
		{"octi", "mcp_server"},
		{"atlas", "mcp_server"},
		{"", "agent"},
		{"something-unknown", "agent"},
	}
	for _, tc := range cases {
		got := mapChitinSourceToEventSource(tc.source)
		if got != tc.want {
			t.Errorf("mapChitinSourceToEventSource(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}
}

// TestChitinToGovernance_EventSourceMapping asserts the full row builder
// carries ce.Source through to the row's EventSource column.
func TestChitinToGovernance_EventSourceMapping(t *testing.T) {
	cases := map[string]string{
		"flow":        "flow",
		"heartbeat":   "heartbeat",
		"policy":      "agent",
		"invariant":   "agent",
		"fail-open":   "agent",
		"fail-closed": "agent",
		"octi":        "mcp_server",
		"atlas":       "mcp_server",
		"mystery":     "agent",
	}
	for src, want := range cases {
		ce := chitinEvent{
			SID:     "sess-x",
			Agent:   "claude-code",
			Tool:    "Bash",
			Outcome: "allow",
			Source:  src,
		}
		row := chitinToGovernance(ce, testTenantID)
		if row.EventSource != want {
			t.Errorf("source %q: EventSource = %q, want %q", src, row.EventSource, want)
		}
	}
}
