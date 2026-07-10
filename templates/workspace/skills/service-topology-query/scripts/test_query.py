import json
import subprocess
import sys
from pathlib import Path

import yaml


SCRIPT = Path(__file__).with_name("query.py")


def edge(source, target, method, path, confidence, status):
    return {
        "from": source,
        "to": target,
        "protocol": "http",
        "method": method,
        "path": path,
        "confidence": confidence,
        "status": status,
        "endpoint_edges": [f"{source}:{method}:{path}>{target}"],
    }


def write_fixture(root, edges):
    refs = root / "skills" / "routing" / "references"
    refs.mkdir(parents=True)
    (refs / "service-topology.yaml").write_text(
        yaml.safe_dump({"services": {}, "edges": edges}), encoding="utf-8"
    )
    evidence = {
        "edges": [
            {
                "id": item["endpoint_edges"][0],
                "location": "fixture:1",
                "reasons": ["fixture"],
            }
            for item in edges
        ]
    }
    (refs / "endpoint-evidence.yaml").write_text(
        yaml.safe_dump(evidence), encoding="utf-8"
    )


def run_query(root, *args):
    proc = subprocess.run(
        [sys.executable, str(SCRIPT), "--workspace", str(root), *args],
        check=True,
        text=True,
        capture_output=True,
    )
    return json.loads(proc.stdout)


def test_query_by_failed_request_returns_ranked_three_hop_path(tmp_path):
    write_fixture(
        tmp_path,
        edges=[
            edge(
                "mall-web",
                "mall-bff",
                "GET",
                "/api/orders/{param}",
                0.98,
                "confirmed",
            ),
            edge(
                "mall-bff",
                "mall-order",
                "POST",
                "/internal/orders",
                0.90,
                "automatic",
            ),
        ],
    )

    result = run_query(
        tmp_path,
        "--service",
        "mall-web",
        "--method",
        "GET",
        "--path",
        "/api/orders/123?expand=items",
        "--json",
    )

    assert result["status"] == "ok"
    assert result["query"] == {
        "service": "mall-web",
        "method": "GET",
        "path": "/api/orders/123",
        "max_depth": 3,
    }
    assert result["paths"][0]["services"] == [
        "mall-web",
        "mall-bff",
        "mall-order",
    ]
    assert result["paths"][0]["edges"][0]["evidence"]
    assert result["fallback"] is None


def test_query_by_url_without_service_finds_matching_start(tmp_path):
    write_fixture(
        tmp_path,
        edges=[
            edge("mall-web", "mall-bff", "GET", "/api/orders/:id", 0.90, "automatic"),
            edge("admin-web", "admin-bff", "POST", "/api/orders/{id}", 0.99, "confirmed"),
        ],
    )

    result = run_query(
        tmp_path,
        "--method",
        "get",
        "--path",
        "https://api.mall.example.com/api/orders/42?source=web#ignored",
        "--json",
    )

    assert result["query"]["service"] is None
    assert result["query"]["path"] == "/api/orders/42"
    assert [path["services"] for path in result["paths"]] == [
        ["mall-web", "mall-bff"]
    ]


def test_query_ranks_human_evidence_then_confidence_then_length(tmp_path):
    write_fixture(
        tmp_path,
        edges=[
            edge("web", "auto", "GET", "/api/items", 0.99, "automatic"),
            edge("web", "human-low", "GET", "/api/items", 0.70, "manual"),
            edge("web", "human-high", "GET", "/api/items", 0.80, "confirmed"),
            edge("human-high", "leaf", "GET", "/internal/items", 0.80, "automatic"),
        ],
    )

    result = run_query(
        tmp_path,
        "--service",
        "web",
        "--method",
        "GET",
        "--path",
        "/api/items",
        "--json",
    )

    assert [path["services"] for path in result["paths"]] == [
        ["web", "human-high", "leaf"],
        ["web", "human-low"],
        ["web", "auto"],
    ]


def test_query_detects_cycles_and_returns_only_acyclic_services(tmp_path):
    write_fixture(
        tmp_path,
        edges=[
            edge("a", "b", "GET", "/api/start", 0.98, "automatic"),
            edge("b", "c", "GET", "/internal/c", 0.90, "automatic"),
            edge("c", "a", "GET", "/internal/a", 0.90, "automatic"),
        ],
    )

    result = run_query(tmp_path, "--service", "a", "--json")

    assert result["paths"][0]["services"] == ["a", "b", "c"]
    assert len(result["paths"][0]["services"]) == len(
        set(result["paths"][0]["services"])
    )
    assert any("cycle" in warning for warning in result["warnings"])


def test_query_defaults_to_three_hops(tmp_path):
    write_fixture(
        tmp_path,
        edges=[
            edge("a", "b", "GET", "/api/start", 0.98, "automatic"),
            edge("b", "c", "GET", "/two", 0.98, "automatic"),
            edge("c", "d", "GET", "/three", 0.98, "automatic"),
            edge("d", "e", "GET", "/four", 0.98, "automatic"),
        ],
    )

    result = run_query(tmp_path, "--service", "a", "--json")

    assert result["query"]["max_depth"] == 3
    assert result["paths"][0]["services"] == ["a", "b", "c", "d"]


def test_query_honors_depth_one_and_clamps_depth_to_supported_bounds(tmp_path):
    write_fixture(
        tmp_path,
        edges=[
            edge("a", "b", "GET", "/api/start", 0.98, "automatic"),
            edge("b", "c", "GET", "/two", 0.98, "automatic"),
        ],
    )

    depth_one = run_query(
        tmp_path, "--service", "a", "--max-depth", "1", "--json"
    )
    depth_zero = run_query(
        tmp_path, "--service", "a", "--max-depth", "0", "--json"
    )
    depth_too_large = run_query(
        tmp_path, "--service", "a", "--max-depth", "99", "--json"
    )

    assert depth_one["paths"][0]["services"] == ["a", "b"]
    assert depth_zero["query"]["max_depth"] == 1
    assert depth_too_large["query"]["max_depth"] == 5


def test_query_reads_generated_route_and_endpoint_evidence_shape(tmp_path):
    refs = tmp_path / "skills" / "routing" / "references"
    refs.mkdir(parents=True)
    topology = {
        "services": {"web": {"repo": "web-repo"}, "bff": {"repo": "bff-repo"}},
        "edges": [
            {
                "from": "web",
                "to": "bff",
                "status": "automatic",
                "confidence": 0.98,
                "routes": [
                    {
                        "protocol": "http",
                        "method": "GET",
                        "path": "/api/orders/[id]",
                        "endpoint_edge": "web:out>bff:in",
                    }
                ],
            }
        ],
    }
    evidence = {
        "endpoints": [
            {"id": "web:out", "location": "src/orders.ts:7"},
            {"id": "bff:in", "location": "routes/orders.go:12"},
        ],
        "edges": [
            {
                "id": "web:out>bff:in",
                "from_endpoint": "web:out",
                "to_endpoint": "bff:in",
                "status": "automatic",
                "reasons": ["method_path_exact"],
            }
        ],
    }
    (refs / "service-topology.yaml").write_text(
        yaml.safe_dump(topology), encoding="utf-8"
    )
    (refs / "endpoint-evidence.yaml").write_text(
        yaml.safe_dump(evidence), encoding="utf-8"
    )

    result = run_query(
        tmp_path,
        "--service",
        "web",
        "--method",
        "GET",
        "--path",
        "/api/orders/7",
        "--json",
    )

    first_edge = result["paths"][0]["edges"][0]
    assert first_edge["routes"][0]["path"] == "/api/orders/{param}"
    assert first_edge["evidence"][0]["from_location"] == "src/orders.ts:7"
    assert first_edge["evidence"][0]["to_location"] == "routes/orders.go:12"


def test_query_marks_missing_topology_as_fallback(tmp_path):
    result = run_query(tmp_path, "--service", "mall-web", "--json")

    assert result["status"] == "unavailable"
    assert result["fallback"] == "routing_rg_read"
    assert result["paths"] == []
    assert result["query"]["service"] == "mall-web"
    assert result["warnings"]
