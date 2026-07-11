import json
import subprocess
import sys
from pathlib import Path

import pytest
import yaml


SCRIPT = Path(__file__).with_name("query.py")


def edge(source, target, method, path, confidence, status):
    endpoint_edge = f"{source}:{method}:{path}>{target}:{method}:{path}"
    return {
        "from": source,
        "to": target,
        "confidence": confidence,
        "status": status,
        "routes": [
            {
                "protocol": "http",
                "method": method,
                "path": path,
                "endpoint_edge": endpoint_edge,
            }
        ],
    }


def grpc_edge(source, target, rpc_method, confidence=0.98, status="automatic"):
    endpoint_edge = f"{source}:grpc:outbound:{rpc_method}>{target}:grpc:inbound:{rpc_method}"
    return {
        "from": source,
        "to": target,
        "confidence": confidence,
        "status": status,
        "routes": [
            {
                "protocol": "grpc",
                "rpc_method": rpc_method,
                "endpoint_edge": endpoint_edge,
            }
        ],
    }


def write_fixture(root, edges):
    refs = root / "skills" / "routing" / "references"
    refs.mkdir(parents=True)
    services = {
        service: {"repo": f"{service}-repo"}
        for item in edges
        for service in (item["from"], item["to"])
    }
    (refs / "service-topology.yaml").write_text(
        yaml.safe_dump(
            {"schema_version": "1", "services": services, "edges": edges}
        ),
        encoding="utf-8",
    )
    endpoint_ids = {
        endpoint_id
        for item in edges
        for route in item["routes"]
        for endpoint_id in route["endpoint_edge"].split(">", 1)
    }
    evidence = {
        "schema_version": "1",
        "endpoints": [
            {"id": endpoint_id, "location": f"{endpoint_id}.fixture:1"}
            for endpoint_id in sorted(endpoint_ids)
        ],
        "edges": [
            {
                "id": route["endpoint_edge"],
                "from_endpoint": route["endpoint_edge"].split(">", 1)[0],
                "to_endpoint": route["endpoint_edge"].split(">", 1)[1],
                "status": item["status"],
                "confidence": item["confidence"],
                "reasons": ["fixture"],
                "conflicts": [],
            }
            for item in edges
            for route in item["routes"]
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


def assert_unavailable(result, service="web"):
    assert result["status"] == "unavailable"
    assert result["paths"] == []
    assert result["fallback"] == "routing_rg_read"
    assert result["query"]["service"] == service
    assert result["warnings"]


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
        "protocol": "http",
        "method": "GET",
        "path": "/api/orders/123",
        "rpc_method": None,
        "max_depth": 3,
    }
    assert result["paths"][0]["services"] == [
        "mall-web",
        "mall-bff",
        "mall-order",
    ]
    assert result["paths"][0]["edges"][0]["evidence"]
    assert result["fallback"] is None


def test_query_cli_selects_protocol_and_fully_qualified_grpc_method(tmp_path):
    write_fixture(
        tmp_path,
        edges=[
            edge("web", "http-profile", "GET", "/profile", 0.98, "automatic"),
            grpc_edge("web", "grpc-profile", "profile.v1.ProfileService/GetProfile"),
            grpc_edge("web", "wrong-grpc", "profile.v1.ProfileService/ListProfiles"),
        ],
    )

    result = run_query(
        tmp_path,
        "--service",
        "web",
        "--protocol",
        "grpc",
        "--rpc-method",
        "/profile.v1.ProfileService/GetProfile",
        "--json",
    )

    assert result["status"] == "ok"
    assert [path["services"] for path in result["paths"]] == [["web", "grpc-profile"]]
    assert result["paths"][0]["edges"][0]["routes"][0]["rpc_method"] == (
        "profile.v1.ProfileService/GetProfile"
    )


def test_query_accepts_exact_unversioned_flat_brief_fixture(tmp_path):
    refs = tmp_path / "skills" / "routing" / "references"
    refs.mkdir(parents=True)
    edges = [
        {
            "from": "mall-web",
            "to": "mall-bff",
            "protocol": "http",
            "method": "GET",
            "path": "/api/orders/{param}",
            "confidence": 0.98,
            "status": "confirmed",
            "endpoint_edges": [
                "mall-web:GET:/api/orders/{param}>mall-bff"
            ],
        },
        {
            "from": "mall-bff",
            "to": "mall-order",
            "protocol": "http",
            "method": "POST",
            "path": "/internal/orders",
            "confidence": 0.90,
            "status": "automatic",
            "endpoint_edges": ["mall-bff:POST:/internal/orders>mall-order"],
        },
    ]
    (refs / "service-topology.yaml").write_text(
        yaml.safe_dump({"services": {}, "edges": edges}), encoding="utf-8"
    )
    (refs / "endpoint-evidence.yaml").write_text(
        yaml.safe_dump(
            {
                "edges": [
                    {
                        "id": item["endpoint_edges"][0],
                        "location": "fixture:1",
                        "reasons": ["fixture"],
                    }
                    for item in edges
                ]
            }
        ),
        encoding="utf-8",
    )

    result = run_query(
        tmp_path,
        "--service",
        "mall-web",
        "--method",
        "GET",
        "--path",
        "/api/orders/123",
        "--json",
    )

    assert result["status"] == "ok"
    assert result["paths"][0]["services"] == [
        "mall-web",
        "mall-bff",
        "mall-order",
    ]
    first_edge = result["paths"][0]["edges"][0]
    assert first_edge["status"] == "confirmed"
    assert first_edge["confidence"] == 0.98
    assert first_edge["routes"] == [
        {
            "protocol": "http",
            "method": "GET",
            "path": "/api/orders/{param}",
            "rpc_method": None,
            "endpoint_edge": "mall-web:GET:/api/orders/{param}>mall-bff",
        }
    ]
    assert first_edge["evidence"] == [
        {
            "id": "mall-web:GET:/api/orders/{param}>mall-bff",
            "status": "confirmed",
            "location": "fixture:1",
            "from_location": "",
            "to_location": "",
            "reasons": ["fixture"],
            "conflicts": [],
        }
    ]


@pytest.mark.parametrize(
    "evidence",
    [
        {
            "endpoints": [1],
            "edges": [
                {
                    "id": "mall-web:GET:/api/orders/{param}>mall-bff",
                    "location": "fixture:1",
                    "reasons": ["fixture"],
                }
            ],
        },
        {
            "endpoints": [{"location": "fixture:1"}],
            "edges": [
                {
                    "id": "mall-web:GET:/api/orders/{param}>mall-bff",
                    "location": "fixture:1",
                    "reasons": ["fixture"],
                }
            ],
        },
        {
            "edges": [
                {
                    "id": "mall-web:GET:/api/orders/{param}>mall-bff",
                    "location": "fixture:1",
                    "reasons": ["fixture"],
                    "confidence": "bad",
                }
            ],
        },
    ],
)
def test_query_malformed_optional_legacy_evidence_is_stable_unavailable(
    tmp_path, evidence
):
    refs = tmp_path / "skills" / "routing" / "references"
    refs.mkdir(parents=True)
    topology = {
        "services": {},
        "edges": [
            {
                "from": "mall-web",
                "to": "mall-bff",
                "protocol": "http",
                "method": "GET",
                "path": "/api/orders/{param}",
                "confidence": 0.98,
                "status": "confirmed",
                "endpoint_edges": [
                    "mall-web:GET:/api/orders/{param}>mall-bff"
                ],
            }
        ],
    }
    (refs / "service-topology.yaml").write_text(
        yaml.safe_dump(topology), encoding="utf-8"
    )
    (refs / "endpoint-evidence.yaml").write_text(
        yaml.safe_dump(evidence), encoding="utf-8"
    )

    proc = subprocess.run(
        [
            sys.executable,
            str(SCRIPT),
            "--workspace",
            str(tmp_path),
            "--service",
            "mall-web",
            "--method",
            "GET",
            "--path",
            "/api/orders/123",
            "--json",
        ],
        check=False,
        text=True,
        capture_output=True,
    )

    assert proc.returncode == 0
    assert "Traceback" not in proc.stderr
    result = json.loads(proc.stdout)
    assert_unavailable(result, service="mall-web")


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
                "confidence": 0.98,
                "reasons": ["method_path_exact"],
                "conflicts": [],
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


def test_query_scopes_first_edge_status_confidence_routes_and_evidence_to_match(tmp_path):
    refs = tmp_path / "skills" / "routing" / "references"
    refs.mkdir(parents=True)
    get_ref = "web:get>bff:get"
    post_ref = "web:post>bff:post"
    topology = {
        "services": {"web": {"repo": "web"}, "bff": {"repo": "bff"}},
        "edges": [
            {
                "from": "web",
                "to": "bff",
                "status": "manual",
                "confidence": 1.0,
                "routes": [
                    {
                        "protocol": "http",
                        "method": "GET",
                        "path": "/orders/{param}",
                        "endpoint_edge": get_ref,
                    },
                    {
                        "protocol": "http",
                        "method": "POST",
                        "path": "/orders/{param}",
                        "endpoint_edge": post_ref,
                    },
                ],
            }
        ],
    }
    evidence = {
        "endpoints": [
            {"id": "web:get", "location": "src/get.ts:1"},
            {"id": "bff:get", "location": "routes/get.go:2"},
            {"id": "web:post", "location": "src/post.ts:3"},
            {"id": "bff:post", "location": "routes/post.go:4"},
        ],
        "edges": [
            {
                "id": "legacy-get-id",
                "from_endpoint": "web:get",
                "to_endpoint": "bff:get",
                "status": "automatic",
                "confidence": 0.91,
                "reasons": ["method_path_exact"],
                "conflicts": [],
            },
            {
                "id": "legacy-post-id",
                "from_endpoint": "web:post",
                "to_endpoint": "bff:post",
                "status": "manual",
                "confidence": 1.0,
                "reasons": ["manual_override"],
                "conflicts": [],
            },
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
        "/orders/42",
        "--json",
    )

    first_edge = result["paths"][0]["edges"][0]
    assert first_edge["status"] == "automatic"
    assert first_edge["confidence"] == 0.91
    assert [route["method"] for route in first_edge["routes"]] == ["GET"]
    assert [item["id"] for item in first_edge["evidence"]] == [get_ref]
    assert first_edge["evidence"][0]["reasons"] == ["method_path_exact"]
    assert result["paths"][0]["score"] == 0.91


@pytest.mark.parametrize(
    ("formal", "concrete"),
    [
        ("/files/*path", "/files/a/b/c"),
        ("/files/:name*", "/files/a/b/c"),
        ("/files/{*name}", "/files/a/b/c"),
        ("/files/[...name]", "/files/a/b/c"),
        ("/files/{wildcard}", "/files/a/b/c"),
    ],
)
def test_query_wildcard_forms_match_one_or_more_concrete_url_segments(
    tmp_path, formal, concrete
):
    write_fixture(
        tmp_path,
        edges=[edge("web", "files", "GET", formal, 0.9, "automatic")],
    )

    result = run_query(
        tmp_path,
        "--service",
        "web",
        "--method",
        "GET",
        "--path",
        f"https://api.example.test{concrete}?download=1",
        "--json",
    )

    assert result["status"] == "ok"
    assert result["paths"][0]["edges"][0]["routes"][0]["path"] == "/files/{wildcard}"


@pytest.mark.parametrize("formal", ["/files/*path", "/files/{wildcard}"])
def test_query_wildcard_requires_at_least_one_segment(tmp_path, formal):
    write_fixture(
        tmp_path,
        edges=[edge("web", "files", "GET", formal, 0.9, "automatic")],
    )

    result = run_query(
        tmp_path, "--service", "web", "--method", "GET", "--path", "/files"
    )

    assert result["status"] == "no_match"


@pytest.mark.parametrize(
    "formal",
    ["/orders/:id", "/orders/{orderId}", "/orders/[id]"],
)
def test_query_param_forms_match_exactly_one_concrete_segment(tmp_path, formal):
    write_fixture(
        tmp_path,
        edges=[edge("web", "orders", "GET", formal, 0.9, "automatic")],
    )

    one = run_query(
        tmp_path, "--service", "web", "--method", "GET", "--path", "/orders/42"
    )
    two = run_query(
        tmp_path,
        "--service",
        "web",
        "--method",
        "GET",
        "--path",
        "/orders/42/items",
    )

    assert one["status"] == "ok"
    assert one["paths"][0]["edges"][0]["routes"][0]["path"] == "/orders/{param}"
    assert two["status"] == "no_match"


def test_query_missing_evidence_is_unavailable(tmp_path):
    write_fixture(
        tmp_path,
        edges=[edge("web", "orders", "GET", "/orders", 0.9, "automatic")],
    )
    evidence_path = (
        tmp_path
        / "skills"
        / "routing"
        / "references"
        / "endpoint-evidence.yaml"
    )
    evidence_path.unlink()

    assert_unavailable(run_query(tmp_path, "--service", "web", "--json"))


def test_query_duplicate_generated_endpoint_ids_are_unavailable(tmp_path):
    write_fixture(
        tmp_path,
        edges=[edge("web", "orders", "GET", "/orders", 0.9, "automatic")],
    )
    evidence_path = (
        tmp_path / "skills" / "routing" / "references" / "endpoint-evidence.yaml"
    )
    evidence = yaml.safe_load(evidence_path.read_text(encoding="utf-8"))
    evidence["endpoints"].append(dict(evidence["endpoints"][0]))
    evidence_path.write_text(yaml.safe_dump(evidence), encoding="utf-8")

    assert_unavailable(run_query(tmp_path, "--service", "web", "--json"))


@pytest.mark.parametrize("endpoint_field", ["from_endpoint", "to_endpoint"])
def test_query_dangling_generated_endpoint_references_are_unavailable(
    tmp_path, endpoint_field
):
    write_fixture(
        tmp_path,
        edges=[edge("web", "orders", "GET", "/orders", 0.9, "automatic")],
    )
    evidence_path = (
        tmp_path / "skills" / "routing" / "references" / "endpoint-evidence.yaml"
    )
    evidence = yaml.safe_load(evidence_path.read_text(encoding="utf-8"))
    evidence["edges"][0][endpoint_field] = "missing:endpoint"
    evidence_path.write_text(yaml.safe_dump(evidence), encoding="utf-8")

    assert_unavailable(run_query(tmp_path, "--service", "web", "--json"))


@pytest.mark.parametrize(
    ("target", "mutate"),
    [
        ("topology", lambda document: "scalar-root"),
        ("topology", lambda document: [document]),
        ("topology", lambda document: {**document, "services": ["web"]}),
        ("topology", lambda document: {**document, "edges": {"bad": "shape"}}),
        (
            "topology",
            lambda document: {
                **document,
                "edges": [{**document["edges"][0], "routes": "not-a-list"}],
            },
        ),
        ("evidence", lambda document: "scalar-root"),
        ("evidence", lambda document: [document]),
        ("evidence", lambda document: {**document, "endpoints": {"bad": "shape"}}),
        ("evidence", lambda document: {**document, "edges": {"bad": "shape"}}),
        (
            "evidence",
            lambda document: {
                **document,
                "edges": [{**document["edges"][0], "reasons": "not-a-list"}],
            },
        ),
        (
            "evidence",
            lambda document: {
                **document,
                "edges": [
                    {
                        key: value
                        for key, value in document["edges"][0].items()
                        if key != "status"
                    }
                ],
            },
        ),
    ],
)
def test_query_malformed_required_documents_return_stable_unavailable(
    tmp_path, target, mutate
):
    write_fixture(
        tmp_path,
        edges=[edge("web", "orders", "GET", "/orders", 0.9, "automatic")],
    )
    refs = tmp_path / "skills" / "routing" / "references"
    path = refs / (
        "service-topology.yaml" if target == "topology" else "endpoint-evidence.yaml"
    )
    document = yaml.safe_load(path.read_text(encoding="utf-8"))
    path.write_text(yaml.safe_dump(mutate(document)), encoding="utf-8")

    result = run_query(tmp_path, "--service", "web", "--json")

    assert_unavailable(result)
    assert all(item is not None for item in result["warnings"])


def test_query_marks_missing_topology_as_fallback(tmp_path):
    result = run_query(tmp_path, "--service", "mall-web", "--json")

    assert_unavailable(result, service="mall-web")
