#!/usr/bin/env python3
"""
triangulate.py —— incident-investigator Step 5(三向交叉)的客观判定工具。

排障准确度的核心矛盾:trace + log + metric 三个数据源时间窗对齐 + 相关性判断
靠 LLM 心算容易错。本脚本机器对齐时间 + 算 correlation,给出结构化结论
让 agent 直接照搬,不用主观推。

用法:
  python3 triangulate.py --env prod --service commerce \\
                         --start "2025-04-29 14:20" --end "2025-04-29 14:30" \\
                         [--baseline-window 24h]      # 跟过去 24h 同时段对比

输出 JSON:
  {
    "incident_window": "...",
    "metric_anomalies": [{"metric":"p99","baseline":"500ms","actual":"5s","delta":"+10x","start_ts":"..."}],
    "log_pattern_hits": [{"pattern":"context deadline exceeded","count":847,"first_seen":"...","last_seen":"..."}],
    "trace_outliers": [{"trace_id":"...","duration_ms":8200,"critical_span":{...}}],
    "correlation": {
      "metric_log_aligned": true,
      "log_trace_aligned": true,
      "trace_metric_aligned": true,
      "consensus_service": "inventory",  // 三向最常指向哪个服务
      "consensus_window": "14:23:00-14:29:55",
      "verdict": "三向高度一致,集中在 inventory 服务,14:23 突发"
    },
    "notes": []
  }

设计:
  - 三路是 best-effort,任一失败不阻塞,error 进 notes
  - 时间对齐:任意两路时间差 ≤ 60s 视为"同窗口"
  - 一致性判定:metric 突变 / log error / trace 慢 三者指向"同一 downstream service" 计为 aligned
  - 实际 query 当前是 stub —— 完整接 PromQL/LogQL/Tempo API 需 IDE 端 MCP 或者本地 curl 凭证;
    本版只做"框架 + 占位输入解析",真实场景 agent 把 trace/log/metric 各自独立调好后,
    把 raw 数据通过 stdin 喂给本脚本,脚本只做对齐和相关性判定。
"""

import argparse
import json
import re
import sys
from datetime import datetime, timedelta, timezone
from typing import Any


def parse_ts(s: str) -> datetime | None:
    if not s:
        return None
    try:
        # 接受 "2025-04-29 14:23" / "2025-04-29T14:23:00Z" / ISO 各种
        normalized = s.replace('Z', '+00:00').replace(' ', 'T')
        if 'T' in normalized and '+' not in normalized and not normalized.endswith('Z'):
            normalized += '+00:00'
        t = datetime.fromisoformat(normalized)
        if t.tzinfo is None:
            t = t.replace(tzinfo=timezone.utc)
        return t
    except Exception:
        return None


def fail(error: str, hint: str = '') -> None:
    print(json.dumps({'error': error, 'hint': hint}, ensure_ascii=False), flush=True)
    sys.exit(1)


def windows_overlap(a_start: datetime | None, a_end: datetime | None,
                     b_start: datetime | None, b_end: datetime | None,
                     tolerance_s: int = 60) -> bool:
    """两个时间窗在 ±tolerance 内有重合就算 aligned。任一缺则 False。"""
    if not all([a_start, a_end, b_start, b_end]):
        return False
    tol = timedelta(seconds=tolerance_s)
    return (a_start - tol) <= b_end and (b_start - tol) <= a_end  # type: ignore[operator]


def services_overlap(a_services: set[str], b_services: set[str]) -> set[str]:
    """两个数据源各自指向的"嫌疑 service"集合的交集。"""
    return a_services & b_services


def analyze(payload: dict[str, Any]) -> dict[str, Any]:
    """payload 包含 metric_anomalies / log_pattern_hits / trace_outliers 三个数组,
    每条都已带 ts / target_service 信息。本函数对齐 + 算 correlation。"""

    notes: list[str] = []

    # ── 解析 metric ──
    metric_anomalies = payload.get('metric_anomalies') or []
    metric_window = (None, None)
    metric_services: set[str] = set()
    if metric_anomalies:
        starts = [parse_ts(m.get('start_ts')) for m in metric_anomalies]
        ends = [parse_ts(m.get('end_ts') or m.get('start_ts')) for m in metric_anomalies]
        starts = [s for s in starts if s]
        ends = [e for e in ends if e]
        if starts and ends:
            metric_window = (min(starts), max(ends))
        for m in metric_anomalies:
            svc = (m.get('target_service') or m.get('service') or '').strip()
            if svc:
                metric_services.add(svc)
    else:
        notes.append('[metric] 没数据;Step 5 应先调 PromQL/Grafana MCP 拉异常指标再喂给本脚本')

    # ── 解析 log ──
    log_hits = payload.get('log_pattern_hits') or []
    log_window = (None, None)
    log_services: set[str] = set()
    if log_hits:
        starts = [parse_ts(l.get('first_seen')) for l in log_hits]
        ends = [parse_ts(l.get('last_seen') or l.get('first_seen')) for l in log_hits]
        starts = [s for s in starts if s]
        ends = [e for e in ends if e]
        if starts and ends:
            log_window = (min(starts), max(ends))
        for l in log_hits:
            svc = (l.get('target_service') or l.get('service') or '').strip()
            if svc:
                log_services.add(svc)
    else:
        notes.append('[log] 没数据;Step 5 应先调 LogQL/ELK 按 selector_chain 拉错误日志')

    # ── 解析 trace ──
    trace_outliers = payload.get('trace_outliers') or []
    trace_window = (None, None)
    trace_services: set[str] = set()
    if trace_outliers:
        starts = [parse_ts(t.get('start_ts')) for t in trace_outliers]
        ends = [parse_ts(t.get('end_ts') or t.get('start_ts')) for t in trace_outliers]
        starts = [s for s in starts if s]
        ends = [e for e in ends if e]
        if starts and ends:
            trace_window = (min(starts), max(ends))
        for t in trace_outliers:
            cs = t.get('critical_span') or {}
            svc = (cs.get('service') or t.get('target_service') or '').strip()
            if svc:
                trace_services.add(svc)
    else:
        notes.append('[trace] 没数据;Step 5 应先调 Jaeger/Tempo/SkyWalking 拉慢/错 trace')

    # ── 时间窗对齐 ──
    metric_log_aligned = windows_overlap(*metric_window, *log_window)
    log_trace_aligned = windows_overlap(*log_window, *trace_window)
    trace_metric_aligned = windows_overlap(*trace_window, *metric_window)

    # ── 服务交集 ──
    consensus = metric_services & log_services & trace_services
    if not consensus:
        # 至少要 2 路指向同一 service
        consensus = (metric_services & log_services) or (log_services & trace_services) or (metric_services & trace_services)
    consensus_service = next(iter(consensus)) if len(consensus) == 1 else (
        next(iter(consensus)) + ' (+其它)' if consensus else ''
    )

    # ── 共识时间窗(三路有效起止的最严格交集) ──
    all_starts = [w[0] for w in (metric_window, log_window, trace_window) if w[0]]
    all_ends = [w[1] for w in (metric_window, log_window, trace_window) if w[1]]
    if all_starts and all_ends:
        consensus_window = f"{max(all_starts).isoformat()} ~ {min(all_ends).isoformat()}"
    else:
        consensus_window = ''

    # ── verdict ──
    aligned_count = sum([metric_log_aligned, log_trace_aligned, trace_metric_aligned])
    has_metric = bool(metric_anomalies)
    has_log = bool(log_hits)
    has_trace = bool(trace_outliers)
    src_count = sum([has_metric, has_log, has_trace])

    if src_count < 2:
        verdict = f'数据不足(只有 {src_count} 路有效),无法做三向交叉;补齐另外两路再判'
        confidence = 'low'
    elif src_count == 3 and aligned_count >= 2 and consensus_service:
        verdict = f'三向高度一致,共识指向服务 `{consensus_service}`,时间窗 {consensus_window}'
        confidence = 'high'
    elif src_count >= 2 and aligned_count >= 1 and consensus_service:
        verdict = f'两向一致(三向中一向缺/不齐),共识 `{consensus_service}`'
        confidence = 'medium'
    else:
        verdict = '数据有但不一致(时间窗错位 / service 不重合),根因可能不在单一服务'
        confidence = 'low'

    return {
        'incident_window': payload.get('incident_window', ''),
        'metric_anomalies': metric_anomalies,
        'log_pattern_hits': log_hits,
        'trace_outliers': trace_outliers,
        'time_windows': {
            'metric': [w.isoformat() if w else None for w in metric_window],
            'log': [w.isoformat() if w else None for w in log_window],
            'trace': [w.isoformat() if w else None for w in trace_window],
        },
        'service_sets': {
            'metric': sorted(metric_services),
            'log': sorted(log_services),
            'trace': sorted(trace_services),
        },
        'correlation': {
            'metric_log_aligned': metric_log_aligned,
            'log_trace_aligned': log_trace_aligned,
            'trace_metric_aligned': trace_metric_aligned,
            'aligned_count': aligned_count,
            'consensus_service': consensus_service,
            'consensus_window': consensus_window,
            'confidence': confidence,
            'verdict': verdict,
        },
        'notes': notes,
    }


def main() -> None:
    p = argparse.ArgumentParser(prog='triangulate.py', description='三向交叉(metric/log/trace)对齐 + 相关性判定。')
    p.add_argument('--env', required=False, help='环境名,纯标记用')
    p.add_argument('--service', required=False, help='主角服务名,纯标记用')
    p.add_argument('--start', default='', help='故障开始时间')
    p.add_argument('--end', default='', help='故障结束时间')
    p.add_argument('--baseline-window', default='24h', help='跟历史同时段对比的窗口大小(纯标记)')
    p.add_argument('--from-stdin', action='store_true',
                   help='从 stdin 读 JSON payload(三路数据已外部拉好,本脚本只做对齐)')
    args = p.parse_args()

    if args.from_stdin:
        try:
            payload = json.load(sys.stdin)
        except Exception as e:
            fail('bad-stdin', f'stdin 不是合法 JSON: {e}')
    else:
        # 没给 stdin 就只展示"该怎么喂数据"的范式,让 agent 知道下一步
        payload = {
            'incident_window': f'{args.start or "<start>"} ~ {args.end or "<end>"}',
            'metric_anomalies': [],
            'log_pattern_hits': [],
            'trace_outliers': [],
        }
        result = analyze(payload)
        result['notes'].append(
            'usage: 先用 PromQL/Grafana MCP 拉指标突变;'
            'LogQL/ELK 拉错误模式(走 known-errors.yaml grep);'
            'Jaeger/Tempo 拉慢/错 trace。三路数据按下面的 schema 拼成一份 JSON,'
            '再 `python3 triangulate.py --from-stdin <<<EOF ...EOF` 跑相关性判定。'
        )
        result['expected_payload_schema'] = {
            'incident_window': '"<start> ~ <end>" 字符串,纯标记',
            'metric_anomalies': [{
                'metric': 'p99 | qps | error_rate ...',
                'target_service': '指标对应的服务名(LogQL/PromQL selector 解出)',
                'baseline': '24h 同时段值',
                'actual': '现在的值',
                'delta': '+10x / +500ms / -50%',
                'start_ts': 'ISO 时间(突变开始)',
                'end_ts': 'ISO 时间(突变结束,可选)',
            }],
            'log_pattern_hits': [{
                'pattern': 'OOMKilled | context deadline exceeded ...(known-errors.yaml 命中的)',
                'target_service': '日志来自哪个服务',
                'count': 'pattern 命中条数',
                'first_seen': 'ISO 时间',
                'last_seen': 'ISO 时间',
                'sample_message': '日志原文样本(可选,1-2 行)',
            }],
            'trace_outliers': [{
                'trace_id': 'xxx',
                'target_service': '入口服务名',
                'duration_ms': '总耗时',
                'critical_span': {
                    'service': '关键 span 所在服务(往下钻的对象)',
                    'operation': 'span 名',
                    'duration_ms': '该 span 耗时',
                },
                'start_ts': 'ISO',
                'end_ts': 'ISO(可选)',
            }],
        }
        print(json.dumps(result, ensure_ascii=False, indent=2), flush=True)
        return

    result = analyze(payload)
    print(json.dumps(result, ensure_ascii=False, indent=2), flush=True)


if __name__ == '__main__':
    main()
