#!/usr/bin/env bash
# Unified API smoke-test entry point. Requires a running API stack.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
exec "${SCRIPT_DIR}/../api-tests/run_all.sh" "$@"
