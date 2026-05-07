package doctor

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Patch 描述一条将要对 troubleshooter.yaml 施加的改动（用户 review 用）。
type Patch struct {
	Kind      string `json:"kind"`       // stack-change / config-center-type / ...
	Path      string `json:"path"`       // 可读的 yaml path，展示给用户
	Key       string `json:"key"`        // 行内实际 key（取 Path 最后一段），用来做行级精确替换
	From      string `json:"from"`       // 旧值
	To        string `json:"to"`         // 新值
	Line      int    `json:"line"`       // 1-based，原 yaml 里这个 scalar 所在行
	FromIssue string `json:"from_issue"` // 触发的 issue category
}

// PlanFixes 读 yaml 文件，对所有能自动修复的 issue 生成 Patch 列表（不写盘）。
// 只解析 yaml 拿行号；真实写回走 ApplyAndWrite 的行级精确替换，保持原文件 bit-perfect。
func PlanFixes(yamlPath string, issues []Issue) (patches []Patch, err error) {
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, fmt.Errorf("parse %s: %w", yamlPath, err)
	}

	for _, iss := range issues {
		if iss.FixKey == "" {
			continue
		}
		p, ok := planOne(&node, iss)
		if ok {
			patches = append(patches, p)
		}
	}
	return patches, nil
}

// ApplyAndWrite 对 yaml 文件做**行级精确替换**，不走 yaml.Marshal，
// 保持用户手写的空行、注释、缩进风格 100% 不变。
// 只改 PlanFixes 标记的行，改前备份到 <yamlPath>.bak.<timestamp>。
func ApplyAndWrite(yamlPath string, patches []Patch) (backupPath string, err error) {
	orig, err := os.ReadFile(yamlPath)
	if err != nil {
		return "", err
	}
	backupPath = yamlPath + ".bak." + nowStamp()
	if err := os.WriteFile(backupPath, orig, 0o644); err != nil {
		return "", err
	}

	lines := strings.Split(string(orig), "\n")
	for _, p := range patches {
		if p.Line < 1 || p.Line > len(lines) {
			continue
		}
		idx := p.Line - 1
		find := p.Key + ": " + p.From
		repl := p.Key + ": " + p.To
		if !strings.Contains(lines[idx], find) {
			// 原行格式不符合预期（例如多空格），skip —— patch 静默失败比破坏 yaml 安全
			continue
		}
		lines[idx] = strings.Replace(lines[idx], find, repl, 1)
	}
	if err := os.WriteFile(yamlPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return "", err
	}
	return backupPath, nil
}

// planOne 为单条 issue 生成一个 Patch。只解析 yaml 拿行号；不改 Node（行级替换走 ApplyAndWrite）。
// 返回 ok=false 表示 FixKey 不认识或定位失败（调用方忽略该 issue 即可）。
func planOne(root *yaml.Node, iss Issue) (Patch, bool) {
	// repo.<name>.stack —— repos[x].stack
	if rest, ok := strings.CutPrefix(iss.FixKey, "repo."); ok {
		parts := strings.SplitN(rest, ".", 2)
		if len(parts) == 2 && parts[1] == "stack" {
			name := parts[0]
			_, node, ok := findRepoField(root, name, "stack")
			if !ok {
				return Patch{}, false
			}
			return Patch{
				Kind:      "stack-change",
				Path:      fmt.Sprintf("repos[%s].stack", name),
				Key:       "stack",
				From:      node.Value,
				To:        iss.FixValue,
				Line:      node.Line,
				FromIssue: iss.Category,
			}, true
		}
	}

	// config-center.type —— infrastructure.config_center.type
	if iss.FixKey == "config-center.type" {
		node, ok := findScalar(root, "infrastructure", "config_center", "type")
		if !ok {
			return Patch{}, false
		}
		return Patch{
			Kind:      "config-center-type",
			Path:      "infrastructure.config_center.type",
			Key:       "type",
			From:      node.Value,
			To:        iss.FixValue,
			Line:      node.Line,
			FromIssue: iss.Category,
		}, true
	}

	return Patch{}, false
}

// ── yaml.Node 辅助 ──

// findScalar 沿 mapping 路径走下去，返回最后一层的 scalar node。
func findScalar(root *yaml.Node, path ...string) (*yaml.Node, bool) {
	cur := root
	if cur.Kind == yaml.DocumentNode {
		if len(cur.Content) == 0 {
			return nil, false
		}
		cur = cur.Content[0]
	}
	for _, key := range path {
		if cur.Kind != yaml.MappingNode {
			return nil, false
		}
		next := mappingValue(cur, key)
		if next == nil {
			return nil, false
		}
		cur = next
	}
	if cur.Kind != yaml.ScalarNode {
		return nil, false
	}
	return cur, true
}

// findRepoField 在 repos 数组里按 name 找到条目，再取其 field scalar。
func findRepoField(root *yaml.Node, repoName, field string) (string, *yaml.Node, bool) {
	doc := root
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return "", nil, false
		}
		doc = doc.Content[0]
	}
	reposNode := mappingValue(doc, "repos")
	if reposNode == nil || reposNode.Kind != yaml.SequenceNode {
		return "", nil, false
	}
	for _, item := range reposNode.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		nameNode := mappingValue(item, "name")
		if nameNode == nil || nameNode.Value != repoName {
			continue
		}
		fieldNode := mappingValue(item, field)
		if fieldNode == nil || fieldNode.Kind != yaml.ScalarNode {
			return "", nil, false
		}
		return fieldNode.Value, fieldNode, true
	}
	return "", nil, false
}

// mappingValue 在 mapping node 里按 key 返回 value node（返回的是原 pointer，可以就地修改）。
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return v
		}
	}
	return nil
}

func nowStamp() string {
	return time.Now().Format("20060102-150405")
}
