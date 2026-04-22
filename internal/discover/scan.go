package discover

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Scan 在给定目录里寻找 .tshoot.json 锚点。每个命中 = 一个已安装机器人。
//
// 搜索策略：
//   - roots 传入的是候选根目录（例如 ~/.openclaw/workspace/、指定项目根）
//   - 每个 root 下最多下探 2 层寻找 .tshoot.json，避免把用户硬盘全扫一遍
//   - 同一个 Meta.SystemID + Target 的机器人在 roots 里出现多次，只保留第一条
//
// 返回结果按 Meta.SystemID 稳定排序。
func Scan(roots []string) ([]DiscoveredAgent, error) {
	seen := map[string]bool{} // systemID|target → 去重 key
	// 初始化成空切片(不是 nil),确保 JSON 编码出 [] 而不是 null。
	// 否则前端 `bots.value = await DiscoverBots()` 会拿到 null,后续 .length 访问崩溃。
	out := []DiscoveredAgent{}

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

// DefaultRoots 返回 discover 默认扫描的位置：
//   - ~/.openclaw/workspace/（OpenClaw 装完的机器人工作区；每个 agent 一个子目录）
//   - CWD（claude-code / cursor / standalone 常直接装在项目根）
func DefaultRoots() []string {
	roots := []string{"~/.openclaw/workspace"}
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, wd)
	}
	return roots
}

// scanOne 从 root 开始 BFS 寻找 .tshoot.json，最深 maxDepth 层（root 算 0）。
func scanOne(root string, maxDepth int) ([]DiscoveredAgent, error) {
	var out []DiscoveredAgent
	rootAbs, _ := filepath.Abs(root)
	rootParts := len(filepath.SplitList(rootAbs))
	_ = rootParts

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // 权限错等静默跳过
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

// readAgent 读并解析单个 .tshoot.json，派生统计字段。
func readAgent(metaPath string) (DiscoveredAgent, error) {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return DiscoveredAgent{}, err
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return DiscoveredAgent{}, err
	}
	if meta.SystemID == "" || meta.Target == "" {
		return DiscoveredAgent{}, os.ErrInvalid // 无效元数据（不是 tshoot 生成的）
	}
	info, _ := os.Stat(metaPath)
	a := DiscoveredAgent{
		Meta: meta,
		Path: filepath.Dir(metaPath),
	}
	if info != nil {
		a.ModTime = info.ModTime().UTC().Format("2006-01-02 15:04:05Z")
	}
	// 从 embedded system.yaml 快速派生统计
	if meta.SystemYAML != "" {
		derive(&a, meta.SystemYAML)
	}
	return a, nil
}

type yamlProbe struct {
	Environments []struct{ ID string } `yaml:"environments"`
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
