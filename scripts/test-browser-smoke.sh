#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

TSHOOT_BROWSER_COLLECT_SMOKE=1 \
  python3 templates/workspace/skills/frontend-repro-investigator/scripts/test_browser_collect.py
