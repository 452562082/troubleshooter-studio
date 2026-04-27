package deploy

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

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
