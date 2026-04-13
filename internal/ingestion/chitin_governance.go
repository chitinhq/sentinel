package ingestion

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// chitinEvent is the JSON shape emitted by chitin hooks (.chitin/events.jsonl).
type chitinEvent struct {
	Ts          time.Time              `json:"ts"`
	SID         string                 `json:"sid"`
	Agent       string                 `json:"agent"`
	Tool        string                 `json:"tool"`
	Action      string                 `json:"action"`
	Path        string                 `json:"path"`
	Command     string                 `json:"command"`
	Outcome     string                 `json:"outcome"` // "allow" or "deny"
	Reason      string                 `json:"reason"`
	Source      string                 `json:"source"` // "policy", "invariant", etc.
	LatencyUs   int64                  `json:"latency_us"`
	Explanation map[string]interface{} `json:"explanation,omitempty"`
	TrustScore  *int                   `json:"trust_score,omitempty"`
	TrustLevel  string                 `json:"trust_level,omitempty"`
	// Fields carries structured payload from flow.Emit / heartbeat callers
	// (e.g. {"host":"ubuntu-...","uptime_seconds":3600,"model":"qwen..."}).
	// Flattened into metadata so rollups like `sentinel drivers` can query
	// metadata->>'host' directly.
	Fields map[string]interface{} `json:"fields,omitempty"`
}

// GovernanceEventRow captures everything we need to persist a single
// governance_events row. The shape mirrors the production table schema
// (see internal/mcp/ingest.go IngestFile for the canonical INSERT) and
// is exposed so tests can assert the writer was invoked with the right data.
type GovernanceEventRow struct {
	TenantID    string
	SessionID   string
	AgentID     string
	EventType   string
	Action      string
	Resource    string
	Outcome     string
	RiskLevel   string
	EventSource string
	DriverType  string
	Metadata    map[string]any
	Timestamp   time.Time
}

// GovernanceWriter is the minimum surface the adapter needs from a DB.
// Tests pass a fake; production uses pgxWriter below.
type GovernanceWriter interface {
	InsertGovernance(ctx context.Context, row GovernanceEventRow) error
}

// PgxGovernanceWriter wraps a pgxpool.Pool so it satisfies GovernanceWriter.
type PgxGovernanceWriter struct {
	Pool *pgxpool.Pool
}

func (w *PgxGovernanceWriter) InsertGovernance(ctx context.Context, row GovernanceEventRow) error {
	md, _ := json.Marshal(row.Metadata)
	_, err := w.Pool.Exec(ctx, `
		INSERT INTO governance_events
			(tenant_id, session_id, agent_id, event_type, action, resource, outcome, risk_level, event_source, driver_type, metadata, timestamp)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12::timestamptz)
	`,
		row.TenantID,
		row.SessionID,
		row.AgentID,
		row.EventType,
		row.Action,
		row.Resource,
		row.Outcome,
		row.RiskLevel,
		row.EventSource,
		row.DriverType,
		string(md),
		row.Timestamp,
	)
	return err
}

// ChitinGovernanceAdapter reads .chitin/events.jsonl from configured workspace
// directories and writes each event directly into governance_events — the
// same table sentinel-mcp's IngestFile targets and the only table the
// analyzer queries. Previously this adapter wrote execution_events, which
// the analyzer never read — a silent drop. See sentinel#31.
type ChitinGovernanceAdapter struct {
	workspacePaths []string
	tenantID       string
	writer         GovernanceWriter
}

// NewChitinGovernanceAdapter constructs a ChitinGovernanceAdapter.
// workspacePaths are directories containing .chitin/events.jsonl files.
// tenantID is the tenants.id FK to stamp on every row (required).
// writer handles the actual persistence; pass &PgxGovernanceWriter{Pool: pool}
// in production or a fake in tests.
func NewChitinGovernanceAdapter(workspacePaths []string, tenantID string, writer GovernanceWriter) *ChitinGovernanceAdapter {
	return &ChitinGovernanceAdapter{
		workspacePaths: workspacePaths,
		tenantID:       tenantID,
		writer:         writer,
	}
}

// checkpointOffsets stores per-workspace file offsets as the checkpoint's LastRunID.
// Format: "path1:offset1,path2:offset2"
type checkpointOffsets map[string]int64

func parseOffsets(s string) checkpointOffsets {
	offsets := make(checkpointOffsets)
	if s == "" {
		return offsets
	}
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) == 2 {
			if n, err := strconv.ParseInt(kv[1], 10, 64); err == nil {
				offsets[kv[0]] = n
			}
		}
	}
	return offsets
}

func (co checkpointOffsets) String() string {
	parts := make([]string, 0, len(co))
	for k, v := range co {
		parts = append(parts, fmt.Sprintf("%s:%d", k, v))
	}
	return strings.Join(parts, ",")
}

// Ingest reads new governance events from all workspace paths since the
// checkpoint and inserts them into governance_events via the adapter's
// writer. Returns the number of rows written and an updated checkpoint.
func (a *ChitinGovernanceAdapter) Ingest(ctx context.Context, cp *Checkpoint) (int, *Checkpoint, error) {
	if a.writer == nil {
		return 0, nil, fmt.Errorf("chitin_governance: no writer configured")
	}
	if a.tenantID == "" {
		return 0, nil, fmt.Errorf("chitin_governance: tenant_id is required")
	}

	offsets := checkpointOffsets{}
	if cp != nil {
		offsets = parseOffsets(cp.LastRunID)
	}

	total := 0
	for _, ws := range a.workspacePaths {
		eventsPath := filepath.Join(ws, ".chitin", "events.jsonl")
		n, newOffset, err := a.ingestFile(ctx, eventsPath, offsets[ws])
		if err != nil {
			// Missing/unreadable is common (empty workspace, permissions).
			// Log at warn so it's visible — silent skips hid the broken
			// placeholder config trap for weeks.
			if os.IsNotExist(err) {
				log.Printf("sentinel: chitin_governance: no events.jsonl at %s (workspace not yet emitting)", eventsPath)
			} else {
				log.Printf("sentinel: chitin_governance: skip %s: %v", eventsPath, err)
			}
			continue
		}
		total += n
		offsets[ws] = newOffset
	}

	newCp := &Checkpoint{
		Adapter:   "chitin_governance",
		LastRunID: offsets.String(),
		LastRunAt: time.Now(),
	}
	return total, newCp, nil
}

// ingestFile reads and inserts events from a single .chitin/events.jsonl file,
// starting at the provided byte offset. Returns the count inserted and the
// new offset for checkpointing.
func (a *ChitinGovernanceAdapter) ingestFile(ctx context.Context, path string, offset int64) (int, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, offset, err
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return 0, offset, err
		}
	}

	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var ce chitinEvent
		if err := json.Unmarshal(line, &ce); err != nil {
			continue // skip malformed lines
		}

		row := chitinToGovernance(ce, a.tenantID)
		if err := a.writer.InsertGovernance(ctx, row); err != nil {
			// Return the position we stopped at so a retry resumes here.
			pos, _ := f.Seek(0, 1)
			return count, pos, fmt.Errorf("insert governance_events: %w", err)
		}
		count++
	}

	newOffset, _ := f.Seek(0, 1) // current position after scanning
	if info, err := f.Stat(); err == nil && newOffset == 0 {
		newOffset = info.Size()
	}

	return count, newOffset, scanner.Err()
}

// chitinToGovernance maps a chitin hook event to a governance_events row.
// Keep this shape aligned with internal/mcp.IngestFile — both paths must
// produce rows the analyzer can read interchangeably.
func chitinToGovernance(ce chitinEvent, tenantID string) GovernanceEventRow {
	resource := ce.Path
	if resource == "" {
		resource = ce.Command
	}

	riskLevel := "low"
	if ce.Outcome == "deny" {
		riskLevel = "medium"
		if ce.Source == "invariant" {
			riskLevel = "high"
		}
	}

	metadata := map[string]any{
		"command":    ce.Command,
		"source":     ce.Source,
		"latency_us": ce.LatencyUs,
		"reason":     ce.Reason,
		"action":     ce.Action,
		"tool":       ce.Tool,
	}
	if ce.TrustScore != nil {
		metadata["trust_score"] = *ce.TrustScore
	}
	if ce.TrustLevel != "" {
		metadata["trust_level"] = ce.TrustLevel
	}
	if ce.Path != "" {
		metadata["path"] = ce.Path
	}
	// Flatten ce.Fields into metadata so callers like `sentinel drivers`
	// can read metadata->>'host', metadata->>'uptime_seconds', etc.
	// Existing keys (tool, action, etc) take precedence over fields.
	for k, v := range ce.Fields {
		if _, exists := metadata[k]; !exists {
			metadata[k] = v
		}
	}

	return GovernanceEventRow{
		TenantID:    tenantID,
		SessionID:   ce.SID,
		AgentID:     ce.Agent,
		EventType:   "tool_call",
		Action:      ce.Tool, // matches IngestFile: action = tool name
		Resource:    resource,
		Outcome:     ce.Outcome,
		RiskLevel:   riskLevel,
		EventSource: mapChitinSourceToEventSource(ce.Source),
		DriverType:  ce.Agent,
		Metadata:    metadata,
		Timestamp:   ce.Ts,
	}
}

// mapChitinSourceToEventSource maps the ce.Source field from a chitin hook
// event to the governance_events.event_source column. See issue #41.
//
//	"flow"                                    -> "flow"       (from flow.Emit)
//	"heartbeat"                               -> "heartbeat"  (from chitin driver heartbeat)
//	"policy"|"invariant"|"fail-open"|
//	  "fail-closed"                           -> "agent"      (governance decisions)
//	"octi"|"atlas"                            -> "mcp_server" (from mcptrace)
//	anything else                             -> "agent"      (safe default)
func mapChitinSourceToEventSource(src string) string {
	switch src {
	case "flow":
		return "flow"
	case "heartbeat":
		return "heartbeat"
	case "policy", "invariant", "fail-open", "fail-closed":
		return "agent"
	case "octi", "atlas":
		return "mcp_server"
	default:
		return "agent"
	}
}

// inferRepo attempts to extract a repo name from a file path.
// e.g. "/path/to/sentinel/.chitin/events.jsonl" → "chitinhq/sentinel"
// Retained for potential future use / cross-adapter consistency.
func inferRepo(path string) string {
	dir := filepath.Dir(filepath.Dir(path)) // strip /.chitin/events.jsonl
	base := filepath.Base(dir)
	if base != "" && base != "." && base != "/" {
		return "chitinhq/" + base
	}
	return ""
}
