#!/usr/bin/env bash
# Unified web test entry point; runs every frontend test suite in package.json.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/../.." && pwd)
cd "${REPO_ROOT}/web"
npm run test:selection
npm run test:doi
npm run test:bibliography
npm run test:translation
npm run test:reading-note
