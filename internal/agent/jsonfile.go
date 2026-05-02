// jsonfile.go —— 包内通用 JSON 文件读写。
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// readJSONOrEmpty 读 JSON 文件到 map[string]any。文件不存在 / 空文件 → 返回空 map。
// JSON 解析失败 → 带 path 的 error。
func readJSONOrEmpty(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("invalid JSON at %s: %w", path, err)
	}
	return out, nil
}

// writeJSONFile 把 data marshal 成 indent JSON 写到 path,自动建父目录。
// Why: tmp + rename 原子写;若 tshoot 跟 IDE 同时改 settings.json,read-modify-write
// 中段 crash 不会留下截断/空文件,失败时原文件保持完整。
func writeJSONFile(path string, data any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	enc, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, enc, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
