#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 not found; required for workspace skill script tests" >&2
  exit 1
fi

python3 - <<'PY'
import importlib.util
import sys

if importlib.util.find_spec("pytest") is None:
    print("pytest not found; install with: python3 -m pip install pytest", file=sys.stderr)
    sys.exit(1)
PY

stdlib_tests=(
  templates/workspace/skills/frontend-repro-investigator/scripts/test_har_analyzer.py
  templates/workspace/skills/frontend-repro-investigator/scripts/test_console_analyzer.py
  templates/workspace/skills/frontend-repro-investigator/scripts/test_sentry_fetch.py
  templates/workspace/skills/frontend-repro-investigator/scripts/test_browser_collect.py
  templates/workspace/skills/frontend-repro-investigator/scripts/test_evidence_merge.py
  templates/workspace/skills/config-executor/scripts/test_kuboard_config.py
)

for test_file in "${stdlib_tests[@]}"; do
  echo "▶ python3 ${test_file}"
  python3 "${test_file}"
done

echo "▶ python3 -m pytest incident/recent script tests"
python3 -m pytest \
  templates/workspace/skills/incident-investigator/scripts/test_cascade_check.py \
  templates/workspace/skills/recent-changes/scripts/test_timeline.py \
  -q

echo "✓ workspace skill script tests passed"
