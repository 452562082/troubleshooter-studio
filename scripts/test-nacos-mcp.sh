#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v uv >/dev/null 2>&1; then
  echo "uv not found; install with: python3 -m pip install uv" >&2
  exit 1
fi

uv run \
  --with pytest \
  --with pytest-asyncio \
  --with respx \
  --with httpx \
  --with 'mcp[cli]>=1.6.0' \
  pytest templates/workspace/skills/config-executor/scripts/test_nacos_mcp.py -q \
  -o asyncio_mode=auto
