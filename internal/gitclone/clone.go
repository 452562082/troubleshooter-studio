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
//
// 默认带 --recurse-submodules --shallow-submodules:
//   truss 这种 umbrella 仓库通过 .gitmodules 引入 7 个子仓库(api/commerce/user/...),
//   不 recurse 克隆出来所有服务子目录都是空的,analyzer 扫不到 go.mod / service_names,
//   UI 上"服务名 / 技术栈 / 分支"全空。recurse 一次性把 submodule 内容拉下来,
//   shallow 避免对每个 submodule 都做全量历史(大仓库能节省数百 MB)。
//
// 如果仓库没有 submodule,这两个 flag 是 no-op,不会出问题。
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

	args := []string{"clone", "--recurse-submodules", "--shallow-submodules"}
	if opts.Depth > 0 {
		args = append(args, "--depth", strconv.Itoa(opts.Depth))
	}
	if opts.Branch != "" {
		args = append(args, "--branch", opts.Branch, "--single-branch")
	} else {
		// 没指定分支时显式 --no-single-branch:git 在 --depth 存在的情况下默认只抓
		// HEAD 指向的那个分支,导致向导里"分支下拉"只剩 main 一条。加这个让所有
		// 远端分支的 tip 都进来(每个分支 depth 层,多占不了多少 —— ShallowDepth 默认 50)。
		// 没 --depth 时这个 flag 是 no-op(全量 clone 本来就多分支),无副作用。
		args = append(args, "--no-single-branch")
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

// EnsureAllBranches 如果 remote.origin.fetch 只追单个分支(single-branch clone 留下的),
// 把它改成 wildcard + 拉一次,让后续 ListBranches 能看到所有远端分支。
// 已经是 wildcard 的直接 no-op;多 remote 的情况只处理 origin。
//
// 背景:analyzerpipe 用 --depth 50 做 shallow clone,git 在有 --depth 时默认隐式
// --single-branch,只抓 HEAD 分支。向导里"分支下拉"只剩 main 一条,用户换不了。
// 新 Clone 已带 --no-single-branch,这个函数兜底修老的遗留 clone 目录。
//
// 不是 git 仓库 / 读不到 origin 返回 nil(不强求)。
func EnsureAllBranches(repoPath string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return ErrGitNotFound
	}
	if err := exec.Command("git", "-C", repoPath, "rev-parse", "--show-toplevel").Run(); err != nil {
		return nil
	}
	// 读当前 origin fetch refspec
	cmd := exec.Command("git", "-C", repoPath, "config", "--get", "remote.origin.fetch")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil // 没 origin,算了
	}
	current := strings.TrimSpace(out.String())
	wildcard := "+refs/heads/*:refs/remotes/origin/*"
	if current == wildcard {
		return nil // 已经是 wildcard,没事可做
	}
	// 改成 wildcard
	if err := exec.Command("git", "-C", repoPath, "config",
		"remote.origin.fetch", wildcard).Run(); err != nil {
		return fmt.Errorf("set origin fetch refspec: %w", err)
	}
	// 拉一次把其它分支 tip 拿进来。
	//   --depth 50:跟 config 里 ShallowDepth 默认一致,避免退回全量历史
	//   --no-recurse-submodules:主 repo 分支够了,submodule 交给 EnsureSubmodules
	//     处理(truss 这种 .gitmodules 里提供的 URL 可能读/写权限不同,带 recurse
	//     容易 exit status 1 污染返回值,即使主 repo 分支其实 fetch 成功了)
	fetchCmd := exec.Command("git", "-C", repoPath, "fetch", "origin",
		"--depth", "50", "--no-recurse-submodules")
	var stderr bytes.Buffer
	fetchCmd.Stderr = &stderr
	if err := fetchCmd.Run(); err != nil {
		return fmt.Errorf("fetch all branches %s: %w (%s)", repoPath, err,
			strings.TrimSpace(stderr.String()))
	}
	return nil
}

// EnsureSubmodules 如果 repo 根下有 .gitmodules,跑 `git submodule update --init --recursive`
// 保证所有 submodule 内容已拉下来。已 init 的子模块 no-op,几乎秒回;partial 的补齐。
//
// 背景:老版本 Clone 没带 --recurse-submodules,用户可能遗留一批"empty-submodule-dirs"
// 克隆(git 建了 api/ commerce/ ... 但内容全空)。analyzerpipe 复用已存在的 clone 时调
// 这个兜底一下,就不用让用户手动 rm -rf 重新 clone。
//
// 没 .gitmodules 的仓库直接返回 nil(no-op)。git 不可用返回 ErrGitNotFound。
func EnsureSubmodules(repoPath string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return ErrGitNotFound
	}
	// 没 .gitmodules 的仓库不用跑(大多数仓库都这种)
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--show-toplevel")
	if err := cmd.Run(); err != nil {
		return nil // 不是 git 仓库或目录不存在,不强求
	}
	// 检查 .gitmodules 存在(跑 git config 更稳,避免对 submodule 的 "." .gitmodules 等奇怪状态误判)
	cmd = exec.Command("git", "-C", repoPath, "config", "--file", ".gitmodules", "--get-regexp", "path")
	if err := cmd.Run(); err != nil {
		return nil // 没 .gitmodules 或读不到,不是 submodule umbrella,skip
	}
	// 拉 submodule。recursive 处理嵌套;init 创建未 init 的;update 同步到记录的 commit。
	cmd = exec.Command("git", "-C", repoPath, "submodule", "update", "--init", "--recursive")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("submodule update %s: %w (%s)", repoPath, err,
			strings.TrimSpace(stderr.String()))
	}
	return nil
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
//
// 关键 case(常踩坑):ssh URL 带 port `ssh://git@host:2222/owner/repo.git`,
// `:` 后面是数字 port 而不是 path,**必须丢掉 port**才能跟同仓 https 形式
// (`https://host/owner/repo.git`)归一化后相等。
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
	s = strings.TrimPrefix(s, "git://")
	// scp-style: user@host:path → host:path
	if idx := strings.Index(s, "@"); idx >= 0 && !strings.Contains(s[:idx], "/") {
		s = s[idx+1:]
	}
	// 处理第一个 ':'(出现在第一个 '/' 之前才算):
	//   - 后接数字 + '/' → ssh port,丢掉(port 不参与归一化)
	//   - 否则 → scp 风格 path 分隔,':' 换 '/'
	colon := strings.Index(s, ":")
	slash := strings.Index(s, "/")
	if colon >= 0 && (slash == -1 || colon < slash) {
		after := s[colon+1:]
		slashAfter := strings.Index(after, "/")
		if slashAfter >= 0 && isAllDigits(after[:slashAfter]) {
			// host:PORT/path → host/path
			s = s[:colon] + after[slashAfter:]
		} else {
			// host:owner/repo(scp) → host/owner/repo
			s = s[:colon] + "/" + after
		}
	}
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
