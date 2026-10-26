#!/usr/bin/env bash
# Build and validate a separate Docker project; never run as part of quick tests.
set -euo pipefail
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/../.." && pwd)
PYTHON=${PYTHON:-python3}
exec "${PYTHON}" "${REPO_ROOT}/scripts/docker/validate.py" "$@"
