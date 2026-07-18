#!/usr/bin/env python3
"""Resolve the one installed troubleshooter that owns the current project.

The resolver is intentionally standard-library only and fail-closed. It never
uses fuzzy names or incident keywords to select a bot.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
from urllib.parse import urlsplit


ROUTER_NAME = "tshoot-router"


def clean(value: object) -> str:
    return str(value or "").strip()


def canonical_git_url(raw: object) -> str:
    value = clean(raw).replace("\\", "/")
    if not value:
        return ""

    # URL form: https://host/org/repo.git or ssh://git@host:2222/org/repo.git
    if "://" in value:
        parsed = urlsplit(value)
        host = (parsed.hostname or "").lower()
        path = parsed.path or ""
        if parsed.scheme.lower() == "file":
            return "file/" + os.path.realpath(path).rstrip("/").lower()
        value = host + "/" + path.lstrip("/")
    else:
        value = re.sub(r"^[^/@]+@", "", value)
        # SCP form: host:org/repo.git. Do not rewrite a Windows drive prefix.
        if re.match(r"^[^/:]+:[^/].*", value) and not re.match(r"^[A-Za-z]:/", value):
            value = value.replace(":", "/", 1)

    value = re.sub(r"^[^/@]+@", "", value)
    value = re.sub(r"^([^/]+):\d+/", r"\1/", value)
    value = re.sub(r"/+", "/", value).strip("/").lower()
    if value.endswith(".git"):
        value = value[:-4]
    return value.rstrip("/")


def real_path(raw: object) -> str:
    value = clean(raw)
    if not value:
        return ""
    return os.path.realpath(os.path.expanduser(value))


def is_within(path: str, parent: str) -> bool:
    if not path or not parent:
        return False
    try:
        return os.path.commonpath([path, parent]) == parent
    except (ValueError, OSError):
        return False


def run_git(cwd: str, *args: str) -> str:
    try:
        result = subprocess.run(
            ["git", "-C", cwd, *args],
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            timeout=3,
        )
    except (OSError, subprocess.SubprocessError):
        return ""
    return result.stdout.strip() if result.returncode == 0 else ""


def git_context(cwd: str) -> tuple[str, set[str]]:
    root = run_git(cwd, "rev-parse", "--show-toplevel")
    if root:
        root = real_path(root)
    remotes: set[str] = set()
    for remote in run_git(cwd, "remote").splitlines():
        remote = remote.strip()
        if not remote:
            continue
        for url in run_git(cwd, "remote", "get-url", "--all", remote).splitlines():
            canonical = canonical_git_url(url)
            if canonical:
                remotes.add(canonical)
    return root, remotes


def load_json(path: Path) -> dict:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
        return value if isinstance(value, dict) else {}
    except (OSError, UnicodeError, json.JSONDecodeError):
        return {}


def fallback_repo_paths(home: Path) -> dict[str, dict[str, str]]:
    config = load_json(home / ".tshoot" / "config.json")
    raw = config.get("repo_paths_by_system", {})
    if not isinstance(raw, dict):
        return {}
    result: dict[str, dict[str, str]] = {}
    for system_id, paths in raw.items():
        if not isinstance(paths, dict):
            continue
        result[clean(system_id)] = {clean(k): clean(v) for k, v in paths.items() if clean(k) and clean(v)}
    return result


def normalize_agents(meta: dict, anchor_name: str) -> dict[str, str]:
    agents: dict[str, str] = {}
    for item in meta.get("internal_agents", []):
        if not isinstance(item, dict):
            continue
        agent_id, role = clean(item.get("id")), clean(item.get("role")).lower()
        if agent_id and role:
            agents[role] = agent_id
    agent_id = clean(meta.get("agent_id")) or anchor_name
    role = clean(meta.get("role")).lower() or "troubleshooter"
    if agent_id:
        agents.setdefault(role, agent_id)
        agents.setdefault("troubleshooter", agent_id if role == "troubleshooter" else anchor_name)
    return agents


def load_systems(root: Path) -> list[dict]:
    fallbacks = fallback_repo_paths(Path.home())
    grouped: dict[str, dict] = {}
    skills = root / "skills"
    for meta_path in sorted(skills.glob("*/tshoot.json")):
        if meta_path.parent.name == ROUTER_NAME:
            continue
        meta = load_json(meta_path)
        system_id = clean(meta.get("system_id"))
        anchor_name = meta_path.parent.name
        if not system_id:
            continue
        key = system_id.casefold()
        system = grouped.setdefault(
            key,
            {
                "system_id": system_id,
                "system_name": clean(meta.get("system_name")),
                "agent_id": clean(meta.get("agent_id")) or anchor_name,
                "agents": {},
                "repos": [],
            },
        )
        system["agents"].update(normalize_agents(meta, anchor_name))

        raw_repos = meta.get("project_repositories", [])
        if not isinstance(raw_repos, list):
            raw_repos = []
        seen_names: set[str] = set()
        for raw in raw_repos:
            if not isinstance(raw, dict):
                continue
            name = clean(raw.get("name"))
            if not name:
                continue
            seen_names.add(name)
            local = clean(raw.get("local_path")) or fallbacks.get(system_id, {}).get(name, "")
            system["repos"].append(
                {
                    "name": name,
                    "url": clean(raw.get("url")),
                    "local_path": local,
                    "sub_path": clean(raw.get("sub_path")).strip("/\\"),
                }
            )
        # Backward compatibility for installed bots whose metadata predates v2.
        for name, local in fallbacks.get(system_id, {}).items():
            if name not in seen_names:
                system["repos"].append({"name": name, "url": "", "local_path": local, "sub_path": ""})
    return list(grouped.values())


def explicit_match(system: dict, requested: str) -> bool:
    needle = requested.casefold()
    values = [system.get("system_id", ""), system.get("system_name", ""), system.get("agent_id", "")]
    values.extend(system.get("agents", {}).values())
    return any(clean(value).casefold() == needle for value in values if clean(value))


def score_system(system: dict, cwd: str, git_root: str, remotes: set[str], requested: str) -> tuple[int, dict]:
    if requested:
        if explicit_match(system, requested):
            return 100000, {"kind": "explicit_system", "value": requested}
        return 0, {}

    # Use the actual cwd for local-path ownership. In a monorepo git_root points
    # above a service sub_path and would otherwise erase the more precise match.
    current = cwd
    best_score, best_match = 0, {}
    for repo in system.get("repos", []):
        local_root = real_path(repo.get("local_path"))
        sub_path = clean(repo.get("sub_path"))
        owned_path = real_path(os.path.join(local_root, sub_path)) if local_root and sub_path else local_root
        if owned_path and is_within(current, owned_path):
            score = 10000 + min(len(owned_path), 5000)
            if score > best_score:
                best_score = score
                best_match = {"kind": "local_path", "repo": repo.get("name", "")}

        canonical = canonical_git_url(repo.get("url"))
        if canonical and canonical in remotes and 5000 > best_score:
            best_score = 5000
            best_match = {"kind": "git_remote", "repo": repo.get("name", ""), "remote": canonical}
    return best_score, best_match


def public_candidate(system: dict, score: int, match: dict) -> dict:
    return {
        "system_id": system.get("system_id", ""),
        "system_name": system.get("system_name", ""),
        "score": score,
        "match": match,
    }


def resolve(root: Path, cwd: str, requested: str, expected_agent: str) -> dict:
    cwd = real_path(cwd) or real_path(os.getcwd())
    systems = load_systems(root)
    git_root, remotes = git_context(cwd)
    scored: list[tuple[int, dict, dict]] = []
    for system in systems:
        score, match = score_system(system, cwd, git_root, remotes, requested)
        if score > 0:
            scored.append((score, system, match))
    scored.sort(key=lambda item: (-item[0], clean(item[1].get("system_id")).casefold()))

    base = {
        "status": "unmatched",
        "allowed": False,
        "cwd": cwd,
        "git_root": git_root,
        "reason": "no_project_binding",
        "candidates": [],
    }
    if not systems:
        base["reason"] = "no_installed_bots"
        return base
    if not scored:
        if requested:
            base["reason"] = "unknown_system"
        return base

    top_score = scored[0][0]
    winners = [item for item in scored if item[0] == top_score]
    if len(winners) != 1:
        base["status"] = "ambiguous"
        base["reason"] = "multiple_equal_project_bindings"
        base["candidates"] = [public_candidate(system, score, match) for score, system, match in winners]
        return base

    score, system, match = winners[0]
    agents = system.get("agents", {})
    known_agent_ids = {clean(value) for value in agents.values() if clean(value)}
    allowed = not expected_agent or expected_agent in known_agent_ids
    return {
        "status": "matched",
        "allowed": allowed,
        "reason": "matched" if allowed else "expected_agent_belongs_to_another_system",
        "cwd": cwd,
        "git_root": git_root,
        "system_id": system.get("system_id", ""),
        "system_name": system.get("system_name", ""),
        "agent_id": system.get("agent_id", ""),
        "agents": agents,
        "match": match,
        "candidates": [public_candidate(system, score, match)],
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Resolve the installed troubleshooter for a project")
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[3]))
    parser.add_argument("--cwd", default=os.getcwd())
    parser.add_argument("--system", default="")
    parser.add_argument("--expect-agent", default="")
    args = parser.parse_args()
    try:
        result = resolve(Path(args.root).expanduser().resolve(), args.cwd, clean(args.system), clean(args.expect_agent))
    except Exception as exc:  # Fail closed, while keeping a machine-readable contract.
        result = {"status": "error", "allowed": False, "reason": "router_error", "error": str(exc)}
    json.dump(result, sys.stdout, ensure_ascii=False, sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
