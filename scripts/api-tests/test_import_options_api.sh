#!/usr/bin/env bash
# Exercises real HTTP -> services -> storage/PostgreSQL/queue contracts.
set -euo pipefail
source "$(dirname "$0")/lib/document_api_test.sh"

init_api_test import-options
trap cleanup_api_test EXIT
wait_for_api

path="${WORK_DIR}/paper.txt"
printf 'import options fixture %s\n' "${RUN_ID}" > "${path}"
doi="10.5555/import-options-${RUN_ID}"
api_request 201 --request POST \
  --form "file=@${path};filename=paper.txt;type=text/plain" \
  --form-string "metadata={\"title\":\"Imported title\",\"doi\":\"${doi}\"}" \
  --form-string 'processing={"source_lookup":"skip"}' \
  "${API_BASE_URL}/api/document/upload"
document_id=$(json_value 'document.id')
TEST_DOCUMENT_IDS+=("${document_id}")
[[ $(json_value 'document.doi') == "${doi}" ]] || fail 'metadata DOI was not promoted'
[[ $(json_value 'document.importMetadata.status') == 'applied' ]] || fail 'metadata was not applied'
api_request 200 "${API_BASE_URL}/api/paper/${document_id}"
[[ $(json_value 'paperMetadata.title') == 'Imported title' ]] || fail 'imported title was not persisted'

api_request 200 "${API_BASE_URL}/api/document/${document_id}/processing"
python3 - "${RESPONSE_FILE}" <<'PY'
import json
import sys

view = json.load(open(sys.argv[1]))['processing']
source = next(item for item in view['stageStates'] if item['stage'] == 'source_lookup')
assert view['sourceLookupMode'] == 'skip', view
assert source['status'] == 'skipped' and source['generation'] == 0, source
assert source['errorCode'] == 'SKIPPED_BY_IMPORT', source
assert '跳过' in source['errorMessage'], source
PY
pass 'metadata, DOI and explicit skip are observable through real HTTP'

api_request 200 --request POST \
  --form "file=@${path};filename=paper.txt;type=text/plain" \
  --form-string "doi=${doi}" \
  --form-string 'metadata={"title":"Must not overwrite","abstract":"New abstract"}' \
  --form-string 'processing={"source_lookup":"auto"}' \
  "${API_BASE_URL}/api/document/upload"
[[ $(json_value 'status') == 'duplicate' ]] || fail 'duplicate outcome was lost'
[[ $(json_value 'document.id') == "${document_id}" ]] || fail 'duplicate created another document'
[[ $(json_value 'document.sourceLookupMode') == 'skip' ]] || fail 'duplicate changed processing policy'
api_request 200 "${API_BASE_URL}/api/paper/${document_id}"
[[ $(json_value 'paperMetadata.title') == 'Imported title' ]] || fail 'fill_missing overwrote title'
[[ $(json_value 'paperMetadata.abstract') == 'New abstract' ]] || fail 'fill_missing did not fill abstract'
pass 'duplicate fills missing metadata without changing saved policy'

for index in 1 2; do printf 'batch item %s %s\n' "${RUN_ID}" "${index}" > "${WORK_DIR}/${index}.txt"; done
api_request 200 --request POST \
  --form "files=@${WORK_DIR}/1.txt;type=text/plain" \
  --form "files=@${WORK_DIR}/2.txt;type=text/plain" \
  --form-string 'metadata=[{"title":"Batch first"},{"title":"Batch second"}]' \
  --form-string 'processing=[{"source_lookup":"skip"},{"source_lookup":"auto"}]' \
  "${API_BASE_URL}/api/document/batch-upload"
mapfile -t imported_ids < <(python3 - "${RESPONSE_FILE}" <<'PY'
import json
import sys

result = json.load(open(sys.argv[1]))
assert result['status'] == 'succeeded', result
assert len(result['items']) == 2, result
for item in result['items']:
    assert item['importMetadata']['status'] == 'applied', item
    print(item['document']['id'])
PY
)
[[ ${#imported_ids[@]} == 2 ]] || fail 'batch did not return both IDs'
TEST_DOCUMENT_IDS+=("${imported_ids[@]}")
for index in 0 1; do
  api_request 200 "${API_BASE_URL}/api/paper/${imported_ids[index]}"
  if [[ ${index} == 0 ]]; then expected='Batch first'; else expected='Batch second'; fi
  [[ $(json_value 'paperMetadata.title') == "${expected}" ]] || fail 'batch metadata was misaligned'
done
pass 'batch upload preserves per-file metadata alignment'

api_request 400 --request POST \
  --form "files=@${WORK_DIR}/1.txt;type=text/plain" \
  --form "files=@${WORK_DIR}/2.txt;type=text/plain" \
  --form-string 'metadata=[{"title":"Only one"}]' \
  "${API_BASE_URL}/api/document/batch-upload"
pass 'misaligned metadata is rejected before persistence'
