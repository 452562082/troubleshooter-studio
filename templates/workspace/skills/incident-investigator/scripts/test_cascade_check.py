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
summarize = cascade_check.summarize
namespace_for_downstream = cascade_check.namespace_for_downstream


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
    downstream_namespaces:
      user: "identity-prod"
      inventory: "warehouse-prod"
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
    downstream_namespaces: {"user": "identity-prod", "order": "order-prod"}
    data_stores: ["mysql:order_db"]
    critical: true
"""


def test_block_lists_parse():
    """生成器实际产出的 block 风格必须被正确解析(核心回归)。"""
    r = parse_dep_map(GENERATED_BLOCK)
    assert r["commerce"]["downstream"] == ["user", "order", "inventory"]
    assert r["commerce"]["downstream_namespaces"] == {
        "user": "identity-prod",
        "inventory": "warehouse-prod",
    }
    assert r["commerce"]["upstream"] == ["api-gateway", "web-bff"]
    assert r["commerce"]["data_stores"] == ["mysql:order_db", "redis:session"]
    assert r["commerce"]["critical"] is False


def test_inline_lists_still_parse():
    """inline 风格(手填用户可能用)继续支持,不能因加 block 支持而回归。"""
    r = parse_dep_map(INLINE)
    assert r["commerce"]["downstream"] == ["user", "order", "inventory"]
    assert r["commerce"]["downstream_namespaces"] == {
        "user": "identity-prod",
        "order": "order-prod",
    }
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


def test_namespace_for_downstream_uses_override_or_default():
    r = parse_dep_map(GENERATED_BLOCK)
    entry = r["commerce"]
    assert namespace_for_downstream(entry, "user", "base-prod") == "identity-prod"
    assert namespace_for_downstream(entry, "order", "base-prod") == "base-prod"


def test_pyyaml_path_handles_comments_after_block_items():
    """PyYAML 可用时,block item 行尾注释不能污染服务名或数据层名。"""
    r = parse_dep_map("""services:
  commerce:
    upstream: []
    downstream:
      - "user"  # identity service
    data_stores:
      - "mysql:order_db"  # primary
    critical: false
""")
    assert r["commerce"]["downstream"] == ["user"]
    assert r["commerce"]["data_stores"] == ["mysql:order_db"]


# ── summarize:unknown 下游不得被当成 healthy(2026-06 正确性回归) ──────────────
# 背景:check_one_service 对查不到的下游返 verdict='unknown'(脚本缺失 / kuboard 不通 /
# 超时 / cluster 名错 / ns-snapshot no_pods_matched)。旧版只数 healthy/degraded,unknown 被吞,
# 全部下游查失败时误判 all_healthy → Step 4 据此判"真因在主角自身"拐错方向。

def _r(target: str, verdict: str) -> dict:
    return {"target": target, "kind": "service", "verdict": verdict}


def test_summarize_all_unknown_is_not_all_healthy():
    """所有下游都查不到 → downstream_unknown,绝不能是 all_healthy(核心回归)。"""
    s = summarize([_r("user", "unknown"), _r("order", "unknown")])
    assert s["verdict"] == "downstream_unknown"
    assert s["verdict"] != "all_healthy"
    assert s["unknown"] == 2
    assert s["healthy"] == 0
    assert set(s["unknown_targets"]) == {"user", "order"}


def test_summarize_healthy_plus_unknown_is_downstream_unknown():
    """有 healthy 但混着 unknown 且无 degraded → 仍判 downstream_unknown,不漂成 all_healthy。"""
    s = summarize([_r("user", "healthy"), _r("order", "unknown")])
    assert s["verdict"] == "downstream_unknown"
    assert s["healthy"] == 1
    assert s["unknown"] == 1


def test_summarize_all_healthy():
    """全 healthy 才是 all_healthy。"""
    s = summarize([_r("user", "healthy"), _r("order", "healthy")])
    assert s["verdict"] == "all_healthy"
    assert s["unknown"] == 0


def test_summarize_degraded_takes_precedence():
    """有 degraded → isolated/widespread(unknown 仍单独计数暴露在 summary)。"""
    iso = summarize([_r("user", "degraded"), _r("order", "unknown")])
    assert iso["verdict"] == "isolated_downstream"
    assert iso["verdict_root_likely"] == "user"
    assert iso["unknown"] == 1  # unknown 不被 degraded 盖掉,仍暴露

    wide = summarize([_r("user", "degraded"), _r("order", "degraded")])
    assert wide["verdict"] == "widespread_downstream"


def test_summarize_no_downstream():
    """空 results → no_downstream_in_map。"""
    assert summarize([])["verdict"] == "no_downstream_in_map"
