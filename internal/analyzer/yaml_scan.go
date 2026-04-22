package analyzer

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ScanYAML 读 YAML 文件并按 config-center 类型抽取 finding
// 返回 nil 表示无命中。
func ScanYAML(absPath, relPath, configCenter string) (*Finding, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, nil //nolint:nilerr // yaml 解析失败(比如非合法 yaml)归为无 finding,不报错
	}
	f := &Finding{ConfigCenter: configCenter, SourceFile: relPath}
	var hit bool
	switch configCenter {
	case CenterNacos:
		hit = walkForNacos(root, f)
	case CenterApollo:
		hit = walkForApollo(root, "", f)
	case CenterConsul:
		hit = walkForConsul(root, f)
	}
	if !hit {
		return nil, nil
	}
	return f, nil
}

func normalizeKey(k string) string {
	k = strings.ToLower(k)
	k = strings.ReplaceAll(k, "-", "")
	k = strings.ReplaceAll(k, "_", "")
	return k
}

// ---------- nacos ----------

func walkForNacos(v any, f *Finding) bool {
	hit := false
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch normalizeKey(k) {
			case "dataid":
				if s, ok := val.(string); ok && s != "" {
					f.DataID = s
					hit = true
				}
			case "group":
				if s, ok := val.(string); ok && s != "" {
					f.Group = s
					hit = true
				}
			case "namespaceid", "namespace":
				if s, ok := val.(string); ok && s != "" {
					f.NamespaceID = s
					hit = true
				}
			case "serveraddr":
				if s, ok := val.(string); ok && s != "" {
					f.ServerAddr = s
					hit = true
				}
			}
			if walkForNacos(val, f) {
				hit = true
			}
		}
	case []any:
		for _, item := range x {
			if walkForNacos(item, f) {
				hit = true
			}
		}
	}
	return hit
}

// ---------- apollo ----------

// Apollo 典型配置项：
//
//	app.id: xxx                   （可能在顶层 app: {id: xxx}）
//	apollo.meta: http://...       （server addr）
//	apollo.bootstrap.enabled: true
//	apollo.bootstrap.namespaces: application,foo.yaml
//	apollo.cluster: default
func walkForApollo(v any, parent string, f *Finding) bool {
	hit := false
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			nk := normalizeKey(k)
			switch nk {
			case "appid":
				if s, ok := val.(string); ok && s != "" {
					f.AppID = s
					hit = true
				}
			case "id":
				// `app: { id: xxx }` 形式
				if parent == "app" {
					if s, ok := val.(string); ok && s != "" {
						f.AppID = s
						hit = true
					}
				}
			case "meta":
				if s, ok := val.(string); ok && s != "" && strings.Contains(strings.ToLower(s), "http") {
					f.ServerAddr = s
					hit = true
				}
			case "namespaces":
				if s, ok := val.(string); ok && s != "" {
					f.Namespaces = splitCSV(s)
					hit = true
				}
				if arr, ok := val.([]any); ok {
					for _, item := range arr {
						if s, ok := item.(string); ok && s != "" {
							f.Namespaces = append(f.Namespaces, s)
						}
					}
					if len(f.Namespaces) > 0 {
						hit = true
					}
				}
			case "cluster":
				if s, ok := val.(string); ok && s != "" {
					f.Cluster = s
					hit = true
				}
			}
			if walkForApollo(val, nk, f) {
				hit = true
			}
		}
	case []any:
		for _, item := range x {
			if walkForApollo(item, parent, f) {
				hit = true
			}
		}
	}
	return hit
}

// ---------- consul ----------

// Spring Cloud Consul：
//
//	spring.cloud.consul.host: consul-host
//	spring.cloud.consul.port: 8500
//	spring.cloud.consul.config.prefix: config
//	spring.cloud.consul.config.default-context: application
func walkForConsul(v any, f *Finding) bool {
	hit := false
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch normalizeKey(k) {
			case "host":
				// 仅当上下文是 consul 时才算；这里用简单策略：如果下面还有 port，在深层匹配时整合
				if s, ok := val.(string); ok && s != "" {
					// 保存为候选 ServerAddr；若同级有 port 则合并
					if f.ServerAddr == "" {
						f.ServerAddr = s
					}
				}
			case "prefix":
				if s, ok := val.(string); ok && s != "" {
					f.KVPrefix = s
					hit = true
				}
			case "defaultcontext":
				if s, ok := val.(string); ok && s != "" {
					f.DefaultContext = s
					hit = true
				}
			case "serveraddr":
				if s, ok := val.(string); ok && s != "" {
					f.ServerAddr = s
					hit = true
				}
			}
			if walkForConsul(val, f) {
				hit = true
			}
		}
	case []any:
		for _, item := range x {
			if walkForConsul(item, f) {
				hit = true
			}
		}
	}
	return hit
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
