#!/usr/bin/env bash
# Cancellation races an active parser by design. A 200 proves the persistent
# cancellation path; a 400 means the tiny fixture completed before the request
# reached the worker and is reported explicitly rather than treated as flaky.
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test cancellation
trap cleanup_api_test EXIT
wait_for_api

path="${WORK_DIR}/cancel.pdf"
make_pdf_fixture "${path}"
upload_file "${path}" "cancel-${RUN_ID}.pdf" "10.5555/cancel-${RUN_ID}" "application/pdf"
document_id=${LAST_DOCUMENT_ID}

status=$(api_status --request POST "${API_BASE_URL}/api/document/${document_id}/cancel")
case "${status}" in
  200)
    [[ $(json_value 'document.status') == 'cancelled' ]] || fail "cancel endpoint did not return cancelled state"
    pass "document cancellation persisted"
    api_request 200 --request POST "${API_BASE_URL}/api/document/${document_id}/cancel"
    [[ $(json_value 'document.status') == 'cancelled' ]] || fail "idempotent cancellation did not return cancelled state"
    pass "document cancellation is idempotent"
    ;;
  400)
    if [[ "${CANCEL_TEST_REQUIRE_SUCCESS:-0}" == "1" ]]; then
      fail "fixture reached a terminal state before cancellation"
    fi
    pass "parser completed before cancellation request; endpoint returned the documented terminal-state response"
    ;;
  *) fail "expected cancellation HTTP 200 or terminal-state HTTP 400, got ${status}" ;;
esac
