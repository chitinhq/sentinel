# Sentinel — Copilot Instructions

> Copilot acts as **Tier C — Execution Workforce** in this repository.
> Implement well-specified issues, open draft PRs, never merge or approve.

## Project Overview

**Sentinel** is the telemetry engine for the Chitin platform. It ingests execution logs from GitHub Actions across all repos, runs detection passes to identify drift and anomalies, mines governance policies from execution patterns, and routes findings back as GitHub issues.

**Core principle**: Sentinel creates a closed-loop governance feedback cycle — agent activity → telemetry → drift detection → policy generation → kernel enforcement.

## Tech Stack

- **Language**: Go + Python
- **Go module**: `github.com/chitinhq/sentinel`
- **Python**: `sentinel_analyze.py`, `sentinel_mine.py` (analytics + mining)
- **Database**: Neon Postgres (execution events, ingestion checkpoints)
- **Data sources**: GitHub Actions logs across all chitinhq repos

## Repository Structure

```
cmd/sentinel/         # Go binary entrypoint
internal/
├── ingest/           # GitHub Actions log ingestion
├── detect/           # 7 detection passes (drift, anomaly, pattern)
├── mine/             # Policy mining from execution data
└── config/           # Configuration, sentinel.yaml
sentinel_analyze.py   # Python analytics scripts
sentinel_mine.py      # Python policy mining
```

## Build & Test

```bash
# Go
go build ./...
go test ./...
golangci-lint run

# Python
python -m pytest tests/
```

## Coding Standards

- Follow Go conventions (`gofmt`, `go vet`)
- Python: follow existing style, type hints preferred
- Error handling: always check and wrap errors with context
- Logging: structured logging via `log/slog` (Go), `logging` (Python)

## Governance Rules

### DENY
- `git push` to main — always use feature branches
- `git force-push` — never rewrite shared history
- Write to `.env`, SSH keys, credentials
- Write or delete `.claude/` files
- Execute `rm -rf` or destructive shell commands

### ALWAYS
- Create feature branches: `agent/<type>/issue-<N>`
- Run `go build ./... && go test ./...` before creating PRs
- Include governance report in PR body
- Link PRs to issues (`Closes #N`)

## Three-Tier Model

- **Tier A — Architect** (Claude Opus): Sprint planning, architecture, risk
- **Tier B — Senior** (@claude on GitHub): Complex implementation, code review
- **Tier C — Execution** (Copilot): Implement specified issues, open draft PRs

### PR Rules

- **NEVER merge PRs** — only Tier B or humans merge
- **NEVER approve PRs** — post first-pass review comments only
- Max 300 lines changed per PR (soft limit)
- Always open as **draft PR** first
- If ambiguous, label `needs-spec` and stop

## Critical Areas

- `internal/ingest/` — ingestion pipeline, data integrity critical
- `internal/detect/` — detection passes, false positive/negative impact
- `sentinel.yaml` — configuration, affects all ingestion behavior
- Database migrations — schema changes need careful review

## Branch Naming

```
agent/feat/issue-<N>
agent/fix/issue-<N>
agent/refactor/issue-<N>
agent/test/issue-<N>
agent/docs/issue-<N>
```

## Autonomy Directive

- **NEVER pause to ask for clarification** — make your best judgment
- If the issue is ambiguous, label it `needs-spec` and stop
- Default to the **safest option** in every ambiguous situation
