#!/usr/bin/env bash
# Shared helpers for the API smoke scripts. Source this file; do not execute it.

set -euo pipefail

PROJECT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
API_BASE_URL=${API_BASE_URL:-http://localhost:8080}
API_BASE_URL=${API_BASE_URL%/}

RESPONSE_FILE=""
WORK_DIR=""
TEST_NAME=""
RUN_ID=""
declare -a TEST_DOCUMENT_IDS=()
declare -a TEST_BASE_IDS=()

pass() { printf 'PASS [%s]: %s\n' "${TEST_NAME}" "$*"; }
fail() {
  printf 'FAIL [%s]: %s\n' "${TEST_NAME}" "$*" >&2
  if [[ -n "${RESPONSE_FILE}" && -s "${RESPONSE_FILE}" ]]; then
    printf 'Response body:\n' >&2
    cat "${RESPONSE_FILE}" >&2
  fi
  exit 1
}

require_command() { command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"; }

init_api_test() {
  TEST_NAME=$1
  RESPONSE_FILE=$(mktemp)
  WORK_DIR=$(mktemp -d)
  RUN_ID="$(date +%s)-${RANDOM}-${RANDOM}"
  require_command curl
  require_command python3
}

api_request() {
  local expected_status=$1
  shift
  local status
  status=$(curl --silent --show-error --output "${RESPONSE_FILE}" --write-out '%{http_code}' \
    --connect-timeout 5 --max-time "${API_TIMEOUT_SECONDS:-120}" "$@") || fail "curl request failed: $*"
  [[ "${status}" == "${expected_status}" ]] || fail "expected HTTP ${expected_status}, got ${status}: $*"
}

api_status() {
  curl --silent --show-error --output "${RESPONSE_FILE}" --write-out '%{http_code}' \
    --connect-timeout 5 --max-time "${API_TIMEOUT_SECONDS:-120}" "$@"
}

wait_for_api() {
  local attempt status
  for attempt in $(seq 1 30); do
    status=$(api_status "${API_BASE_URL}/health" || true)
    if [[ "${status}" == "200" ]] && grep --fixed-strings --quiet '"status":"ok"' "${RESPONSE_FILE}"; then
      pass "health endpoint is ready"
      return
    fi
    sleep 1
  done
  fail "API did not become healthy at ${API_BASE_URL}/health"
}

json_value() {
  local expression=$1
  python3 - "${RESPONSE_FILE}" "${expression}" <<'PY'
import json
import sys

body = json.load(open(sys.argv[1], encoding="utf-8"))
value = body
for part in sys.argv[2].split('.'):
    value = value[int(part)] if isinstance(value, list) else value[part]
print(value)
PY
}

assert_document_response() {
  local filename=$1 doi=$2
  python3 - "${RESPONSE_FILE}" "${filename}" "${doi}" <<'PY'
import json
import sys

document = json.load(open(sys.argv[1], encoding="utf-8")).get("document", {})
if not document.get("id"):
    raise SystemExit(f"missing document id: {document}")
if document.get("originalFilename") != sys.argv[2] or document.get("doi") != sys.argv[3]:
    raise SystemExit(f"unexpected document: {document}")
PY
}

assert_accepted_response() {
  python3 - "${RESPONSE_FILE}" <<'PY'
import json
import sys

body = json.load(open(sys.argv[1], encoding="utf-8"))
if body.get("accepted") is not True:
    raise SystemExit(f"expected accepted=true: {body}")
PY
}

wait_for_document_terminal() {
  local document_id=$1 timeout_seconds=${2:-180}
  local deadline=$((SECONDS + timeout_seconds))
  while :; do
    api_request 200 --get --data-urlencode "id=${document_id}" "${API_BASE_URL}/api/document/search"
    LAST_DOCUMENT_STATUS=$(json_value 'items.0.status')
    case "${LAST_DOCUMENT_STATUS}" in
      ready|failed|cancelled) return ;;
    esac
    (( SECONDS < deadline )) || fail "document ${document_id} did not reach a terminal state within ${timeout_seconds}s (last status: ${LAST_DOCUMENT_STATUS})"
    sleep 2
  done
}

upload_file() {
  local path=$1 filename=$2 doi=$3 content_type=$4
  api_request 201 --request POST \
    --form "file=@${path};filename=${filename};type=${content_type}" \
    --form-string "doi=${doi}" "${API_BASE_URL}/api/document/upload"
  assert_document_response "${filename}" "${doi}"
  LAST_DOCUMENT_ID=$(json_value 'document.id')
  TEST_DOCUMENT_IDS+=("${LAST_DOCUMENT_ID}")
  pass "uploaded ${filename} document_id=${LAST_DOCUMENT_ID}"
}

make_pdf_fixture() {
  local target=$1
  # The upload layer validates only the PDF signature. Processing tests use a
  # real PDF fixture instead, because a parser must receive valid PDF syntax.
  printf '%%PDF-1.7\n%% API test fixture\n' > "${target}"
}

make_valid_pdf_fixture() {
  local target=$1
  python3 - "${target}" <<'PY'
import sys

target = sys.argv[1]
stream = b"BT /F1 18 Tf 72 720 Td (Phase 17 processing fixture) Tj ET\n"
objects = [
    b"<< /Type /Catalog /Pages 2 0 R >>",
    b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
    b"<< /Length %d >>\nstream\n" % len(stream) + stream + b"endstream",
    b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
]
pdf = bytearray(b"%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
offsets = [0]
for number, body in enumerate(objects, 1):
    offsets.append(len(pdf))
    pdf.extend(f"{number} 0 obj\n".encode())
    pdf.extend(body)
    pdf.extend(b"\nendobj\n")
xref = len(pdf)
pdf.extend(f"xref\n0 {len(objects) + 1}\n".encode())
pdf.extend(b"0000000000 65535 f \n")
for offset in offsets[1:]:
    pdf.extend(f"{offset:010d} 00000 n \n".encode())
pdf.extend(
    f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode()
)
with open(target, "wb") as handle:
    handle.write(pdf)
PY
}

make_zip_fixture() { printf 'PK\003\004document-api-fixture-%s' "$(basename "$1")" > "$1"; }
make_legacy_office_fixture() { printf '\320\317\021\340\241\261\032\341document-api-fixture-%s' "$(basename "$1")" > "$1"; }

cleanup_api_test() {
  local id status
  for id in "${TEST_BASE_IDS[@]}"; do
    status=$(api_status --request DELETE "${API_BASE_URL}/api/document/bases/${id}" 2>/dev/null || true)
    [[ "${status}" == "204" || "${status}" == "404" ]] && printf 'CLEANUP [%s]: base %s removed\n' "${TEST_NAME}" "${id}"
  done
  for id in "${TEST_DOCUMENT_IDS[@]}"; do
    status=$(api_status --request DELETE "${API_BASE_URL}/api/document/${id}" 2>/dev/null || true)
    [[ "${status}" == "204" || "${status}" == "404" ]] && printf 'CLEANUP [%s]: document %s removed\n' "${TEST_NAME}" "${id}"
  done
  [[ -n "${RESPONSE_FILE}" ]] && rm -f "${RESPONSE_FILE}"
  [[ -n "${WORK_DIR}" ]] && rm -rf "${WORK_DIR}"
}
