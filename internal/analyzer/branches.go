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
// 实现:调 `git for-each-ref --format=%(refname) refs/heads refs/remotes`,
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
	var remoteNames []string
	remoteCmd := exec.CommandContext(ctx, "git", "-C", repoPath, "remote")
	var remoteBuf bytes.Buffer
	remoteCmd.Stdout = &remoteBuf
	if err := remoteCmd.Run(); err == nil {
		for _, raw := range strings.Split(remoteBuf.String(), "\n") {
			if n := strings.TrimSpace(raw); n != "" {
				remoteNames = append(remoteNames, n)
			}
		}
	}
	sort.Slice(remoteNames, func(i, j int) bool {
		if len(remoteNames[i]) != len(remoteNames[j]) {
			return len(remoteNames[i]) > len(remoteNames[j])
		}
		return remoteNames[i] < remoteNames[j]
	})

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "for-each-ref",
		"--format=%(refname)",
		"refs/heads", "refs/remotes")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.Split(buf.String(), "\n") {
		ref := strings.TrimSpace(raw)
		if ref == "" {
			continue
		}
		name, ok := normalizeBranchRef(ref, remoteNames)
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func normalizeBranchRef(ref string, remoteNames []string) (string, bool) {
	if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return name, name != ""
	}
	remoteRef, ok := strings.CutPrefix(ref, "refs/remotes/")
	if !ok {
		return "", false
	}
	for _, remote := range remoteNames {
		name, matched := strings.CutPrefix(remoteRef, remote+"/")
		if matched && name != "" && name != "HEAD" {
			return name, true
		}
	}
	return "", false
}
