#!/usr/bin/env python3
"""从 creds.json 的 static endpoints 直接读取数据层连接信息（env-vars 模式）

凭证来源：~/.openclaw/<agent-id>-creds.json
{
  "static": {
    "<env>": {
      "redis": "host:port",
      "mongo": "mongodb://user:pass@host:port/db",
      "elasticsearch": "http://host:9200"
    }
  }
}

输出格式与 resolve_runtime_from_nacos.py 完全一致：
  {"runtime": {"redis": {...}, "mongo": {...}, "elasticsearch": {...}}}
"""
import argparse
import json
import os
import sys


def load_static(agent_id: str, env: str) -> dict:
    path = os.path.expanduser(f"~/.openclaw/{agent_id}-creds.json")
    if not os.path.isfile(path):
        raise FileNotFoundError(f"creds file missing: {path}")
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    endpoints = data.get("static", {}).get(env)
    if not endpoints:
        raise ValueError(f"static.{env} missing in creds.json")
    return endpoints


def resolve(endpoints: dict) -> dict:
    runtime = {}

    # redis
    redis_addr = endpoints.get("redis", "")
    if redis_addr:
        parts = redis_addr.rsplit(":", 1)
        runtime["redis"] = {
            "host": parts[0],
            "port": parts[1] if len(parts) > 1 else "6379",
            "resolved": True,
        }
    else:
        runtime["redis"] = {"host": "", "port": "", "resolved": False}

    # mongo
    mongo_uri = endpoints.get("mongo", "")
    if mongo_uri:
        runtime["mongo"] = {"uri": mongo_uri, "hosts": [], "resolved": True}
    else:
        runtime["mongo"] = {"uri": "", "hosts": [], "resolved": False}

    # elasticsearch
    es_hosts = endpoints.get("elasticsearch", "")
    if es_hosts:
        hosts = [h.strip() for h in es_hosts.split(",") if h.strip()]
        runtime["elasticsearch"] = {"hosts": hosts, "resolved": True}
    else:
        runtime["elasticsearch"] = {"hosts": [], "resolved": False}

    # mysql
    mysql_addr = endpoints.get("mysql", "")
    if mysql_addr:
        # 支持 host:port 或 jdbc:mysql://host:port/db 格式
        host, port, db = mysql_addr, "3306", ""
        if "://" in mysql_addr:
            from urllib.parse import urlparse
            u = urlparse(mysql_addr.replace("jdbc:", ""))
            host, port, db = u.hostname or host, str(u.port or 3306), u.path.lstrip("/")
        elif ":" in mysql_addr:
            parts = mysql_addr.rsplit(":", 1)
            host, port = parts[0], parts[1]
        runtime["mysql"] = {"host": host, "port": port, "database": db, "resolved": True}
    else:
        runtime["mysql"] = {"host": "", "port": "", "database": "", "resolved": False}

    # kafka
    kafka_brokers = endpoints.get("kafka", "")
    if kafka_brokers:
        brokers = [b.strip() for b in kafka_brokers.split(",") if b.strip()]
        runtime["kafka"] = {"brokers": brokers, "resolved": True}
    else:
        runtime["kafka"] = {"brokers": [], "resolved": False}

    # postgresql
    pg = endpoints.get("postgresql", "")
    if pg:
        host, port, db = pg, "5432", ""
        if "://" in pg:
            from urllib.parse import urlparse
            u = urlparse(pg)
            host, port, db = u.hostname or host, str(u.port or 5432), u.path.lstrip("/")
        elif ":" in pg:
            parts = pg.rsplit(":", 1)
            host, port = parts[0], parts[1]
        runtime["postgresql"] = {"host": host, "port": port, "database": db, "resolved": True}
    else:
        runtime["postgresql"] = {"host": "", "port": "", "database": "", "resolved": False}

    # rocketmq
    rocketmq = endpoints.get("rocketmq", "")
    if rocketmq:
        runtime["rocketmq"] = {"namesrv_addr": rocketmq, "resolved": True}
    else:
        runtime["rocketmq"] = {"namesrv_addr": "", "resolved": False}

    # rabbitmq
    rabbitmq = endpoints.get("rabbitmq", "")
    if rabbitmq:
        runtime["rabbitmq"] = {"url": rabbitmq, "resolved": True}
    else:
        runtime["rabbitmq"] = {"url": "", "resolved": False}

    # clickhouse
    ch = endpoints.get("clickhouse", "")
    if ch:
        host, port = ch, "8123"
        if ":" in ch and "://" not in ch:
            parts = ch.rsplit(":", 1)
            host, port = parts[0], parts[1]
        runtime["clickhouse"] = {"host": host, "port": port, "resolved": True}
    else:
        runtime["clickhouse"] = {"host": "", "port": "", "resolved": False}

    return {"runtime": runtime}


def error_out(msg: str, hint: str = "") -> int:
    """stdout 输出结构化错误 JSON，方便机器人把 hint 直接复述给用户。"""
    payload = {"error": msg}
    if hint:
        payload["hint"] = hint
    print(json.dumps(payload, ensure_ascii=False, indent=2))
    return 2


def main() -> int:
    p = argparse.ArgumentParser(description="从 creds.json 读取静态数据层连接（env-vars 模式）")
    p.add_argument("--agent-id", required=True)
    p.add_argument("--env", required=True)
    args = p.parse_args()
    try:
        endpoints = load_static(args.agent_id, args.env)
        result = resolve(endpoints)
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0
    except FileNotFoundError as e:
        return error_out(
            str(e),
            "creds.json 不存在。请先跑 `bash scripts/install.sh`，它会引导你填每个 env 的数据层连接串。",
        )
    except ValueError as e:
        return error_out(
            str(e),
            f"env-vars 模式下 `{args.env}` 环境的连接串没填。编辑 `scripts/.env` 里以 `STATIC_*_{args.env.upper()}` 开头的变量，或重跑 `bash scripts/install.sh`（已设的项不会重问）。",
        )
    except Exception as e:
        return error_out(
            f"{type(e).__name__}: {e}",
            "脚本内部异常。请把命令行和完整错误反馈给 system.yaml 的维护者。",
        )


if __name__ == "__main__":
    sys.exit(main())
