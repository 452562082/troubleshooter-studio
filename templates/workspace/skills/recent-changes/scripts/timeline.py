#!/usr/bin/env python3
"""
timeline.py —— 故障窗口"最近变更"聚合脚本。

incident-investigator Step 2(时间轴对齐)的核心工具。一次拉:
  1. K8s rollout history(本服务 Deployment 的 ReplicaSet 序列)—— 部署窗口
  2. 配置中心 history(nacos / apollo / consul)—— 配置改动窗口
  3. git log(本服务对应仓库的 main 分支)—— 代码合并窗口

按时间倒序合并输出,标"故障时间 ±5 分钟内的强相关变更"。

用法:
  python3 timeline.py --env prod --service commerce --since 1h \\
                     [--cluster <c>] [--namespace <ns>] \\
                     [--repo-path /path/to/commerce-repo] \\
                     [--incident-time "2025-04-29 14:23"]

输出:JSON,字段 `events: [{ts, source, kind, summary}]`
凭证读取:同 k8s_query.py(env vars / creds.json 自动检测 OpenClaw/Claude Code/Cursor 部署上下文)。

注意:
- nacos/apollo history 走对应 config-executor scripts(nacos_config.py history / apollo_config.py history)
- git log 默认查 routing/references/repo-path-map.yaml 找仓库本地路径,fallback 到 --repo-path
- 任一来源拉不到不阻塞其它,error 进 notes 字段
"""

import argparse
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any


def fail(error: str, hint: str = '') -> None:
    print(json.dumps({'error': error, 'hint': hint}, ensure_ascii=False), flush=True)
    sys.exit(1)


def parse_since(since: str) -> timedelta:
    """`1h` / `30m` / `2d` / `300s`。"""
    m = re.fullmatch(r'(\d+)\s*([smhd])', since.strip())
    if not m:
        fail('bad-since', f'--since 格式 e.g. 1h / 30m / 2d, got {since!r}')
    n, unit = int(m.group(1)), m.group(2)
    return {'s': timedelta(seconds=n), 'm': timedelta(minutes=n),
            'h': timedelta(hours=n), 'd': timedelta(days=n)}[unit]


def detect_workspace_root() -> Path:
    """跟 k8s_query.py 同款检测:OpenClaw / Claude Code / Cursor / dev。"""
    here = Path(__file__).resolve()
    parts = here.parts
    # OpenClaw: ~/.openclaw/workspace/<ws>/skills/recent-changes/scripts/
    if '.openclaw' in parts and 'workspace' in parts:
        try:
            ws_idx = parts.index('workspace')
            return Path(*parts[: ws_idx + 2])
        except (ValueError, IndexError):
            pass
    # Claude Code / Cursor: <root>/.claude|cursor/skills/<id>/recent-changes/scripts/
    for marker in ('.claude', '.cursor'):
        if marker in parts:
            try:
                idx = parts.index(marker)
                if parts[idx + 1] == 'skills':
                    return Path(*parts[: idx + 3])
            except (ValueError, IndexError):
                pass
    return here.parent.parent.parent.parent


def find_repo_path(workspace_root: Path, service: str) -> str:
    """从 routing/references/repo-path-map.yaml 找仓库本地路径。
    简单文本解析,避免引 PyYAML 强依赖(stdlib 仍可装但用户机器装载不一定)。"""
    p = workspace_root / 'skills' / 'routing' / 'references' / 'repo-path-map.yaml'
    if not p.exists():
        return ''
    text = p.read_text(encoding='utf-8', errors='ignore')
    # 简单解析: "  <service>: \"<path>\""
    for line in text.splitlines():
        s = line.strip()
        if s.startswith('#') or not s:
            continue
        m = re.match(r'([\w\-\._]+):\s*["\']?([^"\']+)["\']?\s*$', s)
        if m and m.group(1) == service:
            return m.group(2).strip()
    return ''


def collect_git_log(repo_path: str, since: timedelta, branch: str = 'main') -> tuple[list[dict[str, Any]], str]:
    """git log --since=...:返回 events + 错误说明。"""
    if not repo_path or not Path(repo_path).exists():
        return [], f'repo-path not found: {repo_path or "(empty)"}'
    fmt = '%H%x09%cI%x09%an%x09%s'  # tab 分隔
    since_str = f'{int(since.total_seconds())}.seconds.ago'
    try:
        out = subprocess.check_output(
            ['git', '-C', repo_path, 'log', '--since', since_str, f'--pretty=format:{fmt}', branch],
            stderr=subprocess.STDOUT, timeout=15,
        ).decode('utf-8', errors='ignore')
    except subprocess.CalledProcessError as e:
        return [], f'git log failed: {e.output.decode("utf-8", errors="ignore")[:200]}'
    except Exception as e:
        return [], f'git log error: {e}'
    events = []
    for line in out.strip().splitlines():
        parts = line.split('\t', 3)
        if len(parts) < 4:
            continue
        sha, when, author, subject = parts
        events.append({
            'ts': when,
            'source': 'git',
            'kind': 'commit',
            'summary': f'{author}: {subject} ({sha[:8]})',
        })
    return events, ''


def collect_k8s_rollouts(env: str, service: str, cluster: str, namespace: str,
                         since: timedelta, ws_root: Path) -> tuple[list[dict[str, Any]], str]:
    """复用 k8s_query.py rollout-history。"""
    k8s_script = ws_root / 'skills' / 'k8s-runtime-query' / 'scripts' / 'k8s_query.py'
    if not k8s_script.exists():
        return [], f'k8s_query.py 不存在: {k8s_script}'
    if not (cluster and namespace):
        return [], 'cluster / namespace 没给,跳过 K8s rollout(从 routing service-map 读 deploy 名)'
    try:
        out = subprocess.check_output(
            [sys.executable, str(k8s_script),
             '--env', env, '--cluster', cluster, 'rollout-history',
             '--namespace', namespace, '--deployment', service],
            stderr=subprocess.STDOUT, timeout=30,
        ).decode('utf-8', errors='ignore')
    except subprocess.CalledProcessError as e:
        return [], f'k8s_query.py rollout-history failed: {e.output.decode("utf-8", errors="ignore")[:200]}'
    except Exception as e:
        return [], f'k8s_query.py error: {e}'
    try:
        data = json.loads(out)
    except Exception:
        return [], f'k8s rollout-history 输出不是 JSON: {out[:200]}'
    revisions = data.get('revisions') or []
    cutoff = datetime.now(timezone.utc) - since
    events = []
    for r in revisions:
        ts = r.get('created_at')
        if not ts:
            continue
        try:
            t = datetime.fromisoformat(ts.replace('Z', '+00:00'))
        except Exception:
            continue
        if t < cutoff:
            continue
        events.append({
            'ts': ts,
            'source': 'k8s',
            'kind': 'rollout',
            'summary': f"rollout to revision {r.get('revision', '?')} (replicas={r.get('replicas_desired')}, image={','.join(r.get('images') or [])[:120]})",
        })
    return events, ''


def collect_config_history(env: str, service: str, ws_root: Path,
                            since: timedelta) -> tuple[list[dict[str, Any]], str]:
    """根据 routing/references/config-map.yaml 找 namespace/dataId,然后调对应 config_history。

    简化实现:只支持 nacos(占主流),其它后端先返空 + note 说明。
    nacos history:走 scripts/nacos_config.py history(stdlib 实现的)。"""
    cm = ws_root / 'skills' / 'routing' / 'references' / 'config-map.yaml'
    if not cm.exists():
        return [], 'config-map.yaml 不存在,跳过配置 history'
    text = cm.read_text(encoding='utf-8', errors='ignore')
    if 'config_center: nacos' not in text:
        return [], '当前后端非 nacos,跳过(其它后端 history 暂不实现)'
    # 找 environments.<env>.<service>.{namespaceId,group,dataId}
    # 简单状态机解析,不引 PyYAML
    ns_id, group, data_id = '', '', ''
    in_env, in_svc, indent_env, indent_svc = False, False, -1, -1
    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith('#'):
            continue
        ind = len(raw) - len(raw.lstrip())
        s = raw.strip()
        if s == f'{env}:' or s.startswith(f'{env}:'):
            in_env, in_svc, indent_env = True, False, ind
            continue
        if in_env and ind <= indent_env and ':' in s and not s.startswith(env):
            in_env = False
        if in_env:
            if s.startswith(f'{service}:'):
                in_svc, indent_svc = True, ind
                continue
            if in_svc and ind <= indent_svc and ':' in s and not s.startswith(service):
                in_svc = False
            if in_svc:
                m = re.match(r'(namespaceId|group|dataId):\s*["\']?([^"\']+)["\']?', s)
                if m:
                    if m.group(1) == 'namespaceId':
                        ns_id = m.group(2).strip()
                    elif m.group(1) == 'group':
                        group = m.group(2).strip()
                    elif m.group(1) == 'dataId':
                        data_id = m.group(2).strip()
    if not data_id:
        return [], f'config-map 里没找到 {env}/{service} 的 dataId'

    nacos_script = ws_root / 'skills' / 'config-executor' / 'scripts' / 'nacos_config.py'
    if not nacos_script.exists():
        return [], 'nacos_config.py 不存在'
    # 凭证从环境变量推:NACOS_ADDR_<ENV> / NACOS_USERNAME_<ENV> / NACOS_PASSWORD_<ENV>
    up = env.upper().replace('-', '_')
    server = os.environ.get(f'NACOS_ADDR_{up}', '') or os.environ.get(f'CC_ADDR_{up}', '')
    user = os.environ.get(f'NACOS_USERNAME_{up}', '') or os.environ.get(f'CC_USER_{up}', '')
    pwd = os.environ.get(f'NACOS_PASSWORD_{up}', '') or os.environ.get(f'CC_PASS_{up}', '')
    if not server:
        return [], f'NACOS_ADDR_{up} / CC_ADDR_{up} 不在环境变量,跳过 nacos history(用户先 source .env)'
    args = [sys.executable, str(nacos_script), 'history',
            '--server', server, '--namespace', ns_id, '--group', group or 'DEFAULT_GROUP',
            '--data-id', data_id]
    if user:
        args += ['--user', user]
    if pwd:
        args += ['--pass', pwd]
    try:
        out = subprocess.check_output(args, stderr=subprocess.STDOUT, timeout=20).decode('utf-8', errors='ignore')
    except subprocess.CalledProcessError as e:
        return [], f'nacos_config.py history failed: {e.output.decode("utf-8", errors="ignore")[:200]}'
    except Exception as e:
        return [], f'nacos history error: {e}'
    # nacos_config.py 输出 JSON
    try:
        data = json.loads(out)
    except Exception:
        return [], f'nacos history 输出不是 JSON: {out[:200]}'
    # 期待形态:{"ok":true,"items":[{...,"lastModifiedTime":"...","opType":"U/I"}]}
    items = data.get('items') or data.get('history') or []
    cutoff = datetime.now(timezone.utc) - since
    events = []
    for it in items:
        ts = it.get('lastModifiedTime') or it.get('modified_time') or it.get('time')
        if not ts:
            continue
        try:
            # nacos 返毫秒时间戳常见
            if isinstance(ts, (int, float)) or ts.isdigit():
                t = datetime.fromtimestamp(int(ts) / 1000, tz=timezone.utc)
                ts_iso = t.isoformat()
            else:
                t = datetime.fromisoformat(str(ts).replace('Z', '+00:00'))
                ts_iso = str(ts)
        except Exception:
            continue
        if t < cutoff:
            continue
        events.append({
            'ts': ts_iso,
            'source': 'nacos',
            'kind': it.get('opType') or 'change',
            'summary': f"{ns_id}/{group}/{data_id} {it.get('opType', 'change')} by {it.get('srcUser', '?')}",
        })
    return events, ''


def _fetch_nacos_diff(env: str, event: dict[str, Any], ws_root: Path) -> str | None:
    """correlated nacos history event → 拉前后两版 content 算 unified diff,塞进 event.diff。

    返回值:
      - 字符串:成功的 diff(已 truncate 到前 100 行)
      - None:脚本/API 不可用 / 拉不到历史 content / 不是 update 类型
      - "":没差异(罕见,正常情况 nacos 改动一定有 diff)
    """
    summary = event.get('summary', '')
    # summary 形如 "<ns>/<group>/<dataId> U by <user>",抽出 ns/group/dataId
    m = re.match(r'(\S+?)/(\S+?)/(\S+?)\s+', summary)
    if not m:
        return None
    ns_id, group, data_id = m.group(1), m.group(2), m.group(3)
    nacos_script = ws_root / 'skills' / 'config-executor' / 'scripts' / 'nacos_config.py'
    if not nacos_script.exists():
        return None
    up = env.upper().replace('-', '_')
    server = os.environ.get(f'NACOS_ADDR_{up}', '') or os.environ.get(f'CC_ADDR_{up}', '')
    user = os.environ.get(f'NACOS_USERNAME_{up}', '') or os.environ.get(f'CC_USER_{up}', '')
    pwd = os.environ.get(f'NACOS_PASSWORD_{up}', '') or os.environ.get(f'CC_PASS_{up}', '')
    if not server:
        return None

    base_args = [sys.executable, str(nacos_script), 'history',
                 '--server', server, '--namespace', ns_id, '--group', group,
                 '--data-id', data_id]
    if user:
        base_args += ['--user', user]
    if pwd:
        base_args += ['--pass', pwd]
    try:
        out = subprocess.check_output(base_args, stderr=subprocess.STDOUT, timeout=15).decode('utf-8', errors='ignore')
        data = json.loads(out)
    except Exception:
        return None
    items = data.get('items') or data.get('history') or []
    if len(items) < 2:
        return None  # 没有"前一版"
    # items 按时间倒序假设(nacos history 默认这样);前一版 = items[1]
    cur_id = items[0].get('id') or items[0].get('nid')
    prev_id = items[1].get('id') or items[1].get('nid')
    if not (cur_id and prev_id):
        return None

    def fetch_content(history_id: str) -> str:
        args_get = [sys.executable, str(nacos_script), 'get-history',
                    '--server', server, '--namespace', ns_id, '--group', group,
                    '--data-id', data_id, '--id', str(history_id)]
        if user:
            args_get += ['--user', user]
        if pwd:
            args_get += ['--pass', pwd]
        try:
            o = subprocess.check_output(args_get, stderr=subprocess.STDOUT, timeout=10).decode('utf-8', errors='ignore')
            d = json.loads(o)
            return d.get('content') or d.get('data', {}).get('content') or ''
        except Exception:
            return ''

    cur_content = fetch_content(str(cur_id))
    prev_content = fetch_content(str(prev_id))
    if not cur_content or not prev_content:
        return None  # nacos history get-content 不支持 / 权限不够

    import difflib
    diff_lines = list(difflib.unified_diff(
        prev_content.splitlines(), cur_content.splitlines(),
        fromfile='prev', tofile='cur', lineterm='', n=2,
    ))
    if not diff_lines:
        return ''
    return '\n'.join(diff_lines[:100])


def main() -> None:
    p = argparse.ArgumentParser(prog='timeline.py')
    p.add_argument('--env', required=True)
    p.add_argument('--service', required=True)
    p.add_argument('--since', default='1h', help='1h / 30m / 2d / 300s')
    p.add_argument('--cluster', default='', help='K8s cluster name(没给则跳过 K8s rollout)')
    p.add_argument('--namespace', default='', help='K8s namespace(没给则跳过 K8s rollout)')
    p.add_argument('--repo-path', default='', help='本地仓库路径覆盖(默认从 repo-path-map.yaml 读)')
    p.add_argument('--branch', default='main', help='git branch')
    p.add_argument('--incident-time', default='', help='故障开始时间 ISO,用来标 ±5 分钟相关变更')
    p.add_argument('--skip-nacos-diff', action='store_true', help='跳过 correlated nacos 事件的前后版本 diff 抓取(默认会抓)')
    args = p.parse_args()

    since_td = parse_since(args.since)
    ws_root = detect_workspace_root()

    notes = []
    all_events = []

    repo_path = args.repo_path or find_repo_path(ws_root, args.service)
    git_events, git_err = collect_git_log(repo_path, since_td, args.branch)
    if git_err:
        notes.append(f'[git] {git_err}')
    all_events += git_events

    k8s_events, k8s_err = collect_k8s_rollouts(args.env, args.service, args.cluster, args.namespace, since_td, ws_root)
    if k8s_err:
        notes.append(f'[k8s] {k8s_err}')
    all_events += k8s_events

    cfg_events, cfg_err = collect_config_history(args.env, args.service, ws_root, since_td)
    if cfg_err:
        notes.append(f'[config] {cfg_err}')
    all_events += cfg_events

    # 倒序
    def parse_ts(e: dict[str, Any]) -> datetime:
        try:
            return datetime.fromisoformat(str(e.get('ts', '')).replace('Z', '+00:00'))
        except Exception:
            return datetime.fromtimestamp(0, tz=timezone.utc)
    all_events.sort(key=parse_ts, reverse=True)

    # 故障时间 ±5 分钟内强相关标记
    incident_dt = None
    if args.incident_time:
        try:
            incident_dt = datetime.fromisoformat(args.incident_time.replace('Z', '+00:00'))
            if incident_dt.tzinfo is None:
                incident_dt = incident_dt.replace(tzinfo=timezone.utc)
        except Exception:
            notes.append(f'[incident-time] 解析失败: {args.incident_time}')
    if incident_dt:
        for e in all_events:
            t = parse_ts(e)
            if abs((t - incident_dt).total_seconds()) <= 300:
                e['correlated'] = True

    # correlated 的 nacos events 自动拉前后 diff:这一步把 agent 多一次 tool call 省掉
    if incident_dt and not args.skip_nacos_diff:
        for e in all_events:
            if not e.get('correlated') or e.get('source') != 'nacos':
                continue
            diff = _fetch_nacos_diff(args.env, e, ws_root)
            if diff:
                e['diff'] = diff
            elif diff is None:
                notes.append(f'[nacos-diff] 无法对比 {e.get("summary", "")} 前后版本(脚本不可用 / API 不返历史 content)')

    print(json.dumps({
        'env': args.env,
        'service': args.service,
        'since': args.since,
        'incident_time': args.incident_time or None,
        'event_count': len(all_events),
        'events': all_events,
        'notes': notes,
    }, ensure_ascii=False, indent=2), flush=True)


if __name__ == '__main__':
    main()
