#!/usr/bin/env python3
"""Read generated routing topology and return bounded service paths as JSON."""

from __future__ import annotations

import argparse
import json
import re
from pathlib import Path
from urllib.parse import urlsplit

import yaml


FORMAL_STATUSES = {"automatic", "confirmed", "manual"}
HUMAN_STATUSES = {"confirmed", "manual"}
PARAM_SEGMENT = re.compile(r"^(?::[^/]+|\{[^/]+\}|\[[^/]+\]|<[^/]+>)$")


def normalize_path(value: object) -> str | None:
    text = str(value or "").strip()
    if not text:
        return None
    parsed = urlsplit(text)
    if parsed.scheme in {"http", "https"} and parsed.netloc:
        text = parsed.path
    else:
        text = text.split("#", 1)[0].split("?", 1)[0]
    if not text.startswith("/"):
        text = "/" + text
    text = re.sub(r"/+", "/", text)
    if text != "/":
        text = text.rstrip("/")
    parts = ["{param}" if PARAM_SEGMENT.match(part) else part for part in text.split("/")]
    return "/".join(parts) or "/"


def path_matches(formal: object, requested: str | None) -> bool:
    if requested is None:
        return True
    left = normalize_path(formal)
    if left is None:
        return False
    left_parts = left.strip("/").split("/") if left != "/" else []
    right_parts = requested.strip("/").split("/") if requested != "/" else []
    if len(left_parts) != len(right_parts):
        return False
    return all(
        formal_part == requested_part
        or formal_part == "{param}"
        or requested_part == "{param}"
        for formal_part, requested_part in zip(left_parts, right_parts)
    )


def safe_document(path: Path) -> dict:
    loaded = yaml.safe_load(path.read_text(encoding="utf-8"))
    return loaded if isinstance(loaded, dict) else {}


def normalized_routes(raw: dict) -> list[dict]:
    route_items = raw.get("routes")
    if not isinstance(route_items, list):
        route_items = [
            {
                "protocol": raw.get("protocol"),
                "method": raw.get("method"),
                "path": raw.get("path"),
                "rpc_method": raw.get("rpc_method"),
            }
        ]
        endpoint_edges = raw.get("endpoint_edges")
        if isinstance(endpoint_edges, list) and endpoint_edges:
            route_items[0]["endpoint_edge"] = endpoint_edges[0]

    routes = []
    for item in route_items:
        if not isinstance(item, dict):
            continue
        route = {
            "protocol": str(item.get("protocol") or "").lower() or None,
            "method": str(item.get("method") or "").upper() or None,
            "path": normalize_path(item.get("path")),
            "rpc_method": str(item.get("rpc_method") or "").strip() or None,
            "endpoint_edge": str(item.get("endpoint_edge") or "").strip() or None,
        }
        routes.append(route)
    return routes


def normalized_edges(document: dict) -> list[dict]:
    result = []
    raw_edges = document.get("edges")
    if not isinstance(raw_edges, list):
        return result
    for raw in raw_edges:
        if not isinstance(raw, dict):
            continue
        source = str(raw.get("from") or "").strip()
        target = str(raw.get("to") or "").strip()
        status = str(raw.get("status") or "").strip().lower()
        if not source or not target or status not in FORMAL_STATUSES:
            continue
        try:
            confidence = float(raw.get("confidence") or 0)
        except (TypeError, ValueError):
            confidence = 0.0
        result.append(
            {
                "from": source,
                "to": target,
                "status": status,
                "confidence": confidence,
                "routes": normalized_routes(raw),
            }
        )
    result.sort(
        key=lambda edge: (
            edge["from"],
            edge["to"],
            edge["status"],
            -edge["confidence"],
            json.dumps(edge["routes"], sort_keys=True),
        )
    )
    return result


def route_matches(route: dict, method: str | None, path: str | None) -> bool:
    if method and route.get("method") != method:
        return False
    if path and not path_matches(route.get("path"), path):
        return False
    return True


def entry_edge_matches(edge: dict, method: str | None, path: str | None) -> bool:
    if not method and not path:
        return True
    return any(route_matches(route, method, path) for route in edge["routes"])


def evidence_indexes(document: dict) -> tuple[dict[str, dict], dict[str, dict]]:
    endpoints = {}
    for item in document.get("endpoints") or []:
        if isinstance(item, dict) and item.get("id"):
            endpoints[str(item["id"])] = item
    edges = {}
    for item in document.get("edges") or []:
        if isinstance(item, dict) and item.get("id"):
            edges[str(item["id"])] = item
    return endpoints, edges


def edge_evidence(
    edge: dict, endpoint_index: dict[str, dict], edge_index: dict[str, dict]
) -> list[dict]:
    result = []
    for route in edge["routes"]:
        evidence_id = route.get("endpoint_edge")
        if not evidence_id:
            continue
        raw = edge_index.get(evidence_id, {})
        from_endpoint = str(raw.get("from_endpoint") or "")
        to_endpoint = str(raw.get("to_endpoint") or "")
        result.append(
            {
                "id": evidence_id,
                "status": str(raw.get("status") or edge["status"]),
                "location": raw.get("location"),
                "from_location": endpoint_index.get(from_endpoint, {}).get("location"),
                "to_location": endpoint_index.get(to_endpoint, {}).get("location"),
                "reasons": list(raw.get("reasons") or []),
                "conflicts": list(raw.get("conflicts") or []),
            }
        )
    return result


def public_edge(
    edge: dict, endpoint_index: dict[str, dict], edge_index: dict[str, dict]
) -> dict:
    return {
        "from": edge["from"],
        "to": edge["to"],
        "status": edge["status"],
        "confidence": edge["confidence"],
        "routes": edge["routes"],
        "evidence": edge_evidence(edge, endpoint_index, edge_index),
    }


def find_paths(
    edges: list[dict],
    starts: list[str],
    method: str | None,
    path: str | None,
    max_depth: int,
) -> tuple[list[tuple[list[str], list[dict]]], bool]:
    adjacency: dict[str, list[dict]] = {}
    for edge in edges:
        adjacency.setdefault(edge["from"], []).append(edge)

    paths = []
    cycle_seen = False

    def visit(service: str, services: list[str], traversed: list[dict]) -> None:
        nonlocal cycle_seen
        if len(traversed) >= max_depth:
            if traversed:
                paths.append((services, traversed))
            return

        eligible = []
        for edge in adjacency.get(service, []):
            if not traversed and not entry_edge_matches(edge, method, path):
                continue
            if edge["to"] in services:
                cycle_seen = True
                continue
            eligible.append(edge)

        if not eligible:
            if traversed:
                paths.append((services, traversed))
            return
        for edge in eligible:
            visit(edge["to"], services + [edge["to"]], traversed + [edge])

    for start in starts:
        visit(start, [start], [])
    return paths, cycle_seen


def path_rank(item: tuple[list[str], list[dict]]) -> tuple:
    services, edges = item
    human = any(edge["status"] in HUMAN_STATUSES for edge in edges)
    confidence = min(edge["confidence"] for edge in edges)
    return (-int(human), -confidence, len(edges), tuple(services))


def candidate_warnings(document: dict) -> list[str]:
    warnings = []
    for item in document.get("edges") or []:
        if not isinstance(item, dict):
            continue
        status = str(item.get("status") or "").lower()
        if status not in {"candidate", "stale"}:
            continue
        edge_id = str(item.get("id") or "unknown")
        warnings.append(
            f"{status} relationship {edge_id} is labeled navigation evidence only"
        )
    return sorted(set(warnings))


def query(args: argparse.Namespace) -> dict:
    max_depth = max(1, min(5, args.max_depth))
    method = str(args.method or "").strip().upper() or None
    path = normalize_path(args.path)
    service = str(args.service or "").strip() or None
    query_contract = {
        "service": service,
        "method": method,
        "path": path,
        "max_depth": max_depth,
    }
    result = {
        "status": "unavailable",
        "query": query_contract,
        "paths": [],
        "warnings": [],
        "fallback": "routing_rg_read",
    }

    refs = args.workspace / "skills" / "routing" / "references"
    topology_path = refs / "service-topology.yaml"
    evidence_path = refs / "endpoint-evidence.yaml"
    if not topology_path.is_file():
        result["warnings"].append(f"missing topology file: {topology_path}")
        return result

    try:
        topology_document = safe_document(topology_path)
    except (OSError, UnicodeError, yaml.YAMLError) as exc:
        result["warnings"].append(f"cannot read topology: {exc}")
        return result

    evidence_document = {}
    if evidence_path.is_file():
        try:
            evidence_document = safe_document(evidence_path)
        except (OSError, UnicodeError, yaml.YAMLError) as exc:
            result["warnings"].append(f"cannot read endpoint evidence: {exc}")
    else:
        result["warnings"].append(f"missing endpoint evidence file: {evidence_path}")

    edges = normalized_edges(topology_document)
    if service:
        starts = [service]
    else:
        starts = sorted(
            {
                edge["from"]
                for edge in edges
                if entry_edge_matches(edge, method, path)
            }
        )
    raw_paths, cycle_seen = find_paths(edges, starts, method, path, max_depth)
    raw_paths.sort(key=path_rank)
    endpoint_index, edge_index = evidence_indexes(evidence_document)
    result["paths"] = [
        {
            "services": services,
            "edges": [
                public_edge(edge, endpoint_index, edge_index) for edge in path_edges
            ],
            "score": min(edge["confidence"] for edge in path_edges),
        }
        for services, path_edges in raw_paths
    ]
    result["warnings"].extend(candidate_warnings(evidence_document))
    if cycle_seen:
        result["warnings"].append("cycle detected and truncated")
    result["warnings"] = sorted(set(result["warnings"]))

    if result["paths"]:
        result["status"] = "ok"
        result["fallback"] = None
    else:
        result["status"] = "no_match"
        result["warnings"].append("no formal topology path matched the query")
    return result


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", type=Path, default=Path.cwd())
    parser.add_argument("--service")
    parser.add_argument("--method")
    parser.add_argument("--path")
    parser.add_argument("--max-depth", type=int, default=3)
    parser.add_argument("--json", action="store_true", help="emit stable JSON")
    args = parser.parse_args()
    if not args.service and not args.path:
        parser.error("at least one of --service or --path is required")
    return args


def main() -> None:
    args = parse_args()
    print(json.dumps(query(args), ensure_ascii=False, sort_keys=True))


if __name__ == "__main__":
    main()
