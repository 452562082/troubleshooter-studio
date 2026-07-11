#!/usr/bin/env python3
"""Read generated routing topology and return bounded service paths as JSON."""

from __future__ import annotations

import argparse
import json
import math
import re
from functools import lru_cache
from pathlib import Path
from urllib.parse import urlsplit

import yaml


FORMAL_STATUSES = {"automatic", "confirmed", "manual"}
HUMAN_STATUSES = {"confirmed", "manual"}
STATUS_PRIORITY = {"automatic": 1, "confirmed": 2, "manual": 3}


class DocumentError(ValueError):
    """A generated routing document does not satisfy the query contract."""


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
    parts = [normalize_segment(part) for part in text.split("/")]
    return "/".join(parts) or "/"


def normalize_segment(segment: str) -> str:
    if segment.startswith("*"):
        return "{wildcard}"
    if segment == "{wildcard}":
        return segment
    if segment.startswith(":"):
        return "{wildcard}" if segment.endswith("*") else "{param}"
    if segment.startswith("{") and segment.endswith("}"):
        return "{wildcard}" if segment[1:].startswith("*") else "{param}"
    if segment.startswith("[") and segment.endswith("]"):
        return "{wildcard}" if "..." in segment else "{param}"
    return segment


def path_matches(formal: object, requested: str | None) -> bool:
    if requested is None:
        return True
    left = normalize_path(formal)
    if left is None:
        return False
    left_parts = left.strip("/").split("/") if left != "/" else []
    right_parts = requested.strip("/").split("/") if requested != "/" else []

    @lru_cache(maxsize=None)
    def match(left_index: int, right_index: int) -> bool:
        if left_index == len(left_parts):
            return right_index == len(right_parts)
        formal_part = left_parts[left_index]
        if formal_part == "{wildcard}":
            return any(
                match(left_index + 1, next_index)
                for next_index in range(right_index + 1, len(right_parts) + 1)
            )
        if right_index == len(right_parts):
            return False
        if formal_part == "{param}" or formal_part == right_parts[right_index]:
            return match(left_index + 1, right_index + 1)
        return False

    return match(0, 0)


def safe_document(path: Path, label: str) -> dict:
    loaded = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(loaded, dict):
        raise DocumentError(f"{label} root must be a mapping")
    return loaded


def required_list(document: dict, key: str, label: str) -> list:
    value = document.get(key)
    if not isinstance(value, list):
        raise DocumentError(f"{label}.{key} must be a list")
    return value


def finite_confidence(value: object, label: str) -> float:
    if isinstance(value, bool):
        raise DocumentError(f"{label} must be a finite number")
    try:
        confidence = float(value)
    except (TypeError, ValueError) as exc:
        raise DocumentError(f"{label} must be a finite number") from exc
    if not math.isfinite(confidence):
        raise DocumentError(f"{label} must be a finite number")
    return confidence


def validate_topology(document: dict) -> bool:
    services = document.get("services")
    if not isinstance(services, dict):
        raise DocumentError("topology.services must be a mapping")
    if any(not isinstance(name, str) or not isinstance(item, dict) for name, item in services.items()):
        raise DocumentError("topology.services entries must be mappings keyed by service")
    edges = required_list(document, "edges", "topology")
    for edge_index, edge in enumerate(edges):
        if not isinstance(edge, dict):
            raise DocumentError(f"topology.edges[{edge_index}] must be a mapping")
    flat_edges = bool(edges) and all("routes" not in edge for edge in edges)
    if flat_edges and str(document.get("schema_version") or "").strip():
        raise DocumentError("schema-versioned topology edges must use routes")
    if not flat_edges and any("routes" not in edge for edge in edges):
        raise DocumentError("topology cannot mix flat edges and route edges")

    for edge_index, edge in enumerate(edges):
        label = f"topology.edges[{edge_index}]"
        for key in ("from", "to"):
            if not isinstance(edge.get(key), str) or not edge[key].strip():
                raise DocumentError(f"{label}.{key} must be a non-empty string")
        status = edge.get("status")
        if not isinstance(status, str) or status.strip().lower() not in FORMAL_STATUSES:
            raise DocumentError(f"{label}.status must be a formal status")
        finite_confidence(edge.get("confidence"), f"{label}.confidence")
        if flat_edges:
            if not isinstance(edge.get("protocol"), str) or not edge["protocol"].strip():
                raise DocumentError(f"{label}.protocol must be a non-empty string")
            for key in ("method", "path", "rpc_method"):
                if key in edge and not isinstance(edge[key], str):
                    raise DocumentError(f"{label}.{key} must be a string")
            endpoint_edges = edge.get("endpoint_edges")
            if (
                not isinstance(endpoint_edges, list)
                or not endpoint_edges
                or any(not isinstance(item, str) or not item.strip() for item in endpoint_edges)
            ):
                raise DocumentError(f"{label}.endpoint_edges must be a non-empty list of strings")
            continue
        routes = edge.get("routes")
        if not isinstance(routes, list):
            raise DocumentError(f"{label}.routes must be a list")
        if not routes:
            raise DocumentError(f"{label}.routes must not be empty")
        for route_index, route in enumerate(routes):
            route_label = f"{label}.routes[{route_index}]"
            if not isinstance(route, dict):
                raise DocumentError(f"{route_label} must be a mapping")
            for key in ("protocol", "endpoint_edge"):
                if not isinstance(route.get(key), str) or not route[key].strip():
                    raise DocumentError(f"{route_label}.{key} must be a non-empty string")
            for key in ("method", "path", "rpc_method"):
                if key in route and not isinstance(route[key], str):
                    raise DocumentError(f"{route_label}.{key} must be a string")
    return flat_edges


def validate_evidence(document: dict, legacy_flat: bool) -> None:
    if legacy_flat:
        if str(document.get("schema_version") or "").strip():
            raise DocumentError("legacy flat evidence must be unversioned")
        endpoint_ids = set()
        if "endpoints" in document:
            if not isinstance(document["endpoints"], list):
                raise DocumentError("evidence.endpoints must be a list")
            for endpoint_index, endpoint in enumerate(document["endpoints"]):
                label = f"evidence.endpoints[{endpoint_index}]"
                if not isinstance(endpoint, dict):
                    raise DocumentError(f"{label} must be a mapping")
                if not isinstance(endpoint.get("id"), str) or not endpoint["id"].strip():
                    raise DocumentError(f"{label}.id must be a non-empty string")
                endpoint_id = endpoint["id"].strip()
                if endpoint_id in endpoint_ids:
                    raise DocumentError(f"duplicate evidence endpoint id: {endpoint_id}")
                endpoint_ids.add(endpoint_id)
                if "location" in endpoint and not isinstance(endpoint["location"], str):
                    raise DocumentError(f"{label}.location must be a string")
        for edge_index, edge in enumerate(required_list(document, "edges", "evidence")):
            label = f"evidence.edges[{edge_index}]"
            if not isinstance(edge, dict):
                raise DocumentError(f"{label} must be a mapping")
            if not isinstance(edge.get("id"), str) or not edge["id"].strip():
                raise DocumentError(f"{label}.id must be a non-empty string")
            if "location" in edge and not isinstance(edge["location"], str):
                raise DocumentError(f"{label}.location must be a string")
            if "status" in edge and (
                not isinstance(edge["status"], str) or not edge["status"].strip()
            ):
                raise DocumentError(f"{label}.status must be a non-empty string")
            if "confidence" in edge:
                finite_confidence(edge["confidence"], f"{label}.confidence")
            for key in ("from_endpoint", "to_endpoint"):
                if key in edge and not isinstance(edge[key], str):
                    raise DocumentError(f"{label}.{key} must be a string")
            for key in ("reasons", "conflicts"):
                value = edge.get(key, [])
                if not isinstance(value, list) or any(not isinstance(item, str) for item in value):
                    raise DocumentError(f"{label}.{key} must be a list of strings")
        return

    endpoint_ids = set()
    for endpoint_index, endpoint in enumerate(required_list(document, "endpoints", "evidence")):
        label = f"evidence.endpoints[{endpoint_index}]"
        if not isinstance(endpoint, dict):
            raise DocumentError(f"{label} must be a mapping")
        if not isinstance(endpoint.get("id"), str) or not endpoint["id"].strip():
            raise DocumentError(f"{label}.id must be a non-empty string")
        endpoint_id = endpoint["id"].strip()
        if endpoint_id in endpoint_ids:
            raise DocumentError(f"duplicate evidence endpoint id: {endpoint_id}")
        endpoint_ids.add(endpoint_id)
        if "location" in endpoint and not isinstance(endpoint["location"], str):
            raise DocumentError(f"{label}.location must be a string")
    for edge_index, edge in enumerate(required_list(document, "edges", "evidence")):
        label = f"evidence.edges[{edge_index}]"
        if not isinstance(edge, dict):
            raise DocumentError(f"{label} must be a mapping")
        if not isinstance(edge.get("id"), str) or not edge["id"].strip():
            raise DocumentError(f"{label}.id must be a non-empty string")
        if not isinstance(edge.get("status"), str) or not edge["status"].strip():
            raise DocumentError(f"{label}.status must be a non-empty string")
        for key in ("from_endpoint", "to_endpoint"):
            if key in edge and not isinstance(edge[key], str):
                raise DocumentError(f"{label}.{key} must be a string")
            endpoint_id = str(edge.get(key) or "").strip()
            if endpoint_id and endpoint_id not in endpoint_ids:
                raise DocumentError(f"{label}.{key} references missing endpoint: {endpoint_id}")
        finite_confidence(edge.get("confidence"), f"{label}.confidence")
        for key in ("reasons", "conflicts"):
            value = edge.get(key, [])
            if not isinstance(value, list) or any(not isinstance(item, str) for item in value):
                raise DocumentError(f"{label}.{key} must be a list of strings")


def normalized_routes(raw: dict, legacy_flat: bool) -> list[dict]:
    items = raw["routes"] if not legacy_flat else [
        {
            "protocol": raw.get("protocol"),
            "method": raw.get("method"),
            "path": raw.get("path"),
            "rpc_method": raw.get("rpc_method"),
            "endpoint_edge": endpoint_edge,
        }
        for endpoint_edge in raw["endpoint_edges"]
    ]
    routes = []
    for item in items:
        route = {
            "protocol": str(item.get("protocol") or "").lower() or None,
            "method": str(item.get("method") or "").upper() or None,
            "path": normalize_path(item.get("path")),
            "rpc_method": str(item.get("rpc_method") or "").strip() or None,
            "endpoint_edge": str(item.get("endpoint_edge") or "").strip() or None,
        }
        routes.append(route)
    return routes


def normalized_edges(document: dict, legacy_flat: bool) -> list[dict]:
    result = []
    for raw in document["edges"]:
        source = str(raw.get("from") or "").strip()
        target = str(raw.get("to") or "").strip()
        status = str(raw.get("status") or "").strip().lower()
        confidence = float(raw["confidence"])
        result.append(
            {
                "from": source,
                "to": target,
                "status": status,
                "confidence": confidence,
                "routes": normalized_routes(raw, legacy_flat),
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


def scoped_entry_edge(
    edge: dict, method: str | None, path: str | None, edge_index: dict[str, dict]
) -> dict | None:
    if not method and not path:
        return edge
    routes = [route for route in edge["routes"] if route_matches(route, method, path)]
    if not routes:
        return None
    records = [edge_index[route["endpoint_edge"]] for route in routes]
    statuses = [str(record.get("status") or edge["status"]).strip().lower() for record in records]
    if any(status not in FORMAL_STATUSES for status in statuses):
        raise DocumentError("matched route evidence must use a formal status")
    scoped = dict(edge)
    scoped["routes"] = routes
    scoped["status"] = max(statuses, key=lambda status: STATUS_PRIORITY[status])
    scoped["confidence"] = max(
        float(record.get("confidence", edge["confidence"])) for record in records
    )
    return scoped


def evidence_indexes(document: dict, legacy_flat: bool) -> tuple[dict[str, dict], dict[str, dict]]:
    endpoints = {}
    endpoint_items = document.get("endpoints", []) if legacy_flat else document["endpoints"]
    for item in endpoint_items:
        endpoints[str(item["id"])] = item
    edges = {}
    for item in document["edges"]:
        keys = [str(item["id"])]
        from_endpoint = str(item.get("from_endpoint") or "")
        to_endpoint = str(item.get("to_endpoint") or "")
        if from_endpoint or to_endpoint:
            keys.insert(0, f"{from_endpoint}>{to_endpoint}")
        for key in keys:
            if key in edges and edges[key] is not item:
                raise DocumentError(f"duplicate endpoint evidence key: {key}")
            edges[key] = item
    return endpoints, edges


def validate_route_evidence(edges: list[dict], edge_index: dict[str, dict]) -> None:
    missing = sorted(
        {
            route["endpoint_edge"]
            for edge in edges
            for route in edge["routes"]
            if route["endpoint_edge"] not in edge_index
        }
    )
    if missing:
        raise DocumentError(f"missing endpoint evidence for route: {missing[0]}")


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
                "location": str(raw.get("location") or ""),
                "from_location": str(endpoint_index.get(from_endpoint, {}).get("location") or ""),
                "to_location": str(endpoint_index.get(to_endpoint, {}).get("location") or ""),
                "reasons": list(raw.get("reasons", [])),
                "conflicts": list(raw.get("conflicts", [])),
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
    edge_index: dict[str, dict],
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
            if not traversed:
                edge = scoped_entry_edge(edge, method, path, edge_index)
                if edge is None:
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
    for item in document["edges"]:
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
        topology_document = safe_document(topology_path, "topology")
        legacy_flat = validate_topology(topology_document)
    except (OSError, UnicodeError, yaml.YAMLError, DocumentError) as exc:
        result["warnings"].append(f"cannot read topology: {exc}")
        return result

    if not evidence_path.is_file():
        result["warnings"].append(f"missing endpoint evidence file: {evidence_path}")
        return result
    try:
        evidence_document = safe_document(evidence_path, "evidence")
        validate_evidence(evidence_document, legacy_flat)
        endpoint_index, edge_index = evidence_indexes(evidence_document, legacy_flat)
        edges = normalized_edges(topology_document, legacy_flat)
        validate_route_evidence(edges, edge_index)
    except (OSError, UnicodeError, yaml.YAMLError, DocumentError) as exc:
        result["warnings"].append(f"cannot read endpoint evidence: {exc}")
        return result

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
    try:
        raw_paths, cycle_seen = find_paths(
            edges, starts, method, path, max_depth, edge_index
        )
    except DocumentError as exc:
        result["warnings"].append(f"cannot match endpoint evidence: {exc}")
        return result
    raw_paths.sort(key=path_rank)
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
