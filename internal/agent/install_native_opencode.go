package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/tailscale/hujson"
)

func openCodeConfigFiles(root string) []string {
	return []string{filepath.Join(root, "config.json"), filepath.Join(root, "opencode.json"), filepath.Join(root, "opencode.jsonc")}
}

// OpenCode merges all global files. Consolidate only this robot's entries into
// the highest-priority file so old entries cannot reappear after uninstall.
func mergeOpenCodeMCP(path, prefix string, servers map[string]any, onlyNew bool) ([]string, error) {
	files := openCodeConfigFiles(filepath.Dir(path))
	previous := map[string]any{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		standard, err := hujson.Standardize(data)
		if err != nil {
			return nil, fmt.Errorf("OpenCode 配置解析失败，原文件保留")
		}
		var obj map[string]any
		if err := json.Unmarshal(standard, &obj); err != nil || obj == nil {
			return nil, fmt.Errorf("OpenCode 配置必须是对象")
		}
		entries, ok := obj["mcp"].(map[string]any)
		if !ok && obj["mcp"] != nil {
			return nil, fmt.Errorf("OpenCode mcp 配置必须是对象")
		}
		for name, value := range entries {
			if strings.HasPrefix(name, prefix) {
				previous[name] = mergeOpenCodeValue(previous[name], value)
			}
		}
	}
	next := map[string]any{}
	for key, value := range servers {
		next[key] = value
	}
	if onlyNew {
		for key, value := range previous {
			next[key] = value
		}
	}
	if _, err := updateOpenCodeMCP(path, prefix, next, false); err != nil {
		return nil, err
	}
	for _, file := range files {
		if file == path {
			continue
		}
		if _, err := updateOpenCodeMCP(file, prefix, nil, false); err != nil {
			return nil, err
		}
	}
	var removed []string
	for key := range previous {
		if _, ok := next[key]; !ok {
			removed = append(removed, key)
		}
	}
	slices.Sort(removed)
	return removed, nil
}

func mergeOpenCodeValue(old, next any) any {
	before, oldOK := old.(map[string]any)
	after, newOK := next.(map[string]any)
	if !oldOK || !newOK {
		return next
	}
	for key, value := range after {
		before[key] = mergeOpenCodeValue(before[key], value)
	}
	return before
}

// OpenCode uses mcp.<name> with command arrays, rather than mcpServers.
func openCodeMCPServers(servers map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for key, raw := range servers {
		spec, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid MCP spec %s", key)
		}
		if url, ok := spec["url"].(string); ok && url != "" {
			entry := map[string]any{"type": "remote", "url": url, "enabled": true, "oauth": false}
			if headers := spec["headers"]; headers != nil {
				entry["headers"] = headers
			}
			out[key] = entry
			continue
		}
		command, ok := spec["command"].(string)
		if !ok || command == "" {
			return nil, fmt.Errorf("MCP %s missing command", key)
		}
		args := []any{command}
		switch values := spec["args"].(type) {
		case []any:
			args = append(args, values...)
		case []string:
			for _, value := range values {
				args = append(args, value)
			}
		}
		entry := map[string]any{"type": "local", "command": args, "enabled": true}
		if env := spec["env"]; env != nil {
			entry["environment"] = env
		}
		out[key] = entry
	}
	return out, nil
}

func updateOpenCodeMCP(path, prefix string, servers map[string]any, onlyNew bool) ([]string, error) {
	obj := map[string]any{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) && len(servers) == 0 {
		return nil, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		standard, parseErr := hujson.Standardize(append([]byte(nil), data...))
		if parseErr != nil {
			return nil, fmt.Errorf("读取 OpenCode 配置失败，原文件保留")
		}
		if err := json.Unmarshal(standard, &obj); err != nil {
			return nil, fmt.Errorf("读取 OpenCode 配置失败，原文件保留: %w", err)
		}
	}
	if obj == nil {
		return nil, fmt.Errorf("OpenCode 配置必须是 JSON 对象")
	}
	current, ok := obj["mcp"].(map[string]any)
	if !ok && obj["mcp"] != nil {
		return nil, fmt.Errorf("OpenCode mcp 配置必须是对象")
	}
	missingMCP := current == nil
	if missingMCP {
		current = map[string]any{}
	}
	var removed []string
	if !onlyNew {
		for key := range current {
			if strings.HasPrefix(key, prefix) {
				if _, exists := servers[key]; !exists {
					delete(current, key)
					removed = append(removed, key)
				}
			}
		}
	}
	var applied []string
	for key, value := range servers {
		if _, exists := current[key]; onlyNew && exists {
			continue
		}
		current[key] = value
		applied = append(applied, key)
	}
	if len(removed) == 0 && len(applied) == 0 {
		return nil, nil
	}
	obj["mcp"] = current
	if err == nil {
		if err := os.WriteFile(path+".bak."+nanoTimestamp(), data, 0600); err != nil {
			return nil, err
		}
	}
	// Patch individual owned entries; retain comments on unrelated MCP entries.
	if len(data) == 0 {
		data = []byte("{}")
	}
	doc, err := hujson.Parse(data)
	if err != nil {
		return nil, err
	}
	var operations []map[string]any
	if missingMCP {
		operations = append(operations, map[string]any{"op": "add", "path": "/mcp", "value": map[string]any{}})
	}
	keyPath := func(key string) string {
		return "/mcp/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
	}
	sort.Strings(removed)
	sort.Strings(applied)
	for _, key := range removed {
		operations = append(operations, map[string]any{"op": "remove", "path": keyPath(key)})
	}
	for _, key := range applied {
		operations = append(operations, map[string]any{"op": "add", "path": keyPath(key), "value": current[key]})
	}
	patch, err := json.Marshal(operations)
	if err != nil {
		return nil, err
	}
	if err := doc.Patch(patch); err != nil {
		return nil, err
	}
	doc.Format()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".opencode-mcp-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(doc.Pack()); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, err
	}
	sort.Strings(removed)
	return removed, nil
}

// Probe the stored values, including credentials preserved during re-deployment.
func probeOpenCodeMCP(path, prefix string, emit func(string)) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err = hujson.Standardize(append([]byte(nil), data...))
	if err != nil {
		return err
	}
	var root struct {
		MCP map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	servers := map[string]any{}
	for name, spec := range root.MCP {
		if !strings.HasPrefix(name, prefix) || spec["enabled"] == false {
			continue
		}
		if spec["type"] == "remote" {
			servers[name] = map[string]any{"url": spec["url"], "headers": spec["headers"]}
		} else if command, ok := spec["command"].([]any); ok && len(command) > 0 {
			servers[name] = map[string]any{"command": command[0], "args": command[1:], "env": spec["environment"]}
		} else {
			return fmt.Errorf("OpenCode MCP %s 缺少可执行命令", name)
		}
	}
	failed := false
	probeMCPServersFromConfig(context.Background(), servers, func(name, status, _ string) {
		emit(fmt.Sprintf("[%s] %s：initialize + tools/list", status, name))
		if status != "PASS" {
			failed = true
		}
	})
	if failed {
		return fmt.Errorf("OpenCode MCP 运行自检未通过，请检查服务与凭据后重试")
	}
	return nil
}
