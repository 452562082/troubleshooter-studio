#!/usr/bin/env python3
import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Dict, Optional

DEFAULT_TIMEOUT = 10
SENSITIVE_KEYS = [
    "password", "passwd", "pwd", "secret", "token", "accesskey",
    "secretkey", "privatekey", "jdbc.password", "ak", "sk"
]


def ok(action: str, **kwargs: Any) -> Dict[str, Any]:
    return {"ok": True, "action": action, **kwargs}


def err(action: str, code: str, message: str) -> Dict[str, Any]:
    return {"ok": False, "action": action, "error": {"code": code, "message": message}}


def print_json(payload: Dict[str, Any]) -> None:
    print(json.dumps(payload, ensure_ascii=False, indent=2))


def normalize_server(server: str) -> str:
    return server.rstrip("/")


def build_base(server: str) -> str:
    server = normalize_server(server)
    if server.endswith("/nacos"):
        return server
    return server + "/nacos"


def detect_content_type(data_id: str, content: str) -> str:
    lower = data_id.lower()
    if lower.endswith((".yaml", ".yml")):
        return "yaml"
    if lower.endswith(".json"):
        return "json"
    if lower.endswith(".properties"):
        return "properties"
    if content.strip().startswith("{") and content.strip().endswith("}"):
        return "json"
    return "text"


def mask_sensitive(content: str) -> str:
    lines = []
    for line in content.splitlines():
        masked = line
        if '=' in line:
            key, value = line.split('=', 1)
            if is_sensitive_key(key):
                masked = f"{key}=******"
        elif ':' in line:
            key, value = line.split(':', 1)
            if is_sensitive_key(key):
                masked = f"{key}: ******"
        lines.append(masked)
    return "\n".join(lines)


def is_sensitive_key(key: str) -> bool:
    norm = re.sub(r"\s+", "", key).lower()
    return any(s in norm for s in SENSITIVE_KEYS)


def request_json_or_text(url: str, params: Dict[str, Any], timeout: int) -> Any:
    full_url = url + "?" + urllib.parse.urlencode({k: v for k, v in params.items() if v is not None})
    req = urllib.request.Request(full_url)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        body = resp.read().decode("utf-8", errors="replace")
        ctype = resp.headers.get("Content-Type", "")
        if "application/json" in ctype:
            return json.loads(body)
        return body


def login(base: str, username: Optional[str], password: Optional[str], timeout: int) -> Optional[str]:
    if not username or not password:
        return None
    url = base + "/v1/auth/login"
    data = urllib.parse.urlencode({"username": username, "password": password}).encode()
    req = urllib.request.Request(url, data=data, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8", errors="replace")
            parsed = json.loads(body)
            return parsed.get("accessToken") or parsed.get("access_token")
    except Exception:
        return None


def common_auth_params(args: argparse.Namespace, token: Optional[str]) -> Dict[str, Any]:
    params: Dict[str, Any] = {}
    if token:
        params["accessToken"] = token
    elif args.access_token:
        params["accessToken"] = args.access_token
    return params


def namespace_params(namespace: Optional[str]) -> Dict[str, Any]:
    if not namespace:
        return {}
    return {"tenant": namespace}


def action_ping(args: argparse.Namespace) -> Dict[str, Any]:
    base = build_base(args.server)
    token = login(base, args.username, args.password, args.timeout)
    params = common_auth_params(args, token)
    params.update(namespace_params(args.namespace))
    url = base + "/v1/console/namespaces"
    try:
        data = request_json_or_text(url, params, args.timeout)
        return ok("ping", server=args.server, reachable=True, auth=("ok" if token or args.access_token or not args.username else "unknown"), namespace=args.namespace, responseType=type(data).__name__)
    except urllib.error.HTTPError as e:
        return err("ping", "HTTP_ERROR", f"HTTP {e.code}")
    except Exception as e:
        return err("ping", "NETWORK_ERROR", str(e))


def action_get(args: argparse.Namespace) -> Dict[str, Any]:
    if not args.group or not args.data_id:
        return err("get", "INVALID_ARGS", "group and data-id are required")
    base = build_base(args.server)
    token = login(base, args.username, args.password, args.timeout)
    params = {
        "dataId": args.data_id,
        "group": args.group,
        **namespace_params(args.namespace),
        **common_auth_params(args, token),
    }
    url = base + "/v1/cs/configs"
    try:
        content = request_json_or_text(url, params, args.timeout)
        if not isinstance(content, str):
            return ok("get", server=args.server, namespace=args.namespace, group=args.group, dataId=args.data_id, found=True, raw=content)
        masked = mask_sensitive(content) if args.mask_secrets else content
        return ok("get", server=args.server, namespace=args.namespace, group=args.group, dataId=args.data_id, found=True, content=masked, contentType=detect_content_type(args.data_id, content))
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return ok("get", server=args.server, namespace=args.namespace, group=args.group, dataId=args.data_id, found=False)
        return err("get", "HTTP_ERROR", f"HTTP {e.code}")
    except Exception as e:
        return err("get", "API_ERROR", str(e))


def action_search(args: argparse.Namespace) -> Dict[str, Any]:
    base = build_base(args.server)
    token = login(base, args.username, args.password, args.timeout)
    params = {
        "search": "blur",
        "dataId": args.query,
        "group": args.group,
        "pageNo": args.page,
        "pageSize": args.page_size,
        **namespace_params(args.namespace),
        **common_auth_params(args, token),
    }
    url = base + "/v1/cs/configs"
    try:
        data = request_json_or_text(url, params, args.timeout)
        return ok("search", server=args.server, namespace=args.namespace, group=args.group, query=args.query, result=data)
    except Exception as e:
        return err("search", "API_ERROR", str(e))


def action_history(args: argparse.Namespace) -> Dict[str, Any]:
    if not args.group or not args.data_id:
        return err("history", "INVALID_ARGS", "group and data-id are required")
    base = build_base(args.server)
    token = login(base, args.username, args.password, args.timeout)
    params = {
        "dataId": args.data_id,
        "group": args.group,
        "pageNo": 1,
        "pageSize": args.limit,
        **namespace_params(args.namespace),
        **common_auth_params(args, token),
    }
    url = base + "/v1/cs/history"
    try:
        data = request_json_or_text(url, params, args.timeout)
        return ok("history", server=args.server, namespace=args.namespace, group=args.group, dataId=args.data_id, result=data)
    except Exception as e:
        return err("history", "API_ERROR", str(e))


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Read-only Nacos config helper")
    sub = parser.add_subparsers(dest="command", required=True)

    def add_common(p: argparse.ArgumentParser) -> None:
        p.add_argument("--server", required=True)
        p.add_argument("--username")
        p.add_argument("--password", default=os.environ.get("NACOS_PASSWORD"))
        p.add_argument("--access-token")
        p.add_argument("--namespace")
        p.add_argument("--group")
        p.add_argument("--data-id")
        p.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT)
        p.add_argument("--mask-secrets", dest="mask_secrets", action="store_true", default=True)
        p.add_argument("--no-mask-secrets", dest="mask_secrets", action="store_false")

    p_ping = sub.add_parser("ping")
    add_common(p_ping)

    p_get = sub.add_parser("get")
    add_common(p_get)

    p_search = sub.add_parser("search")
    add_common(p_search)
    p_search.add_argument("--query", required=True)
    p_search.add_argument("--page", type=int, default=1)
    p_search.add_argument("--page-size", type=int, default=20)

    p_history = sub.add_parser("history")
    add_common(p_history)
    p_history.add_argument("--limit", type=int, default=20)

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    if args.command == "ping":
        payload = action_ping(args)
    elif args.command == "get":
        payload = action_get(args)
    elif args.command == "search":
        payload = action_search(args)
    elif args.command == "history":
        payload = action_history(args)
    else:
        payload = err("unknown", "INVALID_ARGS", f"unsupported command: {args.command}")
    print_json(payload)
    return 0 if payload.get("ok") else 1


if __name__ == "__main__":
    sys.exit(main())
