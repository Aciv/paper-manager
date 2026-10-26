#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test upload
trap cleanup_api_test EXIT
wait_for_api

prefix="upload-${RUN_ID}"
pdf="${WORK_DIR}/paper.pdf"
make_pdf_fixture "${pdf}"
upload_file "${pdf}" "${prefix}-paper.pdf" "10.5555/${prefix}.pdf" "application/pdf"

declare -a fixtures=(
  'plain.txt|text/plain|text'
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

index=0
for fixture in "${fixtures[@]}"; do
  IFS='|' read -r name content_type kind <<< "${fixture}"
  path="${WORK_DIR}/${name}"
  case "${kind}" in
    text) printf 'fixture %s\n' "${name}" > "${path}" ;;
    json) printf '{"fixture":"document-api"}\n' > "${path}" ;;
    zip) make_zip_fixture "${path}" ;;
    legacy) make_legacy_office_fixture "${path}" ;;
  esac
  upload_file "${path}" "${prefix}-${name}" "10.5555/${prefix}.${index}" "${content_type}"
  index=$((index + 1))
done

[[ ${#TEST_DOCUMENT_IDS[@]} -eq 12 ]] || fail "expected 12 supported uploads, got ${#TEST_DOCUMENT_IDS[@]}"
pass "all supported upload formats were accepted"

api_request 409 --request POST --form "file=@${pdf};filename=duplicate.pdf;type=application/pdf" \
  --form-string "doi=10.5555/${prefix}.pdf" "${API_BASE_URL}/api/document/upload"
pass "duplicate DOI is rejected"

printf 'not an accepted format\n' > "${WORK_DIR}/unsupported.exe"
api_request 415 --request POST --form "file=@${WORK_DIR}/unsupported.exe;filename=unsupported.exe;type=application/octet-stream" \
  "${API_BASE_URL}/api/document/upload"
pass "unsupported upload type is rejected"
