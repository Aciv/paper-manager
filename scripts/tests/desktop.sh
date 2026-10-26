#!/usr/bin/env bash
# Unified desktop test entry point.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
exec "${SCRIPT_DIR}/../desktop/run_tests.sh" "$@"
