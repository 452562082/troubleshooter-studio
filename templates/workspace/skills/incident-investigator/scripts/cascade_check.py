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
    两种列表写法都支持(生成器 service-dependency-map.yaml.tmpl 实际产出的是 **block 风格**):

        services:
          svc-inline:
            upstream: [a, b]                  # inline 列表
            downstream: [c, d]
            data_stores: ["mysql:foo"]
          svc-block:
            upstream:                         # block 列表(生成器默认产出这种)
              - a
              - b
            downstream:
              - c
            data_stores:
              - "mysql:foo"
            critical: true

    历史坑(2026-06):旧版只认 inline,block 风格被静默跳过 → 自动生成的依赖图
    downstream 永远空 → incident-investigator Step 4 形同虚设。block 解析是核心,不是 nice-to-have。
    """
    result: dict[str, dict[str, Any]] = {}
    cur_svc = ''
    cur_field = ''  # 非空表示正在收集该字段的 block 列表项(`- xxx`)
    in_services = False
    for raw in yaml_text.splitlines():
        if raw.strip().startswith('#') or not raw.strip():
            continue
        if raw.strip() == 'services:' or raw.startswith('services:'):
            in_services = True
            continue
        if not in_services:
            continue
        # block 列表项(比 service/field 缩进更深的 `- xxx`)—— 先于其它规则判
        mi = re.match(r'^\s+-\s+(.*)$', raw)
        if mi and cur_svc and cur_field in ('upstream', 'downstream', 'data_stores'):
            item = mi.group(1).strip().strip('"\'')
            # 去掉行尾注释(block item 一般不带,稳妥处理)
            if item:
                result[cur_svc][cur_field].append(item)
            continue
        # 服务名层(2 空格缩进)
        m = re.match(r'^  ([\w\-\.]+):\s*$', raw)
        if m:
            cur_svc = m.group(1)
            cur_field = ''
            result[cur_svc] = {'upstream': [], 'downstream': [], 'data_stores': [], 'critical': False}
            continue
        if not cur_svc:
            continue
        # 字段层(4 空格缩进)
        ms = re.match(r'^    (upstream|downstream|data_stores|critical):\s*(.*)$', raw)
        if not ms:
            continue
        field, val = ms.group(1), ms.group(2).strip()
        cur_field = ''  # 默认退出 block 收集;命中 block 字段时下面再开启
        if field == 'critical':
            result[cur_svc]['critical'] = val.lower() == 'true'
        elif val.startswith('['):
            # inline 列表 ["a", "b"](含空 []:inner 为空 → 空列表)
            inner = val.strip('[]').strip()
            items = [i.strip().strip('"\'') for i in inner.split(',') if i.strip()]
            result[cur_svc][field] = items
        elif val == '':
            # block 列表:进入收集模式,后续更深缩进的 `- item` 行累加到本字段
            cur_field = field
    return result


def summarize(results: list[dict[str, Any]]) -> dict[str, Any]:
    """把每个下游的 verdict 汇总成 Step 4 的总判定。

    关键正确性约束(2026-06 修):`unknown`(查不到 —— 脚本缺失 / kuboard 不通 /
    超时 / cluster 名错 / creds 缺)**必须单独计数**,绝不能并进 healthy。
    旧版只数 healthy / degraded,unknown 被吞 → 全部下游查失败时 degraded_count==0
    误判 `all_healthy` → Step 4 据此判"真因在主角自身"拐错方向(把"下游未知"当"下游健康")。

    verdict 取值:
      no_downstream_in_map  —— 没有下游(map 没填或 downstream 空)
      all_healthy           —— 全 healthy,无 degraded 无 unknown
      downstream_unknown    —— 有 unknown 且没有 degraded(查不到,别当健康;Step 4 不能下主角自身结论)
      isolated_downstream   —— 恰好 1 个 degraded
      widespread_downstream —— ≥2 个 degraded
    (有 degraded 时即便夹杂 unknown 也按 degraded 数判 isolated/widespread,unknown 计数仍在 summary 里暴露。)
    """
    healthy = sum(1 for r in results if r.get('verdict') == 'healthy')
    degraded_results = [r for r in results if r.get('verdict') == 'degraded']
    unknown_results = [r for r in results if r.get('verdict') not in ('healthy', 'degraded')]
    degraded_count = len(degraded_results)
    unknown_count = len(unknown_results)

    if not results:
        verdict = 'no_downstream_in_map'
    elif degraded_count == 0:
        verdict = 'all_healthy' if unknown_count == 0 else 'downstream_unknown'
    elif degraded_count == 1:
        verdict = 'isolated_downstream'
    else:
        verdict = 'widespread_downstream'

    return {
        'checked': len(results),
        'healthy': healthy,
        'degraded': degraded_count,
        'unknown': unknown_count,
        'unknown_targets': [r.get('target') for r in unknown_results],
        'verdict_root_likely': degraded_results[0]['target'] if degraded_results else '',
        'verdict': verdict,
    }


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

    # ns-snapshot verdict:healthy / isolated / widespread / no_pods_matched。
    # no_pods_matched(label 对不上 / ns 错 / 真没 pod)→ 归 unknown 而非 degraded —— 它是
    # "查不到"不是"查到异常",summarize 才能正确区分 all_healthy vs downstream_unknown。
    verdict_raw = data.get('verdict', 'unknown')
    if verdict_raw == 'healthy':
        verdict = 'healthy'
    elif verdict_raw in ('isolated', 'widespread'):
        verdict = 'degraded'
    else:  # no_pods_matched / unknown / 其它未预期值
        verdict = 'unknown'
    detail = {
        'total': data.get('total'),
        'healthy_count': data.get('healthy_count'),
        'degraded_count': data.get('degraded_count'),
        'phase_distribution': data.get('phase_distribution'),
        'known_error_distribution': data.get('known_error_distribution'),
        'degraded_pods_top3': (data.get('degraded_pods') or [])[:3],
    }
    if verdict == 'unknown':
        detail['reason'] = f'ns-snapshot verdict={verdict_raw}(total={data.get("total")});下游状态未确认'
    return {
        'target': service,
        'kind': 'service',
        'verdict': verdict,
        'detail': detail,
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

    # data_stores 不直接查(每种 type 不同 skill,agent 主动调),只列 hint。
    # skill_present:校验该 skill 在本 workspace 真存在(不同系统按 yaml 白名单只装了部分数据层 skill);
    # 不存在就明示 fallback,避免提示一个不存在的 skill 让 agent 空找(P3:旧版 rocketmq 等就是 dangling)。
    data_store_hints = []
    skill_map = {
        'mysql': 'mysql-runtime-query', 'doris': 'doris-runtime-query',
        'postgresql': 'postgresql-runtime-query',
        'redis': 'redis-runtime-query', 'mongodb': 'mongodb-runtime-query',
        'es': 'es-runtime-query', 'elasticsearch': 'es-runtime-query',
        'kafka': 'kafka-runtime-query',
        'rabbitmq': 'rabbitmq-runtime-query', 'clickhouse': 'clickhouse-runtime-query',
    }
    for ds in data_stores:
        ds_type = ds.split(':', 1)[0] if ':' in ds else ds
        skill = skill_map.get(ds_type, f'{ds_type}-runtime-query')
        skill_present = (ws_root / 'skills' / skill).exists()
        if skill_present:
            note = f'agent 应主动调 {skill} 看 {ds} 是否异常(慢查询 / 连接池 / 命中率)'
        else:
            note = (f'本 workspace 未装 {skill} skill —— 改用通用工具核对 {ds}'
                    f'(对应数据层 MCP 或 CLI;没有则在快报里标该数据层未验证)')
        data_store_hints.append({
            'target': ds,
            'skill': skill,
            'skill_present': skill_present,
            'note': note,
        })

    summary = summarize(results)
    if summary['verdict'] == 'downstream_unknown':
        notes.append(f"下游 {summary['unknown_targets']} 查不到状态(cluster 名 / label app=<svc> / kuboard 连通 / creds 任一问题);"
                     '不能据此判"真因在主角自身",先补齐再判,置信度上限锁中')

    output = {
        'service': args.service,
        'depth': args.depth,
        'downstream': downstream,
        'data_stores': data_stores,
        'results': results,
        'data_store_hints': data_store_hints,
        'summary': summary,
        'notes': notes,
        'next_steps_for_agent': [
            'verdict_root_likely 字段指向的服务大概率是真因 → 把它当主角递归一遍 7 步流程(含 Step 7 沉淀)',
            '所有 data_store_hints 列出的数据层 → agent 还要主动调对应 skill 验证,不能只看 K8s 层',
            'verdict=all_healthy 但 metric/log 仍异常 → 真因可能在当前服务自身 / 中间网络 / 共享 DB 锁',
            'verdict=downstream_unknown → 下游健康状况未知(非健康!),先按 summary.unknown_targets 补 cluster/label/creds 再追,别直接进 Step 5 当主角自身问题',
        ],
    }
    print(json.dumps(output, ensure_ascii=False, indent=2), flush=True)


if __name__ == '__main__':
    main()
