#!/usr/bin/env python3
"""通过 Consul KV HTTP API 读取配置（不依赖 MCP）

凭证来源：~/.openclaw/<agent-id>-creds.json
{
  "consul": {
    "<env>": {"host": "http://consul-dev:8500", "token": "xxx"}
  }
}

示例:
  python3 consul_config.py get --agent-id iot-troubleshooter --env prod --key config/device-gateway/data
  python3 consul_config.py list --agent-id iot-troubleshooter --env prod --prefix config/
"""
import argparse
import base64
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


def load_creds(agent_id: str, backend: str, env: str) -> dict:
    path = os.path.expanduser(f"~/.openclaw/{agent_id}-creds.json")
    if not os.path.isfile(path):
        raise FileNotFoundError(f"creds file missing: {path}；请先跑 install.sh 或手工补齐")
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    env_data = data.get(backend, {}).get(env)
    if not env_data:
        raise ValueError(f"creds missing {backend}.{env}")
    return env_data


def http_get(url: str, token: str | None, timeout: int = 10) -> bytes:
    req = urllib.request.Request(url)
    if token:
        req.add_header("X-Consul-Token", token)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.read()
    except urllib.error.HTTPError as e:
        raise RuntimeError(f"HTTP {e.code} {e.reason}: {url}") from e
    except urllib.error.URLError as e:
        raise RuntimeError(f"URL error: {e.reason}: {url}") from e


def _base_url(args: argparse.Namespace) -> tuple[str, str | None]:
    creds = load_creds(args.agent_id, "consul", args.env)
    host = (args.host or creds.get("host", "")).rstrip("/")
    if host and not host.startswith("http"):
        host = "http://" + host
    if not host:
        raise ValueError("host missing in CLI and creds")
    token = args.token or creds.get("token")
    return host, token


def cmd_get(args: argparse.Namespace) -> int:
    host, token = _base_url(args)
    key = args.key.lstrip("/")
    url = f"{host}/v1/kv/{urllib.parse.quote(key)}"
    data = http_get(url, token)
    items = json.loads(data.decode())
    # Consul KV 返回 [{Key, Value(base64), ...}]；解 base64 便于直接阅读
    for it in items:
        if it.get("Value"):
            try:
                it["Value_decoded"] = base64.b64decode(it["Value"]).decode("utf-8", errors="replace")
            except Exception:
                pass
    print(json.dumps(items, ensure_ascii=False, indent=2))
    return 0


def cmd_list(args: argparse.Namespace) -> int:
    host, token = _base_url(args)
    prefix = args.prefix.lstrip("/")
    url = f"{host}/v1/kv/{urllib.parse.quote(prefix)}?keys=true"
    data = http_get(url, token)
    print(json.dumps(json.loads(data.decode()), ensure_ascii=False, indent=2))
    return 0


def main() -> int:
    p = argparse.ArgumentParser(description="Consul KV HTTP API 客户端")
    p.add_argument("--agent-id", required=True)
    p.add_argument("--env", required=True)
    p.add_argument("--host", help="覆盖 creds 的 host")
    p.add_argument("--token", help="覆盖 creds 的 token")
    sub = p.add_subparsers(dest="cmd", required=True)

    g = sub.add_parser("get", help="读取单个 KV")
    g.add_argument("--key", required=True)
    g.set_defaults(func=cmd_get)

    ls = sub.add_parser("list", help="列出指定 prefix 下的所有 key")
    ls.add_argument("--prefix", default="")
    ls.set_defaults(func=cmd_list)

    args = p.parse_args()
    try:
        return args.func(args)
    except (FileNotFoundError, ValueError, RuntimeError) as e:
        print(f"[error] {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
