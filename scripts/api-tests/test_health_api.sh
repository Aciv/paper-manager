#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test health
trap cleanup_api_test EXIT
wait_for_api
pass "health response has the expected JSON payload"
