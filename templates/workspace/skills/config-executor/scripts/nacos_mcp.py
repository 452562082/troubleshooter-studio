#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = [
#     "mcp[cli]>=1.6.0",
#     "httpx>=0.28.1",
# ]
# ///
"""nacos MCP server (truss-local).

Why this lives here instead of `uvx nacos-mcp-server`:
  * upstream nacos-mcp-server takes a static --access_token CLI arg with no refresh,
    silently 401s after nacos tokenTtl (default 5h on nacos 2.x). Long-running bot
    can't recover — see internal/agent/install_native_mcp_common.go decision history.
  * upstream uses /nacos/v3/admin/... admin endpoints (nacos 3.0+ only); truss runs
    nacos 2.3.0 where those 404.
  * upstream uses sync httpx.Client inside asyncio.run() — blocks the event loop,
    makes a refresh task impossible.

This script: user/pass auth + asyncio refresh loop at tokenTtl*0.8 + 401 forced
re-login, async httpx throughout, nacos /v1 endpoints (work on 2.x and 3.x).
Only ships the 4 config-reading tools that排障 SKILL actually uses.

Run via PEP 723:
  uv run --script nacos_mcp.py --host X --port 8848 --username U --password P
"""
from __future__ import annotations

import argparse
import asyncio
import logging
import os
import sys
from typing import Any, Mapping, Optional, Union

import httpx
import mcp.server.stdio
import mcp.types as types
from mcp.server import NotificationOptions, Server
from mcp.server.models import InitializationOptions

logger = logging.getLogger("nacos_mcp")

USER_AGENT = "truss-nacos-mcp/0.1"
REFRESH_RATIO = 0.8
LOGIN_TIMEOUT = 10.0
REQUEST_TIMEOUT = 30.0
DEFAULT_TOKEN_TTL_SECONDS = 18000.0
REFRESH_BACKOFF_SECONDS = 60.0
# Floor on the proactive refresh interval — just enough to avoid a busy-loop if nacos
# hands back a pathologically tiny tokenTtl. Must NOT be the 60s failure-backoff: flooring
# the proactive interval at 60s would let a short-TTL token expire before its refresh.
MIN_REFRESH_INTERVAL_SECONDS = 2.0
# How long a tool call waits for the first token before erroring. In dynamic-auth mode
# start() awaits the first login before serving, so a request only hits this wait if that
# login failed and the refresh loop hasn't recovered yet — error fast instead of hanging.
AUTH_WAIT_TIMEOUT = 15.0


class Result:
    def __init__(self, code: int, message: str, data: Any):
        self.code = code
        self.message = message
        self.data = data

    def is_success(self) -> bool:
        return self.code == httpx.codes.OK


class NacosClient:
    def __init__(
        self,
        host: str,
        port: str,
        access_token: str,
        username: Optional[str],
        password: Optional[str],
        refresh_override: Optional[float] = None,
    ):
        self.host = host
        self.port = port
        self._username = username
        self._password = password
        # When set (NACOS_REFRESH_SECONDS), the proactive refresh fires on this fixed
        # interval instead of tokenTtl*0.8. Lets you verify refresh on a real nacos without
        # waiting hours for a long TTL (set it to e.g. 60), and lets ops tune cadence.
        self._refresh_override = refresh_override

        self._token: str = access_token
        self._token_ttl: float = DEFAULT_TOKEN_TTL_SECONDS
        self._token_lock = asyncio.Lock()
        self._token_ready = asyncio.Event()
        self._refresh_task: Optional[asyncio.Task] = None

        if access_token and not self.uses_dynamic_auth:
            self._token_ready.set()

    @property
    def uses_dynamic_auth(self) -> bool:
        return bool(self._username and self._password)

    def _proactive_interval(self) -> float:
        """Seconds until the next proactive refresh after a successful login."""
        if self._refresh_override is not None and self._refresh_override > 0:
            return self._refresh_override
        return max(self._token_ttl * REFRESH_RATIO, MIN_REFRESH_INTERVAL_SECONDS)

    async def start(self) -> None:
        if not self.uses_dynamic_auth:
            return
        try:
            await self._refresh_token()
        except Exception as e:
            # Don't crash the whole MCP server because nacos was momentarily unreachable
            # at boot. Serve anyway; the refresh loop retries and tool calls error clearly
            # until login lands. Crashing here would leave Claude Code with a dead MCP for
            # the whole session.
            logger.warning(
                "initial nacos login failed: %s; serving anyway, refresh loop will retry. "
                "tool calls will error until login succeeds.", e,
            )
        self._refresh_task = asyncio.create_task(
            self._refresh_loop(), name="nacos-token-refresh",
        )

    async def stop(self) -> None:
        if self._refresh_task is None:
            return
        self._refresh_task.cancel()
        try:
            await self._refresh_task
        except (asyncio.CancelledError, Exception):
            pass
        self._refresh_task = None

    async def _do_login(self) -> tuple[str, float]:
        url = f"http://{self.host}:{self.port}/nacos/v1/auth/login"
        data = {"username": self._username, "password": self._password}
        # trust_env=False: nacos is an internal service — never route it through an external
        # HTTP_PROXY/HTTPS_PROXY picked up from env (would 502 / hang on an internal host).
        # Also skips .netrc/SSL env, both irrelevant here (explicit creds, plain http://).
        async with httpx.AsyncClient(trust_env=False) as client:
            resp = await client.post(
                url, data=data, timeout=LOGIN_TIMEOUT,
                headers={"User-Agent": USER_AGENT},
            )
            resp.raise_for_status()
            body = resp.json()
        token = body.get("accessToken") or body.get("access_token")
        ttl = float(body.get("tokenTtl", DEFAULT_TOKEN_TTL_SECONDS))
        if not token:
            raise RuntimeError(f"login response missing accessToken; keys={list(body)}")
        return token, ttl

    async def _refresh_token(self) -> None:
        token, ttl = await self._do_login()
        async with self._token_lock:
            self._token = token
            self._token_ttl = ttl
            self._token_ready.set()
        logger.info("nacos token refreshed, tokenTtl=%.0fs", ttl)

    async def _refresh_loop(self) -> None:
        # next_wait is recomputed from each outcome: a successful login schedules the next
        # refresh at _proactive_interval() (tokenTtl*0.8, or NACOS_REFRESH_SECONDS override);
        # a failure (or a never-succeeded startup login) retries at the short backoff so we
        # don't wait a full TTL after a transient error.
        next_wait = (
            self._proactive_interval()
            if self._token_ready.is_set() else REFRESH_BACKOFF_SECONDS
        )
        try:
            while True:
                logger.debug("next nacos token refresh in %.0fs", next_wait)
                await asyncio.sleep(next_wait)
                try:
                    await self._refresh_token()
                    next_wait = self._proactive_interval()
                except Exception as e:
                    logger.warning(
                        "nacos token refresh failed: %s; retrying in %.0fs",
                        e, REFRESH_BACKOFF_SECONDS,
                    )
                    next_wait = REFRESH_BACKOFF_SECONDS
        except asyncio.CancelledError:
            raise

    async def _refresh_if_stale(self, token_snapshot: str) -> None:
        # True single-flight for a 401 burst: hold the lock across the login so concurrent
        # 401-driven refreshers serialize — the first re-logs in, the rest see the token
        # already changed and skip. Normal requests read self._token WITHOUT this lock
        # (see do_get), so they're not blocked by an in-flight re-login.
        async with self._token_lock:
            if self._token != token_snapshot:
                return  # another coroutine already refreshed under us
            token, ttl = await self._do_login()
            self._token = token
            self._token_ttl = ttl
            self._token_ready.set()
            logger.info("nacos re-logged in after 401, tokenTtl=%.0fs", ttl)

    async def _request(
        self,
        url: str,
        params: Mapping[str, Optional[Union[str, int, float, bool]]] | None = None,
    ) -> Result | None:
        try:
            await asyncio.wait_for(self._token_ready.wait(), timeout=AUTH_WAIT_TIMEOUT)
        except asyncio.TimeoutError:
            return Result(
                httpx.codes.SERVICE_UNAVAILABLE,
                "nacos auth not ready (login is failing — check host/port/credentials "
                "and that nacos is reachable); refresh loop will keep retrying.",
                None,
            )

        async def do_get(client: httpx.AsyncClient) -> httpx.Response:
            return await client.get(
                url,
                headers={"User-Agent": USER_AGENT, "AccessToken": self._token},
                timeout=REQUEST_TIMEOUT,
                params=params,
            )

        try:
            async with httpx.AsyncClient(trust_env=False) as client:  # internal svc, no proxy
                resp = await do_get(client)
                if (resp.status_code in (httpx.codes.UNAUTHORIZED, httpx.codes.FORBIDDEN)
                        and self.uses_dynamic_auth):
                    token_before = self._token
                    logger.info("nacos %d, forcing immediate token re-login", resp.status_code)
                    try:
                        await self._refresh_if_stale(token_before)
                    except Exception as e:
                        return Result(resp.status_code, f"forced re-login failed: {e}", None)
                    resp = await do_get(client)
                if resp.status_code == httpx.codes.OK:
                    try:
                        return Result(resp.status_code, resp.text, resp.json())
                    except ValueError:
                        return Result(resp.status_code, resp.text, resp.text)
                if resp.status_code == httpx.codes.NOT_FOUND:
                    return Result(resp.status_code, "config not found", None)
                if resp.status_code in (httpx.codes.UNAUTHORIZED, httpx.codes.FORBIDDEN):
                    return Result(
                        resp.status_code,
                        "UnAuthorized after re-login retry; check username/password.",
                        None,
                    )
                resp.raise_for_status()
        except httpx.HTTPStatusError as e:
            return Result(e.response.status_code, str(e), None)
        except Exception as e:
            return Result(httpx.codes.INTERNAL_SERVER_ERROR, str(e), None)
        return None

    # NOTE on the namespace param name: callers (config-map.yaml, nacos console) speak
    # `namespaceId`; the nacos v1 REST API wants `tenant`. We accept `namespaceId` at the
    # tool boundary (so the LLM passes exactly what config-map shows — no rename step to get
    # wrong) and map it to `tenant` on the wire here.
    async def get_config(
        self, group: str, dataId: str, namespaceId: str = "public",
    ) -> str:
        url = f"http://{self.host}:{self.port}/nacos/v1/cs/configs"
        params = {"group": group, "dataId": dataId, "tenant": namespaceId}
        result = await self._request(url, params=params)
        if result is None:
            return "error: empty result"
        if result.is_success():
            return str(result.data)
        return f"get_config failed: {result.message}"

    async def list_configs(
        self,
        pageNo: int = 1,
        pageSize: int = 100,
        namespaceId: str = "public",
        group: Optional[str] = None,
        dataId: Optional[str] = None,
        search: str = "blur",
    ) -> str:
        url = f"http://{self.host}:{self.port}/nacos/v1/cs/configs"
        params: dict[str, Any] = {
            "pageNo": pageNo, "pageSize": pageSize,
            "tenant": namespaceId, "search": search,
        }
        if group:
            params["group"] = group
        if dataId:
            params["dataId"] = dataId
        result = await self._request(url, params=params)
        if result is None:
            return "error: empty result"
        if result.is_success():
            return str(result.data)
        return f"list_configs failed: {result.message}"

    async def list_config_history(
        self,
        group: str, dataId: str,
        pageNo: int = 1, pageSize: int = 100, namespaceId: str = "public",
    ) -> str:
        url = f"http://{self.host}:{self.port}/nacos/v1/cs/history"
        params = {
            "search": "accurate", "group": group, "dataId": dataId,
            "pageNo": pageNo, "pageSize": pageSize, "tenant": namespaceId,
        }
        result = await self._request(url, params=params)
        if result is None:
            return "error: empty result"
        if result.is_success():
            return str(result.data)
        return f"list_config_history failed: {result.message}"

    async def get_config_history(
        self, nid: int, group: str, dataId: str, namespaceId: str = "public",
    ) -> str:
        url = f"http://{self.host}:{self.port}/nacos/v1/cs/history"
        params = {"nid": nid, "group": group, "dataId": dataId, "tenant": namespaceId}
        result = await self._request(url, params=params)
        if result is None:
            return "error: empty result"
        if result.is_success():
            return str(result.data)
        return f"get_config_history failed: {result.message}"


TOOLS = [
    types.Tool(
        name="get_config",
        description=(
            "Read the content of a specific nacos config by group + dataId. "
            "Returns the raw config content as text."
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "group": {"type": "string", "description": "config group name"},
                "dataId": {"type": "string", "description": "config dataId"},
                "namespaceId": {"type": "string", "description": "namespace, default `public` (pass routing's namespaceId as-is)"},
            },
            "required": ["group", "dataId"],
        },
    ),
    types.Tool(
        name="list_configs",
        description=(
            "List configs under a namespace, optionally filtered by group / dataId pattern. "
            "Use search='blur' for prefix/suffix `*` wildcards, 'accurate' for exact match."
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "pageNo": {"type": "integer", "default": 1},
                "pageSize": {"type": "integer", "default": 100},
                "namespaceId": {"type": "string", "default": "public"},
                "group": {"type": "string"},
                "dataId": {"type": "string"},
                "search": {"type": "string", "enum": ["blur", "accurate"], "default": "blur"},
            },
        },
    ),
    types.Tool(
        name="list_config_history",
        description=(
            "List the publish history of a specific config (group + dataId), most-recent first. "
            "Returns paged history records with `id` (used as `nid` in get_config_history)."
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "group": {"type": "string"},
                "dataId": {"type": "string"},
                "pageNo": {"type": "integer", "default": 1},
                "pageSize": {"type": "integer", "default": 100},
                "namespaceId": {"type": "string", "default": "public"},
            },
            "required": ["group", "dataId"],
        },
    ),
    types.Tool(
        name="get_config_history",
        description=(
            "Read a specific historical revision of a config. nid comes from list_config_history "
            "result's `id` field."
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "nid": {"type": "integer", "description": "history record id"},
                "group": {"type": "string"},
                "dataId": {"type": "string"},
                "namespaceId": {"type": "string", "default": "public"},
            },
            "required": ["nid", "group", "dataId"],
        },
    ),
]


def _normalize_args(arguments: dict) -> dict:
    """Tolerate the LLM passing the nacos v1 wire name `tenant` instead of `namespaceId`
    (the tool schema name). Either works; namespaceId wins if both present."""
    args = dict(arguments)
    if "tenant" in args:
        args.setdefault("namespaceId", args.pop("tenant"))
        args.pop("tenant", None)
    return args


def make_client(
    host: str, port: str, access_token: str,
    username: Optional[str], password: Optional[str],
    refresh_override: Optional[float] = None,
) -> NacosClient:
    return NacosClient(host, port, access_token, username, password, refresh_override)


async def serve(
    host: str, port: str, access_token: str,
    username: Optional[str], password: Optional[str],
    refresh_override: Optional[float] = None,
):
    client = make_client(host, port, access_token, username, password, refresh_override)
    await client.start()
    server = Server("nacos")

    @server.list_tools()
    async def handle_list_tools() -> list[types.Tool]:
        return TOOLS

    @server.call_tool()
    async def call_tool(name: str, arguments: dict) -> list[types.TextContent]:
        try:
            args = _normalize_args(arguments)
            if name == "get_config":
                out = await client.get_config(**args)
            elif name == "list_configs":
                out = await client.list_configs(**args)
            elif name == "list_config_history":
                out = await client.list_config_history(**args)
            elif name == "get_config_history":
                out = await client.get_config_history(**args)
            else:
                out = f"unknown tool: {name}"
            return [types.TextContent(type="text", text=out)]
        except TypeError as e:
            # Bad/extra kwargs from the LLM — return a clear hint instead of a raw stack.
            return [types.TextContent(
                type="text",
                text=f"tool {name} got bad arguments ({e}); expected keys per the tool schema "
                     f"(group/dataId/namespaceId[/nid/pageNo/pageSize/search]).")]
        except Exception as e:
            return [types.TextContent(type="text", text=f"tool {name} crashed: {e}")]

    try:
        async with mcp.server.stdio.stdio_server() as (read, write):
            logger.info("nacos MCP running on stdio, auth=%s",
                        "dynamic" if client.uses_dynamic_auth else "static")
            await server.run(
                read, write,
                InitializationOptions(
                    server_name="nacos",
                    server_version="0.1.0",
                    capabilities=server.get_capabilities(
                        notification_options=NotificationOptions(),
                        experimental_capabilities={},
                    ),
                ),
            )
    finally:
        await client.stop()


def main():
    parser = argparse.ArgumentParser(description="truss-local nacos MCP server")
    parser.add_argument("--host", default=os.environ.get("NACOS_HOST", "localhost"))
    parser.add_argument("--port", default=os.environ.get("NACOS_PORT", "8848"))
    parser.add_argument("--access-token", default=os.environ.get("NACOS_ACCESS_TOKEN", ""),
                        help="static accessToken; prefer --username/--password for refresh.")
    parser.add_argument("--username", default=os.environ.get("NACOS_USERNAME"))
    parser.add_argument("--password", default=os.environ.get("NACOS_PASSWORD"))
    parser.add_argument("--refresh-seconds", default=os.environ.get("NACOS_REFRESH_SECONDS"),
                        help="override the proactive refresh interval (default tokenTtl*0.8); "
                             "set e.g. 60 to verify refresh on a real nacos without waiting hours.")
    args = parser.parse_args()

    if not args.access_token and not (args.username and args.password):
        print("error: provide --access-token, or both --username and --password",
              file=sys.stderr)
        sys.exit(2)

    refresh_override: Optional[float] = None
    if args.refresh_seconds:
        try:
            refresh_override = float(args.refresh_seconds)
        except ValueError:
            print(f"error: --refresh-seconds must be a number, got {args.refresh_seconds!r}",
                  file=sys.stderr)
            sys.exit(2)

    asyncio.run(serve(
        host=args.host, port=args.port, access_token=args.access_token,
        username=args.username, password=args.password,
        refresh_override=refresh_override,
    ))


if __name__ == "__main__":
    main()
