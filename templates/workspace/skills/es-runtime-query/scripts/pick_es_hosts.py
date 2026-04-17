#!/usr/bin/env python3
import json
import sys

"""
从 resolve_runtime_from_nacos.py 输出中提取 ES hosts。
输入: stdin JSON
输出: JSON {hosts, es_url, resolved}
"""


def main() -> int:
    raw = sys.stdin.read().strip()
    if not raw:
        print('{"ok":false,"error":"empty input"}')
        return 1

    data = json.loads(raw)
    runtime = data.get("runtime", {})
    es = runtime.get("elasticsearch", {})
    hosts = es.get("hosts") or []
    hosts = [h.strip() for h in hosts if isinstance(h, str) and h.strip()]

    if not hosts:
        print('{"ok":false,"error":"es hosts not found"}')
        return 2

    first = hosts[0]
    if first.startswith("http://") or first.startswith("https://"):
        es_url = first
    else:
        es_url = f"http://{first}"

    out = {
        "ok": True,
        "elasticsearch": {
            "hosts": hosts,
            "es_url": es_url,
            "resolved": True,
        },
    }
    print(json.dumps(out, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
