#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test document-crud
trap cleanup_api_test EXIT
wait_for_api

path="${WORK_DIR}/source.txt"
printf 'document CRUD fixture\n' > "${path}"
doi="10.5555/crud-${RUN_ID}"
upload_file "${path}" "crud-${RUN_ID}.txt" "${doi}" "text/plain"
document_id=${LAST_DOCUMENT_ID}

api_request 200 "${API_BASE_URL}/api/document/search?page=1"
python3 - "${RESPONSE_FILE}" "${document_id}" <<'PY'
import json, sys
if sys.argv[2] not in {item.get('id') for item in json.load(open(sys.argv[1])).get('items', [])}:
    raise SystemExit('unfiltered search did not include test document')
PY
pass "unfiltered document search returns the uploaded document"

api_request 200 --get --data-urlencode "id=${document_id}" "${API_BASE_URL}/api/document/search"
[[ $(json_value 'total') == 1 ]] || fail "ID search did not return one document"
pass "exact ID search returns the uploaded document"

api_request 200 --get --data-urlencode "doi=${doi}" "${API_BASE_URL}/api/document/search"
[[ $(json_value 'total') == 1 ]] || fail "DOI search did not return one document"
pass "exact DOI search returns the uploaded document"

updated_doi="10.5555/crud-updated-${RUN_ID}"
api_request 200 --request PATCH --header 'Content-Type: application/json' \
  --data "{\"originalFilename\":\"renamed-${RUN_ID}.txt\",\"doi\":\"${updated_doi}\"}" \
  "${API_BASE_URL}/api/document/${document_id}"
[[ $(json_value 'document.originalFilename') == "renamed-${RUN_ID}.txt" ]] || fail "document filename was not updated"
[[ $(json_value 'document.doi') == "${updated_doi}" ]] || fail "document DOI was not updated"
pass "document filename and DOI update succeeds"

api_request 200 "${API_BASE_URL}/api/document/${document_id}/content"
grep --fixed-strings --quiet 'document CRUD fixture' "${RESPONSE_FILE}" || fail "content endpoint did not stream source bytes"
pass "document content endpoint streams the source file"

api_request 204 --request DELETE "${API_BASE_URL}/api/document/${document_id}"
TEST_DOCUMENT_IDS=()
pass "document deletion succeeds"
api_request 404 "${API_BASE_URL}/api/document/${document_id}/content"
pass "deleted document is no longer accessible"
