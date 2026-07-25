#!/usr/bin/env bash
# End-to-end checks for every currently exposed document API capability.
# Start the API and PostgreSQL first, then run: scripts/test_document_api.sh
set -euo pipefail

PROJECT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
API_BASE_URL=${API_BASE_URL:-http://localhost:8080}
API_BASE_URL=${API_BASE_URL%/}
PDF_FILE=${PDF_FILE:-"${PROJECT_DIR}/DOne_Decoupling_Structure_Rendering.pdf"}
DOI_FILE=${DOI_FILE:-"${PROJECT_DIR}/doi"}
RUN_SIZE_LIMIT_TEST=${RUN_SIZE_LIMIT_TEST:-0}
MAX_UPLOAD_BYTES=${MAX_UPLOAD_BYTES:-$((100 * 1024 * 1024))}

RESPONSE_FILE=$(mktemp)
WORK_DIR=$(mktemp -d)
RUN_ID="$(date +%s)-${RANDOM}-${RANDOM}"
TITLE_PREFIX=${TITLE_PREFIX:-"document-api-e2e-${RUN_ID}"}
declare -a DOCUMENT_IDS=()
declare -a DOCUMENT_BASE_IDS=()
PRIMARY_DOCUMENT_ID=""
LAST_DOCUMENT_ID=""

pass() { printf 'PASS: %s\n' "$*"; }
fail() {
  printf 'FAIL: %s\n' "$*" >&2
  [[ -s "${RESPONSE_FILE}" ]] && { printf 'Response body:\n' >&2; cat "${RESPONSE_FILE}" >&2; }
  exit 1
}

cleanup() {
	local document_id document_base_id status
	for document_base_id in "${DOCUMENT_BASE_IDS[@]}"; do
		status=$(curl --silent --output /dev/null --write-out '%{http_code}' --request DELETE \
			"${API_BASE_URL}/api/document/bases/${document_base_id}" 2>/dev/null || true)
		[[ "${status}" == "204" || "${status}" == "404" ]] && printf 'CLEANUP: document base %s removed\n' "${document_base_id}"
	done
	for document_id in "${DOCUMENT_IDS[@]}"; do
    status=$(curl --silent --output /dev/null --write-out '%{http_code}' --request DELETE \
      "${API_BASE_URL}/api/document/${document_id}" 2>/dev/null || true)
    [[ "${status}" == "204" || "${status}" == "404" ]] && printf 'CLEANUP: document %s removed\n' "${document_id}"
  done
  rm -f "${RESPONSE_FILE}"
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

require_command() { command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"; }

request() {
  local expected_status=$1
  shift
  local status
  status=$(curl --silent --show-error --output "${RESPONSE_FILE}" --write-out '%{http_code}' \
    --connect-timeout 5 --max-time 120 "$@") || fail "curl request failed: $*"
  [[ "${status}" == "${expected_status}" ]] || fail "expected HTTP ${expected_status}, got ${status}: $*"
}

assert_error() {
  local expected_code=$1 expected_message=$2
  python3 - "${RESPONSE_FILE}" "${expected_code}" "${expected_message}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as response:
    body = json.load(response)
error = body.get("error", {})
if body.get("success") is not False or error.get("code") != int(sys.argv[2]) or error.get("message") != sys.argv[3]:
    raise SystemExit(f"unexpected error response: {body}")
PY
}

document_id_from_response() {
  python3 - "${RESPONSE_FILE}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as response:
    document = json.load(response).get("document", {})
if not document.get("id"):
    raise SystemExit(f"upload response lacks document.id: {document}")
print(document["id"])
PY
}

assert_upload() {
  local expected_filename=$1 expected_doi=$2
  python3 - "${RESPONSE_FILE}" "${expected_filename}" "${expected_doi}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as response:
    document = json.load(response).get("document", {})
if document.get("originalFilename") != sys.argv[2] or document.get("doi") != sys.argv[3]:
    raise SystemExit(f"unexpected upload response: {document}")
if document.get("status") != "uploaded" or not document.get("id"):
    raise SystemExit(f"document was not persisted as uploaded: {document}")
PY
}

assert_page() {
  local expected_page=$1 expected_total=$2 expected_count=$3
  python3 - "${RESPONSE_FILE}" "${expected_page}" "${expected_total}" "${expected_count}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as response:
    body = json.load(response)
if body.get("page") != int(sys.argv[2]) or body.get("pageSize") != 10:
    raise SystemExit(f"unexpected page metadata: {body}")
if body.get("total") != int(sys.argv[3]) or len(body.get("items", [])) != int(sys.argv[4]):
    raise SystemExit(f"unexpected page contents: {body}")
PY
}

assert_result_contains() {
  local document_id=$1
  python3 - "${RESPONSE_FILE}" "${document_id}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as response:
    items = json.load(response).get("items", [])
if sys.argv[2] not in {item.get("id") for item in items}:
    raise SystemExit(f"document {sys.argv[2]} is absent from results: {items}")
PY
}

assert_document_metadata() {
  local expected_filename=$1 expected_doi=$2
  python3 - "${RESPONSE_FILE}" "${expected_filename}" "${expected_doi}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as response:
    document = json.load(response).get("document", {})
if document.get("originalFilename") != sys.argv[2] or document.get("doi") != sys.argv[3]:
    raise SystemExit(f"unexpected updated document: {document}")
PY
}

assert_paper_metadata() {
  local expected_title=$1 expected_tags=$2
  python3 - "${RESPONSE_FILE}" "${expected_title}" "${expected_tags}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as response:
    paper = json.load(response).get("paperMetadata", {})
if paper.get("title") != sys.argv[2] or paper.get("tags") != sys.argv[3].split(","):
    raise SystemExit(f"unexpected paper metadata: {paper}")
PY
}

assert_paper_result_contains() {
  local document_id=$1
  python3 - "${RESPONSE_FILE}" "${document_id}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as response:
    items = json.load(response).get("items", [])
if sys.argv[2] not in {item.get("documentId") for item in items}:
    raise SystemExit(f"paper document {sys.argv[2]} is absent from results: {items}")
PY
}

base_id_from_response() {
  python3 - "${RESPONSE_FILE}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as response:
    base = json.load(response).get("documentBase", {})
if not base.get("id"):
    raise SystemExit(f"base response lacks documentBase.id: {base}")
print(base["id"])
PY
}

assert_document_status() {
  local expected_status=$1
  python3 - "${RESPONSE_FILE}" "${expected_status}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as response:
    document = json.load(response).get("document", {})
if document.get("status") != sys.argv[2]:
    raise SystemExit(f"unexpected document status: {document}")
PY
}

wait_for_api() {
  local attempt status
  for attempt in $(seq 1 30); do
    status=$(curl --silent --output "${RESPONSE_FILE}" --write-out '%{http_code}' \
      --connect-timeout 2 "${API_BASE_URL}/health" || true)
    if [[ "${status}" == "200" ]] && grep --fixed-strings --quiet '"status":"ok"' "${RESPONSE_FILE}"; then
      pass "health endpoint is ready"
      return
    fi
    sleep 1
  done
  fail "API did not become healthy at ${API_BASE_URL}/health"
}

upload_file() {
  local file_path=$1 uploaded_filename=$2 document_doi=$3 content_type=$4 document_id
  request 201 --request POST \
    --form "file=@${file_path};filename=${uploaded_filename};type=${content_type}" \
    --form-string "doi=${document_doi}" \
    "${API_BASE_URL}/api/document/upload"
  assert_upload "${uploaded_filename}" "${document_doi}"
  document_id=$(document_id_from_response) || fail "could not extract document ID"
  DOCUMENT_IDS+=("${document_id}")
  LAST_DOCUMENT_ID="${document_id}"
  printf 'PASS: uploaded %-42s document_id=%s\n' "${uploaded_filename}" "${document_id}" >&2
}

make_zip_office_fixture() {
  local target=$1
  printf 'PK\003\004minimal-office-fixture' > "${target}"
}

make_legacy_office_fixture() {
  local target=$1
  printf '\320\317\021\340\241\261\032\341minimal-office-fixture' > "${target}"
}

remove_tracked_document() {
  local target=$1 index
  for index in "${!DOCUMENT_IDS[@]}"; do
    if [[ "${DOCUMENT_IDS[index]}" == "${target}" ]]; then
      unset 'DOCUMENT_IDS[index]'
      return
    fi
  done
}

require_command curl
require_command python3
[[ -f "${PDF_FILE}" ]] || fail "PDF file not found: ${PDF_FILE}"
[[ -s "${DOI_FILE}" ]] || fail "DOI file is empty or missing: ${DOI_FILE}"
SOURCE_DOI=$(tr -d '\r\n' < "${DOI_FILE}")
[[ -n "${SOURCE_DOI}" ]] || fail "DOI file contains only line breaks: ${DOI_FILE}"
PRIMARY_DOI="${SOURCE_DOI,,}.e2e.${RUN_ID}"

printf 'Testing document API at %s\n' "${API_BASE_URL}"
wait_for_api

upload_file "${PDF_FILE}" "${TITLE_PREFIX}-paper.pdf" "${PRIMARY_DOI}" "application/pdf"
PRIMARY_DOCUMENT_ID="${LAST_DOCUMENT_ID}"

declare -a FIXTURES=(
  'text.txt|text/plain|text'
  'data.json|application/json|json'
  'notes.md|text/markdown|text'
  'notes.markdown|text/markdown|text'
  'table.csv|text/csv|text'
  'legacy.doc|application/msword|legacy'
  'word.docx|application/vnd.openxmlformats-officedocument.wordprocessingml.document|zip'
  'sheet.xls|application/vnd.ms-excel|legacy'
  'sheet.xlsx|application/vnd.openxmlformats-officedocument.spreadsheetml.sheet|zip'
  'slides.ppt|application/vnd.ms-powerpoint|legacy'
  'slides.pptx|application/vnd.openxmlformats-officedocument.presentationml.presentation|zip'
)

fixture_index=0
for fixture in "${FIXTURES[@]}"; do
  IFS='|' read -r fixture_name content_type fixture_kind <<< "${fixture}"
  fixture_path="${WORK_DIR}/${fixture_name}"
  case "${fixture_kind}" in
    text) printf 'fixture %s\n' "${fixture_name}" > "${fixture_path}" ;;
    json) printf '{"title":"document api fixture"}\n' > "${fixture_path}" ;;
    legacy) make_legacy_office_fixture "${fixture_path}" ;;
    zip) make_zip_office_fixture "${fixture_path}" ;;
  esac
  upload_file "${fixture_path}" "${TITLE_PREFIX}-${fixture_name}" \
    "${PRIMARY_DOI}.fixture.${fixture_index}" "${content_type}"
  fixture_index=$((fixture_index + 1))
done

expected_total=${#DOCUMENT_IDS[@]}
[[ "${expected_total}" -eq 12 ]] || fail "expected 12 uploads, got ${expected_total}"
pass "all 12 supported file types uploaded"

request 200 "${API_BASE_URL}/api/document/search?page=1"
python3 - "${RESPONSE_FILE}" <<'PY'
import json, sys
body = json.load(open(sys.argv[1], encoding="utf-8"))
if body.get("page") != 1 or body.get("pageSize") != 10 or len(body.get("items", [])) > 10:
    raise SystemExit(f"unexpected all-documents page: {body}")
PY
pass "unfiltered search page 1"

request 200 --get --data-urlencode "id=${PRIMARY_DOCUMENT_ID}" --data 'page=1' "${API_BASE_URL}/api/document/search"
assert_page 1 1 1
assert_result_contains "${PRIMARY_DOCUMENT_ID}"
pass "exact ID search"

request 200 --get --data-urlencode "doi=${PRIMARY_DOI^^}" --data 'page=1' "${API_BASE_URL}/api/document/search"
assert_page 1 1 1
assert_result_contains "${PRIMARY_DOCUMENT_ID}"
pass "case-insensitive DOI search"

request 400 --get --data-urlencode "title=${TITLE_PREFIX}" --data 'page=1' "${API_BASE_URL}/api/document/search"
assert_error 1000 "document search only supports id and doi"
pass "document title search rejected"

request 409 --request POST \
  --form "file=@${WORK_DIR}/text.txt;filename=${TITLE_PREFIX}-duplicate.txt;type=text/plain" \
  --form-string "doi=${PRIMARY_DOI}" "${API_BASE_URL}/api/document/upload"
assert_error 1005 "doi already exists"
pass "duplicate DOI rejected"

request 400 --request PATCH --header 'Content-Type: application/json' --data '{}' \
  "${API_BASE_URL}/api/document/${PRIMARY_DOCUMENT_ID}"
assert_error 1000 "provide originalFilename and/or doi"
pass "empty document metadata update rejected"

request 409 --request PATCH --header 'Content-Type: application/json' \
  --data "{\"doi\":\"${PRIMARY_DOI}.fixture.0\"}" "${API_BASE_URL}/api/document/${PRIMARY_DOCUMENT_ID}"
assert_error 1005 "doi already exists"
pass "duplicate DOI metadata update rejected"

UPDATED_FILENAME="${TITLE_PREFIX}-paper-renamed.pdf"
UPDATED_DOI="${PRIMARY_DOI}.updated"
UPDATED_TITLE="Document API integration paper"
UPDATED_TAGS="computer-science,graphics"
UPDATED_AUTHOR="Ada Lovelace"
request 200 --request PATCH --header 'Content-Type: application/json' \
  --data "{\"originalFilename\":\"${UPDATED_FILENAME}\",\"doi\":\"${UPDATED_DOI}\"}" \
  "${API_BASE_URL}/api/document/${PRIMARY_DOCUMENT_ID}"
assert_document_metadata "${UPDATED_FILENAME}" "${UPDATED_DOI}"
pass "document file and DOI updated"

request 200 --request PATCH --header 'Content-Type: application/json' \
  --data "{\"title\":\"${UPDATED_TITLE}\",\"authors\":[{\"name\":\"${UPDATED_AUTHOR}\"}],\"tags\":[\"computer-science\",\"graphics\"]}" \
  "${API_BASE_URL}/api/paper/${PRIMARY_DOCUMENT_ID}"
assert_paper_metadata "${UPDATED_TITLE}" "${UPDATED_TAGS}"
pass "paper title and discipline tags updated"

request 200 --get --data-urlencode "doi=${UPDATED_DOI^^}" --data 'page=1' "${API_BASE_URL}/api/document/search"
assert_page 1 1 1
assert_result_contains "${PRIMARY_DOCUMENT_ID}"
pass "updated DOI search"

request 200 --get --data-urlencode "title=${UPDATED_TITLE}" --data 'page=1' "${API_BASE_URL}/api/paper/search"
assert_page 1 1 1
assert_paper_result_contains "${PRIMARY_DOCUMENT_ID}"
pass "formal paper title search"

request 200 --get --data-urlencode "author=${UPDATED_AUTHOR}" --data 'page=1' "${API_BASE_URL}/api/paper/search"
assert_page 1 1 1
assert_paper_result_contains "${PRIMARY_DOCUMENT_ID}"
pass "paper author search"

request 200 --get --data-urlencode "tag=computer-science" --data 'tagMode=all' --data 'page=1' "${API_BASE_URL}/api/paper/search"
assert_page 1 1 1
assert_paper_result_contains "${PRIMARY_DOCUMENT_ID}"
pass "paper discipline tag search"

BASE_NAME="${TITLE_PREFIX}-base"
request 201 --request POST --header 'Content-Type: application/json' \
  --data "{\"name\":\"${BASE_NAME}\",\"description\":\"document API integration collection\"}" \
  "${API_BASE_URL}/api/document/bases"
PRIMARY_DOCUMENT_BASE_ID=$(base_id_from_response) || fail "could not extract document base ID"
DOCUMENT_BASE_IDS+=("${PRIMARY_DOCUMENT_BASE_ID}")
pass "document base created"

request 204 --request PUT "${API_BASE_URL}/api/document/bases/${PRIMARY_DOCUMENT_BASE_ID}/documents/${PRIMARY_DOCUMENT_ID}"
pass "document added to document base"

request 200 "${API_BASE_URL}/api/document/bases/${PRIMARY_DOCUMENT_BASE_ID}/documents?page=1"
assert_page 1 1 1
assert_result_contains "${PRIMARY_DOCUMENT_ID}"
pass "document base member listing"

request 204 --request DELETE "${API_BASE_URL}/api/document/bases/${PRIMARY_DOCUMENT_BASE_ID}/documents/${PRIMARY_DOCUMENT_ID}"
pass "document removed from document base without deleting source"

request 200 "${API_BASE_URL}/api/document/bases/${PRIMARY_DOCUMENT_BASE_ID}/documents?page=1"
assert_page 1 0 0
pass "empty document base remains after membership removal"

request 200 --request POST "${API_BASE_URL}/api/document/${PRIMARY_DOCUMENT_ID}/cancel"
assert_document_status "cancelled"
pass "document parse cancellation"

request 200 --request POST "${API_BASE_URL}/api/document/${PRIMARY_DOCUMENT_ID}/cancel"
assert_document_status "cancelled"
pass "document parse cancellation is idempotent"

request 400 --request POST "${API_BASE_URL}/api/document/upload"
assert_error 1000 "multipart field 'file' is required"
pass "missing multipart file rejected"

: > "${WORK_DIR}/empty.txt"
request 400 --request POST --form "file=@${WORK_DIR}/empty.txt;type=text/plain" "${API_BASE_URL}/api/document/upload"
assert_error 1000 "uploaded file is empty"
pass "empty file rejected"

printf 'not a PDF' > "${WORK_DIR}/invalid.pdf"
request 400 --request POST --form "file=@${WORK_DIR}/invalid.pdf;type=application/pdf" "${API_BASE_URL}/api/document/upload"
assert_error 1000 "invalid PDF file"
pass "invalid PDF rejected"

printf 'not a ZIP archive' > "${WORK_DIR}/invalid.docx"
request 400 --request POST --form "file=@${WORK_DIR}/invalid.docx;type=application/vnd.openxmlformats-officedocument.wordprocessingml.document" "${API_BASE_URL}/api/document/upload"
assert_error 1000 "invalid office file"
pass "invalid Office file rejected"

printf 'unsupported' > "${WORK_DIR}/unsupported.exe"
request 415 --request POST --form "file=@${WORK_DIR}/unsupported.exe;type=application/octet-stream" "${API_BASE_URL}/api/document/upload"
assert_error 1012 "unsupported file type"
pass "unsupported extension rejected"

request 400 --request POST --form "file=@${WORK_DIR}/text.txt;type=text/plain" \
  --form-string $'doi=10.1000/invalid\ndoi' "${API_BASE_URL}/api/document/upload"
assert_error 1000 "doi must be at most 255 characters and contain no control characters"
pass "invalid DOI rejected"

request 400 "${API_BASE_URL}/api/document/search?page=0"
assert_error 1000 "page must be between 1 and 10000"
pass "invalid low page rejected"

request 400 "${API_BASE_URL}/api/document/search?page=10001"
assert_error 1000 "page must be between 1 and 10000"
pass "invalid high page rejected"

request 400 "${API_BASE_URL}/api/document/search?id=not-a-uuid"
assert_error 1000 "id must be a UUID"
pass "invalid search ID rejected"

request 400 --request DELETE "${API_BASE_URL}/api/document/not-a-uuid"
assert_error 1000 "invalid document id"
pass "invalid delete ID rejected"

if [[ "${RUN_SIZE_LIMIT_TEST}" == "1" ]]; then
  require_command truncate
  size_limit_file="${WORK_DIR}/over-limit.txt"
  truncate -s $((MAX_UPLOAD_BYTES + 1)) "${size_limit_file}"
  request 413 --request POST --form "file=@${size_limit_file};type=text/plain" "${API_BASE_URL}/api/document/upload"
  assert_error 1011 "file exceeds the configured upload limit"
  pass "configured upload size limit rejected oversized file"
fi

request 204 --request DELETE "${API_BASE_URL}/api/document/${PRIMARY_DOCUMENT_ID}"
remove_tracked_document "${PRIMARY_DOCUMENT_ID}"
pass "document deletion"

request 200 --get --data-urlencode "id=${PRIMARY_DOCUMENT_ID}" --data 'page=1' "${API_BASE_URL}/api/document/search"
assert_page 1 0 0
pass "deleted document absent from search"

request 404 --request DELETE "${API_BASE_URL}/api/document/${PRIMARY_DOCUMENT_ID}"
assert_error 1003 "document not found"
pass "repeat deletion returns not found"

pass "all document API checks completed"
