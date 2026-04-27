package aitools

import "testing"

// 只做形状/不崩测试 —— 真实 claude/cursor 装没装在 CI 不确定,
// 函数必须在两种情况下都不 panic + 返回合法 Result。
func TestDetectClaudeCode_NoPanic(t *testing.T) {
	r := DetectClaudeCode()
	if r == nil {
		t.Fatal("should return non-nil Result")
	}
	if r.Installed {
		// 装了:Path 或 Note 至少有一个(Note 表示 fallback 路径命中)
		if r.Path == "" && r.Note == "" {
			t.Error("Installed=true should provide Path or Note")
		}
	}
}

func TestDetectCursor_NoPanic(t *testing.T) {
	r := DetectCursor()
	if r == nil {
		t.Fatal("should return non-nil Result")
	}
	if r.Installed && r.Path == "" {
		t.Error("Installed=true should provide Path")
	}
}

func TestReadPlistStringKey(t *testing.T) {
	sample := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>CFBundleShortVersionString</key>
	<string>2.6.20</string>
	<key>CFBundleVersion</key>
	<string>2262</string>
</dict>
</plist>`)
	if v := readPlistStringKey(sample, "CFBundleShortVersionString"); v != "2.6.20" {
		t.Errorf("version: got %q want 2.6.20", v)
	}
	if v := readPlistStringKey(sample, "CFBundleVersion"); v != "2262" {
		t.Errorf("build: got %q", v)
	}
	if v := readPlistStringKey(sample, "Missing"); v != "" {
		t.Errorf("missing key: got %q want ''", v)
	}
}
