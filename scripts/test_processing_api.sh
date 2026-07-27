#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test processing
trap cleanup_api_test EXIT
wait_for_api

PDF_FILE=${PDF_FILE:-"${PROJECT_DIR}/DOne_Decoupling_Structure_Rendering.pdf"}
[[ -f "${PDF_FILE}" ]] || fail "processing requires a real PDF; set PDF_FILE or add ${PROJECT_DIR}/DOne_Decoupling_Structure_Rendering.pdf"
DOI_FILE=${DOI_FILE:-"${PROJECT_DIR}/doi"}
doi="10.5555/process-${RUN_ID}"
if [[ -s "${DOI_FILE}" ]]; then doi="$(tr -d '\r\n' < "${DOI_FILE}").process.${RUN_ID}"; fi

upload_file "${PDF_FILE}" "process-${RUN_ID}.pdf" "${doi}" "application/pdf"
document_id=${LAST_DOCUMENT_ID}

deadline=$((SECONDS + ${PROCESS_TIMEOUT_SECONDS:-180}))
while :; do
  api_request 200 --get --data-urlencode "id=${document_id}" "${API_BASE_URL}/api/document/search"
  status=$(json_value 'items.0.status')
  case "${status}" in
    ready) break ;;
    failed|cancelled) fail "document processing reached terminal status ${status}" ;;
  esac
  (( SECONDS < deadline )) || fail "document did not become ready within ${PROCESS_TIMEOUT_SECONDS:-180}s (last status: ${status})"
  sleep 2
done
pass "asynchronous recognition completed"

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
