package router

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/chitinhq/sentinel/internal/analyzer"
)

// digestBodyTmpl is the raw template source for the weekly digest.
// All custom funcs (pct, fmtDate, issueLink, passBadge, inc) are
// registered at call time in RenderDigest.
const digestBodyTmpl = `# Sentinel Research Digest

**Period:** {{ fmtDate .RangeStart }} → {{ fmtDate .RangeEnd }}
**Runs analysed:** {{ .RunCount }}
**Generated:** {{ fmtDate .Now }}

---

## Finding Summary

| # | Policy | Pass | Confidence | Count | Actionable | Issue |
|---|--------|------|------------|-------|------------|-------|
{{ range $i, $f := .Findings -}}
| {{ inc $i }} | {{ $f.Finding.PolicyID }} | {{ passBadge $f.Finding.Pass }} | {{ pct $f.Confidence }} | {{ $f.Finding.Metrics.Count }} | {{ if $f.Actionable }}yes{{ else }}—{{ end }} | {{ issueLink $.IssueURLs $f.Finding.ID }} |
{{ end }}
---

## Detail

{{ range $i, $f := .Findings -}}
### {{ inc $i }}. {{ $f.Finding.PolicyID }} — {{ $f.Finding.Pass }}

**Confidence:** {{ pct $f.Confidence }} | **Novelty:** {{ $f.Novelty }} | **Detected:** {{ fmtDate $f.Finding.DetectedAt }}
{{ if $f.Reasoning }}
**Reasoning:** {{ $f.Reasoning }}
{{ end -}}
{{ if $f.Remediation }}
**Remediation:** {{ $f.Remediation }}
{{ end }}
{{ end -}}
---
_Sentinel — automated policy telemetry analysis_
`

// digestData is the template context for RenderDigest.
type digestData struct {
	Findings   []analyzer.InterpretedFinding
	Decisions  []analyzer.RoutingDecision
	IssueURLs  map[string]string
	RunCount   int
	RangeStart time.Time
	RangeEnd   time.Time
	Now        time.Time
}

// digestFuncMap returns the template.FuncMap used by RenderDigest.
func digestFuncMap(issueURLs map[string]string) template.FuncMap {
	return template.FuncMap{
		"pct":     func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
		"fmtDate": func(t time.Time) string { return t.UTC().Format("2006-01-02") },
		"issueLink": func(issues map[string]string, id string) string {
			if issues == nil {
				return "—"
			}
			if u, ok := issues[id]; ok {
				return u
			}
			return "—"
		},
		"passBadge": func(pass string) string {
			switch pass {
			case "hotspot":
				return "hotspot"
			case "false_positive":
				return "false_positive"
			case "bypass":
				return "bypass"
			case "tool_risk":
				return "tool_risk"
			case "anomaly":
				return "anomaly"
			default:
				return pass
			}
		},
		"inc": func(i int) int { return i + 1 },
	}
}

// RenderDigest builds the weekly markdown digest string.
// findings and decisions are parallel slices; issueURLs maps finding ID →
// GitHub issue URL for findings that were filed.
func RenderDigest(
	findings []analyzer.InterpretedFinding,
	decisions []analyzer.RoutingDecision,
	issueURLs map[string]string,
	runCount int,
	rangeStart, rangeEnd time.Time,
) string {
	tmpl, err := template.New("digest").Funcs(digestFuncMap(issueURLs)).Parse(digestBodyTmpl)
	if err != nil {
		return fmt.Sprintf("# Sentinel Research Digest\n\nPeriod: %s → %s\nFindings: %d\n",
			rangeStart.Format("2006-01-02"), rangeEnd.Format("2006-01-02"), len(findings))
	}

	data := digestData{
		Findings:   findings,
		Decisions:  decisions,
		IssueURLs:  issueURLs,
		RunCount:   runCount,
		RangeStart: rangeStart,
		RangeEnd:   rangeEnd,
		Now:        time.Now().UTC(),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Sprintf("# Sentinel Research Digest\n\nPeriod: %s → %s\nFindings: %d\n",
			rangeStart.Format("2006-01-02"), rangeEnd.Format("2006-01-02"), len(findings))
	}
	return buf.String()
}

// WriteDigest saves the markdown to digestDir/sentinel-digest-<date>.md and,
// if slackWebhookURL is non-empty, posts a brief notification to Slack.
func WriteDigest(ctx context.Context, markdown, digestDir, slackWebhookURL string) error {
	if err := os.MkdirAll(digestDir, 0o755); err != nil {
		return fmt.Errorf("create digest dir: %w", err)
	}

	filename := fmt.Sprintf("sentinel-digest-%s.md", time.Now().UTC().Format("2006-01-02"))
	fullPath := filepath.Join(digestDir, filename)

	if err := os.WriteFile(fullPath, []byte(markdown), 0o644); err != nil {
		return fmt.Errorf("write digest file: %w", err)
	}

	if slackWebhookURL == "" {
		return nil
	}

	// Extract a one-line summary for the Slack notification.
	preview := "Sentinel digest ready."
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(line, "**Period:**") {
			preview = "Sentinel digest: " + strings.TrimPrefix(line, "**Period:** ")
			break
		}
	}

	payload := fmt.Sprintf(`{"text":"%s — %s"}`, preview, fullPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackWebhookURL,
		strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook returned %d", resp.StatusCode)
	}
	return nil
}
