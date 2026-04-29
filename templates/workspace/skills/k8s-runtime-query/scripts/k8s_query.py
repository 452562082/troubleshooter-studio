#!/usr/bin/env python3
"""
k8s_query.py —— 排障机器人调 Kuboard v4 HTTP API 的统一入口。

桌面 wizard 用 wails binding(KuboardListPods 等)实现同样能力,但部署到 OpenClaw /
Claude Code / Cursor 后机器人调不到 wails binding —— 必须有这个 Python 版兜底。

凭证读取顺序:
  1. CLI --url / --access-key / --username / --password(优先,调试用)
  2. 环境变量 KUBOARD_URL_<ENV> / KUBOARD_ACCESS_KEY_<ENV> / KUBOARD_USER_<ENV> / KUBOARD_PASS_<ENV>
  3. <agent-dir>/creds.json 里 kuboard.<source-id>.<env> 子节(主源 source-id="default")
agent-dir 默认推导规则:本脚本所在目录的祖父级(skills/k8s-runtime-query/scripts → 工作区根)。
也可以 --agent-dir 显式覆盖。

action 列表:
  list-pods --cluster <c> --namespace <ns> [--label-selector k=v] [--name-filter str]
  list-deployments --cluster <c> --namespace <ns>
  get-pod-logs --cluster <c> --namespace <ns> --pod <p> [--container <c>] [--previous] [--tail 200]
  list-events --cluster <c> --namespace <ns> [--field-selector involvedObject.name=<p>] [--only-warnings]
  list-services --cluster <c> --namespace <ns>
  pod-snapshot --cluster <c> --namespace <ns> [--label-selector k=v]   # 一站式:pods + events + 主 pod logs(curr/prev)

输出:JSON 到 stdout;失败:exit 1 + 一行人类可读 hint 到 stderr,JSON {error,hint} 到 stdout。
"""

import argparse
import json
import os
import sys
import urllib.parse
from pathlib import Path
from typing import Any

try:
    import requests
except ImportError:
    print('{"error":"requests-not-installed","hint":"pip3 install requests"}', flush=True)
    sys.exit(1)


SECRET_FIELDS = ('password', 'token', 'secret', 'privateKey', 'accessKey', 'api_key', 'authorization', 'bearer')


def redact(text: str) -> str:
    """日志/响应里把常见 secret 字段值替换成 ****;agent 打印时不漏密。"""
    out = text
    for f in SECRET_FIELDS:
        # 形如 "f": "value"
        out = _redact_quoted(out, f)
        # 形如 f=value
        out = _redact_eq(out, f)
    return out


def _redact_quoted(text: str, field: str) -> str:
    import re
    pattern = re.compile(r'"' + re.escape(field) + r'"\s*:\s*"[^"]*"', re.IGNORECASE)
    return pattern.sub(f'"{field}": "****"', text)


def _redact_eq(text: str, field: str) -> str:
    import re
    pattern = re.compile(r'\b' + re.escape(field) + r'=\S+', re.IGNORECASE)
    return pattern.sub(f'{field}=****', text)


def fail(error: str, hint: str = '') -> None:
    """统一的失败输出:JSON 到 stdout,exit 1。agent 看到 error/hint 直接复述给用户。"""
    print(json.dumps({'error': error, 'hint': hint}, ensure_ascii=False), flush=True)
    sys.exit(1)


def load_creds(env: str, agent_dir: str | None) -> dict[str, str]:
    """优先 env vars(KUBOARD_*_<ENV>),次 creds.json,返回 {url, access_key, username, password}。"""
    up = env.upper().replace('-', '_')
    creds = {
        'url': os.environ.get(f'KUBOARD_URL_{up}', ''),
        'access_key': os.environ.get(f'KUBOARD_ACCESS_KEY_{up}', ''),
        'username': os.environ.get(f'KUBOARD_USER_{up}', ''),
        'password': os.environ.get(f'KUBOARD_PASS_{up}', ''),
    }
    if any(creds.values()):
        return creds
    # creds.json fallback
    if agent_dir:
        cred_path = Path(agent_dir) / 'creds.json'
    else:
        # 默认:scripts 在 skills/k8s-runtime-query/scripts/,往上三级到工作区根
        cred_path = Path(__file__).resolve().parent.parent.parent.parent / 'creds.json'
    if not cred_path.exists():
        return creds
    try:
        data = json.loads(cred_path.read_text(encoding='utf-8'))
    except Exception as e:
        fail('bad-creds-json', f'{cred_path} 解析失败: {e}')
    # 多源 schema:{kuboard:{<source-id>:{<env>:{...}}}};单源 fallback:{kuboard:{<env>:{...}}}
    kbroot = (data or {}).get('kuboard') or {}
    if env in kbroot and isinstance(kbroot[env], dict):
        row = kbroot[env]
    else:
        # 多源:第一层 source-id,优先 "default"
        sid = 'default' if 'default' in kbroot else (next(iter(kbroot)) if kbroot else None)
        row = (kbroot.get(sid) or {}).get(env, {}) if sid else {}
    for k in ('url', 'access_key', 'username', 'password'):
        if not creds[k] and row.get(k):
            creds[k] = row[k]
    return creds


class KuboardClient:
    """Kuboard v4 HTTP 客户端:登录(若没 access_key) → cluster name → cluster_uid 解析 → direct API 调用。"""

    def __init__(self, url: str, access_key: str = '', username: str = '', password: str = '', cluster_name: str = ''):
        self.base = url.rstrip('/')
        if not self.base.startswith('http'):
            fail('bad-url', f'kuboard url 必须以 http(s):// 开头: {self.base}')
        self.access_key = access_key
        self.username = username
        self.password = password
        self.cluster_name = cluster_name
        self._token: str | None = None
        self._cluster_uid: str | None = None
        self.session = requests.Session()
        self.session.verify = False  # Kuboard 自签名 cert 常见
        # 抑制 requests 的 InsecureRequestWarning
        try:
            import urllib3
            urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
        except Exception:
            pass

    def token(self) -> str:
        if self._token:
            return self._token
        if self.access_key:
            self._token = self.access_key
            return self._token
        if not (self.username and self.password):
            fail('no-auth', '鉴权:填 access_key 或 username+password')
        try:
            r = self.session.post(
                self.base + '/api/login.kuboard.cn/v4/login',
                json={'username': self.username, 'password': self.password},
                timeout=10,
            )
        except Exception as e:
            fail('login-network', str(e))
        if r.status_code == 401:
            fail('login-401', '账号或密码错')
        if r.status_code >= 400:
            fail('login-http', f'HTTP {r.status_code}: {r.text[:200]}')
        body = r.json()
        tok = (body.get('data') or {}).get('accessToken', '')
        if not tok:
            fail('login-no-token', f'响应里没 accessToken: {redact(r.text)[:200]}')
        self._token = tok
        return tok

    def cluster_uid(self) -> str:
        if self._cluster_uid:
            return self._cluster_uid
        if not self.cluster_name:
            fail('no-cluster', '必须给 --cluster')
        r = self.session.get(
            self.base + '/api/cluster.kuboard.cn/v4/cluster-cache/cluster-namespace-tree'
            '?apiGroupName=&resource=configmaps&namespaced=true',
            headers={'Kb-Access-Key': self.token()}, timeout=10,
        )
        if r.status_code >= 400:
            fail('cluster-tree-http', f'HTTP {r.status_code}: {r.text[:200]}')
        items = (r.json().get('data') or {}).get('treeItems') or []
        for it in items:
            if it.get('name') == self.cluster_name:
                self._cluster_uid = it.get('id', '')
                break
        if not self._cluster_uid:
            fail('cluster-not-found', f'集群 {self.cluster_name} 在 Kuboard 里找不到(或无权限)')
        return self._cluster_uid

    def direct(self, query: str) -> dict[str, Any]:
        """走 cluster-cache/direct(core API resources:pods/services/configmaps/events)。"""
        u = (
            self.base + '/api/cluster.kuboard.cn/v4/cluster-cache/direct'
            f'?clusterId={self.cluster_uid()}&apiVersion=v1&{query}'
        )
        r = self.session.get(u, headers={'Kb-Access-Key': self.token()}, timeout=15)
        if r.status_code >= 400:
            fail('direct-http', f'HTTP {r.status_code}: {redact(r.text)[:300]};URL={u}')
        return r.json()

    def list_paginated(self, api_group: str, resource: str, namespace: str) -> list[dict[str, Any]]:
        """走 cluster-cache 分页接口(deployments 等非 core 资源)。"""
        u = (
            self.base + '/api/cluster.kuboard.cn/v4/cluster-cache'
            f'?pageNum=1&pageSize=500&apiGroup={api_group}&resource={resource}&namespaced=true'
            f'&clusterIdNamespaces={self.cluster_uid()}%2F{urllib.parse.quote(namespace)}'
            f'&orderBy=name'
        )
        r = self.session.get(u, headers={'Kb-Access-Key': self.token()}, timeout=15)
        if r.status_code >= 400:
            fail('list-http', f'HTTP {r.status_code}: {redact(r.text)[:300]};URL={u}')
        body = r.json()
        data = body.get('data') or {}
        for k in ('list', 'items', 'records', 'content', 'rows'):
            v = data.get(k)
            if isinstance(v, list):
                return v
        return []

    def get_pod_logs(self, namespace: str, pod: str, container: str = '', tail_lines: int = 200, previous: bool = False) -> str:
        """专用日志端点。"""
        params = [f'namespace={urllib.parse.quote(namespace)}', f'name={urllib.parse.quote(pod)}',
                  f'tailLines={tail_lines}']
        if container:
            params.append(f'container={urllib.parse.quote(container)}')
        if previous:
            params.append('previous=true')
        u = (
            self.base + '/api/cluster.kuboard.cn/v4/cluster-cache/pod-logs'
            f'?clusterId={self.cluster_uid()}&' + '&'.join(params)
        )
        r = self.session.get(u, headers={'Kb-Access-Key': self.token()}, timeout=20)
        if r.status_code >= 400:
            return f'[error: HTTP {r.status_code} {redact(r.text)[:200]}]'
        # 响应是 plain text logs;direct/log 端点也可能返 JSON wrap
        text = r.text
        try:
            j = json.loads(text)
            if isinstance(j, dict) and 'data' in j:
                return str(j['data'])
        except Exception:
            pass
        return redact(text)


def summarize_pod(pod: dict[str, Any]) -> dict[str, Any]:
    """从 K8s pod 完整对象抽精简快照。"""
    meta = pod.get('metadata') or {}
    spec = pod.get('spec') or {}
    status = pod.get('status') or {}
    cs_list = status.get('containerStatuses') or []
    out_cs = []
    restart_total = 0
    for cs in cs_list:
        state = cs.get('state') or {}
        kind = next(iter(state.keys()), 'unknown')
        ent = state.get(kind) or {}
        item = {
            'name': cs.get('name'),
            'image': cs.get('image'),
            'ready': cs.get('ready', False),
            'restart_count': cs.get('restartCount', 0),
            'state': kind,
        }
        if kind == 'waiting':
            item['wait_reason'] = ent.get('reason')
        elif kind == 'terminated':
            item['term_reason'] = ent.get('reason')
            item['term_exit_code'] = ent.get('exitCode')
        out_cs.append(item)
        restart_total += cs.get('restartCount', 0)
    return {
        'name': meta.get('name'),
        'namespace': meta.get('namespace'),
        'phase': status.get('phase'),
        'status': status.get('phase'),  # 跟 wails binding 的 KuboardPodInfo 对齐
        'node_name': spec.get('nodeName'),
        'pod_ip': status.get('podIP'),
        'start_time': status.get('startTime'),
        'restart_count': restart_total,
        'containers': out_cs,
        'reason': status.get('reason'),
        'message': status.get('message'),
    }


def cmd_list_pods(args: argparse.Namespace, kc: KuboardClient) -> dict[str, Any]:
    q = f'resource=pods&namespace={urllib.parse.quote(args.namespace)}'
    if args.label_selector:
        q += f'&labelSelector={urllib.parse.quote(args.label_selector)}'
    body = kc.direct(q)
    items = ((body.get('data') or {}).get('list') or [])
    out = []
    for it in items:
        pod = it.get('data') or it
        s = summarize_pod(pod)
        if args.name_filter and args.name_filter not in (s.get('name') or ''):
            continue
        out.append(s)
    return {'pods': out, 'count': len(out)}


def cmd_list_services(args: argparse.Namespace, kc: KuboardClient) -> dict[str, Any]:
    q = f'resource=services&namespace={urllib.parse.quote(args.namespace)}'
    body = kc.direct(q)
    items = ((body.get('data') or {}).get('list') or [])
    out = []
    for it in items:
        d = it.get('data') or it
        meta = d.get('metadata') or {}
        spec = d.get('spec') or {}
        out.append({
            'name': meta.get('name'),
            'namespace': meta.get('namespace'),
            'type': spec.get('type'),
            'cluster_ip': spec.get('clusterIP'),
            'selector': spec.get('selector') or {},
            'ports': spec.get('ports') or [],
        })
    return {'services': out, 'count': len(out)}


def cmd_list_deployments(args: argparse.Namespace, kc: KuboardClient) -> dict[str, Any]:
    items = kc.list_paginated('apps', 'deployments', args.namespace)
    out = []
    for it in items:
        d = it.get('data') if isinstance(it.get('data'), dict) else it
        meta = d.get('metadata') or {}
        spec = d.get('spec') or {}
        status = d.get('status') or {}
        sel = ((spec.get('selector') or {}).get('matchLabels') or {})
        selector_str = ','.join(f'{k}={v}' for k, v in sorted(sel.items())) if sel else ''
        out.append({
            'name': meta.get('name'),
            'namespace': meta.get('namespace'),
            'replicas': spec.get('replicas', 0),
            'updated_replicas': status.get('updatedReplicas', 0),
            'ready_replicas': status.get('readyReplicas', 0),
            'available_replicas': status.get('availableReplicas', 0),
            'strategy': (spec.get('strategy') or {}).get('type'),
            'conditions': [f"{c.get('type')}={c.get('status')}" + (f" ({c.get('reason')})" if c.get('reason') else '')
                           for c in (status.get('conditions') or [])],
            'selector': selector_str,
        })
    return {'deployments': out, 'count': len(out)}


def cmd_list_events(args: argparse.Namespace, kc: KuboardClient) -> dict[str, Any]:
    q = f'resource=events&namespace={urllib.parse.quote(args.namespace)}'
    if args.field_selector:
        q += f'&fieldSelector={urllib.parse.quote(args.field_selector)}'
    body = kc.direct(q)
    items = ((body.get('data') or {}).get('list') or [])
    out = []
    for it in items:
        ev = it.get('data') or it
        if args.only_warnings and ev.get('type') != 'Warning':
            continue
        out.append({
            'type': ev.get('type'),
            'reason': ev.get('reason'),
            'message': ev.get('message'),
            'count': ev.get('count', 1),
            'last_timestamp': ev.get('lastTimestamp') or ev.get('eventTime'),
            'involved_object': (ev.get('involvedObject') or {}).get('name'),
            'involved_kind': (ev.get('involvedObject') or {}).get('kind'),
        })
    # 按 last_timestamp 倒序,最近的在前
    out.sort(key=lambda x: x.get('last_timestamp') or '', reverse=True)
    return {'events': out[:args.limit], 'count': len(out)}


def cmd_get_pod_logs(args: argparse.Namespace, kc: KuboardClient) -> dict[str, Any]:
    text = kc.get_pod_logs(args.namespace, args.pod, args.container or '', args.tail, args.previous)
    return {'pod': args.pod, 'container': args.container, 'previous': args.previous, 'tail_lines': args.tail, 'logs': text}


def cmd_pod_snapshot(args: argparse.Namespace, kc: KuboardClient) -> dict[str, Any]:
    """一站式:pods 列表 + 每 pod events + 主容器 logs(current + previous,若有 restart)。"""
    pods_res = cmd_list_pods(args, kc)
    pods = pods_res['pods']
    snap_pods = []
    for p in pods[:5]:  # 最多 5 个 pod 详细取,避免炸上下文
        info = dict(p)
        # events
        ev = cmd_list_events(argparse.Namespace(
            namespace=args.namespace,
            field_selector=f"involvedObject.name={p['name']}",
            only_warnings=False,
            limit=10,
        ), kc)
        info['events'] = ev['events']
        # 主容器(第一个)logs:current 一定取,previous 仅在有 restart 时
        main_c = (p.get('containers') or [{}])[0].get('name', '')
        info['logs_current'] = kc.get_pod_logs(args.namespace, p['name'], main_c, 100, False)
        if (p.get('restart_count') or 0) > 0:
            info['logs_previous'] = kc.get_pod_logs(args.namespace, p['name'], main_c, 100, True)
        snap_pods.append(info)
    return {'pods': snap_pods, 'total_pod_count': pods_res['count'], 'truncated': pods_res['count'] > 5}


def main() -> None:
    p = argparse.ArgumentParser(prog='k8s_query.py')
    p.add_argument('--env', required=True, help='环境名(dev/prod 等)')
    p.add_argument('--agent-dir', default=None, help='工作区根目录,默认从脚本路径推')
    p.add_argument('--url', default='', help='覆盖 Kuboard URL')
    p.add_argument('--access-key', default='', help='覆盖 access key')
    p.add_argument('--username', default='', help='覆盖用户名')
    p.add_argument('--password', default='', help='覆盖密码')
    p.add_argument('--cluster', required=True, help='Kuboard 集群名')

    sub = p.add_subparsers(dest='action', required=True)

    sp = sub.add_parser('list-pods')
    sp.add_argument('--namespace', required=True)
    sp.add_argument('--label-selector', default='')
    sp.add_argument('--name-filter', default='')

    sp = sub.add_parser('list-services')
    sp.add_argument('--namespace', required=True)

    sp = sub.add_parser('list-deployments')
    sp.add_argument('--namespace', required=True)

    sp = sub.add_parser('list-events')
    sp.add_argument('--namespace', required=True)
    sp.add_argument('--field-selector', default='')
    sp.add_argument('--only-warnings', action='store_true')
    sp.add_argument('--limit', type=int, default=20)

    sp = sub.add_parser('get-pod-logs')
    sp.add_argument('--namespace', required=True)
    sp.add_argument('--pod', required=True)
    sp.add_argument('--container', default='')
    sp.add_argument('--previous', action='store_true')
    sp.add_argument('--tail', type=int, default=200)

    sp = sub.add_parser('pod-snapshot')
    sp.add_argument('--namespace', required=True)
    sp.add_argument('--label-selector', default='')
    sp.add_argument('--name-filter', default='')

    args = p.parse_args()

    creds = load_creds(args.env, args.agent_dir)
    url = args.url or creds.get('url') or ''
    if not url:
        fail('no-url', f'env={args.env} 没找到 KUBOARD_URL_<ENV> 也没在 creds.json kuboard 节里;先回 wizard 填 K8s 运行时 URL')

    kc = KuboardClient(
        url=url,
        access_key=args.access_key or creds.get('access_key', ''),
        username=args.username or creds.get('username', ''),
        password=args.password or creds.get('password', ''),
        cluster_name=args.cluster,
    )

    handlers = {
        'list-pods': cmd_list_pods,
        'list-services': cmd_list_services,
        'list-deployments': cmd_list_deployments,
        'list-events': cmd_list_events,
        'get-pod-logs': cmd_get_pod_logs,
        'pod-snapshot': cmd_pod_snapshot,
    }
    fn = handlers[args.action]
    result = fn(args, kc)
    print(json.dumps(result, ensure_ascii=False, indent=2), flush=True)


if __name__ == '__main__':
    main()
