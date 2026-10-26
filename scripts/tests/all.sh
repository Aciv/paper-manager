#!/usr/bin/env bash
# Run every host-side suite. API smoke tests need a live stack, so they only
# run when RUN_API_TESTS=1.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
"${SCRIPT_DIR}/go.sh" "$@"
"${SCRIPT_DIR}/python.sh"
"${SCRIPT_DIR}/web.sh"
"${SCRIPT_DIR}/desktop.sh"
if [[ "${RUN_API_TESTS:-0}" == "1" ]]; then
  "${SCRIPT_DIR}/api.sh"
fi
