package analyzer

import (
	"fmt"
	"math"
	"time"

	"github.com/chitinhq/sentinel/internal/config"
	"github.com/chitinhq/sentinel/internal/db"
)

func DetectAnomalies(volumes []db.HourlyVolume, sessions []db.SessionDenialCount, cfg config.AnomalyConfig) []Finding {
	var findings []Finding
	findings = append(findings, detectVolumeSpikes(volumes, cfg)...)
	findings = append(findings, detectHighDenialSessions(sessions)...)
	return findings
}

func detectVolumeSpikes(volumes []db.HourlyVolume, cfg config.AnomalyConfig) []Finding {
	if len(volumes) < 3 {
		return nil
	}

	var findings []Finding
	for i, v := range volumes {
		// Compute baseline mean and stddev excluding current point
		var sum float64
		n := 0
		for j, u := range volumes {
			if j != i {
				sum += float64(u.Count)
				n++
			}
		}
		if n == 0 {
			continue
		}
		mean := sum / float64(n)
		var sqDiffSum float64
		for j, u := range volumes {
			if j != i {
				diff := float64(u.Count) - mean
				sqDiffSum += diff * diff
			}
		}
		stddev := math.Sqrt(sqDiffSum / float64(n))
		if stddev == 0 {
			continue
		}
		deviation := (float64(v.Count) - mean) / stddev
		if deviation > cfg.VolumeSpikeThreshold {
			findings = append(findings, Finding{
				ID:       fmt.Sprintf("anomaly-spike-%d", v.Hour.Unix()),
				Pass:     "anomaly",
				PolicyID: fmt.Sprintf("volume_spike:%s", v.Hour.Format("2006-01-02T15")),
				Metrics:  Metrics{Count: v.Count, Rate: mean, BaselineRate: mean, Deviation: deviation, SampleSize: len(volumes)},
				DetectedAt: time.Now(),
			})
		}
	}
	return findings
}

func detectHighDenialSessions(sessions []db.SessionDenialCount) []Finding {
	if len(sessions) < 2 {
		return nil
	}

	var findings []Finding
	for i, s := range sessions {
		// Compute baseline mean and stddev excluding current session
		var sum float64
		n := 0
		for j, u := range sessions {
			if j != i {
				sum += float64(u.Denials)
				n++
			}
		}
		if n == 0 {
			continue
		}
		mean := sum / float64(n)
		var sqDiffSum float64
		for j, u := range sessions {
			if j != i {
				diff := float64(u.Denials) - mean
				sqDiffSum += diff * diff
			}
		}
		stddev := math.Sqrt(sqDiffSum / float64(n))
		if stddev == 0 {
			continue
		}
		deviation := (float64(s.Denials) - mean) / stddev
		if deviation > 2.0 {
			findings = append(findings, Finding{
				ID:       fmt.Sprintf("anomaly-session-%s", s.SessionID),
				Pass:     "anomaly",
				PolicyID: fmt.Sprintf("session:%s", s.SessionID),
				Metrics:  Metrics{Count: s.Denials, Rate: float64(s.Denials) / float64(s.Total), Deviation: deviation, SampleSize: s.Total},
				DetectedAt: time.Now(),
			})
		}
	}
	return findings
}
