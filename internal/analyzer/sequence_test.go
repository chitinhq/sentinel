package analyzer

import (
	"testing"

	"github.com/chitinhq/sentinel/internal/config"
	"github.com/chitinhq/sentinel/internal/db"
)

// makeSeq is a helper that builds a SessionSequence from a list of
// (command, hasError) pairs.
func makeSeq(id string, pairs [][2]interface{}) db.SessionSequence {
	events := make([]db.SequenceEntry, len(pairs))
	for i, p := range pairs {
		events[i] = db.SequenceEntry{
			Command:  p[0].(string),
			HasError: p[1].(bool),
		}
	}
	return db.SessionSequence{SessionID: id, Events: events}
}

func TestDetectFailureSequences(t *testing.T) {
	cfg := config.SequenceDetectionConfig{
		NgramRange:           [2]int{2, 5},
		MinFrequency:         3,
		FailureRateThreshold: 0.6,
	}

	// Three identical sessions: "git pull" → "make test" where "make test" always fails.
	repeatedSeqs := []db.SessionSequence{
		makeSeq("s1", [][2]interface{}{{"git pull", false}, {"make test", true}}),
		makeSeq("s2", [][2]interface{}{{"git pull", false}, {"make test", true}}),
		makeSeq("s3", [][2]interface{}{{"git pull", false}, {"make test", true}}),
	}

	tests := []struct {
		name      string
		sequences []db.SessionSequence
		wantMin   int // minimum number of findings expected
		wantPass  string
	}{
		{
			name:      "bigram detected with 3+ occurrences",
			sequences: repeatedSeqs,
			wantMin:   1,
			wantPass:  "sequence_detection",
		},
		{
			name: "below min frequency — skipped",
			sequences: []db.SessionSequence{
				makeSeq("s1", [][2]interface{}{{"checkout", false}, {"test", true}}),
				makeSeq("s2", [][2]interface{}{{"checkout", false}, {"test", true}}),
				// Only 2 occurrences, MinFrequency=3
			},
			wantMin:  0,
			wantPass: "",
		},
		{
			name:      "empty input",
			sequences: []db.SessionSequence{},
			wantMin:   0,
			wantPass:  "",
		},
		{
			name: "below failure rate threshold — skipped",
			sequences: []db.SessionSequence{
				// "build" succeeds 4 out of 5 times after "install"
				makeSeq("s1", [][2]interface{}{{"install", false}, {"build", false}}),
				makeSeq("s2", [][2]interface{}{{"install", false}, {"build", false}}),
				makeSeq("s3", [][2]interface{}{{"install", false}, {"build", false}}),
				makeSeq("s4", [][2]interface{}{{"install", false}, {"build", false}}),
				makeSeq("s5", [][2]interface{}{{"install", false}, {"build", true}}),
			},
			wantMin:  0,
			wantPass: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := DetectFailureSequences(tt.sequences, cfg)
			if len(findings) < tt.wantMin {
				t.Fatalf("want at least %d findings, got %d", tt.wantMin, len(findings))
			}
			if tt.wantMin > 0 {
				for _, f := range findings {
					if f.Pass != tt.wantPass {
						t.Errorf("pass: want %s, got %s", tt.wantPass, f.Pass)
					}
					if f.Metrics.SampleSize < cfg.MinFrequency {
						t.Errorf("sample size %d below min frequency %d", f.Metrics.SampleSize, cfg.MinFrequency)
					}
					if f.Metrics.Rate < cfg.FailureRateThreshold {
						t.Errorf("failure rate %.2f below threshold %.2f", f.Metrics.Rate, cfg.FailureRateThreshold)
					}
				}
			}
		})
	}
}
