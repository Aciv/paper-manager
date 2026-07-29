#!/usr/bin/env bash
# Exercises the HTTP visibility/control plane without depending on a live
# catalog response. The asynchronous provider outcome is intentionally not
# asserted here: provider availability and no-match results are represented by
# Asynq retry/dead-letter state, not a paper_metadata status column.
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

api_request 200 "${API_BASE_URL}/api/paper/${source_document_id}/info"
[[ $(json_value 'paperInfo.documentId') == "${source_document_id}" ]] || fail "paper info status returned wrong document"
python3 - "${RESPONSE_FILE}" <<'PY'
import json, sys
info = json.load(open(sys.argv[1], encoding='utf-8'))['paperInfo']
states = {'idle', 'queued', 'completed', 'unavailable', 'unknown'}
if info['extraction']['state'] not in states or info['catalog']['state'] not in states:
    raise SystemExit(f'invalid paper info status: {info}')
PY
pass "paper information status is independently observable"

api_request 202 --request POST "${API_BASE_URL}/api/paper/${source_document_id}/info/catalog/retry"
[[ $(json_value 'paperInfo.documentId') == "${source_document_id}" ]] || fail "catalog retry returned wrong document"
pass "catalog retry is queued"

# A source can be parsed before this request arrives, or still lack Markdown.
# Both outcomes are valid: the endpoint either queues extraction or explains
# that retry is unavailable until a parsed revision exists.
status=$(api_status --request POST "${API_BASE_URL}/api/paper/${source_document_id}/info/extraction/retry")
case "${status}" in
  202) pass "extraction retry is queued" ;;
  400) pass "extraction retry correctly waits for parsed Markdown" ;;
  *) fail "expected extraction retry HTTP 202 or 400, got ${status}" ;;
esac

api_request 204 --request DELETE "${API_BASE_URL}/api/paper/${source_document_id}/citations/${target_document_id}"
pass "citation deletion succeeds"
