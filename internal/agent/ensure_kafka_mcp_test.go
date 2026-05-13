package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestEnsureKafkaMCPInstalled_PathHit:PATH 探到 kafka-mcp-server 时返回**绝对路径**
// (不是字面 "kafka-mcp-server")。
//
// 这是 install 流程跨 shell PATH / mac GUI launchd PATH 的兼容关键 —— shell 看到 binary
// 写字面到 ~/.claude.json,Claude Code 从 desktop 启动时 launchd PATH 没 brew prefix,
// 字面找不到 ENOENT。等价 commit e44c74d 修过的 findOpenclawCLI bug。
func TestEnsureKafkaMCPInstalled_PathHit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 路径分隔符 + .exe 处理另文测试")
	}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "kafka-mcp-server")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir()) // 让 cache 探测 miss(否则若本机 ~/.tshoot/bin 有装 ver match 会先命中 cache 分支)

	got, err := EnsureKafkaMCPInstalled(nil)
	if err != nil {
		t.Fatalf("EnsureKafkaMCPInstalled() err = %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("returned path must be absolute (不能字面 kafka-mcp-server,desktop launchd PATH 找不到), got: %q", got)
	}
	if got != binPath {
		t.Errorf("EnsureKafkaMCPInstalled() = %q, want %q", got, binPath)
	}
}

// TestEnsureKafkaMCPInstalled_CacheHit:~/.tshoot/bin/kafka-mcp-server-<ver> 已存在且
// 可执行时直接复用,不发起网络下载(网络断的 CI 跑也得过)。
func TestEnsureKafkaMCPInstalled_CacheHit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 走手动安装路径,cache 路径不适用")
	}
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("PATH", t.TempDir()) // PATH miss,逼 cache 命中

	cacheDir := filepath.Join(fakeHome, ".tshoot", "bin")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(cacheDir, "kafka-mcp-server-"+kafkaMCPVersion)
	if err := os.WriteFile(cachePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := EnsureKafkaMCPInstalled(nil)
	if err != nil {
		t.Fatalf("EnsureKafkaMCPInstalled() err = %v", err)
	}
	if got != cachePath {
		t.Errorf("EnsureKafkaMCPInstalled() = %q, want cache path %q", got, cachePath)
	}
}

// TestKafkaMCPCachePath_VersionPinned:cache 路径文件名带版本号,bump kafkaMCPVersion 后
// 旧文件不会被命中,触发重下避免静默用旧版。
func TestKafkaMCPCachePath_VersionPinned(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := kafkaMCPCachePath()
	if err != nil {
		t.Fatalf("kafkaMCPCachePath() err = %v", err)
	}
	if !strings.Contains(filepath.Base(got), kafkaMCPVersion) {
		t.Errorf("cache 路径文件名应包含版本号 %q,实际 %q", kafkaMCPVersion, filepath.Base(got))
	}
}
