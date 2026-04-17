package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var envProfileRegex = regexp.MustCompile(`\.env[._-](dev|development|test|staging|stg|prod|production|pre|uat)`)

// ScanDotEnv 解析 .env / .env.example / .env.production 等文件
// 抽取配置中心相关字段（NACOS_*/APOLLO_*/CONSUL_* 环境变量）
func ScanDotEnv(absPath, relPath, configCenter string) (*Finding, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	f := &Finding{ConfigCenter: configCenter, SourceFile: relPath}
	hit := false

	kv := parseDotEnv(string(data))

	switch configCenter {
	case CenterNacos:
		if v := firstOf(kv, "NACOS_ADDR", "NACOS_SERVER_ADDR"); v != "" {
			f.ServerAddr = v
			hit = true
		}
		if v := firstOf(kv, "NACOS_NAMESPACE", "NACOS_NAMESPACE_ID"); v != "" {
			f.NamespaceID = v
			hit = true
		}
		if v := firstOf(kv, "NACOS_GROUP"); v != "" {
			f.Group = v
			hit = true
		}
		if v := firstOf(kv, "NACOS_DATA_ID"); v != "" {
			f.DataID = v
			hit = true
		}
	case CenterApollo:
		if v := firstOf(kv, "APP_ID", "APOLLO_APP_ID"); v != "" {
			f.AppID = v
			hit = true
		}
		if v := firstOf(kv, "APOLLO_META"); v != "" {
			f.ServerAddr = v
			hit = true
		}
		if v := firstOf(kv, "APOLLO_CLUSTER"); v != "" {
			f.Cluster = v
			hit = true
		}
		if v := firstOf(kv, "APOLLO_NAMESPACE", "APOLLO_NAMESPACES"); v != "" {
			f.Namespaces = splitCSV(v)
			hit = true
		}
	case CenterConsul:
		if v := firstOf(kv, "CONSUL_HTTP_ADDR", "CONSUL_ADDR", "CONSUL_HOST"); v != "" {
			f.ServerAddr = v
			hit = true
		}
		if v := firstOf(kv, "CONSUL_HTTP_TOKEN", "CONSUL_TOKEN"); v != "" {
			// token 不存 finding 里（脱敏），但 hit=true 代表有配置
			_ = v
			hit = true
		}
		if v := firstOf(kv, "CONSUL_KV_PREFIX"); v != "" {
			f.KVPrefix = v
			hit = true
		}
	}

	if !hit {
		return nil, nil
	}

	// 从文件名推断 env profile
	base := strings.ToLower(filepath.Base(relPath))
	if m := envProfileRegex.FindStringSubmatch(base); len(m) == 2 {
		prof := m[1]
		switch prof {
		case "development":
			prof = "dev"
		case "production":
			prof = "prod"
		}
		f.EnvProfile = prof
	}
	return f, nil
}

func parseDotEnv(content string) map[string]string {
	kv := map[string]string{}
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		// 去引号
		val = strings.Trim(val, `"'`)
		if key != "" && val != "" {
			kv[key] = val
		}
	}
	return kv
}

func firstOf(kv map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := kv[k]; ok && v != "" {
			return v
		}
	}
	return ""
}
