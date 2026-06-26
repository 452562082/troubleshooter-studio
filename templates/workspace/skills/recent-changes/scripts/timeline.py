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
    info = _find_repo_entry(workspace_root, service)
    return info.get('local_path', '') if info else ''


def find_umbrella_path(workspace_root: Path, service: str) -> str:
    """umbrella 子模块场景:本服务 yaml 里有 parent_repo X,返回 X 的本地路径。
    没 parent_repo → 返空。给 collect_git_log 做'扫子模块时同时扫 umbrella'用。
    umbrella commit 改 .gitmodules pin / 共享 lib / 公共配置都会影响子模块,
    单独看子模块 git log 漏这部分,排障第一原则'先看最近改了什么'就漏了。"""
    info = _find_repo_entry(workspace_root, service)
    if not info:
        return ''
    parent = info.get('parent_repo', '').strip()
    if not parent:
        return ''
    parent_info = _find_repo_entry(workspace_root, parent)
    return parent_info.get('local_path', '') if parent_info else ''


def _find_repo_entry(workspace_root: Path, service: str) -> dict:
    """简单解析 repo-path-map.yaml 找 <service> 块的所有 K:V(local_path / parent_repo / ...)。"""
    p = workspace_root / 'skills' / 'routing' / 'references' / 'repo-path-map.yaml'
    if not p.exists():
        return {}
    text = p.read_text(encoding='utf-8', errors='ignore')
    in_block = False
    out: dict = {}
    for line in text.splitlines():
        # 4 空格缩进 = 块内 K:V;2 空格缩进 + 冒号结尾 = 服务名声明
        if not line.startswith(' '):
            in_block = False
            continue
        s = line.strip()
        if s.startswith('#') or not s:
            continue
        # 服务名声明行: "  <name>:"
        m = re.match(r'^  ([\w\-\._]+):\s*$', line)
        if m:
            in_block = (m.group(1) == service)
            continue
        # 块内 K:V: "    key: \"value\""
        if in_block:
            m = re.match(r'    ([\w\-\._]+):\s*["\']?([^"\']*)["\']?\s*$', line)
            if m:
                out[m.group(1)] = m.group(2).strip()
    return out


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
            # _sha 是 underscore prefix 表示"内部字段",JSON 输出会带,但下游 LLM 一般不用直接看,
            # 主要给 _fetch_git_diff 用 — 不存这个就得从 summary 反爬 short SHA + git rev-parse,绕。
            '_sha': sha,
            '_repo_path': repo_path,
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


# config-map.yaml 里每个 service 的 `runtime:` 字段 → 后端类型。多源场景下 per-service
# config_source 会覆盖顶层 config_center 主源,所以要按服务自己的 runtime 判,不能只看顶层。
_RUNTIME_TO_CC = {
    'nacos-mcp': 'nacos', 'apollo-http': 'apollo', 'consul-http': 'consul',
    'kuboard-http': 'kuboard', 'one2all-mcp': 'one2all',
}


def _detect_service_cc_type(cm_text: str, env: str, service: str) -> str:
    """从 config-map.yaml 找 environments.<env>.<service>.runtime,映射成后端类型。
    找不到返 ''(调用方回落顶层 config_center 主源)。"""
    in_env, in_svc, indent_env, indent_svc = False, False, -1, -1
    for raw in cm_text.splitlines():
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
                m = re.match(r'runtime:\s*["\']?([\w\-]+)', s)
                if m:
                    return _RUNTIME_TO_CC.get(m.group(1).strip(), '')
    return ''


def collect_config_history(env: str, service: str, ws_root: Path,
                            since: timedelta) -> tuple[list[dict[str, Any]], str]:
    """根据 routing/references/config-map.yaml 找配置后端类型 + namespace/dataId,
    分发到对应 config_history 脚本(nacos / apollo / consul)。

    nacos:nacos_config.py history(走 namespace/group/dataId)
    apollo:apollo_config.py history(走 appId/cluster/namespace)
    consul:consul_config.py history(走 kv_prefix + key)
    one2all / kuboard:无原生 config history API → 返空 + **显式 note**(别静默,提醒手动核对)。
    其它后端(env-vars / kubernetes ConfigMap)无原生 history,返空 + note。

    后端判定优先级(2026-06 修):先按 **本 service 的 runtime 字段**(多源场景 per-service
    config_source 覆盖主源),拿不到再回落顶层 `config_center` 主源。旧版只读顶层主源 →
    (a) 多源时副源服务被错按主源解析 → 静默返空;(b) one2all/kuboard 主源系统配置变更
    在 Step 2 时间轴完全不采集 → 盲区。"""
    cm = ws_root / 'skills' / 'routing' / 'references' / 'config-map.yaml'
    if not cm.exists():
        return [], 'config-map.yaml 不存在,跳过配置 history'
    text = cm.read_text(encoding='utf-8', errors='ignore')
    cc_type = _detect_service_cc_type(text, env, service)
    if not cc_type:
        for marker in ('nacos', 'apollo', 'consul', 'kuboard', 'one2all'):
            if f'config_center: {marker}' in text:
                cc_type = marker
                break
    if cc_type in ('one2all', 'kuboard'):
        return [], (f'{service} 配置走 {cc_type},该后端无原生 config history 接口;'
                    f'Step 2 时间轴此维缺失 —— 请手动核对 {cc_type} 控制台的配置变更时间,'
                    '别当"无配置变更"漏掉')
    if cc_type not in ('nacos', 'apollo', 'consul'):
        return [], '当前后端非 nacos/apollo/consul(env-vars/k8s ConfigMap 无原生 history),跳过'
    if cc_type != 'nacos':
        # 走通用解析:apollo / consul history
        return _collect_apollo_consul_history(env, service, ws_root, since, cc_type, text)
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


# ── 危险变更模式库 ──────────────────────────────────────────────────────
# diff 文本扫这些 regex,命中给 event.diff_risks 字段标 risk 类型 + severity。
# agent 拿到 diff_risks 直接知道这是危险变更,不用 LLM 心算"超时变小风险有多大"。
_DIFF_RISK_PATTERNS: list[dict[str, Any]] = [
    # ── RPC / HTTP 超时变小 → 上游全 timeout ──
    {
        'risk': 'timeout_decreased',
        'severity': 'high',
        # 匹配 -timeout: <bigger>\n+timeout: <smaller>(基础形态;同时支持 read-timeout / connect-timeout / rpc.timeout 等)
        'patterns': [
            r'-\s*([\w\._]*timeout[\w\._]*)\s*:\s*([\d\.]+)(s|ms|m|h)?',
            r'-\s*([\w\._]*Timeout[\w\._]*)\s*:\s*([\d\.]+)(s|ms|m|h)?',
        ],
        'hint': 'RPC/HTTP 超时阈值变小,如果新值比下游真实 p99 小,上游会全 timeout 引发 5xx 雪崩',
    },
    # ── 限流 / QPS 阈值改小 ──
    {
        'risk': 'rate_limit_decreased',
        'severity': 'high',
        'patterns': [
            r'-\s*([\w\._]*(?:rate[\-_]?limit|qps[\-_]?limit|tps|max[\-_]?qps)[\w\._]*)\s*:\s*\d+',
        ],
        'hint': '限流阈值变小,业务流量超过新阈值就会被拒;先看流量基线再调',
    },
    # ── 连接池 / 最大连接数改 ──
    {
        'risk': 'pool_size_changed',
        'severity': 'medium',
        'patterns': [
            r'-\s*([\w\._]*(?:max[\-_]?(?:pool|connection|idle)|pool[\-_]?size)[\w\._]*)\s*:\s*\d+',
            r'-\s*([\w\._]*max[\-_]?conn[\w\._]*)\s*:\s*\d+',
        ],
        'hint': '连接池配置改;池太小会 Too many connections,太大会 DB 打满',
    },
    # ── K8s replicas 改少 ──
    {
        'risk': 'replicas_decreased',
        'severity': 'high',
        'patterns': [
            r'-\s*replicas\s*:\s*(\d+)',
        ],
        'hint': '副本数减少,容量下降;高峰期可能扛不住流量',
    },
    # ── 资源 limit 改小 ──
    {
        'risk': 'resource_limit_decreased',
        'severity': 'medium',
        'patterns': [
            r'-\s*(memory|cpu)\s*:\s*[\d\.]+(Mi|Gi|m)',
        ],
        'hint': '资源 limit 减小,可能引发 OOMKilled / CPU throttle',
    },
    # ── 路由 / 下游地址改 ──
    {
        'risk': 'downstream_url_changed',
        'severity': 'high',
        'patterns': [
            r'-\s*([\w\._]*(?:url|endpoint|host|addr|target)[\w\._]*)\s*:\s*["\']?https?://',
        ],
        'hint': '下游服务地址改了;新地址不可达时调用全失败',
    },
    # ── 数据库 DSN / 主从切换 ──
    {
        'risk': 'database_endpoint_changed',
        'severity': 'critical',
        'patterns': [
            r'-\s*([\w\._]*(?:dsn|jdbc[\-_]?url|database[\-_]?url|mongo[\-_]?uri|redis[\-_]?url)[\w\._]*)\s*:',
        ],
        'hint': '数据库连接串改;主从切换 / 库迁移场景常见,可能引发数据不一致 / 连接失败',
    },
    # ── 鉴权 / 密钥改 ──
    {
        'risk': 'auth_changed',
        'severity': 'high',
        'patterns': [
            r'-\s*([\w\._]*(?:secret|api[\-_]?key|token|password|appkey)[\w\._]*)\s*:',
        ],
        'hint': '鉴权凭证改;旧 token 没轮换的服务全部 401',
    },
    # ── 功能开关 / feature flag ──
    {
        'risk': 'feature_flag_toggled',
        'severity': 'medium',
        'patterns': [
            r'-\s*([\w\._]*(?:enabled|enable|disable|switch|flag)[\w\._]*)\s*:\s*(true|false)',
        ],
        'hint': '开关类配置改;突然开启/关闭某能力可能影响业务行为',
    },
    # ── 重试次数改 ──
    {
        'risk': 'retry_changed',
        'severity': 'medium',
        'patterns': [
            r'-\s*([\w\._]*(?:retry|retries|max[\-_]?retry)[\w\._]*)\s*:\s*\d+',
        ],
        'hint': '重试次数改;变小易 5xx,变大可能放大下游故障',
    },
    # ── 熔断 / 降级 阈值改 ──
    {
        'risk': 'circuit_breaker_changed',
        'severity': 'medium',
        'patterns': [
            r'-\s*([\w\._]*(?:circuit[\-_]?breaker|fallback|fuse|degrad)[\w\._]*)\s*:',
        ],
        'hint': '熔断/降级配置改;不当配置会放大失败传播',
    },
    # ── HPA / 弹性伸缩 改 ──
    {
        'risk': 'hpa_changed',
        'severity': 'medium',
        'patterns': [
            r'-\s*(min[\-_]?replicas|max[\-_]?replicas|targetCPU|targetMemory)\s*:',
        ],
        'hint': 'HPA 配置改;扩缩容策略变化会影响容量响应',
    },

    # ─────────────────────────────────────────────────────────────────
    # 以下是**代码 diff 专用**模式(2026-05 加,补 git commit 维度)。配置 yaml 不会触这些。
    # 配 nacos 那 12 类几乎都对应"运维/SRE 改配置"型故障;下面这 5 类对应"开发改代码"型故障,
    # 实战占故障的另一半,只扫配置不扫代码是明显瘸腿。
    # 设计原则:只覆盖**误报率低的强 signal** —— 用户复审时如果一半 diff_risks 是噪音,标记功能反而没人信,得不偿失。
    # ─────────────────────────────────────────────────────────────────

    # ── 删了 retry 装饰器 / 注解 ──
    # Java @Retryable / Spring Retry / @CircuitBreaker / Hystrix / Sentinel,Python @retry / tenacity,Go middleware.Retry
    {
        'risk': 'retry_decorator_removed',
        'severity': 'high',
        'patterns': [
            r'^-\s*@Retryable\b',                              # Spring Retry
            r'^-\s*@retry\b',                                  # Python tenacity 等
            r'^-\s*\.retry\(',                                 # Go/JS 链式调用(rare)
            r'^-\s*Retry\.\s*[Ww]hen\b',                       # tenacity / RxJS
        ],
        'hint': '删了 retry 装饰器/调用,瞬时网络抖动 / 下游短暂不可用直接外抛 5xx;先回滚此 commit 或单独补回 retry',
    },

    # ── 删了熔断 / 限流 / 降级 装饰器 ──
    {
        'risk': 'circuit_breaker_removed',
        'severity': 'high',
        'patterns': [
            r'^-\s*@CircuitBreaker\b',                         # Resilience4j
            r'^-\s*@HystrixCommand\b',                         # Netflix Hystrix
            r'^-\s*@SentinelResource\b',                       # Alibaba Sentinel
            r'^-\s*@RateLimiter\b',                            # Resilience4j RateLimiter
        ],
        'hint': '删了熔断/限流/降级装饰器,下游异常不再被隔离 → 单点故障会传导成全链路雪崩',
    },

    # ── 删了缓存注解 / 缓存层 ──
    # 删 @Cacheable 后所有命中原本由 cache 兜的请求会全部打 DB,DB 负载瞬间翻几倍
    {
        'risk': 'cache_annotation_removed',
        'severity': 'high',
        'patterns': [
            r'^-\s*@Cacheable\b',                              # Spring Cache
            r'^-\s*@CachePut\b',
        ],
        'hint': '删了 @Cacheable,原本由缓存兜底的请求会直接打 DB,DB QPS / 连接数瞬间放大几倍',
    },

    # ── SQL 前缀通配 LIKE '%xxx' / '%xxx%' ──
    # 加这种 LIKE 几乎必定全表扫,业务一上量就慢查询炸 DB。
    # pattern 设计宽松:LIKE 后第一个引号紧跟 % 即触发(覆盖纯 SQL + Java/Go 字符串拼接两种)。
    {
        'risk': 'sql_prefix_wildcard_added',
        'severity': 'high',
        'patterns': [
            r'^\+.*\bLIKE\s+["\']\s*%',                        # +.. LIKE '%...   或  Java "...LIKE '%" + var
            r"^\+.*\bLIKE\s+CONCAT\(\s*['\"]\s*%",              # +.. LIKE CONCAT('%', ...
        ],
        'hint': "新增 LIKE '%xxx' 前缀通配 → 索引失效全表扫,业务量起来后 DB 必慢查询;改 LIKE 'xxx%' 或上 ES/搜索引擎",
    },

    # ── async 改 sync(去掉 await / async 关键字)──
    # 异步函数被改回同步,在事件循环里阻塞,QPS 直接掉一个数量级
    {
        'risk': 'async_to_sync',
        'severity': 'medium',
        'patterns': [
            r'^-\s*async\s+def\s+\w+',                         # Python async def 被删
            r'^-\s*async\s+function\s+\w+',                    # JS async function 被删
            r'^-\s*await\s+\w+',                               # await xxx 被删(很多对应 # 改回同步)
        ],
        'hint': 'async 改 sync,事件循环里同步阻塞会掉吞吐;先看是否真要同步(可能误改),否则单独补回 async/await',
    },
]


def _classify_diff_risks(diff_text: str) -> list[dict[str, str]]:
    """diff 文本扫所有危险模式,返回命中的 risk 列表(去重)。"""
    if not diff_text:
        return []
    hits: dict[str, dict[str, str]] = {}
    for entry in _DIFF_RISK_PATTERNS:
        for pat in entry['patterns']:
            if re.search(pat, diff_text, re.MULTILINE | re.IGNORECASE):
                hits[entry['risk']] = {
                    'risk': entry['risk'],
                    'severity': entry['severity'],
                    'hint': entry['hint'],
                }
                break
    return list(hits.values())


def _collect_apollo_consul_history(env: str, service: str, ws_root: Path,
                                    since: timedelta, cc_type: str,
                                    cm_text: str) -> tuple[list[dict[str, Any]], str]:
    """apollo / consul history。两种后端的 routing config-map.yaml 结构不同,
    分别按文本解析抽 (env, service) 对应的字段(apollo: appId/cluster/namespace;
    consul: kv_prefix + key),然后调对应 _config.py history。"""
    script_name = f'{cc_type}_config.py'
    cfg_script = ws_root / 'skills' / 'config-executor' / 'scripts' / script_name
    if not cfg_script.exists():
        return [], f'{script_name} 不存在'

    up = env.upper().replace('-', '_')
    # 凭证:nacos/apollo/consul 共用 CC_*_<ENV> 命名约定;具体走 creds.json fallback 也在
    # 各 _config.py 里实现;timeline 这层只传 server 必需值。
    server_var = {
        'apollo': 'APOLLO_META_' + up,
        'consul': 'CONSUL_HOST_' + up,
    }.get(cc_type, '')
    server = os.environ.get(server_var, '') or os.environ.get(f'CC_ADDR_{up}', '')
    token = os.environ.get(f'{cc_type.upper()}_TOKEN_{up}', '') or os.environ.get(f'CC_TOKEN_{up}', '')

    # 解析 config-map.yaml 找 (env, service) 配置标识
    in_env, in_svc, indent_env, indent_svc = False, False, -1, -1
    apollo_app, apollo_cluster, apollo_ns = '', '', ''
    consul_key = ''
    for raw in cm_text.splitlines():
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
                if cc_type == 'apollo':
                    if m := re.match(r'(appId|cluster|namespaces?):\s*["\']?([^"\'\[\]]+)', s):
                        if m.group(1) == 'appId':
                            apollo_app = m.group(2).strip()
                        elif m.group(1) == 'cluster':
                            apollo_cluster = m.group(2).strip()
                        elif m.group(1).startswith('namespace'):
                            apollo_ns = m.group(2).strip()
                if cc_type == 'consul':
                    if m := re.match(r'(kv_prefix|key|kvPath):\s*["\']?([^"\'\s]+)', s):
                        if not consul_key:
                            consul_key = m.group(2).strip()

    if not server:
        return [], f'{server_var} / CC_ADDR_{up} 缺失,跳过 {cc_type} history'

    args: list[str]
    if cc_type == 'apollo':
        if not (apollo_app and apollo_ns):
            return [], f'apollo:{env}/{service} 没在 config-map 里找到 appId/namespaces'
        args = [sys.executable, str(cfg_script), 'history',
                '--meta-url', server, '--app-id', apollo_app,
                '--cluster', apollo_cluster or 'default',
                '--namespace', apollo_ns, '--env', env]
        if token:
            args += ['--token', token]
    else:  # consul
        if not consul_key:
            return [], f'consul:{env}/{service} 没在 config-map 里找到 kv_prefix/key'
        args = [sys.executable, str(cfg_script), 'history',
                '--host', server, '--key', consul_key]
        if token:
            args += ['--token', token]

    try:
        out = subprocess.check_output(args, stderr=subprocess.STDOUT, timeout=20).decode('utf-8', errors='ignore')
        data = json.loads(out)
    except subprocess.CalledProcessError as e:
        return [], f'{script_name} history failed: {e.output.decode("utf-8", errors="ignore")[:200]}'
    except Exception as e:
        return [], f'{cc_type} history error: {e}'

    items = data.get('items') or data.get('history') or data.get('releases') or []
    cutoff = datetime.now(timezone.utc) - since
    events = []
    for it in items:
        ts = it.get('lastModifiedTime') or it.get('modifiedTime') or it.get('time') or it.get('ModifyIndex')
        if not ts:
            continue
        try:
            if isinstance(ts, (int, float)) or (isinstance(ts, str) and ts.isdigit()):
                t = datetime.fromtimestamp(int(ts) / 1000 if int(ts) > 1e10 else int(ts), tz=timezone.utc)
                ts_iso = t.isoformat()
            else:
                t = datetime.fromisoformat(str(ts).replace('Z', '+00:00'))
                ts_iso = str(ts)
        except Exception:
            continue
        if t < cutoff:
            continue
        if cc_type == 'apollo':
            summary = f"{apollo_app}/{apollo_cluster}/{apollo_ns} {it.get('opType', 'change')} by {it.get('dataChangeLastModifiedBy', '?')}"
        else:
            summary = f"{consul_key} change by {it.get('Session', '?')}"
        events.append({
            'ts': ts_iso,
            'source': cc_type,
            'kind': it.get('opType') or it.get('Operation') or 'change',
            'summary': summary,
        })
    return events, ''


def _fetch_git_diff(repo_path: str, sha: str, max_lines: int = 400) -> str | None:
    """correlated git commit → 拉本 commit 的完整 diff(--no-color, no merge),给 _classify_diff_risks 用。

    返回值:
      - 字符串:diff 文本(已截到 max_lines 行,大 commit 防卡 LLM context)
      - None:仓库路径不存在 / git show 失败 / sha 不可用
      - "":空 diff(merge commit / revert 后还原 等罕见情况)
    """
    if not repo_path or not Path(repo_path).exists() or not sha:
        return None
    try:
        # --no-color:CI 默认开 color 会污染 grep;-U2:每段 diff 给 2 行上下文(够 pattern 匹配,够小);
        # --no-merges 在这里没意义(单 commit show),不加。
        out = subprocess.check_output(
            ['git', '-C', repo_path, 'show', '--no-color', '-U2', '--format=', sha],
            stderr=subprocess.STDOUT, timeout=10,
        ).decode('utf-8', errors='ignore')
    except Exception:
        return None
    if not out.strip():
        return ''
    lines = out.splitlines()
    if len(lines) > max_lines:
        lines = lines[:max_lines] + [f'... (truncated, total {len(out.splitlines())} lines)']
    return '\n'.join(lines)


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
    p.add_argument('--skip-git-diff', action='store_true', help='跳过 correlated git 事件的 commit diff 抓取(默认会抓 + 扫代码危险模式)')
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

    # umbrella 子模块场景:本服务从某 umbrella 切出去 → 同时扫 umbrella 自己的 git log。
    # umbrella commit 可能改 .gitmodules pin / 公共 lib / 共享 config,直接影响本子模块行为,
    # 漏看会让"故障 ±5 分钟内最近变更"的关键信号缺失。每条 umbrella commit 加 [umbrella] 前缀。
    umbrella_path = find_umbrella_path(ws_root, args.service)
    if umbrella_path:
        u_events, u_err = collect_git_log(umbrella_path, since_td, args.branch)
        if u_err:
            notes.append(f'[git-umbrella] {u_err}')
        for ev in u_events:
            ev['summary'] = f'[umbrella] {ev.get("summary", "")}'
        all_events += u_events

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
                # 危险变更分类:diff 里 grep 危险模式,命中给 diff_risks 数组
                risks = _classify_diff_risks(diff)
                if risks:
                    e['diff_risks'] = risks
            elif diff is None:
                notes.append(f'[nacos-diff] 无法对比 {e.get("summary", "")} 前后版本(脚本不可用 / API 不返历史 content)')

    # correlated 的 git commits 自动拉 commit 完整 diff + 走危险模式扫描。
    # 跟 nacos 一路对齐 — 但 git diff 生命周期更长,patterns 也不一样:
    # 配置 yaml 模式只命中 nacos 等 yaml diff;@Retryable / @Cacheable 删除 / LIKE '%xxx' 等
    # 代码模式只在 git diff 命中。一份 _classify_diff_risks 同时跑两边没冲突(patterns 是 OR)。
    if incident_dt and not args.skip_git_diff:
        for e in all_events:
            if not e.get('correlated') or e.get('source') != 'git':
                continue
            sha = e.pop('_sha', '')  # pop 掉,不输出 underscore 字段到下游
            rp = e.pop('_repo_path', '')
            if not sha or not rp:
                continue
            diff = _fetch_git_diff(rp, sha)
            if diff:
                e['diff'] = diff
                risks = _classify_diff_risks(diff)
                if risks:
                    e['diff_risks'] = risks
            elif diff is None:
                notes.append(f'[git-diff] 无法拉 {sha[:8]} 的 diff(repo path 不存在 / git show 失败)')

    # 没设 incident_time 时 _sha / _repo_path 仍残留在 events 里(JSON 输出会带 underscore 字段),
    # 删掉保持输出干净 — 它们仅 internal 用。
    for e in all_events:
        e.pop('_sha', None)
        e.pop('_repo_path', None)

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
