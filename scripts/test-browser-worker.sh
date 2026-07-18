#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

node --test internal/browserverify/worker/browser_worker.test.mjs

if [[ "${TSHOOT_BROWSER_SMOKE:-}" == "1" ]]; then
  # Runtime installation allows a 90-minute Chromium download on slow release
  # networks, so Go's default 10-minute package timeout would terminate a
  # healthy smoke before RuntimeManager can report its own bounded outcome.
  go test ./internal/browserverify -run TestRealBrowserSmoke -count=1 -v -timeout 100m
fi
