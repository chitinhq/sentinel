## Agent Identity

At session start, if you see `[AgentGuard] No agent identity set`, ask the user:
1. **Role**: developer / reviewer / ops / security / planner
2. **Driver**: human / claude-code / copilot / ci

Then run: `scripts/write-persona.sh <driver> <role>`

## Project

Sentinel is the telemetry engine for the Chitin platform. It ingests GitHub Actions execution logs, runs 7 detection passes for drift and anomalies, mines governance policies from execution patterns, and routes findings to the kernel as GitHub issues.

**Module**: `github.com/chitinhq/sentinel`
**Languages**: Go + Python
**Database**: Neon Postgres

## Key Directories

- `cmd/sentinel/` — binary entrypoint
- `internal/ingest/` — GitHub Actions log ingestion (data integrity critical)
- `internal/detect/` — 7 detection passes
- `internal/mine/` — policy mining from execution patterns
- `sentinel_analyze.py` — Python analytics
- `sentinel_mine.py` — Python policy mining

## Build

```bash
go build ./...
go test ./...
golangci-lint run
python -m pytest tests/
```

## Assembly Line

Sentinel monitors all repos in the assembly line. It ingests execution events from GitHub Actions and routes findings back to the kernel as issues labeled `sentinel` + `stage:architect`.

```
GitHub Actions logs (all repos)
    → Sentinel ingest
    → Detection passes
    → Policy mining
    → Findings → Kernel issues
```
