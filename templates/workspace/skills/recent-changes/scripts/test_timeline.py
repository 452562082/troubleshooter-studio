"""Behavior tests for timeline.py 的 _detect_service_cc_type —— 配置后端按 per-service runtime 判定。

These tests are NOT shipped to bot workspaces (generator filters test_*.py).
Run from repo root:
  uv run --with pytest \\
    pytest templates/workspace/skills/recent-changes/scripts/test_timeline.py -v

回归背景(2026-06):旧版 collect_config_history 只读顶层 `config_center: <primary>` 主源,
且白名单只有 nacos/apollo/consul。导致 (a) 多源时 per-service config_source 覆盖被忽略 →
副源服务按主源解析返空;(b) one2all/kuboard 主源系统的配置变更在 Step 2 时间轴完全不采集 → 盲区。
这些用例锁死"按服务自己的 runtime 字段定后端 + one2all/kuboard 能被识别"。
"""
from __future__ import annotations

import importlib.util
from pathlib import Path

SCRIPT_PATH = Path(__file__).parent / "timeline.py"
_spec = importlib.util.spec_from_file_location("timeline", SCRIPT_PATH)
timeline = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(timeline)
_detect_service_cc_type = timeline._detect_service_cc_type


# 多源 config-map:主源 nacos,但 payment 服务走 apollo 副源(per-service runtime 覆盖)。
MULTI_SOURCE = """config_center: nacos
sources:
  - id: "default"
    type: "nacos"
  - id: "apollo-1"
    type: "apollo"
environments:
  prod:
    commerce:
      namespaceId: "ns-prod"
      group: "DEFAULT_GROUP"
      dataId: "commerce.yaml"
      runtime: nacos-mcp
      status: verified
    payment:
      config_source: "apollo-1"
      appId: "payment"
      cluster: "default"
      namespaces:
        - "application"
      runtime: apollo-http
      status: verified
"""

ONE2ALL = """config_center: one2all
environments:
  prod:
    commerce:
      runtime: one2all-mcp
      mcp_server: sys-one2all
      cluster_id: "c-1"
      status: inferred
"""


def test_per_service_runtime_overrides_primary():
    """payment 走 apollo 副源,必须按它自己的 runtime=apollo-http 判 apollo,不能跟主源 nacos。"""
    assert _detect_service_cc_type(MULTI_SOURCE, "prod", "payment") == "apollo"


def test_primary_source_service_still_nacos():
    """主源服务 commerce 仍按 runtime=nacos-mcp 判 nacos。"""
    assert _detect_service_cc_type(MULTI_SOURCE, "prod", "commerce") == "nacos"


def test_one2all_detected():
    """one2all 主源系统:runtime=one2all-mcp → 识别成 one2all(供 collect_config_history 出显式 note)。"""
    assert _detect_service_cc_type(ONE2ALL, "prod", "commerce") == "one2all"


def test_unknown_service_returns_empty():
    """config-map 里没有的服务 → 返空,调用方回落顶层 config_center。"""
    assert _detect_service_cc_type(MULTI_SOURCE, "prod", "nonexistent") == ""
