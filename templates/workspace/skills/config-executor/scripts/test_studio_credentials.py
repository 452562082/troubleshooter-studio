"""Shared scripts must read Studio credentials and resolve all four IDE workspaces."""
import importlib.util
from pathlib import Path

import pytest

SKILLS = Path(__file__).resolve().parents[2]


def load_script(skill, name):
    path = SKILLS / skill / "scripts" / f"{name}.py"
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.mark.parametrize("name", ["apollo_config", "consul_config", "resolve_runtime_static"])
def test_credentials_use_studio_only(tmp_path, monkeypatch, name):
    monkeypatch.setenv("HOME", str(tmp_path))
    module = load_script("config-executor", name)
    retired = tmp_path / ".openclaw" / "shop-creds.json"
    retired.parent.mkdir()
    retired.write_text('{}')
    with pytest.raises(FileNotFoundError):
        module._find_creds_file("shop")
    current = tmp_path / ".tshoot" / "shop-creds.json"
    current.parent.mkdir()
    current.write_text('{}')
    assert module._find_creds_file("shop") == str(current)


@pytest.mark.parametrize("marker", [".claude", ".cursor", ".codex", ".config/opencode", "custom-xdg/opencode"])
def test_k8s_credentials_resolve_each_platform(tmp_path, monkeypatch, marker):
    monkeypatch.setenv("HOME", str(tmp_path))
    module = load_script("k8s-runtime-query", "k8s_query")
    module.__file__ = str(tmp_path / marker / "skills/shop/k8s-runtime-query/scripts/k8s_query.py")
    expected = tmp_path / ".tshoot/shop-creds.json"
    expected.parent.mkdir()
    expected.write_text('{}')
    assert module.detect_creds_path(None) == expected
    assert all('.openclaw' not in str(p) for p in module.detect_creds_paths('shop'))


@pytest.mark.parametrize("marker", [".claude", ".cursor", ".codex", ".config/opencode", "custom-xdg/opencode"])
@pytest.mark.parametrize("skill,name", [
    ("incident-investigator", "cascade_check"),
    ("recent-changes", "timeline"),
])
def test_workspace_resolves_each_platform(tmp_path, marker, skill, name):
    module = load_script(skill, name)
    root = tmp_path / marker / "skills/shop"
    module.__file__ = str(root / skill / "scripts" / f"{name}.py")
    assert module.detect_workspace_root() == root


def test_consul_credentials_are_source_scoped(tmp_path, monkeypatch):
    import json
    monkeypatch.setenv("HOME", str(tmp_path))
    module = load_script("config-executor", "consul_config")
    path = tmp_path / ".tshoot/shop-creds.json"
    path.parent.mkdir()
    path.write_text(json.dumps({"consul": {
        "dev": {"host": "legacy"},
        "ops": {"dev": {"host": "ops", "token": "fixture"}},
        "other": {"dev": {"host": "other"}},
    }}))
    assert module.load_creds("shop", "consul", "dev", "ops")["host"] == "ops"
    assert module.load_creds("shop", "consul", "dev", "default")["host"] == "legacy"
    with pytest.raises(ValueError):
        module.load_creds("shop", "consul", "dev", "missing")
    with pytest.raises(ValueError):
        module.load_creds("shop", "consul", "prod", "ops")


@pytest.mark.parametrize("password", ["fixture-pass", "测试密码-é"])
def test_kuboard_v4_login_contract(monkeypatch, password):
    import base64
    import io
    import json
    from types import SimpleNamespace
    encoded = base64.b64encode(password.encode("utf-8")).decode("ascii")
    token = "header.payload.signature"
    config = load_script("config-executor", "kuboard_config")

    def open_request(req, timeout):
        assert json.loads(req.data) == {"username": "audit", "password": encoded, "userSource": "dao"}
        return io.BytesIO(json.dumps({"data": {"accessToken": token}}).encode())

    monkeypatch.setattr(config.urllib.request, "build_opener", lambda *args: SimpleNamespace(open=open_request))
    assert config.kuboard_login("http://fixture", "audit", password) == token
    assert config.kuboard_auth_headers(token) == {"Authorization": "Bearer " + token, "Accept": "application/json"}
    assert config.kuboard_auth_headers("key.secret")["Kb-Access-Key"] == "key.secret"
    runtime = load_script("k8s-runtime-query", "k8s_query")
    client = runtime.KuboardClient("http://fixture", username="audit", password=password)

    def post(url, json, timeout):
        assert json == {"username": "audit", "password": encoded, "userSource": "dao"}
        return SimpleNamespace(status_code=200, json=lambda: {"data": {"accessToken": token}})

    monkeypatch.setattr(client.session, "post", post)
    assert client.token() == token
    assert client.auth_headers() == {"Authorization": "Bearer " + token}
    assert runtime.KuboardClient("http://fixture", access_key="key.secret").auth_headers() == {"Kb-Access-Key": "key.secret"}


def test_kuboard_v4_login_rejected(monkeypatch):
    import urllib.error
    from types import SimpleNamespace
    config = load_script("config-executor", "kuboard_config")

    def rejected(req, timeout):
        import io
        raise urllib.error.HTTPError(req.full_url, 401, "Unauthorized", {}, io.BytesIO(b"bad credentials"))

    monkeypatch.setattr(config.urllib.request, "build_opener", lambda *args: SimpleNamespace(open=rejected))
    with pytest.raises(RuntimeError, match="login HTTP 401"):
        config.kuboard_login("http://fixture", "audit", "wrong")
    runtime = load_script("k8s-runtime-query", "k8s_query")
    client = runtime.KuboardClient("http://fixture", username="audit", password="wrong")
    monkeypatch.setattr(client.session, "post", lambda *args, **kwargs: SimpleNamespace(status_code=401))
    with pytest.raises(SystemExit):
        client.token()
    assert client._token is None
