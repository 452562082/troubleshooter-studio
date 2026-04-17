#!/usr/bin/env python3
import json
import sys

"""
从 resolve_runtime_from_nacos.py 的输出中提取 Mongo 连接串。
输入：stdin JSON
输出：可直接用于 mcp-mongo-server 的 Mongo URI
"""

def main() -> int:
    raw = sys.stdin.read().strip()
    if not raw:
        print("", end="")
        return 1
    data = json.loads(raw)
    runtime = data.get("runtime", {})
    mongo = runtime.get("mongo", {})

    uri = (mongo.get("uri") or "").strip()
    hosts = (mongo.get("hosts") or "").strip()

    if uri:
        print(uri)
        return 0
    if hosts:
        # 兜底：仅有 hosts 时给默认协议
        print(f"mongodb://{hosts}")
        return 0

    return 2

if __name__ == "__main__":
    raise SystemExit(main())
