#!/usr/bin/env bash
# install-timers.sh — install systemd --user timer units for Sentinel.
#
# Idempotent: safe to re-run. Copies units, reloads systemd, enables + starts timers.
#
# Usage:
#   bash scripts/install-timers.sh           # install + enable + start
#   bash scripts/install-timers.sh --uninstall
#
# Secrets (NEON_DATABASE_URL, GITHUB_TOKEN, ANTHROPIC_API_KEY) should live in
# ~/.config/sentinel/env as KEY=value lines (no export, no quotes).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
UNIT_SRC="${REPO_ROOT}/deploy/systemd"
UNIT_DST="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"

UNITS=(
  sentinel-digest.service
  sentinel-digest.timer
  sentinel-analyze.service
  sentinel-analyze.timer
)

TIMERS=(
  sentinel-digest.timer
  sentinel-analyze.timer
)

log() { printf '[install-timers] %s\n' "$*"; }

if [[ "${1:-}" == "--uninstall" ]]; then
  log "stopping + disabling timers"
  for t in "${TIMERS[@]}"; do
    systemctl --user disable --now "$t" 2>/dev/null || true
  done
  for u in "${UNITS[@]}"; do
    rm -f "$UNIT_DST/$u"
  done
  systemctl --user daemon-reload
  log "uninstalled."
  exit 0
fi

# Verify binary exists (the timer will fail silently otherwise).
if [[ ! -x "$REPO_ROOT/bin/sentinel" ]]; then
  log "building sentinel binary first..."
  (cd "$REPO_ROOT" && go build -o bin/sentinel ./cmd/sentinel/)
fi

mkdir -p "$UNIT_DST"
mkdir -p "$HOME/.local/share/sentinel/digests"

log "installing units to $UNIT_DST"
for u in "${UNITS[@]}"; do
  install -m 0644 "$UNIT_SRC/$u" "$UNIT_DST/$u"
done

log "reloading systemd --user"
systemctl --user daemon-reload

log "enabling + starting timers"
for t in "${TIMERS[@]}"; do
  systemctl --user enable --now "$t"
done

log "done. status:"
systemctl --user list-timers --all | grep -E 'sentinel|NEXT' || true

if [[ ! -f "$HOME/.config/sentinel/env" ]]; then
  cat >&2 <<EOF

[install-timers] NOTE: ~/.config/sentinel/env not found.
  Create it with your secrets so the services can run:
    mkdir -p ~/.config/sentinel
    cat > ~/.config/sentinel/env <<SECRETS
    NEON_DATABASE_URL=postgres://...
    GITHUB_TOKEN=ghp_...
    ANTHROPIC_API_KEY=sk-ant-...
    SECRETS
    chmod 600 ~/.config/sentinel/env
EOF
fi
