// jsonfile.go —— 包内通用 JSON 文件读写。被 install_native_*.go / uninstall_native_*.go
// 多处共用(openclaw 的 openclaw.json / IDE 的 settings.json / mcp.json / creds.json),
// 之前散在 install_native_openclaw.go 里造成"openclaw 文件被 IDE 平台代码反向 import"。
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
func writeJSONFile(path string, data any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	enc, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, enc, mode)
}
