#!/usr/bin/env python3
"""通过 Apollo Open API 读取配置（HTTP 直连，不依赖 MCP）

凭证来源：~/.openclaw/<agent-id>-creds.json
{
  "apollo": {
    "<env>": {"meta_url": "http://apollo-dev:8080", "token": "xxx"}
  }
}

示例:
  python3 apollo_config.py get --agent-id shop-troubleshooter --env dev \
      --app-id account-service --cluster default --namespace application.yaml

输出为 JSON；失败时 stderr 打印错误 + 非零退出码。
"""
import argparse
import json
import os
import sys
import urllib.error
import urllib.request


def load_creds(agent_id: str, backend: str, env: str) -> dict:
    path = os.path.expanduser(f"~/.openclaw/{agent_id}-creds.json")
    if not os.path.isfile(path):
        raise FileNotFoundError(f"creds file missing: {path}；请先跑 install.sh 或手工补齐")
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    backend_data = data.get(backend, {})
    env_data = backend_data.get(env)
    if not env_data:
        raise ValueError(f"creds missing {backend}.{env}；已有: {list(backend_data.keys())}")
    return env_data


def http_get(url: str, token: str | None = None, timeout: int = 10) -> bytes:
    req = urllib.request.Request(url)
    if token:
        req.add_header("Authorization", token)
    req.add_header("Accept", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.read()
    except urllib.error.HTTPError as e:
        raise RuntimeError(f"HTTP {e.code} {e.reason}: {url}") from e
    except urllib.error.URLError as e:
        raise RuntimeError(f"URL error: {e.reason}: {url}") from e


def cmd_get(args: argparse.Namespace) -> int:
    creds = load_creds(args.agent_id, "apollo", args.env)
    meta = (args.meta_url or creds.get("meta_url", "")).rstrip("/")
    token = args.token or creds.get("token")
    if not meta:
        print("[error] meta_url missing (CLI 或 creds 均无)", file=sys.stderr)
        return 2
    # Apollo env 标签在 URL 中通常是大写（DEV/PROD/FAT/UAT）
    env_label = (args.env_label or args.env).upper()
    url = f"{meta}/openapi/v1/envs/{env_label}/apps/{args.app_id}/clusters/{args.cluster}/namespaces/{args.namespace}"
    data = http_get(url, token)
    # Apollo 返回 JSON；直接透传
    parsed = json.loads(data.decode())
    print(json.dumps(parsed, ensure_ascii=False, indent=2))
    return 0


def cmd_list_namespaces(args: argparse.Namespace) -> int:
    creds = load_creds(args.agent_id, "apollo", args.env)
    meta = (args.meta_url or creds.get("meta_url", "")).rstrip("/")
    token = args.token or creds.get("token")
    if not meta:
        print("[error] meta_url missing", file=sys.stderr)
        return 2
    env_label = (args.env_label or args.env).upper()
    url = f"{meta}/openapi/v1/envs/{env_label}/apps/{args.app_id}/clusters/{args.cluster}/namespaces"
    data = http_get(url, token)
    print(json.dumps(json.loads(data.decode()), ensure_ascii=False, indent=2))
    return 0


def main() -> int:
    p = argparse.ArgumentParser(description="Apollo Open API 客户端（config-executor 脚本）")
    p.add_argument("--agent-id", required=True, help="对应 creds.json 文件名前缀")
    p.add_argument("--env", required=True, help="环境 id（dev/prod/...）")
    p.add_argument("--env-label", help="Apollo 侧的 env 标签（若与 --env 不同，如 DEV/FAT/UAT）")
    p.add_argument("--meta-url", help="覆盖 creds 的 meta URL")
    p.add_argument("--token", help="覆盖 creds 的 token")
    sub = p.add_subparsers(dest="cmd", required=True)

    g = sub.add_parser("get", help="读取一个 namespace 的配置项")
    g.add_argument("--app-id", required=True)
    g.add_argument("--cluster", default="default")
    g.add_argument("--namespace", required=True)
    g.set_defaults(func=cmd_get)

    ln = sub.add_parser("list-namespaces", help="列出某 cluster 下所有 namespace")
    ln.add_argument("--app-id", required=True)
    ln.add_argument("--cluster", default="default")
    ln.set_defaults(func=cmd_list_namespaces)

    args = p.parse_args()
    try:
        return args.func(args)
    except (FileNotFoundError, ValueError, RuntimeError) as e:
        print(f"[error] {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
