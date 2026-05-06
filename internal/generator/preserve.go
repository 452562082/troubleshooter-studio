package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
)

// Snapshot 从现有 output 目录提取的人工沉淀:仅 config-map.yaml 中 status=verified
// 且无 source 字段的行(视为人工填写),后续 render 时回填到新 config-map。
//
// 历史:曾经按 generation.preserve_on_regenerate 列表整文件保留(SOUL/USER/CHECKLIST 等),
// 实际造成"模板更新被静默吞掉"——snapshot 老内容→render→restore 老内容覆盖,
// 用户看不到模板的任何后续修订。整文件 preserve 已删,只留 config-map verified 行
// (那才是真正用户手填的领域数据,不会随模板版本变)。
type Snapshot struct {
	ConfigOverrides map[string]map[string]analyzer.Finding
	OriginalCenter  string // 上次生成时的 config_center 类型，用于冲突检测
}

// SnapshotExisting 若 outputDir 不存在则返回空 snapshot
func SnapshotExisting(outputDir string) (*Snapshot, error) {
	snap := &Snapshot{
		ConfigOverrides: map[string]map[string]analyzer.Finding{},
	}
	info, err := os.Stat(outputDir)
	if err != nil || !info.IsDir() {
		return snap, nil //nolint:nilerr // 读不到 config-map 算作无 prior
	}

	wsRoot := filepath.Join(outputDir, "templates", "workspace-template")
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

