#!/usr/bin/env bash
# Unified Python parser test entry point. Tests live in pdf_parser/papers_parser/test.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/../.." && pwd)
PYTHON=${PYTHON:-"${REPO_ROOT}/.venv/bin/python"}
if [[ ! -x "${PYTHON}" ]]; then
  PYTHON=python3
fi
cd "${REPO_ROOT}/pdf_parser"
exec "${PYTHON}" -m unittest discover -s papers_parser/test -p 'test_*.py' "$@"
