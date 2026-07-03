#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path


SOURCE_WEIGHT = {
    "sentry": 30,
    "har": 20,
    "console": 10,
    "browser": 15,
    "unknown": 0,
}


def unique(items):
    seen = set()
    out = []
    for item in items:
        if item and item not in seen:
            seen.add(item)
            out.append(item)
    return out


def infer_source(path: str, payload: dict) -> str:
    name = Path(path).name.lower()
    if "sentry" in name or "redacted_event_preview" in payload:
        return "sentry"
    if "har" in name or "failed_requests" in payload or "slow_requests" in payload:
        return "har"
    if "console" in name or "redacted_input_preview" in payload:
        return "console"
    if "browser" in name or "network.har" in json.dumps(payload, ensure_ascii=False):
        return "browser"
    return "unknown"


def read_payload(path: str) -> dict:
    raw = sys.stdin.read() if path == "-" else Path(path).read_text(encoding="utf-8")
    payload = json.loads(raw)
    if not isinstance(payload, dict):
        raise ValueError(f"{path}: evidence payload must be a JSON object")
    return payload


def score_endpoint(endpoint: str, source: str, trace_count: int, hit_count: int = 1) -> tuple[int, int, int, int, str]:
    exact_api_bonus = 5 if endpoint == "/graphql" or endpoint.startswith("/api/") or endpoint == "/api" else 0
    depth = endpoint.count("/")
    return (hit_count, trace_count, SOURCE_WEIGHT.get(source, 0) + exact_api_bonus, depth, endpoint)


def merge(items: list[tuple[str, dict]]) -> dict:
    trace_ids = []
    endpoint_hits = {}
    findings = []
    sources = []

    for path, payload in items:
        source = infer_source(path, payload)
        handoff = payload.get("backend_handoff") or {}
        traces = [str(item) for item in (handoff.get("trace_ids") or []) if item]
        endpoints = [str(item) for item in (handoff.get("candidate_endpoints") or []) if item]
        trace_ids.extend(traces)

        for endpoint in endpoints:
            current = endpoint_hits.get(endpoint)
            hit_count = 1 if current is None else current["hit_count"] + 1
            candidate = {
                "endpoint": endpoint,
                "source": source,
                "hit_count": hit_count,
                "score": score_endpoint(endpoint, source, len(traces), hit_count),
            }
            if current is not None and current["score"] > candidate["score"]:
                current["hit_count"] = hit_count
                current["score"] = score_endpoint(endpoint, current["source"], len(traces), hit_count)
                continue
            endpoint_hits[endpoint] = candidate

        for finding in payload.get("frontend_findings") or []:
            if isinstance(finding, dict):
                item = dict(finding)
                item["source"] = source
                findings.append(item)

        sources.append({
            "path": path,
            "source": source,
            "trace_id_count": len(traces),
            "candidate_endpoint_count": len(endpoints),
            "frontend_finding_count": len(payload.get("frontend_findings") or []),
        })

    ranked_endpoints = sorted(endpoint_hits.values(), key=lambda item: item["score"], reverse=True)
    return {
        "summary": {
            "source_count": len(items),
            "trace_id_count": len(unique(trace_ids)),
            "candidate_endpoint_count": len(ranked_endpoints),
            "frontend_finding_count": len(findings),
        },
        "sources": sources,
        "frontend_findings": findings[:40],
        "backend_handoff": {
            "trace_ids": unique(trace_ids),
            "candidate_endpoints": [item["endpoint"] for item in ranked_endpoints],
            "endpoint_sources": [
                {"endpoint": item["endpoint"], "source": item["source"]}
                for item in ranked_endpoints
            ],
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Merge frontend evidence analyzer JSON outputs.")
    parser.add_argument("files", nargs="+", help="Analyzer JSON files, or '-' for stdin.")
    args = parser.parse_args()

    try:
        payloads = [(path, read_payload(path)) for path in args.files]
        print(json.dumps(merge(payloads), ensure_ascii=False, indent=2))
        return 0
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        print(json.dumps({"error": {"code": 2, "message": str(exc)}}, ensure_ascii=False, indent=2))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
