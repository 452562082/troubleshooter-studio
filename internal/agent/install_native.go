// install_native.go —— claude-code / cursor / codex 三家 IDE 的"用户级安装":
// 把 staging 下的 agent 人格 / skills/ / scripts/ / tshoot.json 锚点拷到
// ~/.<target>/ 真实部署位置(以及 BotsPage 的 discover 锚点)。
//
// 三家 staging 形态:
//   - claude-code / cursor:`agents/<name>.md` + skills/ + scripts/(每个 agent 一个 .md)
//   - codex            :`agents/<name>.toml` + skills/<name>/SKILL.md(顶层调度入口) +
//                          skills/<name>/<sub>/SKILL.md + scripts/(TOML 格式 subagent)
//                          官方文档 https://developers.openai.com/codex/subagents
//
// 凭证 / MCP server 写入由 install_native_mcp.go + install_native_creds.go 负责,
// 这里只管纯文件分发。openclaw 走 install_native_openclaw.go,模型不同不通用。
package agent

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
	root := filepath.Join(home, t.DirName())

	// 1) 找 staging 里的 agent 文件 + 推 agent name(从文件名)。
	//   - claude-code / cursor: staging/agents/<NAME>.md
	//   - codex            : staging/agents/<NAME>.toml
	agentFile, name, err := findStagingAgentFile(stagingDir, t)
	if err != nil {
		return err
	}

	// 2) 准备 ~/<root>/{agents,skills,scripts}
	for _, sub := range []string{"agents", "skills", "scripts"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}

	// 3) 装 agent 文件:<root>/agents/<NAME>.<ext>(已存在 → 备份)
	dstAgent := filepath.Join(root, "agents", name+t.UserAgentExt())
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
		raw, rerr := os.ReadFile(agentFile)
		if rerr != nil {
			return fmt.Errorf("read staging codex toml: %w", rerr)
		}
		skillsRoot := filepath.Join(root, "skills", name)
		patched := strings.ReplaceAll(string(raw), generator.CodexPlaceholderSkillsRoot, skillsRoot)
		if err := os.WriteFile(dstAgent, []byte(patched), 0o644); err != nil {
			return fmt.Errorf("install codex agent toml: %w", err)
		}
	} else {
		if err := copyFileSimple(agentFile, dstAgent); err != nil {
			return fmt.Errorf("install agent file: %w", err)
		}
	}

	// 4) skills/* → ~/<root>/skills/<NAME>/(命名空间隔离,防覆盖其它 agent 的 skills)
	if err := replaceDir(
		filepath.Join(stagingDir, "skills"),
		filepath.Join(root, "skills", name),
	); err != nil {
		return fmt.Errorf("install skills: %w", err)
	}

	// 5) scripts/* → ~/<root>/scripts/<NAME>/
	if err := replaceDir(
		filepath.Join(stagingDir, "scripts"),
		filepath.Join(root, "scripts", name),
	); err != nil {
		return fmt.Errorf("install scripts: %w", err)
	}

	// 6) tshoot.json 锚点 → ~/<root>/skills/<NAME>/tshoot.json,让 BotsPage 的 discover
	// 能扫到。staging 那份没写出来就跳过,不阻塞主流程。
	stagingMeta := filepath.Join(stagingDir, discover.MetaFilename)
	if _, err := os.Stat(stagingMeta); err == nil {
		dstMeta := filepath.Join(root, "skills", name, discover.MetaFilename)
		if err := copyFileSimple(stagingMeta, dstMeta); err != nil {
			return fmt.Errorf("install tshoot.json anchor: %w", err)
		}
	}

	return nil
}

// findStagingAgentFile 找 staging/agents/<NAME>.<ext> —— 取第一个匹配后缀的非 .bak 文件,
// 返回完整路径 + name(不含后缀)。
//
//	claude-code / cursor: <NAME>.md
//	codex            : <NAME>.toml
func findStagingAgentFile(stagingDir string, t IDETarget) (file, name string, err error) {
	dir := filepath.Join(stagingDir, "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", fmt.Errorf("read staging agents dir: %w", err)
	}
	ext := t.UserAgentExt()
	var matches []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.Contains(n, ".bak.") || !strings.HasSuffix(n, ext) {
			continue
		}
		matches = append(matches, n)
	}
	switch len(matches) {
	case 0:
		return "", "", fmt.Errorf("no agents/*%s in staging %s", ext, dir)
	case 1:
		return filepath.Join(dir, matches[0]), strings.TrimSuffix(matches[0], ext), nil
	default:
		// generator 应保证只生成一个;多个匹配 = 上次产物没清干净 / staging 重叠,直接报错
		// 让用户感知,而不是 ReadDir 顺序非确定性下随机选一个。
		return "", "", fmt.Errorf("found %d agent files in staging %s (expected 1): %v", len(matches), dir, matches)
	}
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
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFileSimple(p, target)
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
