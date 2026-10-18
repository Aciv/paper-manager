#!/usr/bin/env bash
# Print task type -> Asynq handler -> service entry -> enqueueing functions.
set -euo pipefail
cd "$(dirname "$0")/.."
exec go run ./scripts/taskmap "$@"
