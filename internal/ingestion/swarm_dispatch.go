package ingestion

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

// SwarmDispatchAdapter reads swarm-events.jsonl and converts entries to
// ExecutionEvents. The file format already closely matches ExecutionEvent.
type SwarmDispatchAdapter struct {
	telemetryPath string
}

// NewSwarmDispatchAdapter constructs a SwarmDispatchAdapter.
func NewSwarmDispatchAdapter(telemetryPath string) *SwarmDispatchAdapter {
	return &SwarmDispatchAdapter{telemetryPath: telemetryPath}
}

// swarmEvent is the JSON shape from emit-telemetry.sh / swarm-events.jsonl.
type swarmEvent struct {
	ID          string            `json:"id"`
	Timestamp   time.Time         `json:"timestamp"`
	Source      string            `json:"source"`
	SessionID   string            `json:"session_id"`
	SequenceNum int               `json:"sequence_num"`
	Actor       string            `json:"actor"`
	AgentID     string            `json:"agent_id"`
	Command     string            `json:"command"`
	Arguments   []string          `json:"arguments"`
	ExitCode    *int              `json:"exit_code"`
	DurationMs  *int64            `json:"duration_ms"`
	WorkingDir  string            `json:"working_dir"`
	Repository  string            `json:"repository"`
	Branch      string            `json:"branch"`
	StdoutHash  string            `json:"stdout_hash"`
	StderrHash  string            `json:"stderr_hash"`
	HasError    bool              `json:"has_error"`
	Tags        map[string]string `json:"tags"`
}

// Ingest reads new swarm dispatch events since the checkpoint offset.
func (a *SwarmDispatchAdapter) Ingest(ctx context.Context, cp *Checkpoint) ([]ExecutionEvent, *Checkpoint, error) {
	var offset int64
	if cp != nil && cp.LastRunID != "" {
		if n, err := strconv.ParseInt(cp.LastRunID, 10, 64); err == nil {
			offset = n
		}
	}

	f, err := os.Open(a.telemetryPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open swarm events: %w", err)
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, nil, fmt.Errorf("seek to offset %d: %w", offset, err)
		}
	}

	var events []ExecutionEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var se swarmEvent
		if err := json.Unmarshal(line, &se); err != nil {
			continue // skip malformed lines
		}

		ev := ExecutionEvent{
			ID:          se.ID,
			Timestamp:   se.Timestamp,
			Source:      SourceSwarmDispatch,
			SessionID:   se.SessionID,
			SequenceNum: se.SequenceNum,
			Actor:       ActorType(se.Actor),
			AgentID:     se.AgentID,
			Command:     se.Command,
			Arguments:   se.Arguments,
			ExitCode:    se.ExitCode,
			DurationMs:  se.DurationMs,
			WorkingDir:  se.WorkingDir,
			Repository:  se.Repository,
			Branch:      se.Branch,
			StdoutHash:  se.StdoutHash,
			StderrHash:  se.StderrHash,
			HasError:    se.HasError,
			Tags:        se.Tags,
		}
		events = append(events, ev)
	}

	// Get new file offset.
	newOffset, _ := f.Seek(0, 1)
	if info, err := f.Stat(); err == nil && newOffset == 0 {
		newOffset = info.Size()
	}

	newCp := &Checkpoint{
		Adapter:   "swarm_dispatch",
		LastRunID: strconv.FormatInt(newOffset, 10),
		LastRunAt: time.Now(),
	}
	return events, newCp, scanner.Err()
}
