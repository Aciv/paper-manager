#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test paper-metadata
trap cleanup_api_test EXIT
wait_for_api

path="${WORK_DIR}/paper.txt"
printf 'paper metadata fixture\n' > "${path}"
upload_file "${path}" "paper-${RUN_ID}.txt" "10.5555/meta-${RUN_ID}" "text/plain"
document_id=${LAST_DOCUMENT_ID}
title="Paper Metadata ${RUN_ID}"

api_request 200 --request PATCH --header 'Content-Type: application/json' \
  --data "{\"title\":\"${title}\",\"authors\":[{\"name\":\"Ada Lovelace\"}],\"tags\":[\"computer-science\",\"ai\"],\"sourceName\":\"API test\"}" \
  "${API_BASE_URL}/api/paper/${document_id}"
[[ $(json_value 'paperMetadata.title') == "${title}" ]] || fail "paper title was not persisted"
pass "paper metadata update succeeds"

api_request 200 "${API_BASE_URL}/api/paper/${document_id}"
[[ $(json_value 'paperMetadata.documentId') == "${document_id}" ]] || fail "paper metadata lookup returned wrong document"
pass "paper metadata lookup succeeds"

api_request 200 --get --data-urlencode "title=${title}" "${API_BASE_URL}/api/paper/search"
python3 - "${RESPONSE_FILE}" "${document_id}" <<'PY'
import json, sys
if sys.argv[2] not in {item.get('documentId') for item in json.load(open(sys.argv[1])).get('items', [])}:
    raise SystemExit('title search did not include test document')
PY
pass "title search succeeds"

api_request 200 --get --data-urlencode 'author=Ada Lovelace' "${API_BASE_URL}/api/paper/search"
python3 - "${RESPONSE_FILE}" "${document_id}" <<'PY'
import json, sys
if sys.argv[2] not in {item.get('documentId') for item in json.load(open(sys.argv[1])).get('items', [])}:
    raise SystemExit('author search did not include test document')
PY
pass "author search succeeds"

api_request 200 --get --data-urlencode 'tag=ai' --data-urlencode 'tag=computer-science' --data 'tagMode=all' "${API_BASE_URL}/api/paper/search"
python3 - "${RESPONSE_FILE}" "${document_id}" <<'PY'
import json, sys
if sys.argv[2] not in {item.get('documentId') for item in json.load(open(sys.argv[1])).get('items', [])}:
    raise SystemExit('tag search did not include test document')
PY
pass "tag search succeeds"
