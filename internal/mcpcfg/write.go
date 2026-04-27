package mcpcfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Server 一条 MCP server 的统一结构。跟 claude-code / cursor / openclaw 4 个平台
// 共享的字段对齐;不同平台里的其它字段(如 cursor 的 "type":"http" / "url")暂不支持,
// 生成器本来就只输出这几个基础字段。
type Server struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// URL 给 HTTP-based MCP server(cursor 的 "type":"http" 模式);
	// 跟 Command/Args 互斥,有 URL 时前者留空。
	URL  string `json:"url,omitempty"`
	Type string `json:"type,omitempty"`
}

// MergeWrite 往 resolved 指定的配置文件里"追加/覆盖" servers 条目,保留文件里用户手配的
// 其它 MCP server 不动。文件不存在会创建(包括父目录);已存在用 JSON 解析后 merge。
//
// 合并策略:servers map 里的 key 会覆盖同名已有条目;不在 servers 里的已有条目保留。
// 想删某个由本工具之前写过的 MCP server,调用方应在 servers 里不带它并另外显式清理 ——
// 这里保守:只加/改,不主动删(避免误伤用户手配条目)。
//
// 同平台 schema 差异由 resolved.NestedUnderMCP 体现:
//   - openclaw(nested)    : 顶层 {"mcp":{"servers":{...}}}
//   - claude-code/cursor  : 顶层 {"mcpServers":{...}}
func MergeWrite(resolved *Resolved, servers map[string]Server) error {
	if resolved == nil {
		return fmt.Errorf("resolved is nil")
	}
	if resolved.Path == "" {
		return fmt.Errorf("resolved.Path empty")
	}
	if len(servers) == 0 {
		return nil // 没东西可写
	}
	if err := os.MkdirAll(filepath.Dir(resolved.Path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(resolved.Path), err)
	}

	// 读已有内容(可能不存在)
	var root map[string]any
	if raw, err := os.ReadFile(resolved.Path); err == nil {
		if err := json.Unmarshal(raw, &root); err != nil {
			return fmt.Errorf("parse existing %s: %w", resolved.Path, err)
		}
	}
	if root == nil {
		root = map[string]any{}
	}

	// 定位目标子树(mcp.servers 或 mcpServers)
	target := locateServersMap(root, resolved.NestedUnderMCP, true)

	// 合并:servers map 的 key 覆盖同名条目;原有的其它 key 不动。
	// 注意 target 可能是从文件里读出来的 map[string]any,里面的值是 interface{},
	// 我们要把 servers 里的 Server 转成同型 map 才不会类型错位。
	for name, s := range servers {
		encoded, err := structToMap(s)
		if err != nil {
			return fmt.Errorf("encode server %q: %w", name, err)
		}
		target[name] = encoded
	}

	// 写回(2-space indent,保持跟 openclaw / cursor 常见风格一致)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := resolved.Path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, resolved.Path); err != nil {
		return fmt.Errorf("rename tmp -> %s: %w", resolved.Path, err)
	}
	return nil
}

// locateServersMap 在 root 里找/建 servers 子 map,返该 map(直接可写)。
// create=true 时路径上缺失的节点会自动建;create=false 时没找到返回空 map(调用方 noop)。
//   - nested=true:root["mcp"]["servers"]
//   - nested=false:root["mcpServers"]
//
// 如果已存在的节点类型不对(比如用户把 mcpServers 设成了 string),返一个新 map
// 让调用方继续,原来的非法值会在下一次 marshal 时被替换(保守是保留,但保留就没法合并,
// 这里优先"能工作"而不是"绝对不改用户手改")。
func locateServersMap(root map[string]any, nested, create bool) map[string]any {
	if nested {
		mcpNode, ok := root["mcp"].(map[string]any)
		if !ok {
			if !create {
				return map[string]any{}
			}
			mcpNode = map[string]any{}
			root["mcp"] = mcpNode
		}
		servers, ok := mcpNode["servers"].(map[string]any)
		if !ok {
			if !create {
				return map[string]any{}
			}
			servers = map[string]any{}
			mcpNode["servers"] = servers
		}
		return servers
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		if !create {
			return map[string]any{}
		}
		servers = map[string]any{}
		root["mcpServers"] = servers
	}
	return servers
}

// structToMap 把 Server struct marshal → unmarshal 一次变成 map[string]any,
// 方便跟已有 JSON 节点无缝合并(避免 encoding/json 在 heterogeneous map 里的类型麻烦)。
func structToMap(s Server) (map[string]any, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
