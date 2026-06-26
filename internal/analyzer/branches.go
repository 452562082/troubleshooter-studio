package analyzer

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// gitCmdTimeout 单个 git 子命令硬上限。git 在 NFS 仓库 / 触发 credential-helper 交互
// 提示时会无限阻塞(读不到 stdin 干等),没有 deadline 会拖死整个 analyze。
const gitCmdTimeout = 10 * time.Second

// GetRemoteURL 读仓库的 origin remote URL:`git -C <path> remote get-url origin`。
// 本地模式下 InitPage 用这个反填 repo.url(有 URL 才能写出合法 yaml)。
// 仓库不是 git / 没 origin remote / git CLI 不存在都返回空串,调用方应保留用户
// 已填的值(或要求用户补 URL)。
func GetRemoteURL(repoPath string) string {
	if fi, err := os.Stat(repoPath); err != nil || !fi.IsDir() {
		return ""
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "remote", "get-url", "origin")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

// ListBranches 列出 repoPath 下 git 仓库所有本地 + 远端分支(去重、去 HEAD 别名、
// 按字母序)。用于 InitPage Step 4 的 env_branches 下拉,让用户不用手敲分支名。
//
// 实现:调 `git for-each-ref --format=%(refname:short) refs/heads refs/remotes`,
// 比 `git branch -a` 解析起来干净(branch -a 带缩进 / HEAD 箭头 / current 星号)。
//
// 错误处理:repoPath 不存在 / 不是 git 仓库 / git CLI 没装 / exec 失败
// 全部静默返回空切片 —— 调用方回落到"没建议",不报错打扰用户(这只是锦上添花的自动填)。
func ListBranches(repoPath string) []string {
	if fi, err := os.Stat(repoPath); err != nil || !fi.IsDir() {
		return nil
	}
	// 确认是 git 仓库(.git 目录或 .git 文件指向 submodule worktree 都算)
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()
	// 先列 remote 名字,用于过滤 "refs/remotes/<remote>" 本身这种奇怪 ref
	// (某些 shallow clone + submodule 操作组合会留下裸 refs/remotes/origin)
	remoteNames := map[string]bool{}
	remoteCmd := exec.CommandContext(ctx, "git", "-C", repoPath, "remote")
	var remoteBuf bytes.Buffer
	remoteCmd.Stdout = &remoteBuf
	if err := remoteCmd.Run(); err == nil {
		for _, raw := range strings.Split(remoteBuf.String(), "\n") {
			if n := strings.TrimSpace(raw); n != "" {
				remoteNames[n] = true
			}
		}
	}

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "for-each-ref",
		"--format=%(refname:short)",
		"refs/heads", "refs/remotes")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.Split(buf.String(), "\n") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		// 过滤 origin/HEAD 这类 alias + 裸 "origin"(某些 shallow+submodule
		// 操作后会出现的空 remote-root ref,不是真实分支)
		if strings.HasSuffix(name, "/HEAD") || name == "HEAD" {
			continue
		}
		if remoteNames[name] {
			continue // 纯 remote 名(没 / 分隔),不是分支
		}
		// 去 remote 前缀再去重:"main" 和 "origin/main" 只保留一个;优先保留短名。
		short := name
		if idx := strings.Index(name, "/"); idx > 0 {
			// 判断是不是 remote 前缀(origin/xxx 这种) vs 带斜杠的分支名(feature/foo)
			// 简单判:如果前缀跟本地分支同名,认为是 remote 别名。这里放宽:统一把
			// 形如 <anything>/<rest> 的都试着剥一次 remote 前缀用 rest 去重。
			cand := name[idx+1:]
			if cand != "" {
				short = cand
			}
		}
		if seen[short] {
			continue
		}
		seen[short] = true
		out = append(out, short)
	}
	sort.Strings(out)
	return out
}
