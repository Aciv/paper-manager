#!/usr/bin/env bash
# Backward-compatible entry point for the complete API smoke suite.
set -euo pipefail

exec "$(dirname "${BASH_SOURCE[0]}")/run_all.sh" "$@"
