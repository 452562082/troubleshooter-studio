package userconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSave_PermAndAtomic 防回归:
//  1. ~/.tshoot/config.json 必须 0o600(含本机路径 + 部署元数据,不能 world-readable)
//  2. 必须 tmp+rename 原子写(写到一半 crash 不能截断已存在的文件)
func TestSave_PermAndAtomic(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cfg := &Config{
		DefaultReposRoot: "~/code",
		RepoPathsBySystem: map[string]map[string]string{
			"shop": {"order-service": "/Users/me/code/shop/order-service"},
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cfgPath := filepath.Join(tmpHome, ".tshoot", "config.json")
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat config.json: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("config.json mode want 0600, got %#o(含本机路径 + 部署元数据,不能 world-readable)", mode)
	}

	// tmp 文件应已被 rename 走,不应残留(残留说明 rename 失败 + 没 cleanup)
	if _, err := os.Stat(cfgPath + ".tmp"); err == nil {
		t.Errorf("config.json.tmp 残留,Save 没清理:%s", cfgPath+".tmp")
	}
}

// TestSave_RoundTrip 防回归:Save → Load 数据完整,JSON 可反序列化。
func TestSave_RoundTrip(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	original := &Config{
		DefaultReposRoot: "/work/repos",
		RepoPathsBySystem: map[string]map[string]string{
			"shop": {"order-service": "/work/shop/order"},
			"b2b":  {"api": "/work/b2b/api"},
		},
		DeployedBots: map[string]DeployedBotEntry{
			DeployedBotKey("shop", "claude-code"): {
				SystemID: "shop", SystemName: "Shop System",
				Target: "claude-code", Path: "/Users/me/.claude/skills/shop-bot",
				LastDeployedAt: 1700000000,
			},
		},
	}
	if err := Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.DefaultReposRoot != original.DefaultReposRoot {
		t.Errorf("DefaultReposRoot lost: want %q got %q", original.DefaultReposRoot, loaded.DefaultReposRoot)
	}
	if loaded.RepoPathsBySystem["shop"]["order-service"] != original.RepoPathsBySystem["shop"]["order-service"] {
		t.Errorf("RepoPathsBySystem lost")
	}
	if got := loaded.DeployedBots[DeployedBotKey("shop", "claude-code")]; got.SystemID != "shop" || got.LastDeployedAt != 1700000000 {
		t.Errorf("DeployedBots lost or corrupted: %+v", got)
	}
}

// TestSave_PreservesExistingFileOnFailure 验证 atomic write:tmp 写失败时原 config 完整保留。
// 模拟方法:先 Save 一份合法 config,然后预创建 .tmp 为 read-only directory(让后续 Save 写
// .tmp 失败),验证原 config.json 仍然完整。
func TestSave_PreservesExistingFileOnFailure(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	original := &Config{DefaultReposRoot: "/original/path"}
	if err := Save(original); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	cfgPath := filepath.Join(tmpHome, ".tshoot", "config.json")
	originalBytes, _ := os.ReadFile(cfgPath)

	// 占位 .tmp 为目录(不能写),让下次 Save 在 tmp 阶段就失败
	tmpPath := cfgPath + ".tmp"
	if err := os.Mkdir(tmpPath, 0o755); err != nil {
		t.Fatalf("mkdir tmp blocker: %v", err)
	}
	defer os.RemoveAll(tmpPath)

	// Save 应失败,但**不能**破坏原 config.json
	err := Save(&Config{DefaultReposRoot: "/should/not/persist"})
	if err == nil {
		t.Fatal("expected Save to fail (tmp is a directory), got nil")
	}

	// 验证原 config.json 字节完全不变
	postBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read original config after failed Save: %v", err)
	}
	if string(postBytes) != string(originalBytes) {
		t.Errorf("失败的 Save 破坏了原 config.json:\nbefore=%s\nafter =%s",
			originalBytes, postBytes)
	}

	// 用 Load 反序列化也应得到原值
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load after failed Save: %v", err)
	}
	if loaded.DefaultReposRoot != "/original/path" {
		t.Errorf("失败 Save 后值被污染:want=%q got=%q", "/original/path", loaded.DefaultReposRoot)
	}
}

// TestSave_OverwriteExistingPreservesPerm 验证覆盖写时新文件仍是 0o600
// (rename 把 tmp 的 0o600 inode 移过去,旧文件 inode 被 unlink → 不会继承旧 perm)
func TestSave_OverwriteExistingPreservesPerm(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// 先创建一个 0o644 的"老文件"(模拟用户从老版本升级)
	cfgDir := filepath.Join(tmpHome, ".tshoot")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")
	old, _ := json.Marshal(&Config{DefaultReposRoot: "/old"})
	if err := os.WriteFile(cfgPath, old, 0o644); err != nil {
		t.Fatal(err)
	}

	// 跑一次 Save,新版应升级 perm 到 0o600
	if err := Save(&Config{DefaultReposRoot: "/new"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, _ := os.Stat(cfgPath)
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("覆盖写后 perm 没升级到 0600,got %#o", mode)
	}
}
