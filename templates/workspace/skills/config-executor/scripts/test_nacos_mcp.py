"""Behavior tests for nacos_mcp.py — auth refresh + 401 retry + tool dispatch.

These tests are NOT shipped to bot workspaces (generator filters test_*.py).
Run from repo root:
  uv run --with pytest --with pytest-asyncio --with respx --with httpx --with 'mcp[cli]>=1.6.0' \\
    pytest templates/workspace/skills/config-executor/scripts/test_nacos_mcp.py -v

Each test corresponds to a class of failure that broke commit 23d503a when MCP-primary
shipped without these checks: 5h later in truss the bake-token MCP silently 401'd.
"""
from __future__ import annotations

import asyncio
import importlib.util
import logging
from pathlib import Path
from unittest.mock import patch

import httpx
import pytest
import respx


SCRIPT_PATH = Path(__file__).parent / "nacos_mcp.py"
_spec = importlib.util.spec_from_file_location("nacos_mcp", SCRIPT_PATH)
nacos_mcp = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(nacos_mcp)
NacosClient = nacos_mcp.NacosClient
TOOLS = nacos_mcp.TOOLS

# asyncio_mode=auto handles async tests; no module-level mark needed (it would warn on
# the two sync tests below).


def _login_route(host: str, token: str, ttl: float):
    return respx.post(f"http://{host}:8848/nacos/v1/auth/login").mock(
        return_value=httpx.Response(200, json={"accessToken": token, "tokenTtl": ttl})
    )


@pytest.fixture
def client_dynamic():
    return NacosClient(host="nacos.test", port="8848", access_token="",
                       username="u", password="p")


@pytest.fixture
def client_static():
    return NacosClient(host="nacos.test", port="8848", access_token="static-tok",
                       username=None, password=None)


# ── tool surface ───────────────────────────────────────────────────────────────

def test_only_four_config_tools_shipped():
    """Don't quietly bloat the LLM's tool list. If we add more, update this test
    and SKILL.md.tmpl together."""
    names = {t.name for t in TOOLS}
    assert names == {"get_config", "list_configs", "get_config_history", "list_config_history"}


def test_get_config_requires_group_and_dataId():
    tool = next(t for t in TOOLS if t.name == "get_config")
    assert set(tool.inputSchema["required"]) == {"group", "dataId"}


def test_tools_expose_namespaceId_not_tenant():
    """config-map.yaml hands the LLM `namespaceId`; the tool schema must use that exact name
    so there's no rename step for the LLM to get wrong (it'd crash with a TypeError)."""
    for t in TOOLS:
        props = set(t.inputSchema.get("properties", {}))
        assert "tenant" not in props, f"{t.name} schema should not expose `tenant`: {props}"
        assert "namespaceId" in props, f"{t.name} schema should expose `namespaceId`: {props}"


def test_normalize_args_accepts_tenant_alias():
    """Belt-and-suspenders: if the LLM passes the wire name `tenant`, accept it as namespaceId."""
    assert nacos_mcp._normalize_args({"tenant": "ns", "group": "g"}) == {"namespaceId": "ns", "group": "g"}
    # namespaceId wins if both present
    assert nacos_mcp._normalize_args({"tenant": "x", "namespaceId": "y"}) == {"namespaceId": "y"}
    # untouched when neither
    assert nacos_mcp._normalize_args({"group": "g", "dataId": "d"}) == {"group": "g", "dataId": "d"}


# ── login + refresh ─────────────────────────────────────────────────────────────

@respx.mock
async def test_start_logs_in_and_populates_token(client_dynamic):
    _login_route("nacos.test", "tok-1", 7200.0)
    await client_dynamic.start()
    try:
        assert client_dynamic._token == "tok-1"
        assert client_dynamic._token_ttl == 7200.0
        assert client_dynamic._token_ready.is_set()
    finally:
        await client_dynamic.stop()


@respx.mock
async def test_static_token_skips_login_and_refresh(client_static):
    await client_static.start()
    try:
        assert client_static._token == "static-tok"
        assert client_static._token_ready.is_set()
        assert client_static._refresh_task is None
    finally:
        await client_static.stop()


@respx.mock
async def test_refresh_loop_re_logs_in_after_ttl_window(client_dynamic):
    """The headline behavior — this is exactly what 23d503a's failure mode lacked."""
    route = _login_route("nacos.test", "tok-A", 100.0)

    sleeps: list[float] = []
    real_sleep = asyncio.sleep

    async def fake_sleep(seconds: float):
        sleeps.append(seconds)
        if len(sleeps) == 1:
            route.mock(return_value=httpx.Response(
                200, json={"accessToken": "tok-B", "tokenTtl": 100.0}))
        await real_sleep(0)

    with patch.object(nacos_mcp.asyncio, "sleep", new=fake_sleep):
        await client_dynamic.start()
        try:
            await real_sleep(0); await real_sleep(0); await real_sleep(0)
        finally:
            await client_dynamic.stop()

    assert client_dynamic._token == "tok-B"
    assert sleeps and sleeps[0] == pytest.approx(80.0)  # ttl*0.8


@respx.mock
async def test_short_ttl_refreshes_at_ratio_not_floored_to_backoff():
    """A short tokenTtl must schedule the next refresh at ttl*0.8 — NOT be floored to the
    60s failure-backoff, which would let the token expire long before its refresh fires."""
    c = NacosClient("nacos.test", "8848", "", "u", "p")
    route = _login_route("nacos.test", "tok-A", 10.0)  # 10s ttl → refresh at 8s

    sleeps: list[float] = []
    real_sleep = asyncio.sleep

    async def fake_sleep(seconds: float):
        sleeps.append(seconds)
        if len(sleeps) == 1:
            route.mock(return_value=httpx.Response(
                200, json={"accessToken": "tok-B", "tokenTtl": 10.0}))
        await real_sleep(0)

    with patch.object(nacos_mcp.asyncio, "sleep", new=fake_sleep):
        await c.start()
        try:
            await real_sleep(0); await real_sleep(0); await real_sleep(0)
        finally:
            await c.stop()

    assert sleeps and sleeps[0] == pytest.approx(8.0), \
        f"10s ttl should refresh at 8s (ttl*0.8), not floored to 60s: {sleeps}"
    assert c._token == "tok-B"


@respx.mock
async def test_refresh_override_forces_fixed_interval():
    """NACOS_REFRESH_SECONDS override: proactive refresh uses the fixed interval, ignoring
    tokenTtl*0.8. Lets ops verify refresh on a real (long-TTL) nacos without waiting hours."""
    c = NacosClient("nacos.test", "8848", "", "u", "p", refresh_override=12.0)
    route = _login_route("nacos.test", "tok-A", 18000.0)  # huge ttl → ttl*0.8 would be 14400s

    sleeps: list[float] = []
    real_sleep = asyncio.sleep

    async def fake_sleep(seconds: float):
        sleeps.append(seconds)
        if len(sleeps) == 1:
            route.mock(return_value=httpx.Response(
                200, json={"accessToken": "tok-B", "tokenTtl": 18000.0}))
        await real_sleep(0)

    with patch.object(nacos_mcp.asyncio, "sleep", new=fake_sleep):
        await c.start()
        try:
            await real_sleep(0); await real_sleep(0); await real_sleep(0)
        finally:
            await c.stop()

    assert sleeps and sleeps[0] == pytest.approx(12.0), \
        f"override should pin refresh at 12s regardless of 18000s ttl: {sleeps}"
    assert c._token == "tok-B"


@respx.mock
async def test_refresh_loop_backoffs_on_login_failure():
    c = NacosClient("nacos.test", "8848", "", "u", "p")
    call_count = {"n": 0}

    def respond(request):
        call_count["n"] += 1
        if call_count["n"] == 1:
            return httpx.Response(200, json={"accessToken": "tok-1", "tokenTtl": 100.0})
        return httpx.Response(500, text="boom")

    respx.post("http://nacos.test:8848/nacos/v1/auth/login").mock(side_effect=respond)

    sleeps: list[float] = []
    real_sleep = asyncio.sleep

    async def fake_sleep(seconds: float):
        sleeps.append(seconds)
        await real_sleep(0)

    with patch.object(nacos_mcp.asyncio, "sleep", new=fake_sleep):
        await c.start()
        try:
            await real_sleep(0); await real_sleep(0); await real_sleep(0)
        finally:
            await c.stop()

    assert 80.0 in sleeps, f"first sleep should be ttl*0.8: {sleeps}"
    assert 60.0 in sleeps, f"backoff sleep should follow failure: {sleeps}"


# ── 401/403 forced re-login ─────────────────────────────────────────────────────

@respx.mock
async def test_401_triggers_relogin_and_retries(client_dynamic):
    _login_route("nacos.test", "tok-fresh", 18000.0)
    call_count = {"n": 0}

    def respond(request):
        call_count["n"] += 1
        if call_count["n"] == 1:
            return httpx.Response(401, text="token expired")
        return httpx.Response(200, json={"content": "config-data"})

    respx.get("http://nacos.test:8848/nacos/v1/cs/configs").mock(side_effect=respond)

    await client_dynamic.start()
    try:
        client_dynamic._token = "tok-stale"
        out = await client_dynamic.get_config(group="g", dataId="d")
        assert "content" in out and "config-data" in out
        assert client_dynamic._token == "tok-fresh"
    finally:
        await client_dynamic.stop()


@respx.mock
async def test_403_also_triggers_relogin(client_dynamic):
    _login_route("nacos.test", "tok-fresh", 18000.0)
    call_count = {"n": 0}

    def respond(request):
        call_count["n"] += 1
        return httpx.Response(403, text="forbidden") if call_count["n"] == 1 \
            else httpx.Response(200, json={"k": "v"})

    respx.get("http://nacos.test:8848/nacos/v1/cs/configs").mock(side_effect=respond)

    await client_dynamic.start()
    try:
        client_dynamic._token = "stale"
        await client_dynamic.get_config(group="g", dataId="d")
        assert client_dynamic._token == "tok-fresh"
    finally:
        await client_dynamic.stop()


@respx.mock
async def test_static_token_does_not_relogin_on_401(client_static):
    respx.get("http://nacos.test:8848/nacos/v1/cs/configs").mock(
        return_value=httpx.Response(401, text="nope"))
    out = await client_static.get_config(group="g", dataId="d")
    assert "UnAuthorized" in out or "401" in out or "failed" in out
    assert client_static._token == "static-tok"


@respx.mock
async def test_startup_login_failure_does_not_crash_and_request_errors_fast():
    """A nacos blip at bot boot must NOT kill the MCP server for the whole session.
    start() should serve anyway; a tool call before recovery errors fast, not hangs."""
    respx.post("http://nacos.test:8848/nacos/v1/auth/login").mock(
        return_value=httpx.Response(500, text="nacos down"))
    c = NacosClient("nacos.test", "8848", "", "u", "p")

    with patch.object(nacos_mcp, "AUTH_WAIT_TIMEOUT", 0.3):
        await c.start()  # must not raise
        try:
            assert not c._token_ready.is_set()
            assert c._refresh_task is not None, "refresh loop must be spawned to retry"
            out = await asyncio.wait_for(c.get_config(group="g", dataId="d"), timeout=2.0)
            assert "not ready" in out or "failed" in out
        finally:
            await c.stop()


# ── tool request shapes ─────────────────────────────────────────────────────────

@respx.mock
async def test_get_config_hits_v1_configs_endpoint_without_search(client_dynamic):
    """Critical: nacos /v1/cs/configs is dual-purpose. WITHOUT `search` param it returns
    content of the specified dataId; WITH search it returns a paginated list. Mixing the
    two silently breaks get_config."""
    _login_route("nacos.test", "tok", 18000.0)
    route = respx.get("http://nacos.test:8848/nacos/v1/cs/configs").mock(
        return_value=httpx.Response(200, json={"content": "x"}))

    await client_dynamic.start()
    try:
        await client_dynamic.get_config(group="G", dataId="D", namespaceId="NS")
    finally:
        await client_dynamic.stop()

    assert route.called
    call_params = dict(route.calls.last.request.url.params)
    # tool param namespaceId maps to the v1 wire param `tenant`
    assert call_params == {"group": "G", "dataId": "D", "tenant": "NS"}, \
        f"get_config must not send `search`; got {call_params}"


@respx.mock
async def test_get_config_404_reports_not_found(client_dynamic):
    _login_route("nacos.test", "tok", 18000.0)
    respx.get("http://nacos.test:8848/nacos/v1/cs/configs").mock(
        return_value=httpx.Response(404, text=""))
    await client_dynamic.start()
    try:
        out = await client_dynamic.get_config(group="g", dataId="missing")
    finally:
        await client_dynamic.stop()
    assert "not found" in out, f"404 should surface a clean not-found message: {out!r}"


@respx.mock
async def test_list_configs_defaults_search_blur(client_dynamic):
    _login_route("nacos.test", "tok", 18000.0)
    route = respx.get("http://nacos.test:8848/nacos/v1/cs/configs").mock(
        return_value=httpx.Response(200, json={"pageItems": []}))

    await client_dynamic.start()
    try:
        await client_dynamic.list_configs(pageNo=1, pageSize=10, namespaceId="NS")
    finally:
        await client_dynamic.stop()

    assert route.called
    params = dict(route.calls.last.request.url.params)
    assert params["search"] == "blur"
    assert params["pageNo"] == "1"
    assert params["pageSize"] == "10"


@respx.mock
async def test_history_get_vs_list_distinguished_by_search(client_dynamic):
    """Same get-vs-list trap on /v1/cs/history."""
    _login_route("nacos.test", "tok", 18000.0)
    route = respx.get("http://nacos.test:8848/nacos/v1/cs/history").mock(
        return_value=httpx.Response(200, json={}))

    await client_dynamic.start()
    try:
        await client_dynamic.get_config_history(nid=42, group="g", dataId="d")
        get_params = dict(route.calls.last.request.url.params)
        assert "search" not in get_params, \
            f"get_config_history must not send `search`; got {get_params}"

        await client_dynamic.list_config_history(group="g", dataId="d")
        list_params = dict(route.calls.last.request.url.params)
        assert list_params["search"] == "accurate"
    finally:
        await client_dynamic.stop()


# ── token leakage guard ────────────────────────────────────────────────────────

@respx.mock
async def test_token_not_emitted_to_logs(caplog, client_dynamic):
    """Guard the bug we removed from upstream where logger.info printed raw token at startup."""
    _login_route("nacos.test", "super-secret-token-abc123", 7200.0)
    with caplog.at_level(logging.DEBUG, logger="nacos_mcp"):
        await client_dynamic.start()
        try:
            pass
        finally:
            await client_dynamic.stop()
    for rec in caplog.records:
        assert "super-secret-token-abc123" not in rec.getMessage(), \
            f"token leaked into log: {rec.getMessage()!r}"
