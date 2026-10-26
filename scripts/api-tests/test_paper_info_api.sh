#!/usr/bin/env bash
# Exercises the HTTP visibility/control plane without depending on a live
# catalog response. Persistent task state comes from the document processing
# endpoint; paper-info control responses only acknowledge queue handoff.
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test paper-info
trap cleanup_api_test EXIT
wait_for_api

source_path="${WORK_DIR}/source.txt"
target_path="${WORK_DIR}/target.txt"
printf 'paper info source fixture\n' > "${source_path}"
printf 'paper info target fixture\n' > "${target_path}"

upload_file "${source_path}" "paper-info-source-${RUN_ID}.txt" "10.5555/source-${RUN_ID}" "text/plain"
source_document_id=${LAST_DOCUMENT_ID}
upload_file "${target_path}" "paper-info-target-${RUN_ID}.txt" "10.5555/target-${RUN_ID}" "text/plain"
target_document_id=${LAST_DOCUMENT_ID}

api_request 200 --request PATCH --header 'Content-Type: application/json' \
  --data "{\"title\":\"Paper Info Source ${RUN_ID}\"}" \
  "${API_BASE_URL}/api/paper/${source_document_id}"
pass "paper lookup identity persisted"

api_request 201 --request POST --header 'Content-Type: application/json' \
  --data "{\"citedDocumentId\":\"${target_document_id}\",\"rawReference\":\"Target reference ${RUN_ID}\"}" \
  "${API_BASE_URL}/api/paper/${source_document_id}/citations"
pass "citation creation succeeds"

api_request 200 "${API_BASE_URL}/api/paper/${source_document_id}/citations"
[[ $(json_value 'items.0.citedDocumentId') == "${target_document_id}" ]] || fail "forward citation returned wrong target"
pass "forward citation listing succeeds"

api_request 200 "${API_BASE_URL}/api/paper/${target_document_id}/cited-by"
[[ $(json_value 'items.0.citingDocumentId') == "${source_document_id}" ]] || fail "reverse citation returned wrong source"
pass "reverse citation listing succeeds"

api_request 200 "${API_BASE_URL}/api/document/${source_document_id}/processing"
[[ $(json_value 'processing.documentId') == "${source_document_id}" ]] || fail "processing view returned wrong document"
python3 - "${RESPONSE_FILE}" <<'PY'
import json, sys
view = json.load(open(sys.argv[1], encoding='utf-8'))['processing']
states = {'queued', 'running', 'succeeded', 'failed', 'cancelled'}
if not isinstance(view.get('stages'), list):
    raise SystemExit(f'invalid processing view: {view}')
if any(stage.get('status') not in states for stage in view['stages']):
    raise SystemExit(f'invalid processing stage: {view}')
progress = view.get('referenceProgress')
if progress is not None and any(not isinstance(progress.get(key), int) for key in ('queued', 'running', 'completed', 'cancelled')):
    raise SystemExit(f'invalid reference progress: {progress}')
PY
pass "paper information state is visible through persistent processing spans"

status=$(api_status --request POST "${API_BASE_URL}/api/paper/${source_document_id}/info/catalog/retry")
case "${status}" in
  202) assert_accepted_response; pass "source catalog retry is queued" ;;
  409) pass "source catalog retry correctly rejects a concurrent active stage" ;;
  *) fail "expected catalog retry HTTP 202 or 409, got ${status}" ;;
esac

# A source can be parsed before this request arrives, or still lack Markdown.
# Both outcomes are valid: the endpoint either queues extraction or explains
# that retry is unavailable until a parsed revision exists.
status=$(api_status --request POST "${API_BASE_URL}/api/paper/${source_document_id}/info/extraction/retry")
case "${status}" in
  202) assert_accepted_response; pass "extraction retry is queued" ;;
  400) pass "extraction retry correctly waits for parsed Markdown" ;;
  409) pass "extraction retry correctly rejects a concurrent active stage" ;;
  *) fail "expected extraction retry HTTP 202, 400, or 409, got ${status}" ;;
esac

status=$(api_status --request POST "${API_BASE_URL}/api/paper/${source_document_id}/info/reference/retry")
case "${status}" in
  202) assert_accepted_response; pass "reference catalog retry is queued" ;;
  400) pass "reference catalog retry correctly waits for parsed Markdown" ;;
  409) pass "reference catalog retry correctly rejects a concurrent active stage" ;;
  *) fail "expected reference retry HTTP 202, 400, or 409, got ${status}" ;;
esac

api_request 204 --request DELETE "${API_BASE_URL}/api/paper/${source_document_id}/citations/${target_document_id}"
pass "citation deletion succeeds"
