package analyzer

import (
	"os"
	"strings"
)

// ScanProperties 按 config-center 类型从 .properties 抽取 finding
func ScanProperties(absPath, relPath, configCenter string) (*Finding, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	f := &Finding{ConfigCenter: configCenter, SourceFile: relPath}
	hit := false
	for _, raw := range strings.Split(string(data), "\n") {
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
		if val == "" {
			continue
		}
		switch configCenter {
		case CenterNacos:
			switch {
			case strings.HasSuffix(key, ".data-id") || strings.HasSuffix(key, ".dataid"):
				f.DataID = val
				hit = true
			case strings.HasSuffix(key, ".group"):
				f.Group = val
				hit = true
			case strings.HasSuffix(key, ".namespace"):
				f.NamespaceID = val
				hit = true
			case strings.HasSuffix(key, ".server-addr") || strings.HasSuffix(key, ".serveraddr"):
				f.ServerAddr = val
				hit = true
			}
		case CenterApollo:
			switch key {
			case "app.id":
				f.AppID = val
				hit = true
			case "apollo.meta":
				f.ServerAddr = val
				hit = true
			case "apollo.bootstrap.namespaces":
				f.Namespaces = splitCSV(val)
				hit = true
			case "apollo.cluster":
				f.Cluster = val
				hit = true
			}
		case CenterConsul:
			switch {
			case strings.HasSuffix(key, "spring.cloud.consul.host"):
				f.ServerAddr = val
				hit = true
			case strings.HasSuffix(key, "spring.cloud.consul.config.prefix"):
				f.KVPrefix = val
				hit = true
			case strings.HasSuffix(key, "spring.cloud.consul.config.default-context"):
				f.DefaultContext = val
				hit = true
			}
		}
	}
	if !hit {
		return nil, nil
	}
	return f, nil
}
