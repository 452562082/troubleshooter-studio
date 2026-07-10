#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
TSHOOT_CODEGRAPH_LIVE=1 go test ./internal/agent/ -run TestCodeGraphLive -count=1 -v
