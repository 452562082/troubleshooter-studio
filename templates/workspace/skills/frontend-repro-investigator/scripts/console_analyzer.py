#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import re
import sys
from urllib.parse import parse_qsl, urlencode, urlparse, urlunparse


TRACE_HEADER_RE = re.compile(
    r"(?im)\b(x-request-id|request-id|x-trace-id|trace-id|x-correlation-id)\s*[:=]\s*([^\s,;]+)"
)
URL_RE = re.compile(r"https?://[^\s\"'<>]+|/(?:api(?:/[\w./%:-]*)?|graphql)\b[^\s\"'<>]*")
REQUEST_RE = re.compile(
    r"(?im)\b(GET|POST|PUT|PATCH|DELETE|OPTIONS|HEAD)\s+"
    r"(https?://[^\s\"'<>]+|/(?:api(?:/[\w./%:-]*)?|graphql)\b[^\s\"'<>]*)"
    r"(?:\s+|\s*-\s*)(\d{3})\b"
)
STACK_FRAME_RE = re.compile(r"(?m)^\s*at\s+.+$")
CHUNK_LOAD_RE = re.compile(r"(?i)\b(ChunkLoadError|Loading chunk [\w.-]+ failed|failed to fetch dynamically imported module)\b")
CSP_RE = re.compile(r"(?i)\b(Content Security Policy|violates the following Content Security Policy directive|refused to load|refused to connect|refused to execute)\b")
MIXED_CONTENT_RE = re.compile(r"(?i)\b(Mixed Content|blocked:mixed-content|was loaded over HTTPS, but requested an insecure|SSL|TLS|certificate)\b")
SOURCE_MAP_RE = re.compile(r"(?i)\b(source map|sourcemap|\.map\b).*(404|not found|failed|error)|failed to parse source map\b")
SENSITIVE_KEYS = ("token", "password", "secret", "authorization", "cookie")
SENSITIVE_QUERY_KEY_PARTS = ("token", "password", "secret", "key", "auth", "session", "cookie")
SENSITIVE_HEADER_KEYS = {
    "authorization",
    "cookie",
    "set-cookie",
    "x-api-key",
}
TRACE_JSON_HEADER_KEYS = {
    "x-request-id",
    "request-id",
    "x-trace-id",
    "trace-id",
    "x-correlation-id",
}


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
    return urlunparse(parsed._replace(query=urlencode(query)))


def redact_text(text: str) -> str:
    text = str(text)
    text = re.sub(
        r"(?im)^(\s*(?:authorization|cookie|set-cookie|x-api-key)\s*[:=]\s*).+$",
        r"\1<redacted>",
        text,
    )
    text = re.sub(
        r"(?i)\b((?:authorization|cookie|set-cookie|x-api-key)\s*[:=]\s*)[^\n\r]+",
        r"\1<redacted>",
        text,
    )
    text = URL_RE.sub(lambda match: redact_url(match.group(0)), text)
    text = re.sub(
        r"(?i)([?&](?:token|password|secret)=)[^&#\s\"'<>]+",
        r"\1<redacted>",
        text,
    )
    text = re.sub(r"(?i)\b(bearer\s+)[a-z0-9._~+/=-]+", r"\1<redacted>", text)
    text = re.sub(
        r"(?i)([\"']?(?:token|password|secret|authorization|cookie)[\"']?\s*[:=]\s*[\"']?)[^,\"'\n\r}&\s]+",
        r"\1<redacted>",
        text,
    )
    return text


def sensitive_key(key: str) -> bool:
    lower = str(key).lower()
    return lower in SENSITIVE_HEADER_KEYS or any(item in lower for item in SENSITIVE_KEYS)


def redact_json_value(value):
    if isinstance(value, dict):
        return {
            key: "<redacted>" if sensitive_key(key) else redact_json_value(val)
            for key, val in value.items()
        }
    if isinstance(value, list):
        return [redact_json_value(item) for item in value]
    if isinstance(value, str):
        return redact_text(value)
    return value


def input_preview(raw: str, parsed=None) -> str:
    if parsed is not None:
        text = json.dumps(redact_json_value(parsed), ensure_ascii=False, sort_keys=True)
    else:
        text = redact_text(raw)
    return text[:1200]


def endpoint_for_url(value: str) -> str:
    value = clean_url(value)
    parsed = urlparse(value)
    path = parsed.path or value.split("?", 1)[0]
    if path == "/graphql" or path.startswith("/api/") or path == "/api":
        return path
    return ""


def extract_trace_ids_from_text(text: str) -> list[str]:
    return unique([match.group(2).strip().strip("\"'") for match in TRACE_HEADER_RE.finditer(text)])


def extract_urls(text: str) -> list[str]:
    return unique([clean_url(match.group(0)) for match in URL_RE.finditer(text)])


def parse_text(text: str) -> dict:
    trace_ids = extract_trace_ids_from_text(text)
    urls = extract_urls(text)
    endpoints = [endpoint_for_url(url) for url in urls]
    endpoints = unique([endpoint for endpoint in endpoints if endpoint])
    stack_frames = [frame.strip() for frame in STACK_FRAME_RE.findall(text)]

    frontend_findings = []
    for match in REQUEST_RE.finditer(text):
        method = match.group(1).upper()
        url = clean_url(match.group(2))
        status = int(match.group(3))
        endpoint = endpoint_for_url(url)
        if endpoint and status >= 400:
            frontend_findings.append(
                {
                    "type": "console_api_failure",
                    "method": method,
                    "status": status,
                    "endpoint": endpoint,
                    "url": redact_url(redact_text(url)),
                }
            )

    if stack_frames:
        frontend_findings.append(
            {
                "type": "js_exception",
                "stack_top": redact_text(stack_frames[0]),
                "stack_frames": [redact_text(frame) for frame in stack_frames[:5]],
            }
        )
    if CHUNK_LOAD_RE.search(text):
        frontend_findings.append(
            {
                "type": "chunk_load_error",
                "hint": "A JS chunk failed to load. Check stale index.html, CDN cache, deploy asset retention, and frontend version skew.",
            }
        )
    if CSP_RE.search(text):
        frontend_findings.append(
            {
                "type": "csp_violation",
                "hint": "Browser console indicates Content Security Policy blocking. Compare the blocked URL with script/connect/img directives.",
            }
        )
    if MIXED_CONTENT_RE.search(text):
        frontend_findings.append(
            {
                "type": "mixed_content_or_tls_block",
                "hint": "Browser security blocked a request or asset. Check HTTPS origins, TLS certificate, HSTS, and mixed-content policy.",
            }
        )
    if SOURCE_MAP_RE.search(text):
        frontend_findings.append(
            {
                "type": "source_map_reference",
                "hint": "Source map evidence may help map minified stack traces, or indicate missing map files in the frontend deploy.",
            }
        )

    return {
        "trace_ids": trace_ids,
        "candidate_endpoints": endpoints,
        "frontend_findings": frontend_findings,
        "stack_frame_count": len(stack_frames),
        "url_count": len(urls),
    }


def collect_json_strings(value) -> list[str]:
    if isinstance(value, dict):
        strings = []
        for key, item in value.items():
            strings.append(str(key))
            strings.extend(collect_json_strings(item))
        return strings
    if isinstance(value, list):
        strings = []
        for item in value:
            strings.extend(collect_json_strings(item))
        return strings
    if isinstance(value, (str, int, float)):
        return [str(value)]
    return []


def collect_json_trace_ids(value) -> list[str]:
    traces = []
    if isinstance(value, dict):
        for key, item in value.items():
            lower_key = str(key).lower()
            normalized_key = lower_key.replace("_", "")
            if normalized_key == "traceid" and item:
                traces.append(str(item))
            elif lower_key in TRACE_JSON_HEADER_KEYS and item:
                traces.append(str(item))
            traces.extend(collect_json_trace_ids(item))
    elif isinstance(value, list):
        for item in value:
            traces.extend(collect_json_trace_ids(item))
    return traces


def load_json_object_or_array(raw: str):
    try:
        value = json.loads(raw)
    except json.JSONDecodeError:
        return None
    if isinstance(value, (dict, list)):
        return value
    return None


def analyze(raw: str) -> dict:
    parsed = load_json_object_or_array(raw)
    if parsed is None:
        text = raw
        source_type = "text"
        json_trace_ids = []
    else:
        text = "\n".join(collect_json_strings(parsed))
        source_type = "json"
        json_trace_ids = collect_json_trace_ids(parsed)

    text_result = parse_text(text)
    trace_ids = unique(json_trace_ids + text_result["trace_ids"])
    endpoints = unique(text_result["candidate_endpoints"])
    findings = text_result["frontend_findings"]

    return {
        "summary": {
            "source_type": source_type,
            "frontend_finding_count": len(findings),
            "candidate_endpoint_count": len(endpoints),
            "trace_id_count": len(trace_ids),
            "stack_frame_count": text_result["stack_frame_count"],
        },
        "frontend_findings": findings[:20],
        "backend_handoff": {
            "trace_ids": trace_ids,
            "candidate_endpoints": endpoints,
        },
        "redacted_input_preview": input_preview(raw, parsed),
    }


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Analyze browser console or Sentry-like evidence for frontend-to-backend troubleshooting."
    )
    parser.add_argument("--file", help="Console text or JSON file path. If omitted, read stdin.")
    args = parser.parse_args()
    raw = open(args.file, "r", encoding="utf-8").read() if args.file else sys.stdin.read()
    print(json.dumps(analyze(raw), ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
