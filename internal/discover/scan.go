package discover

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Scan 在给定目录里寻找 tshoot.json 锚点。每个命中 = 一个已安装机器人。
//
// 搜索策略：
//   - roots 传入的是候选根目录（例如 ~/.openclaw/workspace/、指定项目根）
//   - 每个 root 下最多下探 2 层寻找 tshoot.json，避免把用户硬盘全扫一遍
//   - 同一个 Meta.SystemID + Target 的机器人在 roots 里出现多次，只保留第一条
//
// 返回结果按 Meta.SystemID 稳定排序。
func Scan(roots []string) ([]DiscoveredAgent, error) {
	seen := map[string]bool{} // systemID|target → 去重 key
	// 初始化成空切片(不是 nil),确保 JSON 编码出 [] 而不是 null。
	// 否则前端 `bots.value = await DiscoverBots()` 会拿到 null,后续 .length 访问崩溃。
	out := []DiscoveredAgent{}

	// 历史曾用 systemLocateAgents() 走 mdfind / locate 全盘扫,但误报太多:
	// Studio 自家 dist/staging 输出 / examples/ / 用户老备份 / Time Machine 镜像
	// 都会被命中。卸了真 bot 还能搜到一堆 staging tshoot.json,造成"卸载没生效"假象。
	// 改回纯 roots 扫描:DefaultRoots 里 4 个真实部署根 + 用户显式传入的 extraRoots。
	for _, root := range roots {
		root = expandHome(root)
		if _, err := os.Stat(root); err != nil {
			continue // 不存在的 root 静默跳过
		}
		agents, err := scanOne(root, 2)
		if err != nil {
			return nil, err
		}
		for _, a := range agents {
			key := a.Meta.SystemID + "|" + a.Meta.Target
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, a)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Meta.SystemID != out[j].Meta.SystemID {
			return out[i].Meta.SystemID < out[j].Meta.SystemID
		}
		return out[i].Meta.Target < out[j].Meta.Target
	})
	return out, nil
}

// WorkDirFor 返回 Apply / 重 gen / 卸载 实际写入产物的"工作目录"。跟 ag.Path("UI 显示用的
// 真实部署位置")可能不同 —— Claude Code/Cursor 走"staging 中间包 → InstallNative 拷到真实
// 位置"两段式部署,所以工作目录是 staging 而非真实位置。OpenClaw 单段式,工作目录==真实位置。
//
// staging 路径约定:`<HOME>/.tshoot/<target>/<system_id>/`(跟 cmd/tshoot-desktop/bindings_apply.go
// ::DefaultDestPath 对齐)。
func WorkDirFor(ag DiscoveredAgent) string {
	switch ag.Meta.Target {
	case "claude-code", "cursor", "codex":
		if ag.Meta.SystemID == "" {
			return ag.Path // 异常 fallback,跟老行为一致
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return ag.Path
		}
		return filepath.Join(home, ".tshoot", ag.Meta.Target, ag.Meta.SystemID)
	}
	return ag.Path
}

// DefaultRoots 返回 discover 默认扫描的位置 —— 全是"真实部署位置":
//   - ~/.openclaw/workspace/    OpenClaw 真实部署根
//   - ~/.claude/skills/         Claude Code 真实部署根(每个 agent 一个 skills/<name> 子目录,
//     里面有 InstallNative 写进去的 tshoot.json 锚点)
//   - ~/.cursor/skills/         Cursor 真实部署根,同上
//   - ~/.codex/skills/          OpenAI Codex CLI 真实部署根,同上
//
// **不扫 CWD**:历史版本曾把当前工作目录加入扫描根,理由是"claude-code/cursor/codex
// 也常装项目根"。但实际从未支持过项目级安装,这条只会误扫:Studio 自家 dist/staging 产物
// 会被错认成已装 bot;用户在仓库根跑 desktop app,卸载真 bot 后那条 staging 误扫还在,
// 看起来"卸载不生效"。CLI 真要扫自定义目录传 --roots 即可。
//
// 判断"已装"的标准统一为"真实部署位置存在 tshoot.json"。staging 中间包(~/.tshoot/<target>/)
// 不再扫 —— 它只是 wizard 部署中途的临时落盘,装完成后真实位置才有锚点;扫 staging
// 会把"半装 / 失败残留 / 用户重置后"显示成"已装",误导。用户老版本残留(~/.tshoot/<target>/
// 下还有 tshoot.json)可以手动加扫描路径。
//
// scanOne 最深下探 2 层(见调用 `scanOne(root, 2)`),刚好够 ~/.claude/skills/<name>/tshoot.json
// 这种"root + 1 层子目录 + tshoot.json 文件" 的结构。
func DefaultRoots() []string {
	return []string{
		"~/.openclaw/workspace",
		"~/.claude/skills",
		"~/.cursor/skills",
		"~/.codex/skills",
	}
}

// scanOne 从 root 开始 BFS 寻找 tshoot.json，最深 maxDepth 层（root 算 0）。
func scanOne(root string, maxDepth int) ([]DiscoveredAgent, error) {
	var out []DiscoveredAgent
	rootAbs, _ := filepath.Abs(root)
	rootParts := len(filepath.SplitList(rootAbs))
	_ = rootParts

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // 扫用户目录遇权限拒绝静默跳过,而非让整个 scan 失败
		}
		rel, _ := filepath.Rel(root, p)
		depth := depthOf(rel)
		if d.IsDir() {
			if depth > maxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != MetaFilename {
			return nil
		}
		a, err := readAgent(p)
		if err == nil {
			out = append(out, a)
		}
		// 进入同级其他文件继续扫
		return nil
	})
	return out, err
}

// readAgent 读并解析单个 tshoot.json，派生统计字段。
func readAgent(metaPath string) (DiscoveredAgent, error) {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return DiscoveredAgent{}, err
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return DiscoveredAgent{}, err
	}
	NormalizeMetaForPath(&meta, metaPath)
	if meta.SystemID == "" || meta.Target == "" {
		return DiscoveredAgent{}, os.ErrInvalid // 无效元数据（不是 tshoot 生成的）
	}
	info, _ := os.Stat(metaPath)
	// agent.Path = 真实部署位置(tshoot.json 实际所在目录),给 UI 卡片显示 + 用户感知"我的机器人在哪"。
	//   OpenClaw → ~/.openclaw/workspace/<id>/
	//   Claude Code → ~/.claude/skills/<name>/
	//   Cursor → ~/.cursor/skills/<name>/
	//
	// Apply / 重 gen / 卸载 内部用 WorkDirFor(agent) 反推 staging 工作目录(Claude Code/Cursor
	// 走两段式 staging 中间包 → InstallNative 拷到真实位置;OpenClaw deploy=staging 同一目录)。
	a := DiscoveredAgent{
		Meta: meta,
		Path: filepath.Dir(metaPath),
	}
	if info != nil {
		a.ModTime = info.ModTime().UTC().Format("2006-01-02 15:04:05Z")
	}
	// 从 embedded troubleshooter.yaml 快速派生统计
	if meta.TroubleshooterYAML != "" {
		derive(&a, meta.TroubleshooterYAML)
	}
	return a, nil
}

func normalizeMeta(meta *Meta) {
	if meta == nil {
		return
	}
	if strings.TrimSpace(meta.Role) == "" {
		meta.Role = RoleTroubleshooter
	}
	if strings.TrimSpace(meta.AgentID) == "" && strings.TrimSpace(meta.SystemID) != "" {
		meta.AgentID = meta.SystemID + "-" + meta.Role
	}
	if len(meta.InternalAgents) == 0 && strings.TrimSpace(meta.AgentID) != "" {
		meta.InternalAgents = []InternalAgent{{ID: meta.AgentID, Role: meta.Role}}
	}
}

// NormalizeMetaForPath applies schema defaults and, for IDE installs, backfills
// internal_agents from sibling agents/skills directories. This keeps old
// tshoot.json anchors usable after the single-bot/multi-agent layout change.
func NormalizeMetaForPath(meta *Meta, metaPath string) {
	normalizeMeta(meta)
	inferInternalAgentsFromIDELayout(meta, metaPath)
}

func inferInternalAgentsFromIDELayout(meta *Meta, metaPath string) {
	if meta == nil || len(meta.InternalAgents) > 1 {
		return
	}
	ext := ""
	switch meta.Target {
	case "claude-code", "cursor":
		ext = ".md"
	case "codex":
		ext = ".toml"
	default:
		return
	}
	botRoot := filepath.Dir(metaPath)
	skillsRoot := filepath.Dir(botRoot)
	if filepath.Base(skillsRoot) != "skills" {
		return
	}
	platformRoot := filepath.Dir(skillsRoot)
	ids := map[string]string{}
	add := func(id, role string) {
		id = strings.TrimSpace(id)
		if id == "" || !belongsToMetaAgent(meta, id) {
			return
		}
		if role = strings.TrimSpace(role); role == "" {
			role = inferRoleFromAgentID(meta, id)
		}
		if _, ok := ids[id]; !ok {
			ids[id] = role
		}
	}
	add(meta.AgentID, meta.Role)
	for _, ag := range meta.InternalAgents {
		add(ag.ID, ag.Role)
	}
	if entries, err := os.ReadDir(filepath.Join(platformRoot, "agents")); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || strings.Contains(name, ".bak.") || !strings.HasSuffix(name, ext) {
				continue
			}
			add(strings.TrimSuffix(name, ext), "")
		}
	}
	if entries, err := os.ReadDir(skillsRoot); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				add(e.Name(), "")
			}
		}
	}
	if len(ids) <= 1 {
		return
	}
	out := make([]InternalAgent, 0, len(ids))
	addOut := func(id string) {
		if role, ok := ids[id]; ok {
			out = append(out, InternalAgent{ID: id, Role: role})
			delete(ids, id)
		}
	}
	addOut(meta.AgentID)
	addOut(meta.SystemID + "-" + RoleTroubleshooter)
	addOut(meta.SystemID + "-" + RoleValidator)
	addOut(meta.SystemID + "-" + RoleFixer)
	rest := make([]string, 0, len(ids))
	for id := range ids {
		rest = append(rest, id)
	}
	sort.Strings(rest)
	for _, id := range rest {
		addOut(id)
	}
	meta.InternalAgents = out
}

func belongsToMetaAgent(meta *Meta, id string) bool {
	if id == strings.TrimSpace(meta.AgentID) {
		return true
	}
	systemID := strings.TrimSpace(meta.SystemID)
	return systemID != "" && strings.HasPrefix(id, systemID+"-")
}

func inferRoleFromAgentID(meta *Meta, id string) string {
	if id == strings.TrimSpace(meta.AgentID) && strings.TrimSpace(meta.Role) != "" {
		return meta.Role
	}
	lower := strings.ToLower(id)
	if strings.Contains(lower, "fix") || strings.Contains(lower, "repair") {
		return RoleFixer
	}
	if strings.Contains(lower, "valid") || strings.Contains(lower, "verif") {
		return RoleValidator
	}
	return RoleTroubleshooter
}

type yamlProbe struct {
	Environments []struct{ ID string }   `yaml:"environments"`
	Repos        []struct{ Name string } `yaml:"repos"`
	Generation   struct {
		SkillsWhitelist []string `yaml:"skills_whitelist"`
		Targets         []string `yaml:"targets"`
	} `yaml:"generation"`
}

func derive(a *DiscoveredAgent, yamlSrc string) {
	var p yamlProbe
	if err := yaml.Unmarshal([]byte(yamlSrc), &p); err != nil {
		return
	}
	a.EnvCount = len(p.Environments)
	a.Environments = make([]string, 0, len(p.Environments))
	for _, env := range p.Environments {
		if env.ID != "" {
			a.Environments = append(a.Environments, env.ID)
		}
	}
	a.RepoCount = len(p.Repos)
	a.SkillCount = len(p.Generation.SkillsWhitelist)
	a.Targets = p.Generation.Targets
}

// depthOf 返回相对路径的层级深度；"." = 0，"a" = 0(文件在 root)，"a/b" = 1。
func depthOf(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	n := 0
	for _, c := range rel {
		if c == filepath.Separator {
			n++
		}
	}
	return n
}

func expandHome(p string) string {
	if len(p) == 0 || p[0] != '~' {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}
