// install_native.go —— claude-code / cursor / codex 三家 IDE 的"用户级安装":
// 把 staging 下的 agent .md / skills/ / scripts/ / tshoot.json 锚点拷到
// ~/.<target>/ 真实部署位置(以及 BotsPage 的 discover 锚点)。
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
)

// InstallNative 把 stagingDir 里的产物分发到 ~/.<target>/{agents,skills,scripts}/。
// target 必须是三家 IDETarget 之一(openclaw 走专门入口)。
func InstallNative(stagingDir, target string) error {
	return InstallNativeAt(stagingDir, target, "")
}

// InstallNativeAt 是 InstallNative 的"自定义根目录"变体:customRoot 非空时整体落到
// <customRoot>/{agents,skills,scripts}/<NAME>/ 而不是 ~/.<target>/。给 wizard
// "我已自行安装" 流程的"选安装目录"用 —— 用户把客户端装在非默认位置时仍能部署。
func InstallNativeAt(stagingDir, target, customRoot string) error {
	t, err := ParseIDETarget(target)
	if err != nil {
		return err
	}

	root := customRoot
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("read $HOME: %w", err)
		}
		root = filepath.Join(home, t.DirName())
	}

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

// findAgentMD 取目录下第一个 .md(忽略 .bak),返回完整路径 + name(去后缀)。
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
