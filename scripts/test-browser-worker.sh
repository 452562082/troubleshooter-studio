#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

node --test internal/browserverify/worker/browser_worker.test.mjs

if [[ "${TSHOOT_BROWSER_SMOKE:-}" == "1" ]]; then
  go test ./internal/browserverify -run TestRealBrowserSmoke -count=1 -v
fi
