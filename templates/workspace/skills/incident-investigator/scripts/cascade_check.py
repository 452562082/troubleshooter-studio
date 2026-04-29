#!/usr/bin/env python3
"""
cascade_check.py —— incident-investigator Step 4(沿依赖图追下游)的并发查询工具。

排障准确度的核心矛盾:Step 4 让 agent "对每个下游并发查",但每个下游要查
K8s pod / metric / log,LLM 自己拼 N 个 tool calls 容易漏 / 格式不一致。

本脚本:
  1. 读 routing/references/service-dependency-map.yaml 找主角服务的 downstream + data_stores
  2. 对每个下游并发跑 k8s_query.py ns-snapshot(K8s 状态层)
  3. 对每个下游 grep 简单 known-errors 标记(从 ns-snapshot 已带的 known_error_distribution)
  4. 输出统一结构化 JSON,标"哪些下游异常 / 异常类型"

用法:
  python3 cascade_check.py --env prod --service commerce --cluster <c> \\
                          [--namespace-default base-prod]    # 默认 ns(各下游 service 的)
                          [--depth 1]                        # 1=直接下游;2=下游的下游(慎用,token 暴增)

输出 JSON schema:
  {
    "service": "commerce",
    "downstream": ["user", "order", "inventory"],
    "data_stores": ["mysql:order_db", "redis:session"],
    "results": [
      {"target":"user","kind":"service","verdict":"healthy","detail":{...}},
      {"target":"inventory","kind":"service","verdict":"degraded","detail":{...}}
    ],
    "data_store_hints": [
      {"target":"mysql:order_db","skill":"mysql-runtime-query","note":"agent 应主动调对应数据层 skill 验证"}
    ],
    "summary": {
      "checked": 3,
      "healthy": 2,
      "degraded": 1,
      "verdict_root_likely": "inventory",   // 异常下游;若多个,取第一个
      "verdict": "isolated_downstream | widespread_downstream | all_healthy"
    },
    "notes": []
  }

凭证读取:跟 k8s_query.py 同款(env vars / creds.json 自动检测部署上下文)。
"""

import argparse
import concurrent.futures
import json
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Any


def fail(error: str, hint: str = '') -> None:
    print(json.dumps({'error': error, 'hint': hint}, ensure_ascii=False), flush=True)
    sys.exit(1)


def detect_workspace_root() -> Path:
    here = Path(__file__).resolve()
    parts = here.parts
    if '.openclaw' in parts and 'workspace' in parts:
        try:
            ws_idx = parts.index('workspace')
            return Path(*parts[: ws_idx + 2])
        except (ValueError, IndexError):
            pass
    for marker in ('.claude', '.cursor'):
        if marker in parts:
            try:
                idx = parts.index(marker)
                if parts[idx + 1] == 'skills':
                    return Path(*parts[: idx + 3])
            except (ValueError, IndexError):
                pass
    return here.parent.parent.parent.parent


def parse_dep_map(yaml_text: str) -> dict[str, dict[str, Any]]:
    """简单文本解析 service-dependency-map.yaml(避免引 PyYAML 强依赖)。
    支持的形态:
        services:
          <name>:
            upstream: [a, b]
            downstream: [c, d]
            data_stores: ["mysql:foo", "redis:bar"]
            critical: true
    """
    result: dict[str, dict[str, Any]] = {}
    cur_svc = ''
    in_services = False
    for raw in yaml_text.splitlines():
        if raw.strip().startswith('#') or not raw.strip():
            continue
        if raw.strip() == 'services:' or raw.startswith('services:'):
            in_services = True
            continue
        if not in_services:
            continue
        # 服务名层(2 空格缩进)
        m = re.match(r'^  ([\w\-\.]+):\s*$', raw)
        if m:
            cur_svc = m.group(1)
            result[cur_svc] = {'upstream': [], 'downstream': [], 'data_stores': [], 'critical': False}
            continue
        if not cur_svc:
            continue
        # 字段层(4 空格缩进)
        ms = re.match(r'^    (upstream|downstream|data_stores|critical):\s*(.*)$', raw)
        if not ms:
            continue
        field, val = ms.group(1), ms.group(2).strip()
        if field == 'critical':
            result[cur_svc]['critical'] = val.lower() == 'true'
        elif val.startswith('['):
            # inline 列表 ["a", "b"]
            inner = val.strip('[]').strip()
            items = [i.strip().strip('"\'') for i in inner.split(',') if i.strip()]
            result[cur_svc][field] = items
        elif val == '':
            # block 列表后续 - <item>;此处不展开,跳过(简单解析器局限,够 wizard 占位用)
            pass
    return result


def check_one_service(env: str, cluster: str, namespace: str, service: str,
                      ws_root: Path, timeout: int = 25) -> dict[str, Any]:
    """对一个下游服务跑 ns-snapshot,提取 verdict + 关键信号。"""
    k8s_script = ws_root / 'skills' / 'k8s-runtime-query' / 'scripts' / 'k8s_query.py'
    if not k8s_script.exists():
        return {'target': service, 'kind': 'service', 'verdict': 'unknown',
                'error': 'k8s_query.py 不存在'}
    cmd = [sys.executable, str(k8s_script),
           '--env', env, '--cluster', cluster, 'ns-snapshot',
           '--namespace', namespace, '--label-selector', f'app={service}']
    try:
        out = subprocess.check_output(cmd, stderr=subprocess.STDOUT, timeout=timeout).decode('utf-8', errors='ignore')
        data = json.loads(out)
    except subprocess.CalledProcessError as e:
        return {'target': service, 'kind': 'service', 'verdict': 'unknown',
                'error': e.output.decode('utf-8', errors='ignore')[:200]}
    except subprocess.TimeoutExpired:
        return {'target': service, 'kind': 'service', 'verdict': 'unknown',
                'error': f'timeout > {timeout}s'}
    except Exception as e:
        return {'target': service, 'kind': 'service', 'verdict': 'unknown',
                'error': str(e)[:200]}

    verdict_raw = data.get('verdict', 'unknown')  # healthy / isolated / widespread
    verdict = 'healthy' if verdict_raw == 'healthy' else 'degraded'
    return {
        'target': service,
        'kind': 'service',
        'verdict': verdict,
        'detail': {
            'total': data.get('total'),
            'healthy_count': data.get('healthy_count'),
            'degraded_count': data.get('degraded_count'),
            'phase_distribution': data.get('phase_distribution'),
            'known_error_distribution': data.get('known_error_distribution'),
            'degraded_pods_top3': (data.get('degraded_pods') or [])[:3],
        },
    }


def main() -> None:
    p = argparse.ArgumentParser(prog='cascade_check.py')
    p.add_argument('--env', required=True)
    p.add_argument('--service', required=True, help='主角服务名(从 dependency-map 找它的 downstream)')
    p.add_argument('--cluster', required=True, help='K8s 集群名(传给 k8s_query.py)')
    p.add_argument('--namespace-default', required=True, help='下游服务默认 namespace(假定都在同 ns;实际不同 ns 可在 dependency-map 里扩展字段)')
    p.add_argument('--depth', type=int, default=1, help='追溯深度;1=直接下游(默认),2=下游的下游(token 暴增,慎用)')
    args = p.parse_args()

    ws_root = detect_workspace_root()
    notes: list[str] = []

    dep_path = ws_root / 'skills' / 'routing' / 'references' / 'service-dependency-map.yaml'
    if not dep_path.exists():
        fail('no-dep-map', f'service-dependency-map.yaml 不存在: {dep_path};必须先填这个文件 incident-investigator Step 4 才有效')

    dep_map = parse_dep_map(dep_path.read_text(encoding='utf-8', errors='ignore'))
    self_entry = dep_map.get(args.service)
    if not self_entry:
        fail('service-not-in-map', f'{args.service} 不在 service-dependency-map.yaml;先去填上它的 downstream/data_stores')

    downstream = self_entry.get('downstream') or []
    data_stores = self_entry.get('data_stores') or []
    if not downstream and not data_stores:
        notes.append(f'{args.service} 在 dependency-map 里 downstream/data_stores 都空;Step 4 等同跳过,排障可能漏真因')

    if args.depth > 1:
        # 把每个 downstream 自己的 downstream 也加进列表(去重)
        seen = set(downstream)
        for d in list(downstream):
            child = dep_map.get(d)
            if child:
                for c in (child.get('downstream') or []):
                    if c not in seen:
                        downstream.append(c)
                        seen.add(c)

    # 并发查每个 downstream service
    results: list[dict[str, Any]] = []
    if downstream:
        with concurrent.futures.ThreadPoolExecutor(max_workers=min(8, len(downstream))) as pool:
            futures = {
                pool.submit(check_one_service, args.env, args.cluster, args.namespace_default, svc, ws_root): svc
                for svc in downstream
            }
            for f in concurrent.futures.as_completed(futures):
                results.append(f.result())

    # data_stores 不直接查(每种 type 不同 skill,agent 主动调),只列 hint
    data_store_hints = []
    for ds in data_stores:
        ds_type = ds.split(':', 1)[0] if ':' in ds else ds
        skill_map = {
            'mysql': 'mysql-runtime-query', 'postgresql': 'postgresql-runtime-query',
            'redis': 'redis-runtime-query', 'mongodb': 'mongodb-runtime-query',
            'es': 'es-runtime-query', 'elasticsearch': 'es-runtime-query',
            'kafka': 'kafka-runtime-query', 'rocketmq': 'rocketmq-runtime-query',
            'rabbitmq': 'rabbitmq-runtime-query', 'clickhouse': 'clickhouse-runtime-query',
        }
        data_store_hints.append({
            'target': ds,
            'skill': skill_map.get(ds_type, f'{ds_type}-runtime-query'),
            'note': f'agent 应主动调 {skill_map.get(ds_type, ds_type)} 看 {ds} 是否异常(慢查询 / 连接池 / 命中率)',
        })

    # 汇总
    healthy = sum(1 for r in results if r.get('verdict') == 'healthy')
    degraded_results = [r for r in results if r.get('verdict') == 'degraded']
    degraded_count = len(degraded_results)
    if not results:
        verdict = 'no_downstream_in_map'
    elif degraded_count == 0:
        verdict = 'all_healthy'
    elif degraded_count == 1:
        verdict = 'isolated_downstream'
    else:
        verdict = 'widespread_downstream'
    root_likely = degraded_results[0]['target'] if degraded_results else ''

    output = {
        'service': args.service,
        'depth': args.depth,
        'downstream': downstream,
        'data_stores': data_stores,
        'results': results,
        'data_store_hints': data_store_hints,
        'summary': {
            'checked': len(results),
            'healthy': healthy,
            'degraded': degraded_count,
            'verdict_root_likely': root_likely,
            'verdict': verdict,
        },
        'notes': notes,
        'next_steps_for_agent': [
            'verdict_root_likely 字段指向的服务大概率是真因 → 把它当主角递归一遍 6 步流程',
            '所有 data_store_hints 列出的数据层 → agent 还要主动调对应 skill 验证,不能只看 K8s 层',
            'verdict=all_healthy 但 metric/log 仍异常 → 真因可能在当前服务自身 / 中间网络 / 共享 DB 锁',
        ],
    }
    print(json.dumps(output, ensure_ascii=False, indent=2), flush=True)


if __name__ == '__main__':
    main()
