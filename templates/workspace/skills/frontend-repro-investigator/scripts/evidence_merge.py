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


def strip_yaml_scalar(value: str) -> str:
    value = value.strip()
    if "#" in value:
        value = value.split("#", 1)[0].strip()
    return value.strip("\"'")


def parse_frontend_entry_map(path: str) -> dict[str, list[dict]]:
    """Parse generated frontend-entry-map.yaml without requiring PyYAML."""
    parsed = parse_frontend_entry_map_with_pyyaml(path)
    if parsed is not None:
        return parsed

    endpoint_services: dict[str, list[dict]] = {}
    current_repo = ""
    current_endpoint = ""
    in_entries = False
    in_paths = False
    in_routes = False
    in_services = False
    current_route: dict | None = None

    def finish_route() -> None:
        nonlocal current_route
        if current_endpoint and current_route and current_route.get("service"):
            endpoint_services.setdefault(current_endpoint, []).append({
                "endpoint": current_endpoint,
                "frontend_repo": current_repo,
                "service": current_route.get("service", ""),
                "match": current_route.get("match", ""),
                "route": current_route.get("route", ""),
                "method": current_route.get("method", ""),
                "source": current_route.get("source", "route_candidates"),
            })
        current_route = None

    for raw in Path(path).read_text(encoding="utf-8").splitlines():
        if not raw.strip() or raw.strip().startswith("#"):
            continue
        if raw.strip() == "frontend_entries:":
            in_entries = True
            continue
        if not in_entries:
            continue

        if raw.startswith("  ") and not raw.startswith("    ") and raw.strip().endswith(":"):
            finish_route()
            current_repo = strip_yaml_scalar(raw.strip()[:-1])
            current_endpoint = ""
            in_paths = False
            in_routes = False
            in_services = False
            continue
        if raw.startswith("    path_candidates:"):
            finish_route()
            in_paths = True
            in_routes = False
            in_services = False
            continue
        if in_paths and raw.startswith("      ") and not raw.startswith("        ") and raw.strip().endswith(":"):
            finish_route()
            current_endpoint = strip_yaml_scalar(raw.strip()[:-1])
            in_routes = False
            in_services = False
            continue
        if raw.startswith("        route_candidates:"):
            finish_route()
            in_routes = True
            in_services = False
            continue
        if raw.startswith("        candidate_services:"):
            finish_route()
            in_routes = False
            in_services = True
            continue

        text = raw.strip()
        if in_routes and text.startswith("- service:"):
            finish_route()
            current_route = {"service": strip_yaml_scalar(text.split(":", 1)[1])}
            continue
        if in_routes and current_route is not None and ":" in text:
            key, value = text.split(":", 1)
            if key in {"match", "route", "method", "source"}:
                current_route[key] = strip_yaml_scalar(value)
            continue
        if in_services and text.startswith("- "):
            service = strip_yaml_scalar(text[2:])
            if current_endpoint and service:
                endpoint_services.setdefault(current_endpoint, []).append({
                    "endpoint": current_endpoint,
                    "frontend_repo": current_repo,
                    "service": service,
                    "match": "fallback",
                    "route": "",
                    "method": "",
                    "source": "candidate_services",
                })
    finish_route()
    return endpoint_services


def parse_frontend_entry_map_with_pyyaml(path: str) -> dict[str, list[dict]] | None:
    try:
        import yaml  # type: ignore
    except Exception:
        return None
    try:
        payload = yaml.safe_load(Path(path).read_text(encoding="utf-8")) or {}
    except Exception:
        return None
    entries = payload.get("frontend_entries") if isinstance(payload, dict) else None
    if not isinstance(entries, dict):
        return None

    endpoint_services: dict[str, list[dict]] = {}
    for frontend_repo, raw_entry in entries.items():
        if not isinstance(raw_entry, dict):
            continue
        path_candidates = raw_entry.get("path_candidates") or {}
        if not isinstance(path_candidates, dict):
            continue
        for endpoint, raw_candidate in path_candidates.items():
            endpoint = str(endpoint)
            if not isinstance(raw_candidate, dict):
                raw_candidate = {}
            for route in raw_candidate.get("route_candidates") or []:
                if not isinstance(route, dict) or not route.get("service"):
                    continue
                endpoint_services.setdefault(endpoint, []).append({
                    "endpoint": endpoint,
                    "frontend_repo": str(frontend_repo),
                    "service": str(route.get("service", "")),
                    "match": str(route.get("match", "")),
                    "route": str(route.get("route", "")),
                    "method": str(route.get("method", "")),
                    "source": str(route.get("source", "route_candidates")),
                })
            for service in raw_candidate.get("candidate_services") or []:
                if not service:
                    continue
                endpoint_services.setdefault(endpoint, []).append({
                    "endpoint": endpoint,
                    "frontend_repo": str(frontend_repo),
                    "service": str(service),
                    "match": "fallback",
                    "route": "",
                    "method": "",
                    "source": "candidate_services",
                })
    return endpoint_services


def score_endpoint(endpoint: str, source: str, trace_count: int, hit_count: int = 1) -> tuple[int, int, int, int, str]:
    exact_api_bonus = 5 if endpoint == "/graphql" or endpoint.startswith("/api/") or endpoint == "/api" else 0
    depth = endpoint.count("/")
    return (hit_count, trace_count, SOURCE_WEIGHT.get(source, 0) + exact_api_bonus, depth, endpoint)


def candidate_services_for_endpoints(endpoints: list[str], entry_map: dict[str, list[dict]] | None) -> list[dict]:
    if not entry_map:
        return []
    seen = set()
    out = []
    for endpoint in endpoints:
        for item in entry_map.get(endpoint, []):
            key = (
                item.get("endpoint"),
                item.get("frontend_repo"),
                item.get("service"),
                item.get("match"),
                item.get("route"),
                item.get("method"),
                item.get("source"),
            )
            if key in seen:
                continue
            seen.add(key)
            out.append(item)
    return out


def merge(items: list[tuple[str, dict]], entry_map: dict[str, list[dict]] | None = None) -> dict:
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
    ranked_endpoint_paths = [item["endpoint"] for item in ranked_endpoints]
    candidate_services = candidate_services_for_endpoints(ranked_endpoint_paths, entry_map)
    return {
        "summary": {
            "source_count": len(items),
            "trace_id_count": len(unique(trace_ids)),
            "candidate_endpoint_count": len(ranked_endpoints),
            "candidate_service_count": len(candidate_services),
            "frontend_finding_count": len(findings),
        },
        "sources": sources,
        "frontend_findings": findings[:40],
        "backend_handoff": {
            "trace_ids": unique(trace_ids),
            "candidate_endpoints": ranked_endpoint_paths,
            "candidate_services": candidate_services,
            "endpoint_sources": [
                {"endpoint": item["endpoint"], "source": item["source"]}
                for item in ranked_endpoints
            ],
        },
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Merge frontend evidence analyzer JSON outputs.")
    parser.add_argument("--frontend-entry-map", help="Optional routing/references/frontend-entry-map.yaml.")
    parser.add_argument("files", nargs="+", help="Analyzer JSON files, or '-' for stdin.")
    args = parser.parse_args()

    try:
        payloads = [(path, read_payload(path)) for path in args.files]
        entry_map = parse_frontend_entry_map(args.frontend_entry_map) if args.frontend_entry_map else None
        print(json.dumps(merge(payloads, entry_map), ensure_ascii=False, indent=2))
        return 0
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        print(json.dumps({"error": {"code": 2, "message": str(exc)}}, ensure_ascii=False, indent=2))
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
