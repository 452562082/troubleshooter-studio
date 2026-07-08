// install_native.go —— claude-code / cursor / codex 三家 IDE 的"用户级安装":
// 把 staging 下的 agent 人格 / skills/ / scripts/ / tshoot.json 锚点拷到
// ~/.<target>/ 真实部署位置(以及 BotsPage 的 discover 锚点)。
//
// 三家 staging 形态:
//   - claude-code / cursor:`agents/<name>.md` + skills/ + scripts/(每个 agent 一个 .md)
//   - codex            :`agents/<name>.toml` + skills/<name>/SKILL.md(顶层调度入口) +
//     skills/<name>/<sub>/SKILL.md + scripts/(TOML 格式 subagent)
//     官方文档 https://developers.openai.com/codex/subagents
//
// 凭证 / MCP server 写入由 install_native_mcp.go + install_native_creds.go 负责,
// 这里只管纯文件分发。openclaw 走 install_native_openclaw.go,模型不同不通用。
package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

// InstallNative 把 stagingDir 里的产物分发到 ~/.<target>/{agents,skills,scripts}/。
// target 必须是三家 IDETarget 之一(openclaw 走专门入口)。
//
// 落地位置:固定 $HOME/.<target>/。三家 IDE(claude-code/cursor/codex)各自的扩展目录
// 都是 hardcoded —— Claude Code CLI 启动只读 ~/.claude/agents/,Cursor 只读 ~/.cursor/,
// Codex 只读 ~/.codex/agents/。装到别处 IDE 看不到,就没意义。
func InstallNative(stagingDir, target string) error {
	t, err := ParseIDETarget(target)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("read $HOME: %w", err)
	}
	root := t.RootDir(home)

	// 1) 找 staging 里的 agent 文件 + 推 agent name(从文件名)。
	//   - claude-code / cursor: staging/agents/<NAME>.md
	//   - codex            : staging/agents/<NAME>.toml
	agentFiles, err := findStagingAgentFiles(stagingDir, t)
	if err != nil {
		return err
	}

	// 2) 准备 ~/<root>/{agents,skills,scripts}
	for _, sub := range []string{"agents", "skills", "scripts"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}

	primaryAgentName := primaryAgentNameFromStagingMeta(stagingDir)
	if primaryAgentName == "" {
		primaryAgentName = agentFiles[0].Name
	}
	for _, ag := range agentFiles {
		if err := installOneNativeAgent(stagingDir, root, t, ag, primaryAgentName); err != nil {
			return err
		}
	}

	return nil
}

type stagingAgentFile struct {
	File string
	Name string
}

func installOneNativeAgent(stagingDir, root string, t IDETarget, ag stagingAgentFile, primaryAgentName string) error {
	// 装 agent 文件:<root>/agents/<NAME>.<ext>(已存在 → 备份)
	dstAgent := filepath.Join(root, "agents", ag.Name+t.UserAgentExt())
	if _, err := os.Stat(dstAgent); err == nil {
		ts := nanoTimestamp()
		if err := copyFileSimple(dstAgent, dstAgent+".bak."+ts); err != nil {
			return fmt.Errorf("backup existing agent: %w", err)
		}
	}
	// codex 特殊:staging toml 里 [[skills.config]].path 用 generator.CodexPlaceholderSkillsRoot 占位,
	// 装机时替换成 <root>/skills/<name>/ 的实际绝对路径(codex 不解析 ~ / $HOME)。
	// MCP servers 段的 {{MCP_SERVERS}} 占位由 MergeMCPIntoIDESettingsAt 时填,这里保留原样。
	if t == TargetCodex {
		raw, rerr := os.ReadFile(ag.File)
		if rerr != nil {
			return fmt.Errorf("read staging codex toml: %w", rerr)
		}
		skillsRoot := filepath.Join(root, "skills", ag.Name)
		patched := strings.ReplaceAll(string(raw), generator.CodexPlaceholderSkillsRoot, skillsRoot)
		if err := os.WriteFile(dstAgent, []byte(patched), 0o644); err != nil {
			return fmt.Errorf("install codex agent toml: %w", err)
		}
	} else {
		if err := copyFileSimple(ag.File, dstAgent); err != nil {
			return fmt.Errorf("install agent file: %w", err)
		}
	}

	role := stagingAgentRole(stagingDir, ag.Name)

	// skills/* → ~/<root>/skills/<NAME>/(命名空间隔离,防覆盖其它 agent 的 skills)。
	// 同一机器人内的不同 agent 只安装各自角色需要的 skill,避免验证 agent 带上
	// incident-investigator / recent-changes 这类 RCA 主线能力。
	if err := replaceSkillsDirForRole(
		filepath.Join(stagingDir, "skills"),
		filepath.Join(root, "skills", ag.Name),
		role,
	); err != nil {
		return fmt.Errorf("install skills for %s: %w", ag.Name, err)
	}

	// scripts/* → ~/<root>/scripts/<NAME>/
	if err := replaceDir(
		filepath.Join(stagingDir, "scripts"),
		filepath.Join(root, "scripts", ag.Name),
	); err != nil {
		return fmt.Errorf("install scripts for %s: %w", ag.Name, err)
	}

	// tshoot.json 锚点只放主机器人目录,让 BotsPage / discover 只看到一台机器人。
	// validator 等内部 agent 目录不能带 tshoot.json,否则旧扫描逻辑会把它当第二台机器人。
	dstMeta := filepath.Join(root, "skills", ag.Name, discover.MetaFilename)
	if ag.Name == primaryAgentName {
		stagingMeta := filepath.Join(stagingDir, discover.MetaFilename)
		if _, err := os.Stat(stagingMeta); err == nil {
			if err := copyFileSimple(stagingMeta, dstMeta); err != nil {
				return fmt.Errorf("install tshoot.json anchor for %s: %w", ag.Name, err)
			}
		}
	} else if err := os.Remove(dstMeta); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove internal agent tshoot.json for %s: %w", ag.Name, err)
	}

	return nil
}

func stagingAgentRole(stagingDir, agentName string) generator.AgentRole {
	metaPath := filepath.Join(stagingDir, "agents-meta", agentName, discover.MetaFilename)
	data, err := os.ReadFile(metaPath)
	if err == nil {
		var meta discover.Meta
		if json.Unmarshal(data, &meta) == nil {
			if strings.EqualFold(strings.TrimSpace(meta.Role), discover.RoleValidator) {
				return generator.AgentRoleValidator
			}
			if strings.EqualFold(strings.TrimSpace(meta.Role), discover.RoleTroubleshooter) {
				return generator.AgentRoleTroubleshooter
			}
		}
	}
	if strings.Contains(strings.ToLower(agentName), "valid") || strings.Contains(strings.ToLower(agentName), "verif") {
		return generator.AgentRoleValidator
	}
	return generator.AgentRoleTroubleshooter
}

func primaryAgentNameFromStagingMeta(stagingDir string) string {
	data, err := os.ReadFile(filepath.Join(stagingDir, discover.MetaFilename))
	if err != nil {
		return ""
	}
	var meta discover.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	if strings.TrimSpace(meta.AgentID) != "" {
		return strings.TrimSpace(meta.AgentID)
	}
	for _, ag := range meta.InternalAgents {
		if strings.EqualFold(strings.TrimSpace(ag.Role), discover.RoleTroubleshooter) {
			return strings.TrimSpace(ag.ID)
		}
	}
	return ""
}

// findStagingAgentFile 找 staging/agents/<NAME>.<ext> —— 取第一个匹配后缀的非 .bak 文件,
// 返回完整路径 + name(不含后缀)。
//
//	claude-code / cursor: <NAME>.md
//	codex            : <NAME>.toml
func findStagingAgentFile(stagingDir string, t IDETarget) (file, name string, err error) {
	matches, err := findStagingAgentFiles(stagingDir, t)
	if err != nil {
		return "", "", err
	}
	if len(matches) > 1 {
		return "", "", fmt.Errorf("found %d agent files in staging %s (expected 1): %v", len(matches), filepath.Join(stagingDir, "agents"), agentFileNames(matches))
	}
	return matches[0].File, matches[0].Name, nil
}

func findStagingAgentFiles(stagingDir string, t IDETarget) ([]stagingAgentFile, error) {
	dir := filepath.Join(stagingDir, "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read staging agents dir: %w", err)
	}
	ext := t.UserAgentExt()
	var matches []stagingAgentFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.Contains(n, ".bak.") || !strings.HasSuffix(n, ext) {
			continue
		}
		matches = append(matches, stagingAgentFile{
			File: filepath.Join(dir, n),
			Name: strings.TrimSuffix(n, ext),
		})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })
	if len(matches) == 0 {
		return nil, fmt.Errorf("no agents/*%s in staging %s", ext, dir)
	}
	return matches, nil
}

func agentFileNames(files []stagingAgentFile) []string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, filepath.Base(f.File))
	}
	return names
}

// replaceDir:src 不存在 → 跳过(scripts 可能没有);存在 → 清掉 dst 后整目录拷过去。
func replaceDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("expected dir, got file: %s", src)
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		if installShouldSkipGeneratedArtifact(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFileSimple(p, target)
	})
}

func replaceSkillsDirForRole(src, dst string, role generator.AgentRole) error {
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("expected dir, got file: %s", src)
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		if installShouldSkipGeneratedArtifact(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) == 0 || parts[0] == "" {
			return nil
		}
		skillName := parts[0]
		if d.IsDir() {
			if len(parts) == 1 && !generator.SkillAllowedForAgentRole(skillName, role) {
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		// Codex staging has a root skills/SKILL.md entry for the troubleshooter only.
		if len(parts) == 1 {
			if role == generator.AgentRoleValidator {
				return nil
			}
			return copyFileSimple(p, filepath.Join(dst, rel))
		}
		if !generator.SkillAllowedForAgentRole(skillName, role) {
			return nil
		}
		return copyFileSimple(p, filepath.Join(dst, rel))
	})
}

// moveOutOrRemove 优先把 src 重命名(搬走)到 trashPath(自动建父目录),失败则 RemoveAll(src) 兜底。
// 跨盘 rename 失败时常见,直接 RemoveAll 不阻塞主流程。
//
//	movedTo = trashPath, existed = true  → 搬到 Trash 成功,可恢复
//	movedTo = "",        existed = true  → rename 失败已 RemoveAll(无回收点)
//	movedTo = "",        existed = false → src 不存在,no-op
//	err 非 nil                            → 必须中断
func moveOutOrRemove(src, trashPath string) (movedTo string, existed bool, err error) {
	if _, statErr := os.Stat(src); statErr != nil {
		if os.IsNotExist(statErr) {
			return "", false, nil
		}
		return "", false, statErr
	}
	if mkErr := os.MkdirAll(filepath.Dir(trashPath), 0o755); mkErr == nil {
		if rnErr := os.Rename(src, trashPath); rnErr == nil {
			return trashPath, true, nil
		}
	}
	if rmErr := os.RemoveAll(src); rmErr != nil {
		return "", true, fmt.Errorf("rename to trash + RemoveAll both failed: %w", rmErr)
	}
	return "", true, nil
}

// nanoTimestamp 给 backup / Trash 路径用的"几乎不撞"时间戳:
//
//	"20060102-150405.NNNNNNNNN"(秒级 + 纳秒后 9 位)
//
// 老版本只到秒精度,1 秒内连点两次部署会撞 trashPath:
// 第一次 Rename 成功 → 第二次目标已存在 Rename 失败 → moveOutOrRemove fallthrough 到
// RemoveAll(src) 把第二次正写到一半的产物也删了。加纳秒后 9 位让两次 backup 几乎不可能撞。
func nanoTimestamp() string {
	now := time.Now()
	return now.Format("20060102-150405") + fmt.Sprintf(".%09d", now.Nanosecond())
}

// copyFileSimple 把 src 拷贝到 dst,保留 mode + 自动建父目录。
func copyFileSimple(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func installShouldSkipGeneratedArtifact(name string) bool {
	if name == "__pycache__" {
		return true
	}
	if strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py") {
		return true
	}
	if strings.HasSuffix(name, ".pyc") || strings.HasSuffix(name, ".pyo") {
		return true
	}
	return false
}
