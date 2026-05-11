package agent

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// TestDumpMCPSpec_AllFamilies 打印一个全家桶 cfg 派生出来的 mcpServers JSON,
// 用 `go test ./internal/agent/ -run DumpMCPSpec -v` 跑;不写到磁盘,只打 stdout
// 让人肉眼复核 env 名 / args 形态 / 占位字段 是否跟各上游 README 对得上。
//
// 失败=不会断言,只 t.Logf;主要用于"实测验证 13 家 MCP 配置"这种一次性 audit。
func TestDumpMCPSpec_AllFamilies(t *testing.T) {
	cfg := &config.SystemConfig{
		Environments: []config.Environment{{ID: "dev"}},
		Infrastructure: config.Infrastructure{
			ConfigCenters: []config.ConfigCenter{{Type: "nacos", ID: "primary"}},
			Observability: config.Observability{
				Grafana: config.Grafana{Enabled: true},
				Loki:    config.Loki{Enabled: true},
				Jaeger:  config.Jaeger{Enabled: true},
				ELK:     config.ELK{Enabled: true},
			},
			DataStores: []config.DataStore{
				{Type: "mongodb", Enabled: true},
				{Type: "postgresql", Enabled: true},
				{Type: "redis", Enabled: true},
				{Type: "elasticsearch", Enabled: true},
				{Type: "mysql", Enabled: true},
				{Type: "clickhouse", Enabled: true},
			},
			Messaging:       []config.Messaging{{Platform: "lark", Enabled: true}},
			ProjectTracking: []config.ProjectTracking{{Platform: "feishu_project", Enabled: true}},
		},
	}
	creds := map[string]string{
		// CC_* 是 install_prompts 派生的规范名(envVar 里 sourceID="primary" → CC_*_PRIMARY_<ENV>)
		"CC_ADDR_PRIMARY_DEV": "nacos:8848",
		"CC_USER_PRIMARY_DEV": "u",
		"CC_PASS_PRIMARY_DEV": "p",
		"GRAFANA_URL_DEV":     "http://g:3000",
		"GRAFANA_API_KEY_DEV": "glsa_xxx",
		"JAEGER_URL_DEV":      "http://j:16686",
		"ELK_ES_URL_DEV":      "http://elk-es:9200",
		"ELK_USERNAME":        "elastic",
		"ELK_PASSWORD":        "espw",
		"MONGODB_URI_DEV":     "mongodb://u:p@m:27017/biz",
		"POSTGRES_DSN_DEV":    "postgres://u:p@pg/db",
		"REDIS_URL_DEV":       "redis://r:6379/0",
		"ES_URL_DEV":          "http://es:9200",
		"ES_USER_DEV":         "elastic",
		"ES_PASS_DEV":         "ds-espw",
		"MYSQL_DSN_DEV":       "u:p@tcp(my:3306)/db",
		"CLICKHOUSE_URL_DEV":  "https://chu:chpw@ch:8443/an",
		"LARK_APP_ID":         "appid",
		"LARK_APP_SECRET":     "appsec",
		"LARK_DOMAIN":         "https://open.larksuite.com",
		"MCP_USER_TOKEN":      "feishu-tok",
	}
	servers := BuildMCPServers(cfg, MCPBuildOptions{AgentID: "bot", PruneEmpty: true},
		func(k string) string { return creds[k] })

	// 排序后 pretty-print,方便 diff / 复核
	keys := make([]string, 0, len(servers))
	for k := range servers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	t.Logf("BuildMCPServers 全家桶输出 (%d 家):", len(servers))
	for _, k := range keys {
		spec, _ := servers[k].(map[string]any)
		j, _ := json.MarshalIndent(spec, "  ", "  ")
		t.Logf("\n[%s]\n  %s", k, j)
	}
}
