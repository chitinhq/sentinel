package analyzer

import (
	"testing"
	"time"
)

func mkSoulEvent(session, soul, stage, outcome string, rating float64) Event {
	md := map[string]any{}
	if soul != "" {
		md["soul"] = soul
	}
	if stage != "" {
		md["observed_stage"] = stage
	}
	if rating > 0 {
		md["rating"] = rating
	}
	return Event{
		SessionID: session,
		EventType: "tool_call",
		Action:    "Bash",
		Outcome:   outcome,
		Metadata:  md,
	}
}

func TestProfileSouls_EmptyInput(t *testing.T) {
	if got := ProfileSouls(nil, time.Now()); len(got) != 0 {
		t.Errorf("nil input: want 0 findings, got %d", len(got))
	}
	if got := ProfileSouls([]Event{}, time.Now()); len(got) != 0 {
		t.Errorf("empty input: want 0 findings, got %d", len(got))
	}
}

func TestProfileSouls_MissingSoulIsSkipped(t *testing.T) {
	now := time.Now()
	events := []Event{
		mkSoulEvent("s1", "", "debugging", "allow", 0),
		{SessionID: "s2", Outcome: "allow"}, // nil metadata — must not panic
	}
	if got := ProfileSouls(events, now); len(got) != 0 {
		t.Errorf("rows without soul should be skipped, got %d findings", len(got))
	}
}

func TestProfileSouls_SingleGroup(t *testing.T) {
	now := time.Unix(1700000000, 0)
	events := []Event{
		mkSoulEvent("s1", "feynman", "debugging", "allow", 4),
		mkSoulEvent("s1", "feynman", "debugging", "allow", 0),
		mkSoulEvent("s2", "feynman", "debugging", "deny", 5),
	}
	findings := ProfileSouls(events, now)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.PolicyID != "feynman/debugging" {
		t.Errorf("PolicyID = %q", f.PolicyID)
	}
	if f.Pass != SoulScorecardPass {
		t.Errorf("Pass = %q", f.Pass)
	}
	if f.Metrics.SampleSize != 2 {
		t.Errorf("sessions = %d, want 2", f.Metrics.SampleSize)
	}
	if f.Metrics.Count != 3 {
		t.Errorf("events = %d, want 3", f.Metrics.Count)
	}
	if f.Metrics.Rate < 0.66 || f.Metrics.Rate > 0.67 {
		t.Errorf("safety rate = %f, want ~0.667", f.Metrics.Rate)
	}

	// Unwrap sidecar SoulMetrics from Evidence[0].
	if len(f.Evidence) != 1 {
		t.Fatalf("want 1 evidence event, got %d", len(f.Evidence))
	}
	sm, ok := f.Evidence[0].Metadata["soul_metrics"].(SoulMetrics)
	if !ok {
		t.Fatalf("soul_metrics missing or wrong type: %#v", f.Evidence[0].Metadata)
	}
	if sm.RatingSamples != 2 {
		t.Errorf("rating samples = %d, want 2", sm.RatingSamples)
	}
	if sm.RatingMean != 4.5 {
		t.Errorf("rating mean = %f, want 4.5", sm.RatingMean)
	}
}

func TestProfileSouls_TwoSoulsTwoStagesFourSessions(t *testing.T) {
	now := time.Unix(1700000000, 0)
	events := []Event{
		// davinci/architecture — 1 session, allow
		mkSoulEvent("a1", "davinci", "architecture", "allow", 4.6),
		// davinci/debugging — 1 session, deny
		mkSoulEvent("a2", "davinci", "debugging", "deny", 3.0),
		// feynman/architecture — 1 session, allow
		mkSoulEvent("b1", "feynman", "architecture", "allow", 3.8),
		// feynman/debugging — 1 session, allow x2
		mkSoulEvent("b2", "feynman", "debugging", "allow", 4.2),
		mkSoulEvent("b2", "feynman", "debugging", "allow", 0),
	}
	findings := ProfileSouls(events, now)
	if len(findings) != 4 {
		t.Fatalf("want 4 findings, got %d", len(findings))
	}

	by := map[string]Finding{}
	for _, f := range findings {
		by[f.PolicyID] = f
	}
	for _, want := range []string{
		"davinci/architecture",
		"davinci/debugging",
		"feynman/architecture",
		"feynman/debugging",
	} {
		if _, ok := by[want]; !ok {
			t.Errorf("missing finding for %q", want)
		}
	}

	if r := by["davinci/debugging"].Metrics.Rate; r != 0.0 {
		t.Errorf("davinci/debugging safety rate = %f, want 0", r)
	}
	if r := by["feynman/debugging"].Metrics.Rate; r != 1.0 {
		t.Errorf("feynman/debugging safety rate = %f, want 1", r)
	}
	if c := by["feynman/debugging"].Metrics.Count; c != 2 {
		t.Errorf("feynman/debugging event count = %d, want 2", c)
	}
}

func TestProfileSouls_MissingStageBecomesUnknown(t *testing.T) {
	now := time.Unix(1700000000, 0)
	events := []Event{
		mkSoulEvent("s1", "newton", "", "allow", 0),
	}
	findings := ProfileSouls(events, now)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(findings))
	}
	if findings[0].PolicyID != "newton/unknown" {
		t.Errorf("PolicyID = %q, want newton/unknown", findings[0].PolicyID)
	}
}
