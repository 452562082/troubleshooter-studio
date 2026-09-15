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
