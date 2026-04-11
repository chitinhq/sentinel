package ingestion

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
}

// ChitinGovernanceAdapter reads .chitin/events.jsonl from configured workspace
// directories and converts them to ExecutionEvents.
type ChitinGovernanceAdapter struct {
	workspacePaths []string
}

// NewChitinGovernanceAdapter constructs a ChitinGovernanceAdapter.
// workspacePaths are directories containing .chitin/events.jsonl files.
func NewChitinGovernanceAdapter(workspacePaths []string) *ChitinGovernanceAdapter {
	return &ChitinGovernanceAdapter{workspacePaths: workspacePaths}
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

// Ingest reads new governance events from all workspace paths since the checkpoint.
func (a *ChitinGovernanceAdapter) Ingest(ctx context.Context, cp *Checkpoint) ([]ExecutionEvent, *Checkpoint, error) {
	offsets := checkpointOffsets{}
	if cp != nil {
		offsets = parseOffsets(cp.LastRunID)
	}

	var all []ExecutionEvent
	for _, ws := range a.workspacePaths {
		eventsPath := filepath.Join(ws, ".chitin", "events.jsonl")
		events, newOffset, err := a.readFile(eventsPath, offsets[ws])
		if err != nil {
			// Skip missing or unreadable files — log and continue.
			continue
		}
		all = append(all, events...)
		offsets[ws] = newOffset
	}

	newCp := &Checkpoint{
		Adapter:   "chitin_governance",
		LastRunID: offsets.String(),
		LastRunAt: time.Now(),
	}
	return all, newCp, nil
}

func (a *ChitinGovernanceAdapter) readFile(path string, offset int64) ([]ExecutionEvent, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, offset, err
		}
	}

	// Infer repository from workspace path.
	repo := inferRepo(path)

	var events []ExecutionEvent
	seq := 0
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

		exitCode := 0
		hasError := false
		if ce.Outcome == "deny" {
			exitCode = 2
			hasError = true
		}

		durationMs := ce.LatencyUs / 1000

		ev := ExecutionEvent{
			ID:          fmt.Sprintf("cg-%s-%s-%d", ce.SID, ce.Tool, seq),
			Timestamp:   ce.Ts,
			Source:      SourceChitinGovernance,
			SessionID:   ce.SID,
			SequenceNum: seq,
			Actor:       ActorAgent,
			AgentID:     ce.Agent,
			Command:     fmt.Sprintf("%s:%s", ce.Tool, ce.Action),
			ExitCode:    &exitCode,
			DurationMs:  &durationMs,
			Repository:  repo,
			HasError:    hasError,
			Tags: map[string]string{
				"outcome": ce.Outcome,
				"reason":  ce.Reason,
				"source":  ce.Source,
				"action":  ce.Action,
				"tool":    ce.Tool,
			},
		}
		if ce.Path != "" {
			ev.Tags["path"] = ce.Path
		}
		if ce.Command != "" {
			ev.Tags["command"] = ce.Command
		}
		events = append(events, ev)
		seq++
	}

	newOffset, _ := f.Seek(0, 1) // current position after scanning
	// If scanner didn't reach EOF cleanly, use stat to get position
	if info, err := f.Stat(); err == nil && newOffset == 0 {
		newOffset = info.Size()
	}

	return events, newOffset, scanner.Err()
}

// inferRepo attempts to extract a repo name from a file path.
// e.g. "/home/jared/workspace/sentinel/.chitin/events.jsonl" → "chitinhq/sentinel"
func inferRepo(path string) string {
	dir := filepath.Dir(filepath.Dir(path)) // strip /.chitin/events.jsonl
	base := filepath.Base(dir)
	if base != "" && base != "." && base != "/" {
		return "chitinhq/" + base
	}
	return ""
}
