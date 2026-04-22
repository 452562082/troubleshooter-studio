package deploy

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestFindInstallSh(t *testing.T) {
	t.Run("scripts/ 优先(openclaw layout)", func(t *testing.T) {
		dir := t.TempDir()
		scriptsInstall := filepath.Join(dir, "scripts", "install.sh")
		if err := os.MkdirAll(filepath.Dir(scriptsInstall), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(scriptsInstall, []byte("#!/bin/bash\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// root 下也有一个,但要优先 scripts/
		rootInstall := filepath.Join(dir, "install.sh")
		if err := os.WriteFile(rootInstall, []byte("#!/bin/bash\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		got, err := FindInstallSh(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != scriptsInstall {
			t.Errorf("want %s, got %s", scriptsInstall, got)
		}
	})

	t.Run("root 兜底(standalone/claude-code/cursor layout)", func(t *testing.T) {
		dir := t.TempDir()
		rootInstall := filepath.Join(dir, "install.sh")
		if err := os.WriteFile(rootInstall, []byte("#!/bin/bash\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		got, err := FindInstallSh(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != rootInstall {
			t.Errorf("want %s, got %s", rootInstall, got)
		}
	})

	t.Run("两处都没有返回 ErrNotExist", func(t *testing.T) {
		dir := t.TempDir()
		_, err := FindInstallSh(dir)
		if err != os.ErrNotExist {
			t.Errorf("want os.ErrNotExist, got %v", err)
		}
	})
}

func TestParseInstallPrompts(t *testing.T) {
	t.Run("抓 read_var 基本用法", func(t *testing.T) {
		dir := t.TempDir()
		sh := filepath.Join(dir, "scripts", "install.sh")
		_ = os.MkdirAll(filepath.Dir(sh), 0o755)
		_ = os.WriteFile(sh, []byte(`#!/usr/bin/env bash
read_var CC_ADDR_DEV "NACOS 地址 (dev): "
read_var CC_PASS_DEV "NACOS 密码 (dev): " secret
read_var GRAFANA_URL "Grafana URL: "
# read_var 注释里的不算
  read_var LEADING_SPACE "可以有前置空白: "
`), 0o644)

		got, err := ParseInstallPrompts(dir)
		if err != nil {
			t.Fatal(err)
		}
		want := []Prompt{
			{Name: "CC_ADDR_DEV", Prompt: "NACOS 地址 (dev):", Secret: false},
			{Name: "CC_PASS_DEV", Prompt: "NACOS 密码 (dev):", Secret: true},
			{Name: "GRAFANA_URL", Prompt: "Grafana URL:", Secret: false},
			{Name: "LEADING_SPACE", Prompt: "可以有前置空白:", Secret: false},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("prompts mismatch:\nwant %+v\ngot  %+v", want, got)
		}
	})

	t.Run("去重(同名变量只留第一次)", func(t *testing.T) {
		dir := t.TempDir()
		sh := filepath.Join(dir, "install.sh") // 测 root 位置
		_ = os.WriteFile(sh, []byte(`#!/usr/bin/env bash
read_var TOKEN "first prompt: "
read_var OTHER "xxx: "
read_var TOKEN "second prompt (should be ignored): "
`), 0o644)

		got, err := ParseInstallPrompts(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 unique prompts, got %d: %+v", len(got), got)
		}
		if got[0].Name != "TOKEN" || got[0].Prompt != "first prompt:" {
			t.Errorf("first occurrence not kept: %+v", got[0])
		}
	})

	t.Run("install.sh 不存在 → (nil, nil)", func(t *testing.T) {
		got, err := ParseInstallPrompts(t.TempDir())
		if err != nil {
			t.Errorf("want nil error, got %v", err)
		}
		if got != nil {
			t.Errorf("want nil slice, got %+v", got)
		}
	})

	t.Run("跳过注释行里的 read_var", func(t *testing.T) {
		dir := t.TempDir()
		sh := filepath.Join(dir, "install.sh")
		_ = os.WriteFile(sh, []byte(`#!/usr/bin/env bash
# 这是注释: read_var FAKE "这个不能被解析"
read_var REAL "真的: "
`), 0o644)

		got, err := ParseInstallPrompts(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Name != "REAL" {
			t.Errorf("注释行未跳过: %+v", got)
		}
	})
}

func TestEnvFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := map[string]string{
		"SIMPLE":      "value",
		"WITH_SPACE":  "hello world",
		"WITH_QUOTE":  `it's a test`,
		"EMPTY_VALUE": "",
	}
	if err := WriteEnvFile(dir, in); err != nil {
		t.Fatal(err)
	}

	envPath := filepath.Join(dir, "scripts", ".env")
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("env file not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("want mode 0600, got %o", perm)
	}

	out, err := ReadEnvFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedKeys(in), sortedKeys(out)) {
		t.Errorf("keys mismatch: want %v, got %v", sortedKeys(in), sortedKeys(out))
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("%s: want %q, got %q", k, v, out[k])
		}
	}
}

func TestWriteEnvFileNoOpForEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := WriteEnvFile(dir, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	// 不应建 scripts/ 子目录
	if _, err := os.Stat(filepath.Join(dir, "scripts")); !os.IsNotExist(err) {
		t.Errorf("kv 为空时不应该建 scripts/ 目录,err=%v", err)
	}
}

func TestReadEnvFileMissing(t *testing.T) {
	// 不存在不算错,返回 nil map
	out, err := ReadEnvFile(t.TempDir())
	if err != nil {
		t.Errorf("want nil error, got %v", err)
	}
	if out != nil {
		t.Errorf("want nil map, got %+v", out)
	}
}

func TestReadEnvFileParsesQuotedVariants(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
	// 手写几种 bash env 常见形式
	_ = os.WriteFile(filepath.Join(dir, "scripts", ".env"), []byte(`# 注释行
EMPTY=
SINGLE='hello'
DOUBLE="world"
BARE=raw
  WITH_SPACE  =  trimmed
`), 0o644)

	out, err := ReadEnvFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"EMPTY":      "",
		"SINGLE":     "hello",
		"DOUBLE":     "world",
		"BARE":       "raw",
		"WITH_SPACE": "trimmed",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s: want %q, got %q", k, v, out[k])
		}
	}
	if strings.Contains(out["EMPTY"], "#") {
		t.Errorf("comment not skipped: %+v", out)
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
