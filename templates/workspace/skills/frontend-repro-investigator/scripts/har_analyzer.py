#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import re
import sys
from urllib.parse import urlparse


TRACE_HEADERS = {
    "x-trace-id",
    "trace-id",
    "x-request-id",
    "request-id",
    "x-correlation-id",
    "traceparent",
}
STATIC_EXTENSIONS = (".js", ".css", ".map", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".woff", ".woff2")


def header_values(headers: list[dict], names: set[str]) -> list[str]:
    values = []
    for h in headers or []:
        name = str(h.get("name", "")).lower()
        value = str(h.get("value", "")).strip()
        if name in names and value:
            values.append(value)
    return values


def redact_text(text: str) -> str:
    text = re.sub(
        r"(?i)(token|password|secret|authorization|cookie)([\"']?\s*[:=]\s*[\"']?)[^,\"'&\s}]+",
        r"\1\2<redacted>",
        str(text),
    )
    text = re.sub(r"(?i)(bearer\s+)[a-z0-9._~+/=-]+", r"\1<redacted>", text)
    return text


def normalized_trace_values(values: list[str]) -> list[str]:
    out = []
    for value in values:
        if value.startswith("00-") and value.count("-") >= 3:
            parts = value.split("-")
            if len(parts) >= 4 and len(parts[1]) == 32:
                out.append(parts[1])
                continue
        out.append(value)
    return out


def response_header(entry: dict, name: str) -> str:
    target = name.lower()
    for h in ((entry.get("response") or {}).get("headers") or []):
        if str(h.get("name", "")).lower() == target:
            return str(h.get("value", "")).strip()
    return ""


def path_for(url: str) -> str:
    parsed = urlparse(url)
    return parsed.path or "/"


def is_static_asset(url: str) -> bool:
    return path_for(url).lower().endswith(STATIC_EXTENSIONS)


def body_snippet(entry: dict) -> str:
    text = (((entry.get("response") or {}).get("content") or {}).get("text") or "")
    return redact_text(text)[:500]


def summarize_entry(entry: dict) -> dict:
    req = entry.get("request") or {}
    resp = entry.get("response") or {}
    headers = []
    headers.extend(header_values(req.get("headers") or [], TRACE_HEADERS))
    headers.extend(header_values(resp.get("headers") or [], TRACE_HEADERS))
    return {
        "started_at": entry.get("startedDateTime", ""),
        "method": req.get("method", "GET"),
        "url": redact_text(req.get("url", "")),
        "path": path_for(req.get("url", "")),
        "status": int(resp.get("status") or 0),
        "duration_ms": int(entry.get("time") or 0),
        "trace_ids": sorted(set(normalized_trace_values(headers))),
        "response_snippet": body_snippet(entry),
    }


def analyze(har: dict) -> dict:
    entries = (((har.get("log") or {}).get("entries")) or [])
    failed = []
    slow = []
    frontend_findings = []
    candidate_endpoints = []
    trace_ids = set()

    for entry in entries:
        item = summarize_entry(entry)
        status = item["status"]
        url = item["url"]
        for trace_id in item["trace_ids"]:
            trace_ids.add(trace_id)
        if status == 0:
            failed.append(item)
            frontend_findings.append({
                "type": "network_request_aborted",
                "url": url,
                "status": status,
                "hint": "Browser saw status 0. Check CORS, DNS/TLS, ad blockers, gateway reset, or client-side aborts.",
            })
            candidate_endpoints.append(item["path"])
            continue
        if item["method"].upper() == "OPTIONS" and status >= 400:
            frontend_findings.append({
                "type": "cors_preflight_failed",
                "url": url,
                "status": status,
                "hint": "Check gateway CORS policy, allowed origin, allowed headers, and credentials mode.",
            })
        location = response_header(entry, "Location")
        if 300 <= status < 400 and location and re.search(r"(?i)(login|sso|oauth|auth)", location):
            frontend_findings.append({
                "type": "auth_redirect",
                "url": url,
                "status": status,
                "location": redact_text(location),
                "hint": "Check frontend auth state, session expiry, gateway auth middleware, and environment domain config.",
            })
            candidate_endpoints.append(item["path"])
        if item["path"].endswith("/graphql") and '"errors"' in item["response_snippet"]:
            frontend_findings.append({
                "type": "graphql_error_response",
                "url": url,
                "status": status,
                "hint": "HTTP 200 contains GraphQL errors. Inspect resolver error, operation name, and backend trace/logs.",
            })
            candidate_endpoints.append(item["path"])
        if status >= 400:
            failed.append(item)
            if is_static_asset(url):
                frontend_findings.append({
                    "type": "static_asset_failed",
                    "url": url,
                    "status": status,
                    "hint": "Check frontend deploy version, CDN cache, and stale index.html referencing removed chunks.",
                })
            else:
                candidate_endpoints.append(item["path"])
        if item["duration_ms"] >= 1000 and not is_static_asset(url):
            slow.append(item)
            candidate_endpoints.append(item["path"])

    return {
        "summary": {
            "entry_count": len(entries),
            "failed_request_count": len(failed),
            "slow_request_count": len(slow),
            "frontend_finding_count": len(frontend_findings),
        },
        "failed_requests": failed[:20],
        "slow_requests": sorted(slow, key=lambda x: x["duration_ms"], reverse=True)[:20],
        "frontend_findings": frontend_findings[:20],
        "backend_handoff": {
            "trace_ids": sorted(trace_ids),
            "candidate_endpoints": sorted(set(candidate_endpoints)),
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Analyze HAR evidence for frontend-to-backend troubleshooting.")
    parser.add_argument("--file", help="HAR file path. If omitted, read stdin.")
    args = parser.parse_args()
    raw = open(args.file, "r", encoding="utf-8").read() if args.file else sys.stdin.read()
    payload = analyze(json.loads(raw))
    print(json.dumps(payload, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
