package analyzer

import (
	"fmt"
	"math"
	"time"

	"github.com/chitinhq/sentinel/internal/config"
	"github.com/chitinhq/sentinel/internal/db"
)

func DetectFalsePositives(current, baseline []db.DenialRate, cfg config.FalsePositiveConfig) []Finding {
	baselineMap := make(map[string]db.DenialRate)
	for _, b := range baseline {
		baselineMap[b.Action] = b
	}

	var findings []Finding
	for _, c := range current {
		if c.TotalCount < cfg.MinSampleSize {
			continue
		}
		b, hasBaseline := baselineMap[c.Action]
		if !hasBaseline {
			continue
		}
		baseRate := b.DenialRate
		if baseRate <= 0 || baseRate >= 1 {
			baseRate = max(0.001, min(0.999, baseRate))
		}
		stddev := math.Sqrt(baseRate * (1 - baseRate) / float64(c.TotalCount))
		if stddev == 0 {
			stddev = 0.001
		}
		deviation := (c.DenialRate - b.DenialRate) / stddev
		if deviation > cfg.DeviationThreshold || c.DenialRate > cfg.AbsoluteRateThreshold {
			findings = append(findings, Finding{
				ID:       fmt.Sprintf("fp-%s-%d", c.Action, time.Now().Unix()),
				Pass:     "false_positive",
				PolicyID: c.Action,
				Metrics: Metrics{
					Count: c.DenialCount, Rate: c.DenialRate,
					BaselineRate: b.DenialRate, Deviation: deviation,
					SampleSize: c.TotalCount,
				},
				DetectedAt: time.Now(),
			})
		}
	}
	return findings
}
