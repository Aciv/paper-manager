#!/usr/bin/env bash
# Unified desktop test entry point: Python packaging tests plus Go runtime tests.
# Usage: run_tests.sh [--python-only|--go-only]
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/../.." && pwd)
PYTHON=${PYTHON:-"${REPO_ROOT}/.venv/bin/python"}
if [[ ! -x "${PYTHON}" ]]; then
  PYTHON=python3
fi
if ! command -v "${PYTHON}" >/dev/null 2>&1; then
  PYTHON=python
fi

mode=all
case "${1:-}" in
  --python-only) mode=python ;;
  --go-only) mode=go ;;
  "")
    ;;
  *)
    echo "Usage: $0 [--python-only|--go-only]" >&2
    exit 2
    ;;
esac

if [[ "${mode}" != "go" ]]; then
  export PYTHONPATH="${SCRIPT_DIR}${PYTHONPATH:+:${PYTHONPATH}}"
  cd "${REPO_ROOT}"
  "${PYTHON}" -m unittest discover -s "${SCRIPT_DIR}/tests" -p 'test_*.py'
fi
if [[ "${mode}" != "python" ]]; then
  cd "${REPO_ROOT}"
  go test ./internal/runtime/desktop ./cmd/launcher ./scripts/desktop/upgrade-failure
fi
