#!/usr/bin/env bash
# Deterministic URL-validation checks run by default. Public arXiv/PDF traffic
# is explicitly opt-in because DNS, provider availability, and rate limits are
# outside the application's correctness boundary.
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test remote-import
trap cleanup_api_test EXIT
wait_for_api

api_request 400 --request POST --header 'Content-Type: application/json' \
  --data '{"url":"not-an-absolute-url"}' \
  "${API_BASE_URL}/api/document/import-url"
pass "malformed remote URL is rejected"

api_request 400 --request POST --header 'Content-Type: application/json' \
  --data '{"url":"http://127.0.0.1/private.pdf"}' \
  "${API_BASE_URL}/api/document/import-url"
pass "private-address remote URL is rejected by SSRF policy"

api_request 400 --request POST --header 'Content-Type: application/json' \
  --data '{"url":"https://example.com/not-a-pdf-landing-page"}' \
  "${API_BASE_URL}/api/document/import-url"
pass "generic publisher landing page is not guessed as a PDF"

if [[ "${RUN_LIVE_INTEGRATION_TESTS:-0}" != "1" ]]; then
  pass "public remote imports skipped; set RUN_LIVE_INTEGRATION_TESTS=1 to enable"
  exit 0
fi

live_import() {
  local label=$1 url=$2 expected_doi=$3 filename=${4:-}
  local request_file="${WORK_DIR}/${label}.json"
  python3 - "${url}" "${filename}" > "${request_file}" <<'PY'
import json
import sys
print(json.dumps({"url": sys.argv[1], **({"filename": sys.argv[2]} if sys.argv[2] else {})}))
PY
  api_request 201 --request POST --header 'Content-Type: application/json' \
    --data-binary "@${request_file}" "${API_BASE_URL}/api/document/import-url"
  LAST_DOCUMENT_ID=$(json_value 'document.id')
  TEST_DOCUMENT_IDS+=("${LAST_DOCUMENT_ID}")
  [[ $(json_value 'document.doi') == "${expected_doi}" ]] || fail "${label} import did not normalize DOI to ${expected_doi}"
  pass "${label} remote PDF imported document_id=${LAST_DOCUMENT_ID}"

  api_request 200 "${API_BASE_URL}/api/document/${LAST_DOCUMENT_ID}/content"
  grep --binary-files=text --fixed-strings --quiet '%PDF-' "${RESPONSE_FILE}" || fail "${label} content is not a PDF"
  pass "${label} durable PDF content is readable"

  api_request 200 "${API_BASE_URL}/api/document/${LAST_DOCUMENT_ID}/processing"
  [[ $(json_value 'processing.documentId') == "${LAST_DOCUMENT_ID}" ]] || fail "${label} processing view returned wrong document"
  pass "${label} processing handoff is observable"

  wait_for_document_terminal "${LAST_DOCUMENT_ID}" "${REMOTE_PROCESS_TIMEOUT_SECONDS:-240}"
  if [[ "${REMOTE_IMPORT_REQUIRE_READY:-1}" == "1" && "${LAST_DOCUMENT_STATUS}" != "ready" ]]; then
    fail "${label} import reached ${LAST_DOCUMENT_STATUS}, expected ready"
  fi
  pass "${label} import reached terminal status ${LAST_DOCUMENT_STATUS}"
}

direct_url=${REMOTE_PDF_DIRECT_URL:-https://arxiv.org/pdf/1706.03762.pdf}
live_import direct-pdf "${direct_url}" '10.48550/arxiv.1706.03762' "remote-direct-${RUN_ID}.pdf"

doi_url=${REMOTE_PDF_DOI_URL:-https://doi.org/10.48550/arXiv.1810.04805}
live_import doi-url "${doi_url}" '10.48550/arxiv.1810.04805'
doi_document_id=${LAST_DOCUMENT_ID}

python3 - "${doi_url}" > "${WORK_DIR}/duplicate-doi-url.json" <<'PY'
import json
import sys
print(json.dumps({"url": sys.argv[1]}))
PY
api_request 409 --request POST --header 'Content-Type: application/json' \
  --data-binary "@${WORK_DIR}/duplicate-doi-url.json" "${API_BASE_URL}/api/document/import-url"
pass "duplicate DOI URL import is rejected without creating another document"

api_request 200 "${API_BASE_URL}/api/paper/${doi_document_id}"
[[ $(json_value 'paperMetadata.sourceUrl') == 'https://arxiv.org/abs/1810.04805' ]] || fail "DOI URL did not persist canonical arXiv landing URL"
pass "DOI URL import persists a canonical landing URL separate from PDF bytes"
