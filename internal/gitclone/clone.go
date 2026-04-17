package gitclone

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// ErrGitNotFound git 未安装
var ErrGitNotFound = errors.New("git not found in PATH")

// Options git clone 选项
type Options struct {
	URL    string // 必填
	Dest   string // 必填
	Branch string // 可选；空串=使用仓库默认分支
	Depth  int    // >0 时加 --depth
	Stderr io.Writer
}

// Clone 调用 git clone；返回命令错误（含 stderr 前 1KB 方便排查）
func Clone(opts Options) error {
	if _, err := exec.LookPath("git"); err != nil {
		return ErrGitNotFound
	}
	if opts.URL == "" {
		return fmt.Errorf("gitclone: URL required")
	}
	if opts.Dest == "" {
		return fmt.Errorf("gitclone: Dest required")
	}

	args := []string{"clone"}
	if opts.Depth > 0 {
		args = append(args, "--depth", strconv.Itoa(opts.Depth))
	}
	if opts.Branch != "" {
		args = append(args, "--branch", opts.Branch, "--single-branch")
	}
	args = append(args, "--", opts.URL, opts.Dest)

	cmd := exec.Command("git", args...)
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s → %s: %w", opts.URL, opts.Dest, err)
	}
	return nil
}

// Available 返回 git 是否可用
func Available() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// ErrNotGitRepo 目标目录不是 git 仓库或没有 origin
var ErrNotGitRepo = errors.New("not a git repository or no origin")

// ReadOrigin 读取仓库的 origin URL；非 git 仓库/无 origin 返回 ErrNotGitRepo
func ReadOrigin(path string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", ErrGitNotFound
	}
	cmd := exec.Command("git", "-C", path, "remote", "get-url", "origin")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", ErrNotGitRepo
	}
	return strings.TrimSpace(stdout.String()), nil
}

// CanonicalURL 把 ssh/https/git 协议的同一仓库 URL 归一化为 "host/owner/repo"（小写、无 .git 尾缀）
// 便于跨协议比对。识别失败时原样返回 trim 过的字符串。
func CanonicalURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// ssh:// 或 git+ssh://
	s = strings.TrimPrefix(s, "ssh://")
	s = strings.TrimPrefix(s, "git+ssh://")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	// scp-style: user@host:path → host/path
	if idx := strings.Index(s, "@"); idx >= 0 && !strings.Contains(s[:idx], "/") {
		s = s[idx+1:]
	}
	// 把第一个 ':' 换成 '/'（scp 形式）
	if colon := strings.Index(s, ":"); colon >= 0 && !strings.Contains(s[:colon], "/") {
		s = s[:colon] + "/" + s[colon+1:]
	}
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}
