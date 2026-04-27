package openclaw

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetect_DirMissing(t *testing.T) {
	tmp := t.TempDir()
	_, err := Detect(filepath.Join(tmp, "does-not-exist"))
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("expected ErrNotInstalled, got %v", err)
	}
}

func TestDetect_DirExistsButNoConfigJSON(t *testing.T) {
	// 目录在,但没 openclaw.json → 不认为是 openclaw 安装
	tmp := t.TempDir()
	_, err := Detect(tmp)
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("expected ErrNotInstalled when openclaw.json missing, got %v", err)
	}
}

func TestDetect_RealSchema(t *testing.T) {
	// 跟用户本机 /Users/xiaolong/.openclaw/openclaw.json 一致的最小片段
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "openclaw.json"), `{
  "meta": {"lastTouchedVersion": "2026.4.9"},
  "agents": {
    "defaults": {
      "model":  {"primary": "openai-codex/gpt-5.4"},
      "models": {"openai-codex/gpt-5.4": {}}
    },
    "list": [
      {"id": "ai-troubleshooter", "model": "openai-codex/gpt-5.3-codex"}
    ]
  },
  "auth": {
    "profiles": {
      "openai-codex:default":            {"provider": "openai-codex", "mode": "oauth"},
      "openai-codex:me@example.com":     {"provider": "openai-codex", "mode": "oauth"}
    }
  }
}`)
	r, err := Detect(tmp)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if r.Version != "2026.4.9" {
		t.Errorf("Version: got %q", r.Version)
	}
	if r.InstalledButEmpty {
		t.Error("InstalledButEmpty should be false")
	}
	// 期望 2 个模型(primary + 历史;defaults.models 里那条跟 primary 同 id 被去重)
	if len(r.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(r.Models), r.Models)
	}
	// 首位是 primary
	if !r.Models[0].Primary || r.Models[0].ID != "openai-codex/gpt-5.4" {
		t.Errorf("Models[0] primary check: %+v", r.Models[0])
	}
	if r.Models[0].Label != "openai-codex/gpt-5.4 (默认)" {
		t.Errorf("primary label should tag (默认): %q", r.Models[0].Label)
	}
	// provider 从 id 前缀抠出
	if r.Models[0].Provider != "openai-codex" {
		t.Errorf("Provider: got %q", r.Models[0].Provider)
	}
	// 历史那条(非 primary)
	if r.Models[1].ID != "openai-codex/gpt-5.3-codex" || r.Models[1].Primary {
		t.Errorf("Models[1]: %+v", r.Models[1])
	}
	// auth.profiles 去重出 1 个 provider
	if len(r.AuthProviders) != 1 || r.AuthProviders[0] != "openai-codex" {
		t.Errorf("AuthProviders: %v", r.AuthProviders)
	}
}

func TestDetect_InstalledButNoModels(t *testing.T) {
	// openclaw.json 存在解析通过,但 agents 块空 —— 装了但没 agent 的全新状态
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "openclaw.json"), `{
  "meta": {"lastTouchedVersion": "2026.4.9"},
  "gateway": {"mode": "local"}
}`)
	r, err := Detect(tmp)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !r.InstalledButEmpty {
		t.Error("InstalledButEmpty should be true")
	}
	if len(r.Models) != 0 {
		t.Errorf("expected 0 models, got %+v", r.Models)
	}
}

func TestDetect_CorruptJSON(t *testing.T) {
	// openclaw.json 存在但解析失败 → 返回 error(不是 ErrNotInstalled,让用户知道不是 "没装")
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "openclaw.json"), `{ this is not json `)
	_, err := Detect(tmp)
	if err == nil {
		t.Fatal("expected error for corrupt JSON")
	}
	if errors.Is(err, ErrNotInstalled) {
		t.Error("corrupt JSON should NOT be reported as ErrNotInstalled")
	}
}

func TestDetect_ModelDedupAcrossSources(t *testing.T) {
	// primary / defaults.models / agents.list 都出现 同一个 id → 只保留一条(primary 优先)
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "openclaw.json"), `{
  "agents": {
    "defaults": {
      "model":  {"primary": "x/y"},
      "models": {"x/y": {}, "x/z": {}}
    },
    "list": [
      {"model": "x/y"},
      {"model": "x/z"},
      {"model": "x/w"}
    ]
  }
}`)
	r, err := Detect(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Models) != 3 {
		t.Fatalf("expected 3 deduped models, got %d: %+v", len(r.Models), r.Models)
	}
	// primary 应是第一条 且带 Primary=true
	if !r.Models[0].Primary || r.Models[0].ID != "x/y" {
		t.Errorf("first should be primary x/y, got %+v", r.Models[0])
	}
}
