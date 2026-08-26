#!/usr/bin/env bash
# Exercises the bibliography acquisition control plane and durable processing
# projection. Actual public downloads are optional; bounded fan-out and
# per-target behavior are covered deterministically by focused Go tests.
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test reference-acquisition
trap cleanup_api_test EXIT
wait_for_api

source_path="${WORK_DIR}/source.txt"
printf 'reference acquisition source fixture\n' > "${source_path}"
upload_file "${source_path}" "reference-acquisition-${RUN_ID}.txt" "10.5555/reference-acquisition-${RUN_ID}" "text/plain"
source_document_id=${LAST_DOCUMENT_ID}

wait_for_document_terminal "${source_document_id}" "${PROCESS_TIMEOUT_SECONDS:-180}"
source_status=${LAST_DOCUMENT_STATUS}
pass "source document reached stable status ${source_status} before acquisition"

reference_count=${REFERENCE_ACQUISITION_COUNT:-3}
declare -a target_ids=()
for index in $(seq 1 "${reference_count}"); do
  api_request 201 --request POST --header 'Content-Type: application/json' \
    --data "{\"rawReference\":\"Reference acquisition fixture ${RUN_ID} number ${index}\"}" \
    "${API_BASE_URL}/api/paper/${source_document_id}/citations"
  target_id=$(json_value 'citation.citedDocumentId')
  target_ids+=("${target_id}")
  TEST_DOCUMENT_IDS+=("${target_id}")
  pass "created unresolved reference target ${target_id}"

  if [[ -n "${REFERENCE_ACQUISITION_LIVE_URL:-}" ]]; then
    python3 - "${REFERENCE_ACQUISITION_LIVE_URL}" > "${WORK_DIR}/source-url-${index}.json" <<'PY'
import json
import sys
print(json.dumps({"sourceUrl": sys.argv[1]}))
PY
    api_request 200 --request PATCH --header 'Content-Type: application/json' \
      --data-binary "@${WORK_DIR}/source-url-${index}.json" \
      "${API_BASE_URL}/api/paper/${target_id}"
    pass "attached opt-in remote source URL to target ${target_id}"
  fi
done

api_request 202 --request POST "${API_BASE_URL}/api/paper/${source_document_id}/references/download"
assert_accepted_response
pass "reference PDF acquisition dispatcher is queued"

deadline=$((SECONDS + ${REFERENCE_ACQUISITION_TIMEOUT_SECONDS:-30}))
while :; do
  api_request 200 "${API_BASE_URL}/api/document/${source_document_id}/processing"
  if python3 - "${RESPONSE_FILE}" <<'PY'
import json, sys
view = json.load(open(sys.argv[1], encoding='utf-8'))['processing']
raise SystemExit(0 if any(stage.get('kind') == 'reference_download' for stage in view.get('stages', [])) else 1)
PY
  then
    break
  fi
  (( SECONDS < deadline )) || fail "reference acquisition span did not become observable"
  sleep 1
done
pass "reference acquisition run is visible through processing spans"

python3 - "${RESPONSE_FILE}" <<'PY'
import json, sys
view = json.load(open(sys.argv[1], encoding='utf-8'))['processing']
progress = view.get('referenceProgress')
if progress is not None:
    for key in ('queued', 'running', 'completed', 'cancelled'):
        if not isinstance(progress.get(key), int) or progress[key] < 0:
            raise SystemExit(f'invalid compact reference progress: {progress}')
PY
pass "compact reference progress has a stable non-negative shape"

api_request 202 --request POST "${API_BASE_URL}/api/paper/${source_document_id}/references/download/cancel"
assert_accepted_response
pass "reference PDF acquisition cancellation is accepted"

api_request 200 --get --data-urlencode "id=${source_document_id}" "${API_BASE_URL}/api/document/search"
[[ $(json_value 'items.0.status') == "${source_status}" ]] || fail "reference acquisition changed source document lifecycle status"
pass "reference acquisition leaves source document lifecycle unchanged"

for target_id in "${target_ids[@]}"; do
  status=$(api_status --request DELETE "${API_BASE_URL}/api/paper/${source_document_id}/citations/${target_id}")
  [[ "${status}" == "204" || "${status}" == "404" ]] || fail "failed to remove reference fixture ${target_id}"
done
pass "reference acquisition fixtures were detached"
