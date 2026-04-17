#!/usr/bin/env python3
import argparse
import difflib
import json
import sys
from typing import List


def build_summary(left: str, right: str) -> List[str]:
    if left == right:
        return ["no differences"]
    summary = []
    left_lines = left.splitlines()
    right_lines = right.splitlines()
    removed = sum(1 for line in difflib.ndiff(left_lines, right_lines) if line.startswith('- '))
    added = sum(1 for line in difflib.ndiff(left_lines, right_lines) if line.startswith('+ '))
    summary.append(f"lines removed: {removed}")
    summary.append(f"lines added: {added}")
    return summary


def main() -> int:
    parser = argparse.ArgumentParser(description="Diff two Nacos config texts")
    parser.add_argument("--left-file")
    parser.add_argument("--right-file")
    parser.add_argument("--left-text")
    parser.add_argument("--right-text")
    args = parser.parse_args()

    try:
        if args.left_file:
            with open(args.left_file, "r", encoding="utf-8") as f:
                left = f.read()
        elif args.left_text is not None:
            left = args.left_text
        else:
            raise ValueError("missing left input")

        if args.right_file:
            with open(args.right_file, "r", encoding="utf-8") as f:
                right = f.read()
        elif args.right_text is not None:
            right = args.right_text
        else:
            raise ValueError("missing right input")

        diff = "".join(
            difflib.unified_diff(
                left.splitlines(keepends=True),
                right.splitlines(keepends=True),
                fromfile="left",
                tofile="right",
            )
        )
        print(json.dumps({
            "ok": True,
            "same": left == right,
            "summary": build_summary(left, right),
            "diff": diff,
        }, ensure_ascii=False, indent=2))
        return 0
    except Exception as e:
        print(json.dumps({
            "ok": False,
            "error": {
                "code": "DIFF_ERROR",
                "message": str(e),
            }
        }, ensure_ascii=False, indent=2))
        return 1


if __name__ == "__main__":
    sys.exit(main())
