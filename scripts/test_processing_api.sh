#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test processing
trap cleanup_api_test EXIT
wait_for_api

if [[ -n "${PDF_FILE:-}" ]]; then
  [[ -f "${PDF_FILE}" ]] || fail "configured PDF_FILE does not exist: ${PDF_FILE}"
else
  PDF_FILE="${WORK_DIR}/processing.pdf"
  make_valid_pdf_fixture "${PDF_FILE}"
fi
DOI_FILE=${DOI_FILE:-"${PROJECT_DIR}/doi"}
doi="10.5555/process-${RUN_ID}"
if [[ -s "${DOI_FILE}" ]]; then doi="$(tr -d '\r\n' < "${DOI_FILE}").process.${RUN_ID}"; fi

upload_file "${PDF_FILE}" "process-${RUN_ID}.pdf" "${doi}" "application/pdf"
document_id=${LAST_DOCUMENT_ID}

api_request 200 "${API_BASE_URL}/api/document/${document_id}/processing"
[[ $(json_value 'processing.documentId') == "${document_id}" ]] || fail "initial processing view returned wrong document"
pass "processing state is observable while the worker is active"

wait_for_document_terminal "${document_id}" "${PROCESS_TIMEOUT_SECONDS:-180}"
[[ "${LAST_DOCUMENT_STATUS}" == "ready" ]] || fail "document processing reached terminal status ${LAST_DOCUMENT_STATUS}"
pass "asynchronous recognition completed"

api_request 200 "${API_BASE_URL}/api/document/${document_id}/processing"
python3 - "${RESPONSE_FILE}" <<'PY'
import json, sys
view = json.load(open(sys.argv[1], encoding='utf-8'))['processing']
if view.get('documentStatus') != 'ready':
    raise SystemExit(f'processing view did not refresh to ready: {view}')
pipeline = [stage for stage in view.get('stages', []) if stage.get('kind') == 'pipeline']
recognition = [stage for stage in view.get('stages', []) if stage.get('kind') == 'recognition']
if not pipeline or not recognition:
    raise SystemExit(f'missing durable pipeline stages: {view}')
valid_statuses = {'pending', 'running', 'succeeded', 'failed', 'cancelled'}
if any(stage.get('status') not in valid_statuses for stage in pipeline + recognition):
    raise SystemExit(f'processing view returned an invalid stage status: {view}')
if not any(stage.get('status') == 'succeeded' for stage in recognition):
    raise SystemExit(f'recognition did not reach its durable success state: {recognition}')
PY
pass "processing view refreshes recognition and pipeline state independently"

api_request 200 "${API_BASE_URL}/api/document/${document_id}/content"
grep --binary-files=text --fixed-strings --quiet '%PDF-' "${RESPONSE_FILE}" || fail "PDF content endpoint did not return a PDF"
pass "PDF content endpoint streams the original PDF"

api_request 200 "${API_BASE_URL}/api/document/${document_id}/blocks?page=1"
python3 - "${RESPONSE_FILE}" <<'PY'
import json, sys
body = json.load(open(sys.argv[1]))
if not isinstance(body.get('items'), list):
    raise SystemExit(f'invalid block response: {body}')
PY
pass "parsed Block listing is available"

image_url=$(python3 - "${RESPONSE_FILE}" <<'PY'
import json, sys
for block in json.load(open(sys.argv[1])).get('items', []):
    if block.get('type') in {'image', 'table'} and block.get('url'):
        print(block['url'])
        break
PY
)
if [[ -n "${image_url}" ]]; then
  api_request 200 "${API_BASE_URL}${image_url}"
  file_size=$(wc -c < "${RESPONSE_FILE}")
  (( file_size > 0 )) || fail "block image endpoint returned no bytes"
  pass "rendered block image endpoint streams PNG bytes"
elif [[ "${REQUIRE_IMAGE_BLOCKS:-0}" == "1" ]]; then
  fail "fixture produced no image/table block"
else
  pass "fixture produced no image/table block; set REQUIRE_IMAGE_BLOCKS=1 to require one"
fi
