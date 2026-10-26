#!/usr/bin/env bash
# Single entry point for the repository test suites.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

case "${1:-}" in
  go)       exec "${SCRIPT_DIR}/tests/go.sh" "${@:2}" ;;
  python)   exec "${SCRIPT_DIR}/tests/python.sh" "${@:2}" ;;
  web)      exec "${SCRIPT_DIR}/tests/web.sh" "${@:2}" ;;
  api)      exec "${SCRIPT_DIR}/tests/api.sh" "${@:2}" ;;
  docker)   exec "${SCRIPT_DIR}/tests/docker.sh" "${@:2}" ;;
  desktop)  exec "${SCRIPT_DIR}/tests/desktop.sh" "${@:2}" ;;
  all)      exec "${SCRIPT_DIR}/tests/all.sh" "${@:2}" ;;
  *)
    cat >&2 <<'USAGE'
Usage: scripts/run_tests.sh {go|python|web|api|desktop|docker|all}

go       Go backend tests (go test ./...)
python   Python parser tests (pdf_parser/papers_parser/test)
web      Frontend suites (selection, DOI, bibliography, translation, reading note)
api      API smoke tests; requires a running API stack
desktop  Desktop packaging/runtime tests
docker   Isolated Docker build/runtime check; requires --output-dir PATH
all      go + python + web + desktop; add RUN_API_TESTS=1 to include API smoke tests
USAGE
    exit 2
    ;;
esac
