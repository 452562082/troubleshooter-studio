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
	w.setCurrent(a)

	w.printf("欢迎使用 troubleshooter-studio init 向导\n")
	w.printf("将引导生成一份可用的 troubleshooter.yaml，大多数问题有默认值（回车接受）\n")

	d := w.Defaults // 可能为 nil；下面用 defaultOr() 统一兼容

	// 1) 系统身份
	w.section("1/9 系统基本信息")
	for {
		def := "my-system"
		if d != nil {
			def = defaultOr(d.SystemID, def)
		}
		id, err := w.ask("系统短 id (小写，字母数字短横线)", def)
		if err != nil {
			return nil, err
		}
		if idRe.MatchString(id) {
			a.SystemID = id
			break
		}
		w.printf("    ! id 必须以字母或数字开头，只包含 [a-z0-9-]\n")
	}
	w.setCurrent(a)

	var err error
	sysNameDef := strings.ToUpper(a.SystemID[:1]) + a.SystemID[1:]
	if d != nil {
		sysNameDef = defaultOr(d.SystemName, sysNameDef)
	}
	a.SystemName, err = w.ask("系统显示名", sysNameDef)
	if err != nil {
		return nil, err
	}
	sysDescDef := ""
	if d != nil {
		sysDescDef = d.SystemDescription
	}
	a.SystemDescription, err = w.ask("系统描述 (可选)", sysDescDef)
	if err != nil {
		return nil, err
	}
	w.setCurrent(a)

	// 2) Agent
	w.section("2/9 机器人身份")
	defaultAgent := a.SystemName + "排障机器人"
	if d != nil {
		defaultAgent = defaultOr(d.AgentName, defaultAgent)
	}
	a.AgentName, err = w.ask("机器人显示名", defaultAgent)
	if err != nil {
		return nil, err
	}
	wsDef := a.AgentName
	if d != nil {
		wsDef = defaultOr(d.WorkspaceName, wsDef)
	}
	a.WorkspaceName, err = w.ask("工作区名称", wsDef)
	if err != nil {
		return nil, err
	}
	modelDef := "anthropic/claude-sonnet-4-6"
	if d != nil {
		modelDef = defaultOr(d.AgentModel, modelDef)
	}
	a.AgentModel, err = w.askModel(modelDef)
	if err != nil {
		return nil, err
	}
	w.setCurrent(a)

	// 3) 环境
	w.section("3/9 环境列表（输入空 id 结束；例：dev、test、prod）")
	// -i 预填：列出已有 env，让用户选是否原样复用
	if d != nil && len(d.Envs) > 0 {
		labels := make([]string, len(d.Envs))
		for i, e := range d.Envs {
			labels[i] = e.ID
		}
		w.printf("  已从输入 yaml 载入 %d 个环境：%s\n", len(d.Envs), strings.Join(labels, ", "))
		reuse, err := w.askBool("  原样复用这些环境？", true)
		if err != nil {
			return nil, err
		}
		if reuse {
			a.Envs = append(a.Envs, d.Envs...)
			w.setCurrent(a)
			goto envsDone
		}
	}
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
		w.setCurrent(a)
	}
envsDone:
	if len(a.Envs) == 0 {
		return nil, fmt.Errorf("至少需要 1 个环境")
	}

	// 4) 仓库
	w.section("4/9 代码仓库（空 name 结束）")
	if d != nil && len(d.Repos) > 0 {
		labels := make([]string, len(d.Repos))
		for i, r := range d.Repos {
			labels[i] = r.Name
		}
		w.printf("  已从输入 yaml 载入 %d 个仓库：%s\n", len(d.Repos), strings.Join(labels, ", "))
		reuse, err := w.askBool("  原样复用这些仓库？", true)
		if err != nil {
			return nil, err
		}
		if reuse {
			a.Repos = append(a.Repos, d.Repos...)
			w.setCurrent(a)
			goto reposDone
		}
	}
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
			Name: name, URL: url, Stack: stack, Framework: framework,
			ServiceNames: services, EnvBranches: branches,
		})
		w.setCurrent(a)
	}
reposDone:

	// 5) 配置中心
	w.section("5/9 配置中心")
	ccDef := "nacos"
	if d != nil {
		ccDef = defaultOr(d.ConfigCenterType, ccDef)
	}
	a.ConfigCenterType, err = w.askChoice("类型", []string{"nacos", "apollo", "consul", "env-vars", "kuboard", "one2all", "none"}, ccDef)
	if err != nil {
		return nil, err
	}
	w.setCurrent(a)

	// 6) 可观测性
	w.section("6/9 可观测性")
	grafDef, lokiDef, promDef := true, true, true
	if d != nil {
		grafDef, lokiDef, promDef = d.GrafanaEnabled, d.LokiEnabled, d.PrometheusEnabled
	}
	a.GrafanaEnabled, err = w.askBool("启用 Grafana", grafDef)
	if err != nil {
		return nil, err
	}
	a.LokiEnabled, err = w.askBool("启用 Loki", lokiDef)
	if err != nil {
		return nil, err
	}
	a.PrometheusEnabled, err = w.askBool("启用 Prometheus", promDef)
	if err != nil {
		return nil, err
	}
	w.setCurrent(a)

	// 7) 数据层
	w.section("7/9 数据层 runtime-query skills")
	defaults := map[string]bool{"redis": true, "mongodb": true, "elasticsearch": true, "mysql": false, "doris": false, "kafka": false}
	for _, typ := range []string{"redis", "mongodb", "elasticsearch", "mysql", "doris", "kafka"} {
		def := defaults[typ]
		if d != nil {
			if v, ok := d.DataStoresEnabled[typ]; ok {
				def = v
			}
		}
		on, err := w.askBool("启用 "+typ, def)
		if err != nil {
			return nil, err
		}
		a.DataStoresEnabled[typ] = on
	}
	w.setCurrent(a)

	// 8) 消息通道
	w.section("8/9 消息通道")
	larkDef := true
	larkAttDef := true
	if d != nil {
		larkDef = d.LarkEnabled
		larkAttDef = d.LarkAttachment
	}
	a.LarkEnabled, err = w.askBool("启用 Lark", larkDef)
	if err != nil {
		return nil, err
	}
	if a.LarkEnabled {
		a.LarkAttachment, err = w.askBool("附件发送走 Lark", larkAttDef)
		if err != nil {
			return nil, err
		}
	}

	// 9) 项目管理
	w.section("9/9 项目管理")
	fpDef := false
	if d != nil {
		fpDef = d.FeishuProjectEnabled
	}
	a.FeishuProjectEnabled, err = w.askBool("启用 Feishu Project(⚠ 实验性,目前 mcp 包是字节内部 prototype 且无 SKILL 接入,选 Y 也不会真接入,等正式版)", fpDef)
	if err != nil {
		return nil, err
	}
	w.setCurrent(a)

	// 输出目标格式（一次选多种，回车=全部 4 种）
	w.section("输出")
	tgtDef := ""
	if d != nil && len(d.Targets) > 0 {
		tgtDef = strings.Join(d.Targets, " ")
	}
	targetsRaw, err := w.ask(
		"输出目标 [openclaw/claude-code/cursor,空格分隔,回车=全部]",
		tgtDef)
	if err != nil {
		return nil, err
	}
	a.Targets = parseTargets(targetsRaw)
	w.setCurrent(a)

	return a, nil
}

// parseTargets 把用户输入的 "openclaw claude-code" / "openclaw, cursor" / ""
// 解析成合法 target 列表；空输入 = 全部 3 种；未知 token 忽略并在 UI 层由 ask 流程已打印提示。
func parseTargets(raw string) []string {
	valid := map[string]bool{"openclaw": true, "claude-code": true, "cursor": true}
	order := []string{"openclaw", "claude-code", "cursor"}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return append([]string{}, order...)
	}
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.FieldsFunc(raw, func(r rune) bool { return r == ' ' || r == ',' || r == ';' }) {
		tok = strings.ToLower(strings.TrimSpace(tok))
		if valid[tok] && !seen[tok] {
			out = append(out, tok)
			seen[tok] = true
		}
	}
	if len(out) == 0 {
		return []string{"openclaw"}
	}
	return out
}
