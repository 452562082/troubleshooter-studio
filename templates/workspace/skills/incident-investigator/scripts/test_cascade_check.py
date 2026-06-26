"""Behavior tests for cascade_check.py 的 parse_dep_map —— block / inline 两种 YAML 列表写法。

These tests are NOT shipped to bot workspaces (generator filters test_*.py).
Run from repo root:
  uv run --with pytest \\
    pytest templates/workspace/skills/incident-investigator/scripts/test_cascade_check.py -v

回归背景(2026-06):旧版 parse_dep_map 只认 inline 列表 `[a, b]`,但生成器
service-dependency-map.yaml.tmpl 实际产出的是 block 列表(`downstream:` 换行 `- x`)。
两者不匹配 → 自动生成的依赖图 downstream 永远解析为空 → incident-investigator Step 4
(沿依赖图追下游)对几乎所有部署静默空转。这些用例锁死两种格式都能解析。
"""
from __future__ import annotations

import importlib.util
from pathlib import Path

SCRIPT_PATH = Path(__file__).parent / "cascade_check.py"
_spec = importlib.util.spec_from_file_location("cascade_check", SCRIPT_PATH)
cascade_check = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(cascade_check)
parse_dep_map = cascade_check.parse_dep_map


# 跟 service-dependency-map.yaml.tmpl 渲染出的形状逐字对齐(block 列表 + 引号包裹)。
GENERATED_BLOCK = """services:
  commerce:
    role: "backend"
    upstream:
      - "api-gateway"
      - "web-bff"
    downstream:
      - "user"
      - "order"
      - "inventory"
    data_stores:
      - "mysql:order_db"
      - "redis:session"
    critical: false
  user:
    role: "backend"
    upstream: []
    downstream:
      - "auth"
    data_stores: []
    critical: true
"""

INLINE = """services:
  commerce:
    role: "backend"
    downstream: ["user", "order", "inventory"]
    data_stores: ["mysql:order_db"]
    critical: true
"""


def test_block_lists_parse():
    """生成器实际产出的 block 风格必须被正确解析(核心回归)。"""
    r = parse_dep_map(GENERATED_BLOCK)
    assert r["commerce"]["downstream"] == ["user", "order", "inventory"]
    assert r["commerce"]["upstream"] == ["api-gateway", "web-bff"]
    assert r["commerce"]["data_stores"] == ["mysql:order_db", "redis:session"]
    assert r["commerce"]["critical"] is False


def test_inline_lists_still_parse():
    """inline 风格(手填用户可能用)继续支持,不能因加 block 支持而回归。"""
    r = parse_dep_map(INLINE)
    assert r["commerce"]["downstream"] == ["user", "order", "inventory"]
    assert r["commerce"]["data_stores"] == ["mysql:order_db"]
    assert r["commerce"]["critical"] is True


def test_empty_lists():
    """`[]` 和缺省都解析成空列表,不报错也不串味。"""
    r = parse_dep_map(GENERATED_BLOCK)
    assert r["user"]["upstream"] == []
    assert r["user"]["data_stores"] == []
    assert r["user"]["downstream"] == ["auth"]
    assert r["user"]["critical"] is True


def test_block_field_isolation():
    """block 列表项只归属当前字段,不会漏进下一个字段或下一个服务。"""
    r = parse_dep_map(GENERATED_BLOCK)
    # user 的 downstream 不能混入 commerce 的项
    assert "user" not in r["user"]["downstream"]
    assert set(r["commerce"]["downstream"]).isdisjoint(r["user"]["downstream"])
