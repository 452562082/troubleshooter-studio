// install_native.go —— claude-code / cursor 的"用户级安装"原生 Go 实现。
//
// 历史:之前由生成的 install.sh 完成"staging → ~/.claude|cursor/agents/<name>.md"
// 这一步,导致 Studio 必须 shell-out 一个 bash 进程才能让 AI 平台真正读到 agent。
// 现在直接在 Go 里做(纯文件拷贝+备份),Studio 一键部署=即装即用。
//
// 适用 target:claude-code、cursor。openclaw 仍走 scripts/install.sh —— 它有 brew/apt
// 依赖装载、配置中心 MCP 注册、交互凭证收集,porting 工程量大,先不动。
package agent

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// InstallNative 把 stagingDir 里的产物分发到用户级目录。target 决定 root:
//   - claude-code → $HOME/.claude/{agents,skills,scripts}/
//   - cursor      → $HOME/.cursor/{agents,skills,scripts}/
//
// 行为对齐原 install.sh.tmpl:
//   - agents/<NAME>.md → <root>/agents/<NAME>.md(已存在则备份 .bak.YYYYMMDD-HHMMSS)
//   - skills/*         → <root>/skills/<NAME>/(整目录覆盖,先 RemoveAll)
//   - scripts/*        → <root>/scripts/<NAME>/(整目录覆盖,先 RemoveAll)
//
// NAME 取自 stagingDir/agents/*.md 第一个文件名(.md 去除)。
func InstallNative(stagingDir, target string) error {
	var rootName string
	switch target {
	case "claude-code":
		rootName = ".claude"
	case "cursor":
		rootName = ".cursor"
	default:
		return fmt.Errorf("install_native: unsupported target %q (only claude-code / cursor)", target)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("read $HOME: %w", err)
	}
	root := filepath.Join(home, rootName)

	// 1) 找 agents/<NAME>.md(只取第一个;generator 只生成一个)
	agentFile, name, err := findAgentMD(filepath.Join(stagingDir, "agents"))
	if err != nil {
		return err
	}

	// 2) 准备 ~/<root>/{agents,skills,scripts}
	for _, sub := range []string{"agents", "skills", "scripts"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}

	// 3) 装 agent .md(已存在 → 备份)
	dstAgent := filepath.Join(root, "agents", name+".md")
	if _, err := os.Stat(dstAgent); err == nil {
		ts := time.Now().Format("20060102-150405")
		if err := copyFileSimple(dstAgent, dstAgent+".bak."+ts); err != nil {
			return fmt.Errorf("backup existing agent: %w", err)
		}
	}
	if err := copyFileSimple(agentFile, dstAgent); err != nil {
		return fmt.Errorf("install agent .md: %w", err)
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

	return nil
}

// findAgentMD 取目录下第一个 .md(忽略 .bak),返回完整路径 + name(去后缀)。
// 没找到 → error,因为没 agent .md 后续 install 全是空动作。
func findAgentMD(dir string) (file, name string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", fmt.Errorf("read agents dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".md") || strings.Contains(n, ".bak.") {
			continue
		}
		return filepath.Join(dir, n), strings.TrimSuffix(n, ".md"), nil
	}
	return "", "", fmt.Errorf("no agents/*.md in staging %s", dir)
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

// copyFileSimple:把 src 拷贝到 dst,保留 mode。dst 父目录已存在(调用方负责)。
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
