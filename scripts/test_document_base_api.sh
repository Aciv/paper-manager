#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test document-base
trap cleanup_api_test EXIT
wait_for_api

path="${WORK_DIR}/base-member.txt"
printf 'base member fixture\n' > "${path}"
upload_file "${path}" "base-member-${RUN_ID}.txt" "10.5555/base-${RUN_ID}" "text/plain"
document_id=${LAST_DOCUMENT_ID}
base_name="API Base ${RUN_ID}"

api_request 201 --request POST --header 'Content-Type: application/json' \
  --data "{\"name\":\"${base_name}\",\"description\":\"initial description\"}" "${API_BASE_URL}/api/document/bases"
base_id=$(json_value 'documentBase.id')
TEST_BASE_IDS+=("${base_id}")
pass "document base created base_id=${base_id}"

api_request 200 "${API_BASE_URL}/api/document/bases"
python3 - "${RESPONSE_FILE}" "${base_id}" <<'PY'
import json, sys
if sys.argv[2] not in {item.get('id') for item in json.load(open(sys.argv[1])).get('items', [])}:
    raise SystemExit('base listing did not include the created base')
PY
pass "document base listing succeeds"

api_request 200 "${API_BASE_URL}/api/document/bases/${base_id}"
[[ $(json_value 'documentBase.id') == "${base_id}" ]] || fail "base lookup returned wrong id"
pass "document base lookup succeeds"

api_request 200 --request PATCH --header 'Content-Type: application/json' \
  --data '{"description":"updated description"}' "${API_BASE_URL}/api/document/bases/${base_id}"
[[ $(json_value 'documentBase.description') == 'updated description' ]] || fail "document base update was not persisted"
pass "document base update succeeds"

api_request 204 --request PUT "${API_BASE_URL}/api/document/bases/${base_id}/documents/${document_id}"
pass "document added to base"
api_request 200 "${API_BASE_URL}/api/document/bases/${base_id}/documents?page=1"
python3 - "${RESPONSE_FILE}" "${document_id}" <<'PY'
import json, sys
if sys.argv[2] not in {item.get('id') for item in json.load(open(sys.argv[1])).get('items', [])}:
    raise SystemExit('base member listing did not include test document')
PY
pass "base membership listing succeeds"

api_request 204 --request DELETE "${API_BASE_URL}/api/document/bases/${base_id}/documents/${document_id}"
pass "document removed from base"
api_request 204 --request DELETE "${API_BASE_URL}/api/document/bases/${base_id}"
TEST_BASE_IDS=()
pass "document base deletion succeeds"
