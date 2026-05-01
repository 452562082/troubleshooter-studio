// bindings_workspace.go —— 已装机器人工作目录浏览 / 编辑 binding。
//
// 给 BotsPage 的"📂 浏览工作目录"功能用:用户能在桌面 app 内打开机器人的实际部署
// 目录(~/.openclaw/workspace/<name> / ~/.claude/skills/<name> 等),以目录树展开
// 看每个 skill / SKILL.md / scripts/ 的内容,并在 app 内直接编辑保存 ——
// 不必反复改 yaml + 重部署,适合调试一个 skill 里的细节(改个变量、试个 prompt 文案)。
//
// 安全约束:
//   - 列文件 / 读文件 / 写文件三个 binding 都强制 rootPath 必须是 discover.Scan 扫到的
//     真实部署根(BotsPage 卡片里的 path 字段),且 relPath 解析后必须仍在 rootPath 下,
//     防止 ".." 路径穿越访问 app 不该碰的位置。
//   - 写入限制:文件大小 <= 1MB(防止用户误粘海量内容把磁盘塞满);文本编码内容
//     不要求严格,但二进制(含 \0)的文件不允许在 UI 编辑,只能查看。
//   - 隐藏文件(. 开头)默认参与列表(skill 配置 / .clawhub/lock.json 都可能要看),
//     但前端会用图标提示这些是 OpenClaw 元数据,改之前自己心里有数。
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

// FileNode 文件树节点,递归结构。前端 v-for 展开成多级缩进。
type FileNode struct {
	Name     string     `json:"name"`     // 文件 / 目录名(不含路径)
	Path     string     `json:"path"`     // 相对 rootPath 的路径,前端读 / 写时回传给后端
	IsDir    bool       `json:"is_dir"`
	Size     int64      `json:"size,omitempty"` // 文件字节数(目录不写)
	Children []FileNode `json:"children,omitempty"`
}

// 写入限制:单文件最大 1 MiB。预算给 SKILL.md / 小脚本用,贴整个 yaml 库进来会被拒。
const maxWritableFileSize int64 = 1 * 1024 * 1024

// 列树时跳过的目录:.git 仅占空间没人编辑;.DS_Store 之类 OS 噪音也不展示。
// 注:.clawhub / .openclaw 等 OpenClaw 元数据 dir 仍展示(用户可能要看 lock.json)。
var workspaceTreeExcludeDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true,
	"node_modules": true, "__pycache__": true,
}

// ListBotWorkspaceFiles 列出 rootPath 下的文件树。
//
// rootPath 必须是 discover.Scan 出来的某个真实 bot 部署根(BotsPage 卡片 path)。
// 我们用 discover 自己扫一遍验证 —— 如果 rootPath 没出现在结果里就拒绝,防止用户随便
// 传个目录进来浏览。
func (a *App) ListBotWorkspaceFiles(rootPath string) (*FileNode, error) {
	if err := assertBotWorkspacePath(rootPath); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve abs: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("stat root: %w", err)
	}
	root := &FileNode{Name: filepath.Base(abs), Path: "", IsDir: true}
	if err := walkWorkspaceDir(abs, "", root); err != nil {
		return nil, err
	}
	return root, nil
}

// walkWorkspaceDir 递归列 abs(rootPath/relSoFar)下的内容,挂到 parent.Children。
func walkWorkspaceDir(abs, relSoFar string, parent *FileNode) error {
	entries, err := os.ReadDir(abs)
	if err != nil {
		return err
	}
	// 排序:目录排前,然后按名字字母序(让 UI 树看着稳定)
	sort.Slice(entries, func(i, j int) bool {
		ei, ej := entries[i], entries[j]
		if ei.IsDir() != ej.IsDir() {
			return ei.IsDir()
		}
		return ei.Name() < ej.Name()
	})
	for _, e := range entries {
		name := e.Name()
		if workspaceTreeExcludeDirs[name] {
			continue
		}
		// macOS .DS_Store 噪音
		if name == ".DS_Store" {
			continue
		}
		nodeRel := filepath.Join(relSoFar, name)
		nodeAbs := filepath.Join(abs, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		node := FileNode{
			Name:  name,
			Path:  filepath.ToSlash(nodeRel),
			IsDir: e.IsDir(),
		}
		if !e.IsDir() {
			node.Size = info.Size()
		} else {
			if err := walkWorkspaceDir(nodeAbs, nodeRel, &node); err != nil {
				// 递归失败不中断整体(权限问题 / 软链断裂),只记空目录
			}
		}
		parent.Children = append(parent.Children, node)
	}
	return nil
}

// ReadBotWorkspaceFile 读单文件内容(UTF-8 文本)。
// rootPath 同 ListBotWorkspaceFiles 的约束;relPath 必须解析后仍在 rootPath 下。
//
// 二进制文件(含 \0 字节)走 ok=false 返回,前端据此显示"二进制文件,不支持编辑"。
type ReadFileResult struct {
	Content   string `json:"content"`
	IsBinary  bool   `json:"is_binary"`
	Truncated bool   `json:"truncated,omitempty"`
	Size      int64  `json:"size"`
}

func (a *App) ReadBotWorkspaceFile(rootPath, relPath string) (*ReadFileResult, error) {
	if err := assertBotWorkspacePath(rootPath); err != nil {
		return nil, err
	}
	abs, err := safeResolveInsideRoot(rootPath, relPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	res := &ReadFileResult{Size: info.Size()}
	// 简单二进制判定:含 NUL 字节的视为二进制,UI 不让编辑
	if isBinary(data) {
		res.IsBinary = true
		return res, nil
	}
	// 文本但巨大 → 截断到 1 MiB 后给前端,Truncated=true 让用户知道未显示完整
	if int64(len(data)) > maxWritableFileSize {
		res.Truncated = true
		data = data[:maxWritableFileSize]
	}
	res.Content = string(data)
	return res, nil
}

// WriteBotWorkspaceFile 写单文件。size > 1MB 直接拒,二进制内容(含 NUL)也拒 ——
// UI 编辑器走纯文本,塞 NUL 进去基本是 bug 不是特性。
//
// 不创建新文件:relPath 必须指向已存在的文件;新增文件需要走系统级 file manager(避免
// 被 binding 滥用成"任意写盘"通道)。也不允许写入根 tshoot.json / .clawhub/lock.json
// 这种 generator 管理的文件,改了下次重部署会被覆盖,先在 UI 上拦掉省得用户白干。
func (a *App) WriteBotWorkspaceFile(rootPath, relPath, content string) error {
	if err := assertBotWorkspacePath(rootPath); err != nil {
		return err
	}
	abs, err := safeResolveInsideRoot(rootPath, relPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("file not found: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("cannot write to a directory")
	}
	if int64(len(content)) > maxWritableFileSize {
		return fmt.Errorf("file too large (>%d bytes); save smaller chunks", maxWritableFileSize)
	}
	if strings.ContainsRune(content, 0) {
		return fmt.Errorf("content contains NUL byte; binary edits not supported")
	}
	// generator 管理的元数据文件不让 UI 编辑(改了重部署即覆盖,徒增混乱)
	base := filepath.Base(abs)
	rel := filepath.ToSlash(relPath)
	if base == "tshoot.json" || rel == ".clawhub/lock.json" {
		return fmt.Errorf("file %q 由 generator 管理,UI 编辑会在下次部署时被覆盖,请通过修改 system.yaml + 重新部署来更新", base)
	}
	return os.WriteFile(abs, []byte(content), info.Mode().Perm())
}

// safeResolveInsideRoot 把 relPath 拼到 rootPath 下并展开成绝对路径,
// 校验结果仍在 rootPath 内部(防 ".." 穿越)。relPath 接受空字符串(=指 root 本身)。
func safeResolveInsideRoot(rootPath, relPath string) (string, error) {
	rootAbs, err := filepath.Abs(rootPath)
	if err != nil {
		return "", err
	}
	rel := filepath.Clean(filepath.FromSlash(relPath))
	if rel == "." {
		rel = ""
	}
	abs := filepath.Join(rootAbs, rel)
	// 再 Abs 一次以解析任何符号链接 / 残留 ..
	abs, err = filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	rel2, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", err
	}
	if rel2 == ".." || strings.HasPrefix(rel2, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace root")
	}
	return abs, nil
}

// assertBotWorkspacePath 校验 rootPath 是 discover.Scan 出来的某个机器人部署根。
// 不直接信任前端传值 —— 防止 binding 被当成"读任意目录"的通用 file API 用。
func assertBotWorkspacePath(rootPath string) error {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return errors.New("rootPath required")
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return err
	}
	// discover 走默认根 + 已知 custom roots,跟 BotsPage::DiscoverBots 同一套逻辑;
	// 这里不传 extraRoots(浏览功能不需要让用户输入额外路径)。
	roots := []string{
		"~/.openclaw/workspace",
		"~/.claude/skills",
		"~/.cursor/skills",
		"~/.codex/skills",
	}
	found, err := discover.Scan(roots)
	if err != nil {
		return err
	}
	for _, ag := range found {
		agAbs, _ := filepath.Abs(ag.Path)
		if agAbs == abs {
			return nil
		}
	}
	return fmt.Errorf("rootPath %q 不是已识别的机器人部署目录,只能浏览 BotsPage 上展示的机器人", rootPath)
}

// isBinary 简单判定:头 8KB 含 \0 视为二进制。够用,不追求精确。
func isBinary(data []byte) bool {
	probe := data
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	for _, b := range probe {
		if b == 0 {
			return true
		}
	}
	return false
}
