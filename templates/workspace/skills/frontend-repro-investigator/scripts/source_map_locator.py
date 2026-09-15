#!/usr/bin/env python3
"""Resolve a generated 1-based line/column through a local Source Map v3 file."""

import argparse
import json
import os
import sys
from typing import Any

MAX_MAP_BYTES = 16 * 1024 * 1024
BASE64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
BASE64_VALUES = {char: index for index, char in enumerate(BASE64)}


class SourceMapError(ValueError):
    pass


def decode_vlq(segment: str) -> list[int]:
    values: list[int] = []
    value = 0
    shift = 0
    for char in segment:
        digit = BASE64_VALUES.get(char)
        if digit is None:
            raise SourceMapError("mappings contains an invalid base64 VLQ character")
        continuation = digit & 32
        digit &= 31
        value += digit << shift
        if continuation:
            shift += 5
            if shift > 30:
                raise SourceMapError("mappings contains an oversized VLQ value")
            continue
        negative = value & 1
        decoded = value >> 1
        values.append(-decoded if negative else decoded)
        value = 0
        shift = 0
    if shift:
        raise SourceMapError("mappings contains an incomplete VLQ value")
    return values


def load_source_map(path: str) -> dict[str, Any]:
    size = os.path.getsize(path)
    if size > MAX_MAP_BYTES:
        raise SourceMapError("source map exceeds 16 MiB")
    with open(path, "r", encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict) or value.get("version") != 3:
        raise SourceMapError("source map version must be 3")
    if not isinstance(value.get("mappings"), str) or not isinstance(value.get("sources"), list):
        raise SourceMapError("source map requires mappings and sources")
    if value.get("sections") is not None:
        raise SourceMapError("indexed source maps with sections are not supported")
    return value


def locate(source_map: dict[str, Any], line: int, column: int) -> dict[str, Any]:
    if line < 1 or column < 1:
        raise SourceMapError("line and column must be 1-based positive integers")
    mapping_lines = source_map["mappings"].split(";")
    target_line = line - 1
    target_column = column - 1
    if target_line >= len(mapping_lines):
        raise SourceMapError("generated line is outside the source map")

    source_index = 0
    original_line = 0
    original_column = 0
    name_index = 0
    selected: dict[str, int] | None = None

    for generated_line, encoded_line in enumerate(mapping_lines[: target_line + 1]):
        generated_column = 0
        for encoded_segment in encoded_line.split(",") if encoded_line else []:
            fields = decode_vlq(encoded_segment)
            if not fields:
                continue
            generated_column += fields[0]
            if len(fields) == 1:
                continue
            if len(fields) not in (4, 5):
                raise SourceMapError("mapped segment must contain 4 or 5 fields")
            source_index += fields[1]
            original_line += fields[2]
            original_column += fields[3]
            if len(fields) == 5:
                name_index += fields[4]
            if generated_line == target_line and generated_column <= target_column:
                selected = {
                    "generated_column": generated_column,
                    "source_index": source_index,
                    "original_line": original_line,
                    "original_column": original_column,
                    "name_index": name_index if len(fields) == 5 else -1,
                }
        if generated_line == target_line:
            break

    if selected is None:
        raise SourceMapError("no mapped segment covers the generated position")
    sources = source_map["sources"]
    index = selected["source_index"]
    if index < 0 or index >= len(sources) or not isinstance(sources[index], str):
        raise SourceMapError("mapped source index is invalid")
    names = source_map.get("names") if isinstance(source_map.get("names"), list) else []
    mapped_name = ""
    if selected["name_index"] >= 0:
        if selected["name_index"] >= len(names) or not isinstance(names[selected["name_index"]], str):
            raise SourceMapError("mapped name index is invalid")
        mapped_name = names[selected["name_index"]]
    sources_content = source_map.get("sourcesContent")
    has_source_content = isinstance(sources_content, list) and index < len(sources_content) and isinstance(sources_content[index], str)
    return {
        "generated": {"line": line, "column": column, "segment_column": selected["generated_column"] + 1},
        "original": {
            "source": sources[index],
            "source_root": source_map.get("sourceRoot", "") if isinstance(source_map.get("sourceRoot", ""), str) else "",
            "line": selected["original_line"] + 1,
            "column": selected["original_column"] + 1,
            "name": mapped_name,
            "has_source_content": has_source_content,
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--map", required=True, dest="map_path", help="local Source Map v3 file")
    parser.add_argument("--line", required=True, type=int, help="generated 1-based line")
    parser.add_argument("--column", required=True, type=int, help="generated 1-based column")
    args = parser.parse_args()
    try:
        result = locate(load_source_map(args.map_path), args.line, args.column)
    except (OSError, json.JSONDecodeError, SourceMapError) as error:
        print(json.dumps({"status": "error", "error": str(error)}, ensure_ascii=False))
        return 2
    print(json.dumps({"status": "mapped", **result}, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
