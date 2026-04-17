package generator

import (
	"strings"
	"text/template"

	"github.com/xiaolong/troubleshooter-factory/internal/analyzer"
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
			for _, s := range ctx.Generation.SkillsWhitelist {
				if s == name {
					return true
				}
			}
			return len(ctx.Generation.SkillsWhitelist) == 0
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
	}
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
