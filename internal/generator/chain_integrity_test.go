package generator

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// scriptRefRe 抓 SKILL.md 里的 python 脚本调用路径(两种约定):
//   - workspace 根相对:`python3 skills/<skill>/scripts/<f>.py`
//   - skill 目录相对:  `python3 scripts/<f>.py`(同 skill 自带脚本)
var scriptRefRe = regexp.MustCompile(`python3 +((?:skills|scripts)/[^\s'"` + "`" + `]+\.py)`)

// skillFileRefRe 抓 skill 文档里 workspace 根相对的静态文件路径。只匹配带扩展名的
// literal，避免把 skills/<skill>/ 说明性目录或含占位符的示例误当成真实文件。
var skillFileRefRe = regexp.MustCompile(`skills/[A-Za-z0-9._/-]+\.(?:md|yaml|py)`)

// TestSkillScriptPathsExist 渲染一份完整 fixture,遍历所有生成的 SKILL.md,
// 断言里头引用的每个 python 脚本路径在产物里真实存在。
//
// 护栏背景(2026-06):incident-investigator 的 Step 2/3 长期写 `scripts/timeline.py` /
// `scripts/k8s_query.py`,但这俩脚本在 recent-changes / k8s-runtime-query 的 scripts 目录,
// 不在 incident-investigator/scripts/ → 路径漂移,机器人按文档跑直接 file-not-found。
// 这类"文档 vs 实际布局脱节"是项目反复踩的坑(AGENTS.md 明示),self-test 只 probe MCP
// 没覆盖到 skill 脚本链 —— 本测试补上 CI 级守卫。
func TestSkillScriptPathsExist(t *testing.T) {
	// 场景 1:全 skill(whitelist 清空 → hasSkill 全 true)—— 覆盖 incident-investigator 跨
	// skill 引用 recent-changes/timeline.py + k8s-runtime-query/k8s_query.py + config-executor。
	t.Run("all-skills", func(t *testing.T) {
		cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
		cfg.Generation.SkillsWhitelist = nil
		assertSkillScriptPathsExist(t, cfg)
	})

	// 场景 2:含 incident-investigator 但砍掉 recent-changes + k8s-runtime-query —— 验证
	// hasSkill 守卫真生效:Step 2/3 走 else 分支,不会渲染指向未生成 skill 的破引用。
	t.Run("guarded-optional-skills-absent", func(t *testing.T) {
		cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
		cfg.Generation.SkillsWhitelist = []string{"routing", "incident-investigator", "config-executor", "diagram-generator"}
		assertSkillScriptPathsExist(t, cfg)
	})

	// 场景 3:CodeGraph opt-in skill 引用 routing 的三份静态映射；每条 literal 路径
	// 必须能在同一份生成 workspace 中解析，避免文档路径和产物布局漂移。
	t.Run("code-intelligence-static-paths", func(t *testing.T) {
		cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
		cfg.CodeIntelligence = config.CodeIntelligence{Enabled: true, Provider: "codegraph"}
		cfg.Generation.SkillsWhitelist = []string{"routing", "code-intelligence-query"}
		assertCodeIntelligenceFilePathsExist(t, cfg)
	})
}

func assertCodeIntelligenceFilePathsExist(t *testing.T, cfg *config.SystemConfig) {
	t.Helper()
	out := t.TempDir()
	if err := New(cfg, filepath.Join(projectRoot(t), "templates"), out).Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	wsRoot := filepath.Join(out, "templates/workspace-template")
	skillPath := filepath.Join(wsRoot, "skills/code-intelligence-query/SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read generated CodeGraph skill: %v", err)
	}

	refs := skillFileRefRe.FindAllString(string(data), -1)
	if len(refs) == 0 {
		t.Fatal("CodeGraph skill has no literal skills/... file references")
	}
	for _, ref := range refs {
		if _, err := os.Stat(filepath.Join(wsRoot, filepath.FromSlash(ref))); err != nil {
			t.Errorf("CodeGraph skill references missing file %q: %v", ref, err)
		}
	}
}

func assertSkillScriptPathsExist(t *testing.T, cfg *config.SystemConfig) {
	t.Helper()
	out := t.TempDir()
	tr := filepath.Join(projectRoot(t), "templates")
	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	wsRoot := filepath.Join(out, "templates/workspace-template")
	skillsRoot := filepath.Join(wsRoot, "skills")

	checked := 0
	err := filepath.WalkDir(skillsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, mm := range scriptRefRe.FindAllStringSubmatch(string(data), -1) {
			ref := mm[1]
			var resolved string
			if strings.HasPrefix(ref, "skills/") {
				resolved = filepath.Join(wsRoot, ref) // workspace 根相对
			} else {
				resolved = filepath.Join(filepath.Dir(path), ref) // skill 目录相对
			}
			if _, statErr := os.Stat(resolved); statErr != nil {
				rel, _ := filepath.Rel(skillsRoot, path)
				t.Errorf("skills/%s 引用脚本 %q 不存在(应在 %s)", rel, ref, resolved)
			}
			checked++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("没扫到任何 python 脚本引用,正则可能失效或模板结构变了")
	}
	t.Logf("校验了 %d 处 SKILL 脚本引用,全部存在", checked)
}

// TestDepMapParserHandlesGeneratedFormat 用生成器实际产出的 block 风格依赖图喂
// cascade_check.py 的 parse_dep_map,断言 downstream/data_stores 能解析出来。
//
// 护栏背景(2026-06):旧版 parse_dep_map 只认 inline `[a,b]`,而生成器
// service-dependency-map.yaml.tmpl 产出的是 block 列表 → downstream 永远空 →
// incident-investigator Step 4 静默空转。这里锁死 "生成器产出格式 ↔ 解析器" 契约。
// 没装 python3 的 CI 自动 skip(脚本是 Python,无 runtime 无从校验)。
func TestDepMapParserHandlesGeneratedFormat(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用,跳过 dep-map parser 契约校验")
	}
	scriptPath := filepath.Join(projectRoot(t),
		"templates/workspace/skills/incident-investigator/scripts/cascade_check.py")

	// 这段 block YAML 跟 service-dependency-map.yaml.tmpl 渲染形状一致(block 列表 + 引号)。
	const pysrc = `
import importlib.util, sys, json
spec = importlib.util.spec_from_file_location('cc', sys.argv[1])
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
block = '''services:
  commerce:
    role: "backend"
    downstream:
      - "user"
      - "order"
    data_stores:
      - "mysql:order_db"
    critical: false
'''
print(json.dumps(m.parse_dep_map(block)['commerce']))
`
	outB, err := exec.Command(py, "-c", pysrc, scriptPath).CombinedOutput()
	if err != nil {
		t.Fatalf("跑 python parse_dep_map 失败: %v\n%s", err, outB)
	}
	got := string(outB)
	for _, want := range []string{`"user"`, `"order"`, "mysql:order_db"} {
		if !strings.Contains(got, want) {
			t.Errorf("parse_dep_map 没从 block 风格解析出 %q(Bug1 回归):%s", want, got)
		}
	}
}
