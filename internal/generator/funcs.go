package generator

import (
	"slices"
	"strings"
	"text/template"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

func toFindingView(f analyzer.Finding) *findingView {
	return &findingView{
		ConfigCenter:   f.ConfigCenter,
		SourceFile:     f.SourceFile,
		ServerAddr:     f.ServerAddr,
		DataID:         f.DataID,
		Group:          f.Group,
		NamespaceID:    f.NamespaceID,
		AppID:          f.AppID,
		Namespaces:     f.Namespaces,
		Cluster:        f.Cluster,
		KVPrefix:       f.KVPrefix,
		DefaultContext: f.DefaultContext,
	}
}

var dataStoreSkillAlias = map[string]string{
	"elasticsearch": "es",
}

func dataStoreSkillName(typ string) string {
	if alias, ok := dataStoreSkillAlias[typ]; ok {
		return alias + "-runtime-query"
	}
	return typ + "-runtime-query"
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"upper":          strings.ToUpper,
		"lower":          strings.ToLower,
		"dataStoreSkill": dataStoreSkillName,
		"list":           func(items ...string) []string { return items },
		"hasSkill": func(ctx *Context, name string) bool {
			if len(ctx.Generation.SkillsWhitelist) == 0 {
				return true
			}
			return slices.Contains(ctx.Generation.SkillsWhitelist, name)
		},
		// findConfig 返回给定 service+env 的 analyzer finding 视图；命中优先级：精确 env > 无 env 默认 > nil
		"findConfig": func(ctx *Context, service, env string) *findingView {
			byEnv, ok := ctx.Findings[service]
			if !ok {
				return nil
			}
			if f, ok := byEnv[env]; ok {
				return toFindingView(f)
			}
			if f, ok := byEnv[""]; ok {
				return toFindingView(f)
			}
			return nil
		},
		// findPrior 返回来自上次生成产物中的人工 override；命中优先级同上
		"findPrior": func(ctx *Context, service, env string) *findingView {
			byEnv, ok := ctx.PriorOverrides[service]
			if !ok {
				return nil
			}
			if f, ok := byEnv[env]; ok {
				return toFindingView(f)
			}
			if f, ok := byEnv[""]; ok {
				return toFindingView(f)
			}
			return nil
		},
		// repoConfigSource 找到拥有 service 的 repo,返回它的 config_source。
		// 没找到 / 没绑(单源场景):返回 "default"。
		"repoConfigSource": func(ctx *Context, service string) string {
			for _, r := range ctx.Repos {
				names := r.ServiceNames
				if len(names) == 0 {
					names = []string{r.Name}
				}
				for _, sn := range names {
					if sn == service {
						if r.ConfigSource != "" {
							return r.ConfigSource
						}
						return "default"
					}
				}
			}
			return "default"
		},
		// configSourceByID 按 id 找 ConfigCenters[] 里的源;找不到返回主源(兜底,
		// 避免 yaml 引用不存在 id 时模板崩),配 doctor 检查给用户预警。
		"configSourceByID": func(ctx *Context, id string) ConfigCenterView {
			for _, cc := range ctx.Infrastructure.ConfigCenters {
				if cc.ID == id {
					return toConfigCenterView(cc)
				}
			}
			return toConfigCenterView(ctx.Infrastructure.PrimaryConfigCenter())
		},
		// mcpKeyForSource 跟 internal/agent/install_naming.go 的 mcpKey 保持镜像:
		//   sourceID=="default" 或空 → "<prefix>-<env>"(老命名,向后兼容)
		//   显式多源 → "<prefix>-<sourceID>-<env>"
		"mcpKeyForSource": func(prefix, sourceID, envID string) string {
			if sourceID == "" || sourceID == "default" {
				return prefix + "-" + envID
			}
			return prefix + "-" + sourceID + "-" + envID
		},
		// scannedDownstreamsForService 给 service-dependency-map.yaml.tmpl 用:从 ctx 找指定服务名
		// 对应 repo 的 DownstreamCalls,提取去重后的目标服务名列表。同 repo 多服务时复用 repo 的
		// 下游列表(无法精确到 service 级,因为 dependency_scan 是 repo 级 regex 扫)。
		"scannedDownstreamsForService": func(ctx *Context, svc string) []string {
			seen := map[string]bool{}
			var out []string
			for _, repo := range ctx.Repos {
				match := false
				if repo.Name == svc {
					match = true
				}
				for _, sn := range repo.ServiceNames {
					if sn == svc {
						match = true
						break
					}
				}
				if !match {
					continue
				}
				calls := ctx.DownstreamCallsByRepo[repo.Name]
				for _, c := range calls {
					if c.Target == "" || seen[c.Target] {
						continue
					}
					seen[c.Target] = true
					out = append(out, c.Target)
				}
			}
			return out
		},
		// scannedDataStoresForService 给 service-dependency-map.yaml.tmpl 用:类似上面,提数据层使用。
		// 输出格式 "<type>:scanned"(没 logical 名字时占位 "scanned",用户填具体逻辑名)。
		"scannedDataStoresForService": func(ctx *Context, svc string) []string {
			seen := map[string]bool{}
			var out []string
			for _, repo := range ctx.Repos {
				match := false
				if repo.Name == svc {
					match = true
				}
				for _, sn := range repo.ServiceNames {
					if sn == svc {
						match = true
						break
					}
				}
				if !match {
					continue
				}
				usages := ctx.DataStoreUsagesByRepo[repo.Name]
				for _, u := range usages {
					if u.Type == "" {
						continue
					}
					logical := u.Logical
					if logical == "" {
						logical = "scanned"
					}
					key := u.Type + ":" + logical
					if seen[key] {
						continue
					}
					seen[key] = true
					out = append(out, key)
				}
			}
			return out
		},
		// scannedSchemaTablesForService 给 data-schema-map.yaml.tmpl 用:从 ctx 找指定服务名
		// 对应 repo 的 SchemaTables。同 repo 多服务时复用 repo 的 schema 列表。
		"scannedSchemaTablesForService": func(ctx *Context, svc string) []analyzer.SchemaTable {
			seen := map[string]bool{}
			var out []analyzer.SchemaTable
			for _, repo := range ctx.Repos {
				match := false
				if repo.Name == svc {
					match = true
				}
				for _, sn := range repo.ServiceNames {
					if sn == svc {
						match = true
						break
					}
				}
				if !match {
					continue
				}
				for _, t := range ctx.SchemaTablesByRepo[repo.Name] {
					key := t.Name + "|" + t.Kind
					if seen[key] {
						continue
					}
					seen[key] = true
					out = append(out, t)
				}
			}
			return out
		},
		// mcpKeyForAgentSource 跟 mcpKeyForAgent 镜像:加 agent-id 前缀,Claude Code/Cursor
		// 共享 settings.json 池场景必备,避免多 system 同名 mcp 互相覆盖。OpenClaw 也走
		// 这条路保持三平台命名统一(install_native_openclaw 同步用 mcpKeyForAgent)。
		"mcpKeyForAgentSource": func(agentID, prefix, sourceID, envID string) string {
			base := prefix + "-" + envID
			if sourceID != "" && sourceID != "default" {
				base = prefix + "-" + sourceID + "-" + envID
			}
			if agentID == "" {
				return base
			}
			return agentID + "-" + base
		},
	}
}

// ConfigCenterView 模板侧可访问的 config_centers[] 单元素视图。
// 字段子集来自 config.ConfigCenter,显式列出避免把 config 包整个泄漏到模板。
type ConfigCenterView struct {
	ID             string
	Type           string
	DataIDPatterns []string
	Endpoints      []endpointView
}

type endpointView struct {
	Env           string
	Addr          string
	NamespaceHint string
}

func toConfigCenterView(cc config.ConfigCenter) ConfigCenterView {
	out := ConfigCenterView{
		ID:             cc.ID,
		Type:           cc.Type,
		DataIDPatterns: cc.DataIDPatterns,
	}
	for _, ep := range cc.Endpoints {
		out.Endpoints = append(out.Endpoints, endpointView{
			Env: ep.Env, Addr: ep.Addr, NamespaceHint: ep.NamespaceHint,
		})
	}
	return out
}

// findingView 是模板侧可访问的结构（复制自 analyzer.Finding，避免直接把 analyzer 包泄漏到模板）
type findingView struct {
	ConfigCenter   string
	SourceFile     string
	ServerAddr     string
	DataID         string
	Group          string
	NamespaceID    string
	AppID          string
	Namespaces     []string
	Cluster        string
	KVPrefix       string
	DefaultContext string
}
