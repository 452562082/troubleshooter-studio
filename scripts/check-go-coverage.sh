#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

out_file="$(mktemp)"
trap 'rm -f "$out_file"' EXIT

go test -cover ./... 2>&1 | tee "$out_file"

thresholds='
github.com/xiaolong/troubleshooter-studio/api 50
github.com/xiaolong/troubleshooter-studio/internal/agent 60
github.com/xiaolong/troubleshooter-studio/internal/generator 60
github.com/xiaolong/troubleshooter-studio/internal/deploy 80
github.com/xiaolong/troubleshooter-studio/internal/doctor 70
'

failed=0
while read -r pkg min; do
  [ -z "${pkg:-}" ] && continue
  line="$(awk -v p="$pkg" '$1 == "ok" && $2 == p { last = $0 } END { print last }' "$out_file")"
  if [ -z "$line" ]; then
    echo "coverage gate: missing package output for $pkg" >&2
    failed=1
    continue
  fi
  coverage="$(printf '%s\n' "$line" | sed -n 's/.*coverage: \([0-9][0-9.]*\)% of statements.*/\1/p')"
  if [ -z "$coverage" ]; then
    echo "coverage gate: missing coverage value for $pkg" >&2
    failed=1
    continue
  fi
  if awk -v got="$coverage" -v want="$min" 'BEGIN { exit !(got + 0 < want + 0) }'; then
    echo "coverage gate: FAIL $pkg ${coverage}% < ${min}%" >&2
    failed=1
  else
    echo "coverage gate: PASS $pkg ${coverage}% >= ${min}%"
  fi
done <<< "$thresholds"

exit "$failed"
