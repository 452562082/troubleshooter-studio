package initwizard

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// 顺序追踪：answers 按 Run() 提问顺序给，空串 = 接受默认
// 1) 系统 id / 2) 系统显示名 / 3) 描述 /
// 4) agent 名 / 5) workspace / 6) model /
// 7+ 环境循环（id→domain→isProd） 直到空 id /
// 仓库循环 / 配置中心 / 可观测 / 数据层 / 消息 / 项目管理 / 输出目录
func script(lines ...string) io.Reader {
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func TestRun_MinimalAllDefaults(t *testing.T) {
	in := script(
		"shop",      // system id
		"Shop",      // name
		"",          // description
		"",          // agent name
		"",          // workspace
		"",          // model
		"dev",       // env 1 id
		"api-dev.x", // domain
		"",          // is_prod default (n, since id != "prod")
		"prod",      // env 2 id
		"api.x",     // domain
		"",          // is_prod (default y since id == "prod")
		"",          // env 3 empty → end
		"",          // repo 1 empty → end
		"",          // config center type → default nacos
		"",          // grafana default y
		"",          // loki default y
		"",          // prometheus default y
		"",          // redis default y
		"",          // mongodb default y
		"",          // elasticsearch default y
		"",          // mysql default n
		"",          // kafka default n
		"",          // lark default y
		"",          // lark attachment default y
		"",          // feishu project default n
		"",          // output_dir default ./dist/shop
		"",          // targets default (all 4)
	)
	var out bytes.Buffer
	w := New(in, &out)
	ans, err := w.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ans.SystemID != "shop" {
		t.Errorf("SystemID: got %q", ans.SystemID)
	}
	if ans.AgentName != "Shop排障机器人" {
		t.Errorf("AgentName default: got %q", ans.AgentName)
	}
	if len(ans.Envs) != 2 || ans.Envs[0].ID != "dev" || ans.Envs[1].ID != "prod" {
		t.Errorf("Envs: %+v", ans.Envs)
	}
	if !ans.Envs[1].IsProd {
		t.Errorf("prod env should default is_prod=true")
	}
	if ans.ConfigCenterType != "nacos" {
		t.Errorf("ConfigCenterType: got %q", ans.ConfigCenterType)
	}
	if !ans.GrafanaEnabled || !ans.LokiEnabled {
		t.Errorf("observability defaults expected on")
	}
	if !ans.DataStoresEnabled["redis"] || ans.DataStoresEnabled["mysql"] {
		t.Errorf("data store defaults wrong: %+v", ans.DataStoresEnabled)
	}
}

func TestRun_FullWithOneRepo(t *testing.T) {
	in := script(
		"bank", "Bank", "银行中台",
		"", "", "",
		"dev", "api-dev.bank", "",
		"", // end envs (only 1 env)
		// repo 1
		"account-service",
		"git@github.com:bank/account-service.git",
		"backend",
		"java",
		"spring-boot",
		"account-service, account-worker",
		"develop",
		// end repos
		"",
		// config center
		"apollo",
		"", "", "", // grafana/loki/prom default y
		"", "", "", "", "", // data stores defaults
		"n", // lark disabled
		"",  // feishu project default n
		"",  // output_dir default
		"",  // targets default (all 4)
	)
	var out bytes.Buffer
	ans, err := New(in, &out).Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ans.Repos) != 1 {
		t.Fatalf("repos count: %d", len(ans.Repos))
	}
	r := ans.Repos[0]
	if r.Name != "account-service" || r.Stack != "java" || r.Framework != "spring-boot" {
		t.Errorf("repo: %+v", r)
	}
	if len(r.ServiceNames) != 2 {
		t.Errorf("service_names: %v", r.ServiceNames)
	}
	if ans.ConfigCenterType != "apollo" {
		t.Errorf("ConfigCenter: got %q", ans.ConfigCenterType)
	}
	if ans.LarkEnabled {
		t.Errorf("lark should be disabled")
	}
}

// WriteYAML 产物应能被 config.Load 解析并通过校验
func TestWriteYAML_ProducesValidTroubleshooterYaml(t *testing.T) {
	in := script(
		"demo", "Demo", "",
		"", "", "",
		"dev", "api-dev.demo", "",
		"prod", "api.demo", "",
		"",
		"svc", "git@x:y.git", "backend", "go", "", "",
		"develop", "main",
		"",
		"nacos",
		"", "", "",
		"", "", "", "", "",
		"",
		"",
		"",
		"",
		"", // targets default (all 4)
	)
	var out bytes.Buffer
	ans, err := New(in, &out).Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "troubleshooter.yaml")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ans.WriteYAML(f); err != nil {
		t.Fatalf("WriteYAML: %v", err)
	}
	f.Close()

	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("load generated yaml: %v", err)
	}
	if cfg.System.ID != "demo" {
		t.Errorf("round-trip system.id: got %q", cfg.System.ID)
	}
	if len(cfg.Environments) != 2 {
		t.Errorf("round-trip envs: got %d", len(cfg.Environments))
	}
	if len(cfg.Repos) != 1 {
		t.Errorf("round-trip repos: got %d", len(cfg.Repos))
	}
	if cfg.Infrastructure.ConfigCenter.Type != "nacos" {
		t.Errorf("round-trip config center: got %q", cfg.Infrastructure.ConfigCenter.Type)
	}
}

func TestRun_BadIDReprompts(t *testing.T) {
	in := script(
		"BadID!",   // invalid → re-prompt
		"good-id",  // valid
		"Good", "", // name, description
		"", "", "", // agent
		"dev", "api", "",
		"",         // envs end
		"",         // repos end
		"none",     // config center
		"", "", "", // obs defaults
		"", "", "", "", "", // data stores
		"", // lark enabled default
		"", // lark attachment default
		"", // feishu default
		"", // output dir default
		"", // targets default (all 4)
	)
	var out bytes.Buffer
	ans, err := New(in, &out).Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ans.SystemID != "good-id" {
		t.Errorf("expected reprompt to accept good-id, got %q", ans.SystemID)
	}
}
