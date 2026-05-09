#!/usr/bin/env python3
"""sink_postmortem.py —— 把本次排障抽象成的 known-error 条目追加到 known-errors.yaml。

incident-investigator Step 7 的工具:`confidence=high` 时把"症状 → 根因 → 处置步骤"
固化成可被未来 Step 1.3 grep 命中的 pattern 条目。

输入(stdin / --input file)JSON:
  {
    "pattern": "正则",                                 # 必填,Step 1.3 grep 用
    "typical_cause": "机制级根因描述",                  # 必填
    "next_actions": ["步骤 1", "步骤 2"],              # 必填,通用可复用
    "mitigation": "止血操作",                           # 可选
    "causation_chain": {                                # 可选
      "check_upstream_for": ["..."],
      "check_downstream_for": ["..."],
      "explanation": "..."
    }
  }

行为:
  - 校验必填 → 不全报错退 1
  - 校验 pattern 是合法 regex → 不合法报错退 2
  - **去重**:已存在同 pattern → stderr 打 [skip] 退 0
  - 追加到 routing/references/known-errors.yaml 的 errors: 列表末
  - 头部加注释 `# auto-sunk YYYY-MM-DD by incident <env>/<service>`(--env / --service 给的话)

为啥不引 PyYAML 而是字符串拼接 append:
  - 整文件 PyYAML round-trip 会丢用户原注释 + 重新格式化所有缩进,生产配置文件碰不起
  - append 模式只动文件末尾,不碰已有内容,稳
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path


def _detect_known_errors_path(workspace_root: Path) -> Path:
    """workspace_root/skills/routing/references/known-errors.yaml"""
    p = workspace_root / 'skills' / 'routing' / 'references' / 'known-errors.yaml'
    if not p.exists():
        # fallback:从脚本自身位置反推 workspace 根
        # 脚本在 skills/incident-investigator/scripts/sink_postmortem.py
        # ../../../routing/references/known-errors.yaml
        guess = Path(__file__).resolve().parent.parent.parent / 'routing' / 'references' / 'known-errors.yaml'
        if guess.exists():
            return guess
        raise FileNotFoundError(f'known-errors.yaml not found at {p} or {guess}')
    return p


def _has_pattern(yaml_text: str, pattern: str) -> bool:
    """检测 yaml 里是否已经有相同 pattern 的条目。

    不解析 yaml(避免引 PyYAML 重格式化整文件),纯字符串字面搜索:
      - 拿 pattern 编码成跟 _yaml_str 输出一致的字面 token(双引号版本 + 单引号版本 + 裸版本)
      - 在 yaml_text 里直接 substring 查找

    避免 re.escape 跟反斜杠 escape 互相嵌套地狱(踩过坑:pattern='foo\\.bar' 时
    re.escape 后 + yaml 的 \\\\. 字面对不上,误判"不存在"重复追加)。
    """
    # 三种 yaml 里 pattern 行可能的样子(对应 _yaml_str 三条分支:双引号 / 单引号 / 裸)
    # 双引号:_yaml_str 实际只产生这一种(needs_quote 时只发双引号),所以这条最关键。
    quoted_double = _yaml_str(pattern)  # _yaml_str 内置双引号 + escape,直接复用
    # 兜底两种:已有 yaml 里可能用单引号 / 裸 写的老条目
    plain_token = pattern  # 裸字面
    candidates = [
        f'- pattern: {quoted_double}',
        f"- pattern: '{plain_token}'",
        f'- pattern: {plain_token}\n',
    ]
    return any(c in yaml_text for c in candidates)


def _yaml_str(s: str) -> str:
    """把 string 编码成 yaml 安全的字面量。
    简单策略:含特殊字符( : - # > | ` ' " { } [ ] , 反斜杠 \r \n)用双引号 + escape;否则裸出。
    """
    if not s:
        return '""'
    # 含 yaml 特殊字符 / 数字开头 → 加双引号
    needs_quote = any(c in s for c in ':#-?&*!|>%@`{}[],\n\r\t"\\') or s[0] in '0123456789' or s.lower() in ('null', 'true', 'false', 'yes', 'no', '~', 'on', 'off')
    if needs_quote:
        # 双引号转义:\\ 和 \"
        escaped = s.replace('\\', '\\\\').replace('"', '\\"')
        return f'"{escaped}"'
    return s


def _format_entry(entry: dict, env: str, service: str) -> str:
    """把 entry dict 渲染成跟 known-errors.yaml 风格一致的 yaml 段(2 空格缩进,字符串带双引号)。"""
    lines = []
    today = datetime.now(timezone.utc).strftime('%Y-%m-%d')
    where = f'{env}/{service}' if env and service else 'unknown context'
    lines.append('')  # 跟前面已有条目隔一空行
    lines.append(f'  # auto-sunk {today} by incident {where}')
    lines.append(f'  - pattern: {_yaml_str(entry["pattern"])}')
    lines.append(f'    typical_cause: {_yaml_str(entry["typical_cause"])}')
    lines.append('    next_actions:')
    for action in entry['next_actions']:
        lines.append(f'      - {_yaml_str(str(action))}')
    if entry.get('mitigation'):
        lines.append(f'    mitigation: {_yaml_str(entry["mitigation"])}')
    cc = entry.get('causation_chain')
    if isinstance(cc, dict):
        lines.append('    causation_chain:')
        for k in ('check_upstream_for', 'check_downstream_for'):
            v = cc.get(k)
            if isinstance(v, list) and v:
                items = ', '.join(_yaml_str(str(x)) for x in v)
                lines.append(f'      {k}: [{items}]')
        if cc.get('explanation'):
            lines.append(f'      explanation: {_yaml_str(cc["explanation"])}')
    return '\n'.join(lines) + '\n'


def main() -> None:
    ap = argparse.ArgumentParser(prog='sink_postmortem.py')
    ap.add_argument('--input', help='JSON 文件路径(默认从 stdin 读)')
    ap.add_argument('--env', default='', help='故障 env,写注释用')
    ap.add_argument('--service', default='', help='故障 service,写注释用')
    ap.add_argument('--workspace-root', default='',
                    help='workspace 根(默认从脚本路径反推 ../../../)')
    ap.add_argument('--dry-run', action='store_true', help='不真追加,只打印将要写的内容')
    args = ap.parse_args()

    raw = Path(args.input).read_text() if args.input else sys.stdin.read()
    try:
        entry = json.loads(raw)
    except json.JSONDecodeError as e:
        print(f'[error] JSON 解析失败: {e}', file=sys.stderr)
        sys.exit(1)

    # 必填校验
    for required in ('pattern', 'typical_cause', 'next_actions'):
        if not entry.get(required):
            print(f'[error] 缺必填字段: {required}', file=sys.stderr)
            sys.exit(1)
    if not isinstance(entry['next_actions'], list) or len(entry['next_actions']) == 0:
        print('[error] next_actions 必须是非空数组', file=sys.stderr)
        sys.exit(1)

    # pattern 必须是合法 regex(下游 Step 1.3 grep 要 compile)
    try:
        re.compile(entry['pattern'])
    except re.error as e:
        print(f'[error] pattern 不是合法正则: {e}', file=sys.stderr)
        sys.exit(2)

    # 找 known-errors.yaml
    if args.workspace_root:
        ws = Path(args.workspace_root).resolve()
    else:
        # 脚本在 <ws>/skills/incident-investigator/scripts/sink_postmortem.py
        ws = Path(__file__).resolve().parent.parent.parent.parent
    try:
        ke_path = _detect_known_errors_path(ws)
    except FileNotFoundError as e:
        print(f'[error] {e}', file=sys.stderr)
        sys.exit(3)

    yaml_text = ke_path.read_text(encoding='utf-8')

    # 去重
    if _has_pattern(yaml_text, entry['pattern']):
        print(f'[skip] pattern {entry["pattern"]!r} 已存在,不重复追加', file=sys.stderr)
        sys.exit(0)

    new_block = _format_entry(entry, args.env, args.service)

    if args.dry_run:
        print('=== 将追加(dry-run,不写盘) ===', file=sys.stderr)
        print(new_block)
        sys.exit(0)

    # 追加到文件末。先校验文件以 \n 结尾,不是就补一个,确保新条目跟既有最后一条之间有空行。
    if not yaml_text.endswith('\n'):
        yaml_text += '\n'
    new_text = yaml_text + new_block
    ke_path.write_text(new_text, encoding='utf-8')

    print(f'[ok] 追加到 {ke_path}', file=sys.stderr)
    sys.exit(0)


if __name__ == '__main__':
    main()
