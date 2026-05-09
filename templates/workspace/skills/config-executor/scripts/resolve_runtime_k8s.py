#!/usr/bin/env python3
"""从 K8s ConfigMap/Secret 解析数据层连接信息（kubernetes 模式）

凭证来源：~/.openclaw/<agent-id>-creds.json
{
  "kubernetes": {
    "<env>": {
      "context": "dev-context",
      "namespace": "app-namespace",
      "configmap": "app-config",
      "secret": "app-secret"
    }
  }
}

工作流程：
  1. 读 creds.json 获取 context/namespace/configmap/secret 名称
  2. kubectl get configmap <name> → 解析 data 字段
  3. 从 data 中提取 redis/mongo/es 连接串
  4. 输出与 resolve_runtime_from_nacos.py 相同格式

需要：kubectl 已安装且有对应 context 的访问权限。
"""
import argparse
import json
import os
import subprocess
import sys


def _find_creds_file(agent_id: str) -> str:
    """凭证文件双路径回退:
       - ~/.openclaw/<id>-creds.json    OpenClaw 部署专用(install.sh 写)
       - ~/.tshoot/<id>-creds.json      Claude Code / Cursor / Codex 通用(WriteIDECredsFile 写)
       两份 schema 完全一致,谁先存在用谁。
    """
    for p in (f"~/.openclaw/{agent_id}-creds.json", f"~/.tshoot/{agent_id}-creds.json"):
        ap = os.path.expanduser(p)
        if os.path.isfile(ap):
            return ap
    raise FileNotFoundError(
        f"creds file not found in any of: ~/.openclaw/{agent_id}-creds.json, ~/.tshoot/{agent_id}-creds.json"
    )


def load_k8s_config(agent_id: str, env: str) -> dict:
    path = _find_creds_file(agent_id)
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    k8s = data.get("kubernetes", {}).get(env)
    if not k8s:
        raise ValueError(f"kubernetes.{env} missing in {path}")
    return k8s


def kubectl_get(resource: str, name: str, namespace: str, context: str) -> dict:
    cmd = ["kubectl", "get", resource, name, "-n", namespace, "-o", "json"]
    if context:
        cmd += ["--context", context]
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=15)
    if result.returncode != 0:
        raise RuntimeError(f"kubectl failed: {result.stderr.strip()}")
    return json.loads(result.stdout)


def extract_connections(data: dict) -> dict:
    """从 ConfigMap/Secret 的 data 字段提取常见连接串 key"""
    import base64

    flat = {}
    raw_data = data.get("data", {})
    # Secret 的值是 base64 编码
    if data.get("kind") == "Secret":
        for k, v in raw_data.items():
            try:
                flat[k.upper()] = base64.b64decode(v).decode("utf-8", errors="replace")
            except Exception:
                flat[k.upper()] = v
    else:
        for k, v in raw_data.items():
            flat[k.upper()] = v

    runtime = {}

    # redis
    redis_host = flat.get("REDIS_HOST", flat.get("REDIS_URL", ""))
    if redis_host:
        parts = redis_host.rsplit(":", 1)
        runtime["redis"] = {
            "host": parts[0],
            "port": parts[1] if len(parts) > 1 else "6379",
            "resolved": True,
        }
    else:
        runtime["redis"] = {"host": "", "port": "", "resolved": False}

    # mongo
    mongo = flat.get("MONGO_URI", flat.get("MONGODB_URI", flat.get("MONGO_URL", "")))
    if mongo:
        runtime["mongo"] = {"uri": mongo, "hosts": [], "resolved": True}
    else:
        runtime["mongo"] = {"uri": "", "hosts": [], "resolved": False}

    # elasticsearch
    es = flat.get("ES_URL", flat.get("ELASTICSEARCH_URL", flat.get("ES_HOSTS", "")))
    if es:
        hosts = [h.strip() for h in es.split(",") if h.strip()]
        runtime["elasticsearch"] = {"hosts": hosts, "resolved": True}
    else:
        runtime["elasticsearch"] = {"hosts": [], "resolved": False}

    # mysql
    mysql = flat.get("MYSQL_HOST", flat.get("MYSQL_URL", flat.get("DB_HOST", "")))
    if mysql:
        host, port, db = mysql, "3306", flat.get("MYSQL_DB", flat.get("DB_DATABASE", ""))
        if ":" in mysql and "://" not in mysql:
            parts = mysql.rsplit(":", 1)
            host, port = parts[0], parts[1]
        runtime["mysql"] = {"host": host, "port": port, "database": db, "resolved": True}
    else:
        runtime["mysql"] = {"host": "", "port": "", "database": "", "resolved": False}

    # kafka
    kafka = flat.get("KAFKA_BROKERS", flat.get("KAFKA_BOOTSTRAP_SERVERS", flat.get("KAFKA_HOSTS", "")))
    if kafka:
        brokers = [b.strip() for b in kafka.split(",") if b.strip()]
        runtime["kafka"] = {"brokers": brokers, "resolved": True}
    else:
        runtime["kafka"] = {"brokers": [], "resolved": False}

    # postgresql
    pg = flat.get("PG_HOST", flat.get("POSTGRES_HOST", flat.get("PGHOST", "")))
    if pg:
        host, port = pg, flat.get("PG_PORT", flat.get("PGPORT", "5432"))
        db = flat.get("PG_DB", flat.get("POSTGRES_DB", flat.get("PGDATABASE", "")))
        runtime["postgresql"] = {"host": host, "port": port, "database": db, "resolved": True}
    else:
        runtime["postgresql"] = {"host": "", "port": "", "database": "", "resolved": False}

    # rocketmq
    rmq = flat.get("ROCKETMQ_NAMESRV", flat.get("NAMESRV_ADDR", ""))
    if rmq:
        runtime["rocketmq"] = {"namesrv_addr": rmq, "resolved": True}
    else:
        runtime["rocketmq"] = {"namesrv_addr": "", "resolved": False}

    # rabbitmq
    rabbit = flat.get("RABBITMQ_URL", flat.get("AMQP_URL", ""))
    if rabbit:
        runtime["rabbitmq"] = {"url": rabbit, "resolved": True}
    else:
        runtime["rabbitmq"] = {"url": "", "resolved": False}

    # clickhouse
    ch = flat.get("CLICKHOUSE_HOST", flat.get("CH_HOST", ""))
    if ch:
        port = flat.get("CLICKHOUSE_PORT", flat.get("CH_PORT", "8123"))
        runtime["clickhouse"] = {"host": ch, "port": port, "resolved": True}
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
    p = argparse.ArgumentParser(description="从 K8s ConfigMap/Secret 解析数据层连接")
    p.add_argument("--agent-id", required=True)
    p.add_argument("--env", required=True)
    p.add_argument("--resource", default="configmap", help="configmap or secret")
    args = p.parse_args()
    try:
        k8s = load_k8s_config(args.agent_id, args.env)
        context = k8s.get("context", "")
        namespace = k8s.get("namespace", "default")
        name = k8s.get(args.resource, k8s.get("configmap", ""))
        if not name:
            raise ValueError(f"kubernetes.{args.env}.{args.resource} not specified in creds.json")
        data = kubectl_get(args.resource, name, namespace, context)
        result = extract_connections(data)
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0
    except FileNotFoundError as e:
        return error_out(
            str(e),
            "creds.json 不存在。请先跑 `bash scripts/install.sh`，它会引导你填每个 env 的 K8s context / namespace / configmap 名。",
        )
    except ValueError as e:
        return error_out(
            str(e),
            f"`{args.env}` 的 K8s 配置不全。编辑 `scripts/.env` 里 `K8S_CONTEXT_{args.env.upper()}` / `K8S_NAMESPACE_{args.env.upper()}` / `K8S_CONFIGMAP_{args.env.upper()}`，或重跑 `bash scripts/install.sh`。",
        )
    except RuntimeError as e:
        msg = str(e)
        hint = "kubectl 调用失败。"
        if "forbidden" in msg.lower() or "unauthorized" in msg.lower():
            hint += " 可能 RBAC 不足：`kubectl auth can-i get configmap -n <ns> --context <ctx>` 先验证权限。"
        elif "not found" in msg.lower() and "context" in msg.lower():
            hint += " context 不存在：`kubectl config get-contexts` 查可用 context，更新 `scripts/.env` 的 `K8S_CONTEXT_*`。"
        else:
            hint += " 先用 `kubectl --context <ctx> get configmap <name> -n <ns>` 手动跑一次，定位是 K8s 侧问题还是脚本问题。"
        return error_out(msg, hint)
    except Exception as e:
        return error_out(
            f"{type(e).__name__}: {e}",
            "脚本内部异常。请把命令行和完整错误反馈给 troubleshooter.yaml 的维护者。",
        )


if __name__ == "__main__":
    sys.exit(main())
