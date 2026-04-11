package health

import (
	"testing"
)

func TestLatencyScore(t *testing.T) {
	tests := []struct {
		p95Ms    int64
		expected int
	}{
		{0, 100},
		{30000, 50},
		{60000, 0},
		{90000, 0},    // above max → 0
		{-1, 100},     // negative → 100
		{1000, 99},    // 1s → 99
		{10000, 84},   // 10s → 84
	}

	for _, tt := range tests {
		got := latencyScore(tt.p95Ms)
		if got != tt.expected {
			t.Errorf("latencyScore(%d) = %d, want %d", tt.p95Ms, got, tt.expected)
		}
	}
}

func TestClamp(t *testing.T) {
	tests := []struct {
		input    int
		expected int
	}{
		{-10, 0},
		{0, 0},
		{50, 50},
		{100, 100},
		{150, 100},
	}

	for _, tt := range tests {
		got := clamp(tt.input)
		if got != tt.expected {
			t.Errorf("clamp(%d) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestWeightedScore(t *testing.T) {
	s := &Scorer{weights: DefaultWeights()}

	dims := map[string]int{
		"success_rate":          100,
		"governance_compliance": 100,
		"latency":              100,
		"budget_health":        100,
		"stability":            100,
	}
	score := s.weightedScore(dims)
	if score != 100 {
		t.Errorf("all 100s should give 100, got %d", score)
	}

	dims2 := map[string]int{
		"success_rate":          0,
		"governance_compliance": 0,
		"latency":              0,
		"budget_health":        0,
		"stability":            0,
	}
	score2 := s.weightedScore(dims2)
	if score2 != 0 {
		t.Errorf("all 0s should give 0, got %d", score2)
	}

	// Mixed: success_rate=80 (30%), compliance=90 (25%), latency=70 (15%), budget=60 (15%), stability=50 (15%)
	// = 24 + 22.5 + 10.5 + 9 + 7.5 = 73.5 → 74
	dims3 := map[string]int{
		"success_rate":          80,
		"governance_compliance": 90,
		"latency":              70,
		"budget_health":        60,
		"stability":            50,
	}
	score3 := s.weightedScore(dims3)
	if score3 != 74 {
		t.Errorf("mixed dims should give 74, got %d", score3)
	}
}

func TestComputeDimensions(t *testing.T) {
	s := &Scorer{weights: DefaultWeights()}

	st := &stats{
		total:      20,
		successes:  15,
		failures:   5,
		p95Latency: 1000,
		govTotal:   18,
		govAllow:   16,
	}

	dims := s.computeDimensions(st, 0.70)
	// success_rate = 15/20 = 75
	if dims["success_rate"] != 75 {
		t.Errorf("success_rate = %d, want 75", dims["success_rate"])
	}
	// governance_compliance = 16/18 = 88
	if dims["governance_compliance"] != 88 {
		t.Errorf("governance_compliance = %d, want 88", dims["governance_compliance"])
	}
	// latency = 1s → 99
	if dims["latency"] != 99 {
		t.Errorf("latency = %d, want 99", dims["latency"])
	}
	// stability: current=0.75, baseline=0.70, delta=+0.05 → 50+5=55
	if dims["stability"] != 55 {
		t.Errorf("stability = %d, want 55", dims["stability"])
	}
}

func TestComputeDimensions_ZeroEvents(t *testing.T) {
	s := &Scorer{weights: DefaultWeights()}

	st := &stats{total: 0}
	dims := s.computeDimensions(st, 0)
	if dims["success_rate"] != 0 {
		t.Errorf("zero events success_rate = %d, want 0", dims["success_rate"])
	}
}

func TestComputeDimensions_AllSuccess(t *testing.T) {
	s := &Scorer{weights: DefaultWeights()}

	st := &stats{
		total:     100,
		successes: 100,
		govTotal:  50,
		govAllow:  50,
	}
	dims := s.computeDimensions(st, 0.95)
	if dims["success_rate"] != 100 {
		t.Errorf("all success success_rate = %d, want 100", dims["success_rate"])
	}
	if dims["governance_compliance"] != 100 {
		t.Errorf("all allowed governance_compliance = %d, want 100", dims["governance_compliance"])
	}
}

func TestComputeDimensions_NoGovernanceData(t *testing.T) {
	s := &Scorer{weights: DefaultWeights()}

	st := &stats{
		total:     10,
		successes: 8,
		govTotal:  0,
		govAllow:  0,
	}
	dims := s.computeDimensions(st, 0)
	// No governance data → neutral 50.
	if dims["governance_compliance"] != 50 {
		t.Errorf("no gov data governance_compliance = %d, want 50", dims["governance_compliance"])
	}
}
