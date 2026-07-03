#!/usr/bin/env python3
"""通过 Kuboard v4 HTTP API 读取 K8s ConfigMap（不依赖 MCP / kubectl）。

凭证来源：
  1. CLI --url / --access-key / --username / --password
  2. ~/.openclaw/<agent-id>-creds.json
  3. ~/.tshoot/<agent-id>-creds.json

支持两种 creds 结构：
  {"kuboard":{"dev":{"url":"...","access_key":"..."}}}
  {"kuboard":{"default":{"dev":{"url":"...","access_key":"..."}}}}

示例：
  python3 kuboard_config.py get --agent-id shop --env dev \
    --cluster dev-cluster --namespace default --configmap app-config
"""

from __future__ import annotations

import argparse
import json
import os
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request


def error_out(msg: str, hint: str = "", code: int = 2) -> int:
    payload = {"error": msg}
    if hint:
        payload["hint"] = hint
    print(json.dumps(payload, ensure_ascii=False, indent=2))
    return code


def find_creds_file(agent_id: str) -> str:
    paths = [
        os.path.expanduser(f"~/.openclaw/{agent_id}-creds.json"),
        os.path.expanduser(f"~/.tshoot/{agent_id}-creds.json"),
    ]
    for p in paths:
        if os.path.isfile(p):
            return p
    raise FileNotFoundError(
        "creds file not found in any of: "
        f"~/.openclaw/{agent_id}-creds.json, ~/.tshoot/{agent_id}-creds.json"
    )


def load_creds(agent_id: str, env: str, source_id: str = "") -> dict:
    path = find_creds_file(agent_id)
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    kbroot = data.get("kuboard") or {}

    row = None
    if isinstance(kbroot.get(env), dict):
        row = kbroot.get(env)
    else:
        candidates = []
        if source_id:
            candidates.append(source_id)
        candidates.extend(["default", "kuboard"])
        candidates.extend([k for k in kbroot.keys() if k not in candidates])
        for sid in candidates:
            by_source = kbroot.get(sid)
            if isinstance(by_source, dict) and isinstance(by_source.get(env), dict):
                row = by_source.get(env)
                break

    if not row:
        raise ValueError(f"creds missing kuboard.{env} (in {path})")
    return {
        "url": row.get("url", ""),
        "access_key": row.get("access_key", ""),
        "username": row.get("username", ""),
        "password": row.get("password", ""),
    }


def http_json(url: str, headers: dict[str, str], timeout: int = 15) -> dict:
    req = urllib.request.Request(url, headers=headers)
    ctx = ssl.create_default_context()
    if os.environ.get("TSHOOT_INSECURE_TLS") == "1":
        ctx = ssl._create_unverified_context()
    opener = urllib.request.build_opener(
        urllib.request.ProxyHandler({}),
        urllib.request.HTTPSHandler(context=ctx),
    )
    try:
        with opener.open(req, timeout=timeout) as r:
            raw = r.read().decode("utf-8", errors="replace")
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {e.code}: {body[:300]}") from e
    except urllib.error.URLError as e:
        raise RuntimeError(f"URL error: {e.reason}") from e
    try:
        return json.loads(raw)
    except Exception as e:
        raise RuntimeError(f"bad JSON response: {raw[:300]}") from e


class HTTPStatusError(RuntimeError):
    def __init__(self, code: int, body: str):
        super().__init__(f"HTTP {code}: {body[:300]}")
        self.code = code
        self.body = body


def http_json_allow_status(url: str, headers: dict[str, str], timeout: int = 15) -> dict:
    try:
        return http_json(url, headers, timeout=timeout)
    except RuntimeError as e:
        msg = str(e)
        if msg.startswith("HTTP "):
            try:
                code = int(msg.split(" ", 2)[1].rstrip(":"))
            except Exception:
                raise
            raise HTTPStatusError(code, msg.split(": ", 1)[1] if ": " in msg else "") from e
        raise


def kuboard_login(base: str, username: str, password: str) -> str:
    url = base + "/api/login.kuboard.cn/v4/login"
    body = json.dumps({"username": username, "password": password}).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=body,
        headers={"Content-Type": "application/json", "Accept": "application/json"},
        method="POST",
    )
    ctx = ssl.create_default_context()
    if os.environ.get("TSHOOT_INSECURE_TLS") == "1":
        ctx = ssl._create_unverified_context()
    opener = urllib.request.build_opener(
        urllib.request.ProxyHandler({}),
        urllib.request.HTTPSHandler(context=ctx),
    )
    try:
        with opener.open(req, timeout=15) as r:
            payload = json.loads(r.read().decode("utf-8", errors="replace"))
    except urllib.error.HTTPError as e:
        raise RuntimeError(f"login HTTP {e.code}: {e.read().decode('utf-8', errors='replace')[:200]}") from e
    token = ((payload.get("data") or {}).get("accessToken") or "").strip()
    if not token:
        raise RuntimeError("login response missing data.accessToken")
    return token


def resolve_cluster_uid(base: str, token: str, cluster_name: str) -> str:
    url = (
        base
        + "/api/cluster.kuboard.cn/v4/cluster-cache/cluster-namespace-tree"
        + "?apiGroupName=&resource=configmaps&namespaced=true"
    )
    payload = http_json_allow_status(url, {"Kb-Access-Key": token, "Accept": "application/json"})
    items = ((payload.get("data") or {}).get("treeItems") or [])
    for it in items:
        if it.get("name") == cluster_name:
            uid = (it.get("id") or "").strip()
            if uid:
                return uid
    raise RuntimeError(f"cluster {cluster_name!r} not found in Kuboard tree")


def fetch_configmap_data(base: str, token: str, cluster_uid: str, namespace: str, name: str) -> dict[str, str]:
    query = urllib.parse.urlencode({
        "clusterId": cluster_uid,
        "apiVersion": "v1",
        "resource": "configmaps",
        "namespace": namespace,
        "name": name,
    })
    url = base + "/api/cluster.kuboard.cn/v4/cluster-cache/direct?" + query
    payload = http_json(url, {"Kb-Access-Key": token, "Accept": "application/json"})

    for it in ((payload.get("data") or {}).get("list") or []):
        cm = it.get("data") or it
        meta = cm.get("metadata") or {}
        if meta.get("name") == name:
            return cm.get("data") or {}
    single = (((payload.get("data") or {}).get("data") or {}).get("data") or {})
    if single:
        return single
    raise RuntimeError(f"configmap {namespace}/{name} not found or response shape unsupported")


def fetch_configmap_data_v3(base: str, username: str, access_key: str, cluster: str, namespace: str, name: str) -> dict[str, str]:
    if not username:
        raise RuntimeError("Kuboard v3 requires username for Cookie KuboardUsername")
    path = (
        f"/k8s-api/{urllib.parse.quote(cluster, safe='')}"
        f"/api/v1/namespaces/{urllib.parse.quote(namespace, safe='')}"
        f"/configmaps/{urllib.parse.quote(name, safe='')}"
    )
    url = base + path
    payload = http_json(url, {
        "Cookie": f"KuboardUsername={username}; KuboardAccessKey={access_key}",
        "Accept": "application/json",
    })
    data = payload.get("data")
    if isinstance(data, dict):
        return data
    raise RuntimeError(f"configmap {namespace}/{name} not found or response shape unsupported")


def should_try_kuboard_v3_from_tree_error(err: Exception, access_key: str) -> bool:
    if not access_key:
        return False
    if isinstance(err, HTTPStatusError):
        return True
    return str(err).startswith("bad JSON response:")


def first_value(data: dict[str, str], *keys: str) -> str:
    upper = {str(k).upper().replace(".", "_").replace("-", "_"): str(v) for k, v in data.items()}
    for key in keys:
        val = upper.get(key.upper().replace(".", "_").replace("-", "_"), "")
        if val:
            return val
    return ""


def int_or_none(value: str):
    return int(value) if str(value).isdigit() else None


def csv_values(value: str) -> list[str]:
    return [item.strip() for item in str(value).split(",") if item.strip()]


def parse_runtime_url(value: str) -> dict:
    raw = str(value or "").strip()
    if not raw:
        return {}
    if raw.startswith("jdbc:"):
        raw = raw[len("jdbc:"):]
    parsed = urllib.parse.urlparse(raw)
    if not parsed.scheme:
        parsed = urllib.parse.urlparse("//" + raw)
    return {
        "scheme": parsed.scheme.lower(),
        "host": parsed.hostname or "",
        "port": parsed.port,
        "database": urllib.parse.unquote(parsed.path.lstrip("/")),
        "user": urllib.parse.unquote(parsed.username or ""),
        "path": urllib.parse.unquote(parsed.path.lstrip("/")),
    }


def runtime_host_url(parsed: dict, default_port=None) -> str:
    host = parsed.get("host") or ""
    if not host:
        return ""
    port = parsed.get("port") or default_port
    scheme = parsed.get("scheme") or ""
    netloc = f"{host}:{port}" if port else host
    return f"{scheme}://{netloc}" if scheme else netloc


def first_nonempty(*values) -> str:
    for value in values:
        if value:
            return value
    return ""


def resolve_runtime(data: dict[str, str]) -> dict:
    redis_host = first_value(data, "REDIS_HOST", "SPRING_REDIS_HOST")
    redis_port = first_value(data, "REDIS_PORT", "SPRING_REDIS_PORT")
    redis_url = first_value(data, "REDIS_URL", "SPRING_REDIS_URL", "SPRING_DATA_REDIS_URL")
    redis_parsed = parse_runtime_url(redis_url)

    database_url = first_value(data, "DATABASE_URL", "SPRING_DATASOURCE_URL", "JDBC_DATABASE_URL")
    database_parsed = parse_runtime_url(database_url)
    datasource_user = first_value(data, "SPRING_DATASOURCE_USERNAME", "SPRING_DATASOURCE_USER", "DATASOURCE_USER")

    mysql_host = first_value(data, "MYSQL_HOST", "DB_HOST", "DATABASE_HOST")
    mysql_port = first_value(data, "MYSQL_PORT", "DB_PORT", "DATABASE_PORT")
    mysql_db = first_value(data, "MYSQL_DATABASE", "MYSQL_DB", "DB_DATABASE", "DATABASE_NAME")
    mysql_user = first_value(data, "MYSQL_USER", "DB_USERNAME", "DB_USER", "DATABASE_USER")
    postgres_host = first_value(data, "POSTGRES_HOST", "POSTGRESQL_HOST", "PG_HOST", "SPRING_DATASOURCE_HOST")
    postgres_port = first_value(data, "POSTGRES_PORT", "POSTGRESQL_PORT", "PG_PORT")
    postgres_db = first_value(data, "POSTGRES_DATABASE", "POSTGRES_DB", "POSTGRESQL_DATABASE", "POSTGRESQL_DB", "PGDATABASE")
    postgres_user = first_value(data, "POSTGRES_USER", "POSTGRESQL_USER", "PGUSER")
    mongo_uri = first_value(data, "MONGO_URI", "MONGODB_URI", "SPRING_DATA_MONGODB_URI")
    es_url = first_value(data, "ELASTICSEARCH_URL", "ES_URL", "ELASTICSEARCH_HOSTS", "ES_HOSTS")
    clickhouse_url = first_value(data, "CLICKHOUSE_URL", "CLICKHOUSE_HOSTS", "CH_URL", "CH_HOSTS", "CLICKHOUSE_DSN")
    clickhouse_parsed = parse_runtime_url(clickhouse_url) if "," not in clickhouse_url else {}
    clickhouse_host = first_value(data, "CLICKHOUSE_HOST", "CH_HOST")
    clickhouse_port = first_value(data, "CLICKHOUSE_PORT", "CH_PORT")
    kafka_servers = first_value(data, "KAFKA_BOOTSTRAP_SERVERS", "KAFKA_BROKERS", "SPRING_KAFKA_BOOTSTRAP_SERVERS")
    rabbitmq_host = first_value(data, "RABBITMQ_HOST", "SPRING_RABBITMQ_HOST")
    rabbitmq_port = first_value(data, "RABBITMQ_PORT", "SPRING_RABBITMQ_PORT")
    rabbitmq_vhost = first_value(data, "RABBITMQ_VHOST", "SPRING_RABBITMQ_VIRTUAL_HOST")
    rabbitmq_url = first_value(data, "RABBITMQ_URL", "AMQP_URL", "AMQPS_URL", "SPRING_RABBITMQ_URL")
    rabbitmq_parsed = parse_runtime_url(rabbitmq_url)

    if database_parsed.get("scheme") in ("mysql", "mariadb"):
        mysql_host = first_nonempty(mysql_host, database_parsed.get("host"))
        mysql_port = first_nonempty(mysql_port, str(database_parsed.get("port") or ""))
        mysql_db = first_nonempty(mysql_db, database_parsed.get("database"))
        mysql_user = first_nonempty(mysql_user, database_parsed.get("user"), datasource_user)
    if database_parsed.get("scheme") in ("postgres", "postgresql"):
        postgres_host = first_nonempty(postgres_host, database_parsed.get("host"))
        postgres_port = first_nonempty(postgres_port, str(database_parsed.get("port") or ""))
        postgres_db = first_nonempty(postgres_db, database_parsed.get("database"))
        postgres_user = first_nonempty(postgres_user, database_parsed.get("user"), datasource_user)

    redis_host = first_nonempty(redis_host, redis_parsed.get("host"))
    redis_port = first_nonempty(redis_port, str(redis_parsed.get("port") or ""))

    rabbitmq_host = first_nonempty(rabbitmq_host, rabbitmq_parsed.get("host"))
    rabbitmq_port = first_nonempty(rabbitmq_port, str(rabbitmq_parsed.get("port") or ""))
    rabbitmq_vhost = first_nonempty(rabbitmq_vhost, rabbitmq_parsed.get("path"))

    clickhouse_host_url = runtime_host_url(clickhouse_parsed)
    return {
        "redis": {
            "host": redis_host,
            "port": int_or_none(redis_port),
            "resolved": bool(redis_host),
        },
        "mysql": {
            "host": mysql_host,
            "port": int_or_none(mysql_port) or 3306,
            "database": mysql_db,
            "user": mysql_user,
            "resolved": bool(mysql_host),
        },
        "postgres": {
            "host": postgres_host,
            "port": int_or_none(postgres_port) or 5432,
            "database": postgres_db,
            "user": postgres_user,
            "resolved": bool(postgres_host),
        },
        "mongo": {
            "uri": mongo_uri,
            "resolved": bool(mongo_uri),
        },
        "elasticsearch": {
            "hosts": csv_values(es_url) if es_url else [],
            "resolved": bool(es_url),
        },
        "clickhouse": {
            "hosts": [clickhouse_host_url] if clickhouse_host_url else (csv_values(clickhouse_url) if clickhouse_url else ([f"{clickhouse_host}:{int_or_none(clickhouse_port) or 8123}"] if clickhouse_host else [])),
            "resolved": bool(clickhouse_url or clickhouse_host),
        },
        "kafka": {
            "bootstrap_servers": csv_values(kafka_servers),
            "resolved": bool(kafka_servers),
        },
        "rabbitmq": {
            "host": rabbitmq_host,
            "port": int_or_none(rabbitmq_port) or 5672,
            "vhost": rabbitmq_vhost,
            "resolved": bool(rabbitmq_host),
        },
    }


def cmd_get(args: argparse.Namespace) -> int:
    creds = load_creds(args.agent_id, args.env, args.source_id)
    base = (args.url or creds.get("url") or "").rstrip("/")
    access_key = args.access_key or creds.get("access_key") or ""
    username = args.username or creds.get("username") or ""
    password = args.password or creds.get("password") or ""
    if not base:
        return error_out("kuboard url missing", f"{args.env} 环境 Kuboard URL 为空,请补 KUBOARD_URL 或重跑 install.sh")
    if not base.startswith(("http://", "https://")):
        return error_out("bad kuboard url", f"Kuboard URL 必须以 http:// 或 https:// 开头: {base}")
    if not access_key and not (username and password):
        return error_out("kuboard auth missing", f"{args.env} 环境需 access_key 或 username+password")

    try:
        token = access_key or kuboard_login(base, username, password)
        try:
            cluster_uid = resolve_cluster_uid(base, token, args.cluster)
        except Exception as e:
            if not should_try_kuboard_v3_from_tree_error(e, access_key):
                raise
            data = fetch_configmap_data_v3(base, username, access_key, args.cluster, args.namespace, args.configmap)
        else:
            data = fetch_configmap_data(base, token, cluster_uid, args.namespace, args.configmap)
    except Exception as e:
        return error_out(str(e), "Kuboard ConfigMap 读取失败:检查 URL/鉴权/cluster/namespace/configmap 是否正确")

    content = json.dumps(data, ensure_ascii=False, separators=(",", ":"))
    print(json.dumps({
        "cluster": args.cluster,
        "namespace": args.namespace,
        "configmap": args.configmap,
        "format": "k8s-env-flat",
        "data": data,
        "content": content,
        "runtime": resolve_runtime(data),
    }, ensure_ascii=False, indent=2))
    return 0


def main() -> int:
    p = argparse.ArgumentParser(description="Kuboard ConfigMap HTTP API 客户端")
    sub = p.add_subparsers(dest="cmd", required=True)

    g = sub.add_parser("get", help="读取单个 ConfigMap data")
    g.add_argument("--agent-id", required=True)
    g.add_argument("--env", required=True)
    g.add_argument("--source-id", default="", help="多源 creds 的 source id,默认自动探测 default/kuboard/首个源")
    g.add_argument("--url", help="覆盖 creds 的 Kuboard URL")
    g.add_argument("--access-key", help="覆盖 creds 的 API access key")
    g.add_argument("--username", help="覆盖 creds 的用户名")
    g.add_argument("--password", help="覆盖 creds 的密码")
    g.add_argument("--cluster", required=True)
    g.add_argument("--namespace", required=True)
    g.add_argument("--configmap", required=True)
    g.set_defaults(func=cmd_get)

    args = p.parse_args()
    try:
        return args.func(args)
    except FileNotFoundError as e:
        return error_out(str(e), "creds.json 不存在。请先跑 install.sh 或在 Studio 里补齐 Kuboard 凭证再部署。")
    except ValueError as e:
        return error_out(str(e), f"`{args.env}` 的 Kuboard 凭证缺失。请检查 creds.json 或重跑 install.sh。")
    except Exception as e:
        return error_out(f"{type(e).__name__}: {e}", "脚本内部异常，请反馈。")


if __name__ == "__main__":
    sys.exit(main())
