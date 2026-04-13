package analyzer_test

import (
	"testing"
	"time"

	"github.com/chitinhq/sentinel/internal/analyzer"
)

// mkStart/mkComplete/mkFail build dotted-suffix flow events at age seconds
// ago. We use the dotted-suffix form in most tests because it keeps the
// fixtures short; see TestUnacked_HookSchema for the metadata form.
func mkEvent(flow, suffix, session string, ageSec int) analyzer.Event {
	return analyzer.Event{
		Action:    flow + suffix,
		SessionID: session,
		Timestamp: time.Now().Add(-time.Duration(ageSec) * time.Second),
	}
}

func TestUnacked_MatchedPair_NoFinding(t *testing.T) {
	events := []analyzer.Event{
		mkEvent("flow.dispatch", ".started", "s1", 300),
		mkEvent("flow.dispatch", ".completed", "s1", 290),
	}
	findings := analyzer.DetectUnacked(events, 60*time.Second)
	if len(findings) != 0 {
		t.Fatalf("matched pair: want 0 findings, got %d", len(findings))
	}
}

func TestUnacked_MatchedPair_Failed_NoFinding(t *testing.T) {
	events := []analyzer.Event{
		mkEvent("flow.dispatch", ".started", "s1", 300),
		mkEvent("flow.dispatch", ".failed", "s1", 290),
	}
	findings := analyzer.DetectUnacked(events, 60*time.Second)
	if len(findings) != 0 {
		t.Fatalf("failed-ack: want 0 findings, got %d", len(findings))
	}
}

func TestUnacked_PastTTL_Finding(t *testing.T) {
	events := []analyzer.Event{
		mkEvent("flow.dispatch", ".started", "s1", 300),
	}
	findings := analyzer.DetectUnacked(events, 60*time.Second)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(findings))
	}
	if findings[0].Metrics.Count != 1 {
		t.Errorf("want count=1, got %d", findings[0].Metrics.Count)
	}
	if findings[0].PolicyID != "flow.dispatch" {
		t.Errorf("want PolicyID=flow.dispatch, got %q", findings[0].PolicyID)
	}
	if findings[0].Pass != "unacked" {
		t.Errorf("want Pass=unacked, got %q", findings[0].Pass)
	}
	if len(findings[0].Evidence) != 1 {
		t.Errorf("want 1 evidence row, got %d", len(findings[0].Evidence))
	}
}

func TestUnacked_WithinTTL_NoFinding(t *testing.T) {
	events := []analyzer.Event{
		mkEvent("flow.dispatch", ".started", "s1", 10),
	}
	findings := analyzer.DetectUnacked(events, 60*time.Second)
	if len(findings) != 0 {
		t.Fatalf("live start within TTL: want 0 findings, got %d", len(findings))
	}
}

func TestUnacked_CrossSession_NotMatched(t *testing.T) {
	// Session A starts, Session B "completes" — these must NOT cancel out.
	events := []analyzer.Event{
		mkEvent("flow.dispatch", ".started", "sA", 300),
		mkEvent("flow.dispatch", ".completed", "sB", 290),
	}
	findings := analyzer.DetectUnacked(events, 60*time.Second)
	// sA has an unacked start. sB has a stray completed with no open — ignored.
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(findings))
	}
	if findings[0].PolicyID != "flow.dispatch" {
		t.Errorf("PolicyID = %q", findings[0].PolicyID)
	}
}

func TestUnacked_CrossSession_BothUnacked(t *testing.T) {
	events := []analyzer.Event{
		mkEvent("flow.dispatch", ".started", "sA", 300),
		mkEvent("flow.dispatch", ".started", "sB", 290),
	}
	findings := analyzer.DetectUnacked(events, 60*time.Second)
	if len(findings) != 2 {
		t.Fatalf("want 2 findings (one per session), got %d", len(findings))
	}
}

func TestUnacked_Interleaved_MultipleFlows(t *testing.T) {
	events := []analyzer.Event{
		mkEvent("flow.a", ".started", "s1", 300),
		mkEvent("flow.b", ".started", "s1", 300),
		mkEvent("flow.a", ".completed", "s1", 290),
		// flow.b left unacked, past TTL
	}
	findings := analyzer.DetectUnacked(events, 60*time.Second)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding (flow.b only), got %d", len(findings))
	}
	if findings[0].PolicyID != "flow.b" {
		t.Errorf("PolicyID = %q, want flow.b", findings[0].PolicyID)
	}
}

func TestUnacked_Empty(t *testing.T) {
	findings := analyzer.DetectUnacked(nil, 60*time.Second)
	if len(findings) != 0 {
		t.Fatalf("empty input: want 0, got %d", len(findings))
	}
}

func TestUnacked_MoreStartsThanCompletes(t *testing.T) {
	// 3 starts, 1 complete → 2 unacked for the same (flow, session).
	events := []analyzer.Event{
		mkEvent("flow.x", ".started", "s1", 300),
		mkEvent("flow.x", ".started", "s1", 280),
		mkEvent("flow.x", ".started", "s1", 260),
		mkEvent("flow.x", ".completed", "s1", 250),
	}
	findings := analyzer.DetectUnacked(events, 60*time.Second)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(findings))
	}
	if findings[0].Metrics.Count != 2 {
		t.Errorf("unacked count = %d, want 2", findings[0].Metrics.Count)
	}
}

func TestUnacked_HookSchema(t *testing.T) {
	// Chitin hook / flow.Emit form: Action = "flow.<name>", lifecycle in
	// metadata["action"] = "flow_started"/"flow_completed"/"flow_failed".
	now := time.Now()
	events := []analyzer.Event{
		{
			Action:    "flow.dispatch",
			SessionID: "s1",
			Timestamp: now.Add(-300 * time.Second),
			Metadata:  map[string]any{"action": "flow_started"},
		},
	}
	findings := analyzer.DetectUnacked(events, 60*time.Second)
	if len(findings) != 1 {
		t.Fatalf("hook schema: want 1 finding, got %d", len(findings))
	}
	if findings[0].PolicyID != "flow.dispatch" {
		t.Errorf("PolicyID = %q, want flow.dispatch", findings[0].PolicyID)
	}
}
