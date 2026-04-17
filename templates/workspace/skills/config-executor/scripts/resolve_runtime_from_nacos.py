#!/usr/bin/env python3
"""
从 Nacos 配置文本中提取 Redis/Mongo/ES 运行时连接信息。
输入可以是 YAML / JSON / properties 混合文本。

用法：
  python3 resolve_runtime_from_nacos.py --text "..."
  cat config.yaml | python3 resolve_runtime_from_nacos.py --stdin
"""
import argparse
import json
import re
import sys
from typing import Dict, Any, List


def parse_properties(text: str) -> Dict[str, str]:
    out = {}
    for line in text.splitlines():
        s = line.strip()
        if not s or s.startswith('#'):
            continue
        if '=' in s:
            k, v = s.split('=', 1)
            out[k.strip()] = v.strip()
        elif ':' in s and not s.startswith('-'):
            k, v = s.split(':', 1)
            out[k.strip()] = v.strip().strip('"\'')
    return out


def find_first(patterns: List[str], text: str) -> str:
    for p in patterns:
        m = re.search(p, text, re.IGNORECASE | re.MULTILINE)
        if m:
            return m.group(1).strip().strip("\"'")
    return ""


def parse_yaml_list_block(text: str, section: str, key: str) -> List[str]:
    """提取如下结构：
    section:
      key:
        - v1
        - v2
    """
    pat = rf"{re.escape(section)}\s*:\s*\n(?:[ \t].*\n)*?[ \t]+{re.escape(key)}\s*:\s*\n((?:[ \t]+-\s*[^\n]+\n)+)"
    m = re.search(pat, text, re.IGNORECASE | re.MULTILINE)
    if not m:
        return []
    block = m.group(1)
    out: List[str] = []
    for line in block.splitlines():
        line = line.strip()
        if line.startswith('-'):
            out.append(line[1:].strip().strip('"\''))
    return [x for x in out if x]


def resolve(text: str) -> Dict[str, Any]:
    props = parse_properties(text)

    # Redis
    redis_host = props.get('redis.host') or props.get('redis.default.host') or props.get('spring.redis.host') or find_first([
        r"redis\.host\s*[:=]\s*([^\s,]+)",
        r"redis\.default\.host\s*[:=]\s*([^\s,]+)",
        r"spring\.redis\.host\s*[:=]\s*([^\s,]+)",
        r"redis\s*:\s*\n(?:[ \t].*\n)*?[ \t]+host\s*:\s*([^\s\n#]+)",
        r"redis\s*:\s*\n(?:[ \t].*\n)*?[ \t]+\w+\s*:\s*\n(?:[ \t].*\n)*?[ \t]+host\s*:\s*([^\s\n#]+)",
        r"spring\s*:\s*\n(?:[ \t].*\n)*?[ \t]+redis\s*:\s*\n(?:[ \t].*\n)*?[ \t]+host\s*:\s*([^\s\n#]+)",
    ], text)
    redis_port = props.get('redis.port') or props.get('redis.default.port') or props.get('spring.redis.port') or find_first([
        r"redis\.port\s*[:=]\s*(\d+)",
        r"redis\.default\.port\s*[:=]\s*(\d+)",
        r"spring\.redis\.port\s*[:=]\s*(\d+)",
        r"redis\s*:\s*\n(?:[ \t].*\n)*?[ \t]+port\s*:\s*(\d+)",
        r"redis\s*:\s*\n(?:[ \t].*\n)*?[ \t]+\w+\s*:\s*\n(?:[ \t].*\n)*?[ \t]+port\s*:\s*(\d+)",
        r"spring\s*:\s*\n(?:[ \t].*\n)*?[ \t]+redis\s*:\s*\n(?:[ \t].*\n)*?[ \t]+port\s*:\s*(\d+)",
    ], text)

    # Mongo
    mongo_uri = props.get('mongo.uri') or props.get('spring.data.mongodb.uri') or find_first([
        r"spring\.data\.mongodb\.uri\s*[:=]\s*([^\s]+)",
        r"mongo\.uri\s*[:=]\s*([^\s]+)",
        r"mongodb\s*:\s*\n(?:[ \t].*\n)*?[ \t]+uri\s*:\s*([^\s\n#]+)",
        r"spring\s*:\s*\n(?:[ \t].*\n)*?[ \t]+data\s*:\s*\n(?:[ \t].*\n)*?[ \t]+mongodb\s*:\s*\n(?:[ \t].*\n)*?[ \t]+uri\s*:\s*([^\s\n#]+)",
    ], text)
    mongo_hosts = ""
    if not mongo_uri:
        mh = props.get('mongo.host') or props.get('mongodb.host') or find_first([
            r"mongo\.host\s*[:=]\s*([^\s,]+)",
            r"mongodb\.host\s*[:=]\s*([^\s,]+)",
            r"mongodb\s*:\s*\n(?:[ \t].*\n)*?[ \t]+host\s*:\s*([^\s\n#]+)",
        ], text)
        mp = props.get('mongo.port') or props.get('mongodb.port') or find_first([
            r"mongo\.port\s*[:=]\s*(\d+)",
            r"mongodb\.port\s*[:=]\s*(\d+)",
            r"mongodb\s*:\s*\n(?:[ \t].*\n)*?[ \t]+port\s*:\s*(\d+)",
        ], text)
        if mh:
            mongo_hosts = f"{mh}:{mp or '27017'}"

    # Elasticsearch
    es_hosts = props.get('es.hosts') or props.get('elasticsearch.hosts') or find_first([
        r"es\.hosts\s*[:=]\s*([^\n]+)",
        r"elasticsearch\.hosts\s*[:=]\s*([^\n]+)",
        r"elasticsearch\s*:\s*\n(?:[ \t].*\n)*?[ \t]+hosts\s*:\s*\[([^\]]+)\]",
    ], text)

    es_list = parse_yaml_list_block(text, section='elasticsearch', key='hosts')

    if not es_hosts and not es_list:
        es_host = props.get('es.host') or props.get('elasticsearch.host') or find_first([
            r"es\.host\s*[:=]\s*([^\s,]+)",
            r"elasticsearch\.host\s*[:=]\s*([^\s,]+)",
            r"elasticsearch\s*:\s*\n(?:[ \t].*\n)*?[ \t]+host\s*:\s*([^\s\n#]+)",
        ], text)
        es_port = props.get('es.port') or props.get('elasticsearch.port') or find_first([
            r"es\.port\s*[:=]\s*(\d+)",
            r"elasticsearch\.port\s*[:=]\s*(\d+)",
            r"elasticsearch\s*:\s*\n(?:[ \t].*\n)*?[ \t]+port\s*:\s*(\d+)",
        ], text)
        if es_host:
            es_list = [f"{es_host}:{es_port or '9200'}"]

    if es_hosts and not es_list:
        raw = es_hosts.strip().strip('[]')
        es_list = [h.strip().strip('"\'') for h in raw.split(',') if h.strip()]

    return {
        "redis": {
            "host": redis_host,
            "port": int(redis_port) if str(redis_port).isdigit() else None,
            "resolved": bool(redis_host),
        },
        "mongo": {
            "uri": mongo_uri,
            "hosts": mongo_hosts,
            "resolved": bool(mongo_uri or mongo_hosts),
        },
        "elasticsearch": {
            "hosts": es_list,
            "resolved": bool(es_list),
        },
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--text', default='')
    ap.add_argument('--stdin', action='store_true')
    args = ap.parse_args()

    text = args.text
    if args.stdin:
        text = sys.stdin.read()
    if not text:
        print(json.dumps({"ok": False, "error": "empty input"}, ensure_ascii=False))
        return 1

    data = resolve(text)
    print(json.dumps({"ok": True, "runtime": data}, ensure_ascii=False, indent=2))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
