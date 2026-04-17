#!/usr/bin/env python3
import json
import sys

"""
从 resolve_runtime_from_nacos.py 输出中提取 Redis 运行时信息。
输入: stdin JSON
输出: JSON {host, port, resolved}
"""


def main() -> int:
    raw = sys.stdin.read().strip()
    if not raw:
        print('{"ok":false,"error":"empty input"}')
        return 1

    data = json.loads(raw)
    runtime = data.get("runtime", {})
    redis = runtime.get("redis", {})

    host = (redis.get("host") or "").strip()
    port = redis.get("port")
    if not isinstance(port, int):
        port = 6379

    out = {
        "ok": bool(host),
        "redis": {
            "host": host,
            "port": port,
            "resolved": bool(host),
        },
    }
    print(json.dumps(out, ensure_ascii=False))
    return 0 if host else 2


if __name__ == "__main__":
    raise SystemExit(main())
