#!/usr/bin/env python3
"""Build a JSON manifest for local bug attachment evidence files."""

from __future__ import annotations

import argparse
import hashlib
import json
import mimetypes
import os
import struct
from pathlib import Path
from typing import Any


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def image_dimensions(path: Path) -> tuple[int, int] | None:
    with path.open("rb") as f:
        head = f.read(32)
        if head.startswith(b"\x89PNG\r\n\x1a\n") and len(head) >= 24:
            width, height = struct.unpack(">II", head[16:24])
            return int(width), int(height)
        if head[:6] in (b"GIF87a", b"GIF89a") and len(head) >= 10:
            width, height = struct.unpack("<HH", head[6:10])
            return int(width), int(height)
        if head.startswith(b"\xff\xd8"):
            f.seek(2)
            while True:
                marker_prefix = f.read(1)
                if not marker_prefix:
                    return None
                if marker_prefix != b"\xff":
                    continue
                marker = f.read(1)
                while marker == b"\xff":
                    marker = f.read(1)
                if marker in (b"\xd8", b"\xd9"):
                    continue
                size_data = f.read(2)
                if len(size_data) != 2:
                    return None
                segment_size = struct.unpack(">H", size_data)[0]
                if segment_size < 2:
                    return None
                if marker in {bytes([m]) for m in range(0xC0, 0xC4)} | {bytes([m]) for m in range(0xC5, 0xC8)} | {bytes([m]) for m in range(0xC9, 0xCC)} | {bytes([m]) for m in range(0xCD, 0xD0)}:
                    data = f.read(5)
                    if len(data) != 5:
                        return None
                    height, width = struct.unpack(">HH", data[1:5])
                    return int(width), int(height)
                f.seek(segment_size - 2, os.SEEK_CUR)
    return None


def classify(path: Path, mime: str) -> str:
    lower = path.name.lower()
    if mime.startswith("image/"):
        return "screenshot"
    if mime.startswith("video/"):
        return "recording"
    if lower.endswith(".har"):
        return "har"
    if lower.endswith((".log", ".txt")):
        return "text"
    if lower.endswith((".json", ".ndjson")):
        return "text"
    return "other"


def inspect_file(path: Path) -> dict[str, Any]:
    item: dict[str, Any] = {
        "path": str(path),
        "filename": path.name,
    }
    try:
        st = path.stat()
        mime = mimetypes.guess_type(path.name)[0] or "application/octet-stream"
        dims = image_dimensions(path) if mime.startswith("image/") else None
        item.update(
            {
                "exists": True,
                "size_bytes": st.st_size,
                "mime": mime,
                "type": classify(path, mime),
                "sha256": sha256_file(path),
                "dimensions": {"width": dims[0], "height": dims[1]} if dims else None,
            }
        )
    except Exception as exc:  # noqa: BLE001 - manifest should record per-file failures.
        item.update({"exists": path.exists(), "error": str(exc)})
    return item


def expand_inputs(files: list[str], dirs: list[str]) -> list[Path]:
    out: list[Path] = []
    for f in files:
        out.append(Path(f).expanduser())
    for d in dirs:
        root = Path(d).expanduser()
        if root.is_dir():
            for p in sorted(root.iterdir()):
                if p.is_file():
                    out.append(p)
        else:
            out.append(root)
    seen: set[str] = set()
    unique: list[Path] = []
    for p in out:
        key = str(p)
        if key not in seen:
            seen.add(key)
            unique.append(p)
    return unique


def main() -> int:
    parser = argparse.ArgumentParser(description="Build local attachment evidence manifest.")
    parser.add_argument("--file", action="append", default=[], help="Attachment file path. Repeatable.")
    parser.add_argument("--dir", action="append", default=[], help="Directory containing attachment files. Repeatable.")
    args = parser.parse_args()
    paths = expand_inputs(args.file, args.dir)
    manifest = {"artifacts": [inspect_file(p) for p in paths]}
    print(json.dumps(manifest, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
