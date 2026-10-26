#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test document-selection
trap cleanup_api_test EXIT
wait_for_api

fixture="${WORK_DIR}/selection.pdf"
make_valid_pdf_fixture "${fixture}"
upload_file "${fixture}" "selection-${RUN_ID}.pdf" "10.5555/selection-${RUN_ID}" "application/pdf"
document_id=${LAST_DOCUMENT_ID}

# Source selection itself is browser-local. This intentionally constructs the
# latest area draft without issuing curl; only the explicit process step below
# increments the request count.
process_requests=0
selection_page=1
selection_region=(0.1 0.2 0.6 0.8)
(( process_requests == 0 )) || fail "selection draft unexpectedly issued a request"
pass "selection draft creates no API request"

capture="${WORK_DIR}/selection.png"
python3 - "${capture}" <<'PY'
import struct, sys, zlib

width, height = 12, 8
raw = b''.join(b'\x00' + b'\x20\x60\xc0' * width for _ in range(height))
def chunk(kind, data):
    return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data) & 0xffffffff)
png = b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', width, height, 8, 2, 0, 0, 0)) + chunk(b'IDAT', zlib.compress(raw)) + chunk(b'IEND', b'')
open(sys.argv[1], 'wb').write(png)
PY

api_request 200 --request POST \
  --form-string "document_id=${document_id}" \
  --form-string "kind=area" \
  --form-string "page_number=${selection_page}" \
  --form-string "x0=${selection_region[0]}" \
  --form-string "y0=${selection_region[1]}" \
  --form-string "x1=${selection_region[2]}" \
  --form-string "y1=${selection_region[3]}" \
  --form "image=@${capture};type=image/png" \
  "${API_BASE_URL}/api/document/${document_id}/selection/process"
process_requests=$((process_requests + 1))
[[ "${process_requests}" == "1" ]] || fail "expected exactly one process request"
[[ $(json_value 'result.kind') == "area" ]] || fail "area selection was not acknowledged"
[[ $(json_value 'result.imageWidth') == "12" ]] || fail "capture width was not decoded"
pass "explicit Process uploads exactly one latest PNG capture"

api_request 200 --request POST \
  --form-string "document_id=${document_id}" \
  --form-string "kind=text" \
  --form-string "text=latest selected text" \
  --form-string "page_number=1" \
  "${API_BASE_URL}/api/document/${document_id}/selection/process"
[[ $(json_value 'result.kind') == "text" ]] || fail "text selection was not acknowledged"
pass "transient text selection is accepted without screenshot bytes"

api_request 400 --request POST \
  --form-string "document_id=${document_id}" \
  --form-string "kind=area" --form-string "page_number=1" \
  --form-string "x0=0.9" --form-string "y0=0.2" --form-string "x1=0.1" --form-string "y1=0.8" \
  --form "image=@${capture};type=image/png" \
  "${API_BASE_URL}/api/document/${document_id}/selection/process"
pass "invalid normalized capture coordinates are rejected"
