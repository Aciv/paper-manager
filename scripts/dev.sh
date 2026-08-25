#!/usr/bin/env bash
# Hybrid development entry point. See README.md for the three-terminal flow.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

compose=(docker compose -f docker-compose.dev.yml)
command_name="${1:-}"

case "$command_name" in
  start)
    "${compose[@]}" up -d
    ;;
  stop)
    "${compose[@]}" down
    ;;
  restart)
    "${compose[@]}" restart
    ;;
  status)
    "${compose[@]}" ps
    ;;
  logs)
    "${compose[@]}" logs -f
    ;;
  app)
    if command -v air >/dev/null 2>&1; then
      exec air -c .air.toml
    fi
    echo "Air is not installed; running without automatic reload." >&2
    echo "Install it with: go install github.com/air-verse/air@latest" >&2
    exec go run ./cmd/server
    ;;
  parser)
    if [[ ! -x .venv/bin/python ]]; then
      echo "Python virtual environment is missing." >&2
      echo "Run: python3 -m venv .venv && .venv/bin/pip install -r python/requirements.txt" >&2
      exit 1
    fi
    cd python
    exec ../.venv/bin/python -m papers_parser.server
    ;;
  frontend)
    cd web
    exec npm run dev -- --host 127.0.0.1
    ;;
  *)
    cat >&2 <<'USAGE'
Usage: scripts/dev.sh {start|stop|restart|status|logs|app|parser|frontend}

start / stop / restart / status / logs operate Docker infrastructure only.
app, parser, and frontend run source code on the host; app uses Air when available.
USAGE
    exit 2
    ;;
esac
