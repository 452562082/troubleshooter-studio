#!/usr/bin/env python3
from __future__ import annotations

import argparse
import http.client
import json
import os
import re
import sys
from urllib import error, request
from urllib.parse import parse_qsl, quote, urlencode, urlparse, urlunparse


URL_RE = re.compile(r"(?<![\w./-])(?:https?://[^\s\"'<>]+|/(?:api(?:/[\w./%:-]*)?|graphql)\b[^\s\"'<>]*)")
BEARER_RE = re.compile(r"(?i)\b(bearer\s+)[a-z0-9._~+/=-]+")
SENSITIVE_QUERY_KEY_PARTS = ("access_token", "token", "secret", "password", "key", "auth", "session", "cookie")
SENSITIVE_JSON_KEY_PARTS = ("token", "secret", "password", "authorization", "cookie", "api_key", "session", "auth", "key")
TRACE_KEYS = {
    "trace_id",
    "traceid",
    "x-request-id",
    "request-id",
    "x-trace-id",
    "trace-id",
    "traceparent",
}
TRACEPARENT_RE = re.compile(r"^00-([0-9a-fA-F]{32})-[0-9a-fA-F]{16}-[0-9a-fA-F]{2}$")
SENSITIVE_HEADER_RE = re.compile(r"(?im)^(\s*(?:authorization|cookie|set-cookie|x-api-key)\s*[:=]\s*).+$")
INLINE_SENSITIVE_HEADER_RE = re.compile(r"(?i)\b((?:authorization|cookie|set-cookie|x-api-key)\s*[:=]\s*)[^\n\r]+")
SENSITIVE_ASSIGNMENT_RE = re.compile(
    r"(?i)([\"']?(?:access_token|token|secret|password|key|auth|session|cookie)[\"']?\s*[:=]\s*[\"']?)[^,\"'\n\r}&#\s<>]+"
)


def unique(values: list[str]) -> list[str]:
    seen = set()
    out = []
    for value in values:
        if value and value not in seen:
            seen.add(value)
            out.append(value)
    return out


def clean_url(value: str) -> str:
    return str(value).strip().rstrip(".,);]")


def sensitive_query_key(key: str) -> bool:
    lower = str(key).lower()
    return any(part in lower for part in SENSITIVE_QUERY_KEY_PARTS)


def redact_url(value: str) -> str:
    parsed = urlparse(value)
    if not parsed.query:
        return value
    query = []
    changed = False
    for key, val in parse_qsl(parsed.query, keep_blank_values=True):
        if sensitive_query_key(key):
            query.append((key, "<redacted>"))
            changed = True
        else:
            query.append((key, val))
    if not changed:
        return value
    return urlunparse(parsed._replace(query=urlencode(query, quote_via=quote)))


def redact_text(text: str) -> str:
    text = str(text)
    text = SENSITIVE_HEADER_RE.sub(r"\1<redacted>", text)
    text = INLINE_SENSITIVE_HEADER_RE.sub(r"\1<redacted>", text)
    text = URL_RE.sub(lambda match: redact_url(match.group(0)), text)
    text = BEARER_RE.sub(r"\1<redacted>", text)
    text = SENSITIVE_ASSIGNMENT_RE.sub(r"\1<redacted>", text)
    return text


def sensitive_json_key(key: str) -> bool:
    lower = str(key).lower().replace("-", "_")
    return any(part in lower for part in SENSITIVE_JSON_KEY_PARTS)


def redact_json_value(value):
    if isinstance(value, dict):
        header_key = value.get("key") or value.get("name")
        redacted = {}
        for key, val in value.items():
            if sensitive_json_key(key):
                redacted[key] = "<redacted>"
            elif str(key).lower() == "value" and header_key and sensitive_json_key(str(header_key)):
                redacted[key] = "<redacted>"
            else:
                redacted[key] = redact_json_value(val)
        return redacted
    if isinstance(value, list):
        if len(value) >= 2 and sensitive_json_key(str(value[0])):
            return [redact_json_value(value[0]), "<redacted>"] + [redact_json_value(item) for item in value[2:]]
        return [redact_json_value(item) for item in value]
    if isinstance(value, str):
        return redact_text(value)
    return value


def endpoint_for_url(value: str) -> str:
    value = clean_url(value)
    parsed = urlparse(value)
    path = parsed.path or value.split("?", 1)[0]
    if path == "/graphql" or path == "/api" or path.startswith("/api/"):
        return path
    return ""


def collect_strings(value) -> list[str]:
    if isinstance(value, dict):
        out = []
        for key, item in value.items():
            out.append(str(key))
            out.extend(collect_strings(item))
        return out
    if isinstance(value, list):
        out = []
        for item in value:
            out.extend(collect_strings(item))
        return out
    if isinstance(value, (str, int, float)):
        return [str(value)]
    return []


def extract_candidate_endpoints(event: dict) -> list[str]:
    endpoints = []
    for text in collect_strings(event):
        for match in URL_RE.finditer(text):
            endpoint = endpoint_for_url(match.group(0))
            if endpoint:
                endpoints.append(endpoint)
    return unique(endpoints)


def trace_key_match(key: str) -> bool:
    lower = str(key).lower()
    normalized = lower.replace("_", "")
    return lower in TRACE_KEYS or normalized == "traceid"


def trace_value(value) -> str:
    text = str(value).strip()
    match = TRACEPARENT_RE.match(text)
    if match:
        return match.group(1)
    return text


def extract_trace_ids(value) -> list[str]:
    traces = []
    if isinstance(value, dict):
        header_key = value.get("key") or value.get("name")
        header_value = value.get("value")
        if trace_key_match(str(header_key)) and header_value:
            traces.append(trace_value(header_value))
        for key, item in value.items():
            if trace_key_match(key) and item:
                traces.append(trace_value(item))
            elif isinstance(item, (list, tuple)) and len(item) >= 2 and trace_key_match(str(item[0])) and item[1]:
                traces.append(trace_value(item[1]))
            traces.extend(extract_trace_ids(item))
    elif isinstance(value, list):
        for item in value:
            if isinstance(item, (list, tuple)) and len(item) >= 2 and trace_key_match(str(item[0])) and item[1]:
                traces.append(trace_value(item[1]))
            traces.extend(extract_trace_ids(item))
    return unique(traces)


def collect_stack_frames(value) -> list[dict]:
    frames = []
    if isinstance(value, dict):
        for key, item in value.items():
            if key == "frames" and isinstance(item, list):
                frames.extend([frame for frame in item if isinstance(frame, dict)])
            else:
                frames.extend(collect_stack_frames(item))
    elif isinstance(value, list):
        for item in value:
            frames.extend(collect_stack_frames(item))
    return frames


def summarize_frame(frame: dict) -> str:
    function = frame.get("function") or "<anonymous>"
    filename = frame.get("filename") or frame.get("absPath") or frame.get("module") or ""
    line = frame.get("lineno") or frame.get("lineNo") or ""
    suffix = f":{line}" if line else ""
    return redact_text(f"{function} ({filename}{suffix})")


def event_message(event: dict) -> str:
    for key in ("message", "title", "culprit"):
        value = event.get(key)
        if value:
            return redact_text(str(value))
    values = (((event.get("exception") or {}).get("values")) or [])
    if values:
        item = values[0] or {}
        return redact_text(" ".join(str(item.get(key, "")) for key in ("type", "value")).strip())
    return ""


def normalize_event(event: dict) -> dict:
    endpoints = extract_candidate_endpoints(event)
    trace_ids = extract_trace_ids(event)
    frames = collect_stack_frames(event)
    message = event_message(event)

    frontend_findings = []
    if message:
        frontend_findings.append({"type": "sentry_event", "message": message})
    if endpoints:
        frontend_findings.append({"type": "api_endpoint_seen", "endpoint": endpoints[0]})
    if frames:
        frontend_findings.append(
            {
                "type": "js_exception",
                "stack_top": summarize_frame(frames[-1]),
                "stack_frames": [summarize_frame(frame) for frame in frames[-5:]],
            }
        )

    event_id = str(event.get("eventID") or event.get("event_id") or event.get("id") or "")
    return {
        "summary": {
            "event_id": event_id,
            "message": message,
            "frontend_finding_count": len(frontend_findings),
            "candidate_endpoint_count": len(endpoints),
            "trace_id_count": len(trace_ids),
            "stack_frame_count": len(frames),
        },
        "frontend_findings": frontend_findings[:20],
        "backend_handoff": {
            "trace_ids": trace_ids,
            "candidate_endpoints": endpoints,
        },
        "redacted_event_preview": json.dumps(
            redact_json_value(event),
            ensure_ascii=False,
            sort_keys=True,
        )[:2000],
    }


def json_error(code: int, message: str) -> dict:
    return {"error": {"code": code, "message": message}}


def fetch_event(base_url: str, organization: str, project: str, event_id: str, token: str) -> dict:
    base = base_url.rstrip("/")
    org = quote(organization.strip("/"), safe="")
    proj = quote(project.strip("/"), safe="")
    event = quote(event_id.strip("/"), safe="")
    url = f"{base}/api/0/projects/{org}/{proj}/events/{event}/"
    req = request.Request(url, headers={"Authorization": f"Bearer {token}", "Accept": "application/json"})
    opener = request.build_opener(request.ProxyHandler({}))
    with opener.open(req, timeout=20) as resp:
        data = resp.read()
    parsed = json.loads(data.decode("utf-8"))
    if not isinstance(parsed, dict):
        raise ValueError("Sentry event response is not a JSON object")
    return parsed


def main() -> int:
    parser = argparse.ArgumentParser(description="Fetch and normalize a Sentry event for backend handoff.")
    parser.add_argument("--base-url", required=True, help="Sentry base URL, for example https://sentry.example.com")
    parser.add_argument("--organization", required=True)
    parser.add_argument("--project", required=True)
    parser.add_argument("--event-id", required=True)
    parser.add_argument("--token", default=os.environ.get("SENTRY_AUTH_TOKEN"))
    args = parser.parse_args()

    if not args.token:
        print(
            json.dumps(
                json_error(2, "Missing Sentry auth token. Pass --token or set SENTRY_AUTH_TOKEN."),
                ensure_ascii=False,
                indent=2,
            )
        )
        return 2

    try:
        event = fetch_event(args.base_url, args.organization, args.project, args.event_id, args.token)
        print(json.dumps(normalize_event(event), ensure_ascii=False, indent=2))
        return 0
    except (error.URLError, error.HTTPError, TimeoutError, OSError, ValueError, json.JSONDecodeError, http.client.HTTPException) as exc:
        print(
            json.dumps(
                json_error(3, f"Failed to fetch or parse Sentry event: {redact_text(str(exc))}"),
                ensure_ascii=False,
                indent=2,
            )
        )
        return 3


if __name__ == "__main__":
    raise SystemExit(main())
