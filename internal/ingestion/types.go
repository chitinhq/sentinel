package ingestion

import "time"

type EventSource string

const (
	SourceGitHubActions EventSource = "github_actions"
	SourceShellHistory  EventSource = "shell_history"
	SourceTermius       EventSource = "termius"
)

type ActorType string

const (
	ActorHuman   ActorType = "human"
	ActorAgent   ActorType = "agent"
	ActorUnknown ActorType = "unknown"
)

type ExecutionEvent struct {
	ID          string            `json:"id"`
	Timestamp   time.Time         `json:"timestamp"`
	Source      EventSource       `json:"source"`
	SessionID   string            `json:"session_id"`
	SequenceNum int               `json:"sequence_num"`
	Actor       ActorType         `json:"actor"`
	AgentID     string            `json:"agent_id,omitempty"`
	Command     string            `json:"command"`
	Arguments   []string          `json:"arguments,omitempty"`
	ExitCode    *int              `json:"exit_code,omitempty"`
	DurationMs  *int64            `json:"duration_ms,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Repository  string            `json:"repository,omitempty"`
	Branch      string            `json:"branch,omitempty"`
	StdoutHash  string            `json:"stdout_hash,omitempty"`
	StderrHash  string            `json:"stderr_hash,omitempty"`
	HasError    bool              `json:"has_error"`
	Tags        map[string]string `json:"tags,omitempty"`
}
