#!/usr/bin/env bash
# Unified API smoke-suite entry point. Run it from any directory; every
# child script in this folder is self-contained and cleans up its fixtures. Each child script owns its data and
# cleanup, so a failure leaves the preceding test's result readable.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
tests=(
  test_health_api.sh
  test_upload_api.sh
  test_document_crud_api.sh
  test_document_selection_api.sh
  test_paper_metadata_api.sh
  test_paper_info_api.sh
  test_remote_import_api.sh
  test_reference_acquisition_api.sh
  test_document_base_api.sh
  test_cancel_api.sh
)
if [[ "${SKIP_PROCESS_TEST:-0}" != "1" ]]; then
  tests+=(test_processing_api.sh)
fi

printf 'Running %d API test group(s) against %s\n' "${#tests[@]}" "${API_BASE_URL:-http://localhost:8080}"
for test_script in "${tests[@]}"; do
  printf '\n==> %s\n' "${test_script}"
  "${SCRIPT_DIR}/${test_script}"
done
printf '\nPASS: all API test groups completed\n'
