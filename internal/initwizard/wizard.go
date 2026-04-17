package initwizard

import (
	"fmt"
	"regexp"
	"strings"
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Run 跑完所有交互，返回 Answers；任何步骤 EOF/错误中断返回 error
func (w *Wizard) Run() (*Answers, error) {
	a := &Answers{
		DataStoresEnabled: map[string]bool{},
	}

	w.printf("欢迎使用 troubleshooter-factory init 向导\n")
	w.printf("将引导生成一份可用的 system.yaml，大多数问题有默认值（回车接受）\n")

	// 1) 系统身份
	w.section("1/9 系统基本信息")
	for {
		id, err := w.ask("系统短 id (小写，字母数字短横线)", "my-system")
		if err != nil {
			return nil, err
		}
		if idRe.MatchString(id) {
			a.SystemID = id
			break
		}
		w.printf("    ! id 必须以字母或数字开头，只包含 [a-z0-9-]\n")
	}
	var err error
	a.SystemName, err = w.ask("系统显示名", strings.ToUpper(a.SystemID[:1])+a.SystemID[1:])
	if err != nil {
		return nil, err
	}
	a.SystemDescription, err = w.ask("系统描述 (可选)", "")
	if err != nil {
		return nil, err
	}

	// 2) Agent
	w.section("2/9 机器人身份")
	defaultAgent := a.SystemName + "排障机器人"
	a.AgentName, err = w.ask("机器人显示名", defaultAgent)
	if err != nil {
		return nil, err
	}
	a.WorkspaceName, err = w.ask("工作区名称", a.AgentName)
	if err != nil {
		return nil, err
	}
	a.AgentModel, err = w.ask("Agent 模型", "openai-codex/gpt-5.3-codex")
	if err != nil {
		return nil, err
	}

	// 3) 环境
	w.section("3/9 环境列表（输入空 id 结束；例：dev、test、prod）")
	for i := 0; ; i++ {
		id, err := w.ask(fmt.Sprintf("环境 #%d id", i+1), "")
		if err != nil {
			return nil, err
		}
		if id == "" {
			break
		}
		domain, err := w.ask("  API 域名", "")
		if err != nil {
			return nil, err
		}
		isProd, err := w.askBool("  是生产环境吗", id == "prod")
		if err != nil {
			return nil, err
		}
		a.Envs = append(a.Envs, EnvAnswer{ID: id, APIDomain: domain, IsProd: isProd})
	}
	if len(a.Envs) == 0 {
		return nil, fmt.Errorf("至少需要 1 个环境")
	}

	// 4) 仓库
	w.section("4/9 代码仓库（空 name 结束）")
	for i := 0; ; i++ {
		name, err := w.ask(fmt.Sprintf("仓库 #%d name", i+1), "")
		if err != nil {
			return nil, err
		}
		if name == "" {
			break
		}
		url, err := w.ask("  URL", "")
		if err != nil {
			return nil, err
		}
		role, err := w.askChoice("  角色", []string{"backend", "frontend", "infra", "shared", "gateway"}, "backend")
		if err != nil {
			return nil, err
		}
		stack, err := w.askChoice("  技术栈", []string{"go", "java", "node", "python", "rust"}, "go")
		if err != nil {
			return nil, err
		}
		framework, err := w.ask("  框架 (可选)", "")
		if err != nil {
			return nil, err
		}
		svcRaw, err := w.ask("  服务名（逗号分隔，留空=使用仓库名）", "")
		if err != nil {
			return nil, err
		}
		var services []string
		for _, s := range strings.Split(svcRaw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				services = append(services, s)
			}
		}
		branches := map[string]string{}
		w.printf("  各环境分支:\n")
		for _, e := range a.Envs {
			def := "main"
			if !e.IsProd {
				def = "develop"
			}
			b, err := w.ask(fmt.Sprintf("    %s 分支", e.ID), def)
			if err != nil {
				return nil, err
			}
			branches[e.ID] = b
		}
		a.Repos = append(a.Repos, RepoAnswer{
			Name: name, URL: url, Role: role, Stack: stack, Framework: framework,
			ServiceNames: services, EnvBranches: branches,
		})
	}

	// 5) 配置中心
	w.section("5/9 配置中心")
	a.ConfigCenterType, err = w.askChoice("类型", []string{"nacos", "apollo", "consul", "env-vars", "kubernetes", "none"}, "nacos")
	if err != nil {
		return nil, err
	}

	// 6) 可观测性
	w.section("6/9 可观测性")
	a.GrafanaEnabled, err = w.askBool("启用 Grafana", true)
	if err != nil {
		return nil, err
	}
	a.LokiEnabled, err = w.askBool("启用 Loki", true)
	if err != nil {
		return nil, err
	}
	a.PrometheusEnabled, err = w.askBool("启用 Prometheus", true)
	if err != nil {
		return nil, err
	}

	// 7) 数据层
	w.section("7/9 数据层 runtime-query skills")
	defaults := map[string]bool{"redis": true, "mongodb": true, "elasticsearch": true, "mysql": false, "kafka": false}
	for _, typ := range []string{"redis", "mongodb", "elasticsearch", "mysql", "kafka"} {
		on, err := w.askBool("启用 "+typ, defaults[typ])
		if err != nil {
			return nil, err
		}
		a.DataStoresEnabled[typ] = on
	}

	// 8) 消息通道
	w.section("8/9 消息通道")
	a.LarkEnabled, err = w.askBool("启用 Lark", true)
	if err != nil {
		return nil, err
	}
	if a.LarkEnabled {
		a.LarkAttachment, err = w.askBool("附件发送走 Lark", true)
		if err != nil {
			return nil, err
		}
	}

	// 9) 项目管理
	w.section("9/9 项目管理")
	a.FeishuProjectEnabled, err = w.askBool("启用 Feishu Project", false)
	if err != nil {
		return nil, err
	}

	// 输出目录
	w.section("输出")
	a.OutputDir, err = w.ask("生成包输出目录", "./dist/"+a.SystemID)
	if err != nil {
		return nil, err
	}

	return a, nil
}
