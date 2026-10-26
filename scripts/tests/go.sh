#!/usr/bin/env bash
# Unified Go backend test entry point. Extra arguments are passed to `go test`.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/../.." && pwd)
cd "${REPO_ROOT}"
exec go test "$@" ./...
