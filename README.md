# Sentinel

**The telemetry engine that watches your AI agents and tells you when
something is off.**

Sentinel reads the execution logs your agents leave behind (GitHub
Actions runs, Chitin Kernel events), stores them in Postgres, and runs
seven detection passes to surface patterns you should care about:
commands that fail too often, policies that deny too much, agents that
keep hitting the same wall.

Findings it is confident about become GitHub issues. The rest go into
a weekly digest.

## What problem does this solve?

If you run more than one AI agent, more than one repo, or more than a
few days of agent work, you start drowning in raw logs. You cannot
eyeball "is my governance policy too strict?" or "is Claude Code
getting stuck on the same thing across my fleet?" Sentinel turns that
raw exhaust into a small number of actionable findings per week.

Sentinel is the **senses** of the Chitin Platform — the component
that notices things.

## Try it

```bash
# Build
git clone https://github.com/chitinhq/sentinel.git && cd sentinel
go build -o sentinel ./cmd/sentinel/

# Point it at a Postgres DB (Neon works; so does local docker)
export NEON_DATABASE_URL="postgres://..."
psql "$NEON_DATABASE_URL" -f migrations/001_execution_events.sql

# Pull recent GitHub Actions runs into the database
export GITHUB_TOKEN="ghp_..."
sentinel ingest

# Run the seven detection passes; optionally add an LLM for summaries
export ANTHROPIC_API_KEY="sk-ant-..."   # optional
sentinel analyze

# Get a markdown digest
sentinel digest
```

Configuration lives in `sentinel.yaml`: which repos to ingest,
thresholds for each detection pass, and where findings get routed.

## The seven detection passes

| # | Name            | What it finds                                       |
|---|-----------------|-----------------------------------------------------|
| 1 | Hotspot         | The actions your agents take most often             |
| 2 | False Positive  | Policies whose denial rate drifted from baseline    |
| 3 | Bypass          | Deny-then-retry-then-allow sequences (workarounds)  |
| 4 | Tool Risk       | Tools with a high denial-to-total ratio             |
| 5 | Anomaly         | Volume spikes, sessions with too many denials       |
| 6 | Command Failure | Commands that fail across repos and sessions        |
| 7 | Sequence        | Repeating n-gram patterns in failing runs           |

## Scheduling

Sentinel is a batch engine — `sentinel ingest`, `sentinel analyze`, and
`sentinel digest` must be invoked on a schedule for the platform to have live
telemetry. This repo ships **systemd `--user` timer units** as a first-class
deliverable (see [`deploy/systemd/`](deploy/systemd/)):

| Unit | Schedule | What it does |
|------|----------|--------------|
| `sentinel-digest.timer`  | every 15 min | renders markdown digest to `$SENTINEL_DIGEST_DIR` |
| `sentinel-analyze.timer` | every 1 hour | runs 7 detection passes + routes findings |

Both timers use `Persistent=true`, so missed runs (laptop asleep, reboots)
are caught up on resume.

### Install

```bash
# 1. Put secrets in a file the units can read (not checked into git):
mkdir -p ~/.config/sentinel
cat > ~/.config/sentinel/env <<'EOF'
NEON_DATABASE_URL=postgres://...
GITHUB_TOKEN=ghp_...
ANTHROPIC_API_KEY=sk-ant-...
EOF
chmod 600 ~/.config/sentinel/env

# 2. Build the binary + install the timers:
bash scripts/install-timers.sh

# 3. Verify:
systemctl --user list-timers | grep sentinel
```

Digests are written to `~/.local/share/sentinel/digests/` by default
(`SENTINEL_DIGEST_DIR` in the unit files — override there if you want a
different path). Unit logs live in the user journal:

```bash
journalctl --user -u sentinel-digest.service -f
journalctl --user -u sentinel-analyze.service -f
```

To uninstall: `bash scripts/install-timers.sh --uninstall`.

For non-Linux hosts or fleet deployment, use the equivalent cron entries:

```cron
*/15 * * * *  cd /path/to/sentinel && SENTINEL_DIGEST_DIR=$HOME/.local/share/sentinel/digests ./bin/sentinel digest
0    * * * *  cd /path/to/sentinel && ./bin/sentinel analyze
```

## Where next

- [Chitin Platform overview](https://github.com/chitinhq/workspace) —
  how Sentinel fits with the Chitin Kernel (governance), Octi
  (dispatch), Atlas (memory), and the rest.
- [`migrations/`](./migrations/) — the Postgres schema.
- `sentinel-mcp` — a sister binary that exposes Sentinel's findings to
  AI coding agents via MCP.

## Development

```bash
go build ./...
go test ./...
```

## License

MIT
