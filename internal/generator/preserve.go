package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
)

// Snapshot 从现有 output 目录提取的人工沉淀：
//   - PreservedFiles：列在 generation.preserve_on_regenerate 中的文件内容（按 workspace 相对路径）
//   - ConfigOverrides：config-map.yaml 中 status=verified 且无 source 字段的行（视为人工填写）
type Snapshot struct {
	PreservedFiles  map[string][]byte
	ConfigOverrides map[string]map[string]analyzer.Finding
	OriginalCenter  string // 上次生成时的 config_center 类型，用于冲突检测
}

// SnapshotExisting 若 outputDir 不存在则返回空 snapshot
func SnapshotExisting(outputDir string, preserveList []string) (*Snapshot, error) {
	snap := &Snapshot{
		PreservedFiles:  map[string][]byte{},
		ConfigOverrides: map[string]map[string]analyzer.Finding{},
	}
	info, err := os.Stat(outputDir)
	if err != nil || !info.IsDir() {
		return snap, nil //nolint:nilerr // 读不到 preserve 文件算作无
	}

	wsRoot := filepath.Join(outputDir, "templates", "workspace-template")

	for _, rel := range preserveList {
		full := filepath.Join(wsRoot, rel)
		if data, err := os.ReadFile(full); err == nil {
			snap.PreservedFiles[rel] = data
		}
	}

	cmPath := filepath.Join(wsRoot, "skills", "routing", "references", "config-map.yaml")
	if data, err := os.ReadFile(cmPath); err == nil {
		if err := parseConfigMapOverrides(data, snap); err != nil {
			return nil, fmt.Errorf("parse prior config-map: %w", err)
		}
	}
	return snap, nil
}

// parseConfigMapOverrides 按 status=verified 且 source 为空 的规则抽取人工行
func parseConfigMapOverrides(data []byte, snap *Snapshot) error {
	var root struct {
		ConfigCenter string                                     `yaml:"config_center"`
		Environments map[string]map[string]map[string]yaml.Node `yaml:"environments"`
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	snap.OriginalCenter = root.ConfigCenter

	for env, services := range root.Environments {
		for svc, fields := range services {
			statusNode, ok := fields["status"]
			if !ok {
				continue
			}
			var status string
			_ = statusNode.Decode(&status)
			if status != "verified" {
				continue
			}
			if _, hasSource := fields["source"]; hasSource {
				// 有 source 字段 → 视为 analyzer 来源，不保留（让下轮分析覆盖）
				continue
			}
			f := analyzer.Finding{
				ConfigCenter: root.ConfigCenter,
				SourceFile:   "_manual",
			}
			for k, v := range fields {
				if k == "status" {
					continue
				}
				var s string
				if err := v.Decode(&s); err == nil {
					applyFieldToFinding(&f, k, s)
				} else {
					// 可能是数组（Apollo namespaces）
					var arr []string
					if err := v.Decode(&arr); err == nil {
						if k == "namespaces" {
							f.Namespaces = arr
						}
					}
				}
			}
			if snap.ConfigOverrides[svc] == nil {
				snap.ConfigOverrides[svc] = map[string]analyzer.Finding{}
			}
			snap.ConfigOverrides[svc][env] = f
		}
	}
	return nil
}

func applyFieldToFinding(f *analyzer.Finding, key, val string) {
	switch key {
	// nacos
	case "namespaceId":
		f.NamespaceID = val
	case "group":
		f.Group = val
	case "dataId":
		f.DataID = val
	// apollo
	case "appId":
		f.AppID = val
	case "cluster":
		f.Cluster = val
	case "meta":
		f.ServerAddr = val
	// consul
	case "kv_prefix":
		f.KVPrefix = val
	case "default_context":
		f.DefaultContext = val
	case "host":
		f.ServerAddr = val
	}
}

// Restore 把 preserved 文件内容回写到 outputDir
func (s *Snapshot) Restore(outputDir string) error {
	wsRoot := filepath.Join(outputDir, "templates", "workspace-template")
	for rel, content := range s.PreservedFiles {
		full := filepath.Join(wsRoot, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}
