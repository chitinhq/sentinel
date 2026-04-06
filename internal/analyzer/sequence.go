package analyzer

import (
	"fmt"
	"strings"
	"time"

	"github.com/chitinhq/sentinel/internal/config"
	"github.com/chitinhq/sentinel/internal/db"
)

// DetectFailureSequences extracts n-grams from session command sequences and
// identifies those that appear frequently with a high failure rate.
//
// A sequence's "failure" is defined as the last command in the n-gram having
// HasError == true.  N-gram range and thresholds are taken from cfg.
func DetectFailureSequences(sequences []db.SessionSequence, cfg config.SequenceDetectionConfig) []Finding {
	if len(sequences) == 0 {
		return nil
	}

	minN := cfg.NgramRange[0]
	maxN := cfg.NgramRange[1]
	if minN < 1 {
		minN = 1
	}
	if maxN < minN {
		maxN = minN
	}

	type ngramStats struct {
		count       int
		failureCount int
	}
	stats := make(map[string]*ngramStats)

	for _, seq := range sequences {
		events := seq.Events
		for n := minN; n <= maxN; n++ {
			for i := 0; i+n <= len(events); i++ {
				slice := events[i : i+n]
				key := ngramKey(slice)
				s, ok := stats[key]
				if !ok {
					s = &ngramStats{}
					stats[key] = s
				}
				s.count++
				// Failure: last command in the n-gram has an error.
				if slice[n-1].HasError {
					s.failureCount++
				}
			}
		}
	}

	var findings []Finding
	for key, s := range stats {
		if s.count < cfg.MinFrequency {
			continue
		}
		failureRate := float64(s.failureCount) / float64(s.count)
		if failureRate < cfg.FailureRateThreshold {
			continue
		}
		id := fmt.Sprintf("sequence-%s-%d", sanitizeID(key), time.Now().Unix())
		findings = append(findings, Finding{
			ID:       id,
			Pass:     "sequence_detection",
			PolicyID: key,
			Metrics: Metrics{
				Count:      s.failureCount,
				Rate:       failureRate,
				SampleSize: s.count,
			},
			DetectedAt: time.Now(),
		})
	}
	return findings
}

// ngramKey produces a stable string key for an n-gram slice.
// Commands are joined with " → " so the separator is unlikely to appear in
// real command names.
func ngramKey(entries []db.SequenceEntry) string {
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = e.Command
	}
	return strings.Join(parts, " → ")
}
