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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

// FileNode 文件树节点,递归结构。前端 v-for 展开成多级缩进。
type FileNode struct {
	Name     string     `json:"name"` // 文件 / 目录名(不含路径)
	Path     string     `json:"path"` // 相对 rootPath 的路径,前端读 / 写时回传给后端
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
	if layout, ok := loadIDEWorkspaceLayout(abs); ok {
		return listIDEWorkspace(layout)
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
			// 递归失败不中断整体(权限问题 / 软链断裂),只记空目录
			_ = walkWorkspaceDir(nodeAbs, nodeRel, &node)
			if len(node.Children) == 0 {
				continue
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
	abs, err := resolveBotWorkspaceFile(rootPath, relPath)
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
	abs, err := resolveBotWorkspaceFile(rootPath, relPath)
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
		return fmt.Errorf("file %q 由 generator 管理,UI 编辑会在下次部署时被覆盖,请通过修改 troubleshooter.yaml + 重新部署来更新", base)
	}
	return os.WriteFile(abs, []byte(content), info.Mode().Perm())
}

type ideWorkspaceLayout struct {
	RootPath     string
	PlatformRoot string
	SkillsRoot   string
	Target       string
	SystemID     string
	AgentExt     string
	AgentIDs     []string
}

func loadIDEWorkspaceLayout(rootPath string) (ideWorkspaceLayout, bool) {
	meta, ok := readWorkspaceMeta(rootPath)
	if !ok {
		return ideWorkspaceLayout{}, false
	}
	target := strings.TrimSpace(meta.Target)
	if target != string(agent.TargetClaudeCode) && target != string(agent.TargetCursor) && target != string(agent.TargetCodex) {
		return ideWorkspaceLayout{}, false
	}
	rootAbs, err := filepath.Abs(rootPath)
	if err != nil {
		return ideWorkspaceLayout{}, false
	}
	skillsRoot := filepath.Dir(rootAbs)
	if filepath.Base(skillsRoot) != "skills" {
		return ideWorkspaceLayout{}, false
	}
	t, err := agent.ParseIDETarget(target)
	if err != nil {
		return ideWorkspaceLayout{}, false
	}
	ids := internalAgentIDs(meta, filepath.Base(rootAbs))
	return ideWorkspaceLayout{
		RootPath:     rootAbs,
		PlatformRoot: filepath.Dir(skillsRoot),
		SkillsRoot:   skillsRoot,
		Target:       target,
		SystemID:     strings.TrimSpace(meta.SystemID),
		AgentExt:     t.UserAgentExt(),
		AgentIDs:     ids,
	}, true
}

func readWorkspaceMeta(rootPath string) (discover.Meta, bool) {
	var meta discover.Meta
	data, err := os.ReadFile(filepath.Join(rootPath, discover.MetaFilename))
	if err != nil {
		return meta, false
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, false
	}
	discover.NormalizeMetaForPath(&meta, filepath.Join(rootPath, discover.MetaFilename))
	return meta, true
}

func internalAgentIDs(meta discover.Meta, fallback string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	add(meta.AgentID)
	for _, ag := range meta.InternalAgents {
		add(ag.ID)
	}
	add(fallback)
	sort.Strings(out)
	return out
}

func listIDEWorkspace(layout ideWorkspaceLayout) (*FileNode, error) {
	name := layout.SystemID
	if name == "" {
		name = filepath.Base(layout.RootPath)
	}
	root := &FileNode{Name: name, Path: "", IsDir: true}
	if metaNode, err := fileNodeForPath(filepath.Join(layout.RootPath, discover.MetaFilename), discover.MetaFilename); err == nil {
		root.Children = append(root.Children, metaNode)
	}

	agentsNode := FileNode{Name: "agents", Path: "agents", IsDir: true}
	for _, id := range layout.AgentIDs {
		abs := filepath.Join(layout.PlatformRoot, "agents", id+layout.AgentExt)
		if node, err := fileNodeForPath(abs, filepath.ToSlash(filepath.Join("agents", id+layout.AgentExt))); err == nil {
			agentsNode.Children = append(agentsNode.Children, node)
		}
	}
	root.Children = append(root.Children, agentsNode)

	skillsNode := FileNode{Name: "skills", Path: "skills", IsDir: true}
	for _, id := range layout.AgentIDs {
		abs := filepath.Join(layout.SkillsRoot, id)
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			node := FileNode{Name: id, Path: filepath.ToSlash(filepath.Join("skills", id)), IsDir: true}
			_ = walkWorkspaceDir(abs, filepath.Join("skills", id), &node)
			skillsNode.Children = append(skillsNode.Children, node)
		}
	}
	root.Children = append(root.Children, skillsNode)

	scriptsNode := FileNode{Name: "scripts", Path: "scripts", IsDir: true}
	for _, id := range layout.AgentIDs {
		abs := filepath.Join(layout.PlatformRoot, "scripts", id)
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			node := FileNode{Name: id, Path: filepath.ToSlash(filepath.Join("scripts", id)), IsDir: true}
			_ = walkWorkspaceDir(abs, filepath.Join("scripts", id), &node)
			scriptsNode.Children = append(scriptsNode.Children, node)
		}
	}
	root.Children = append(root.Children, scriptsNode)
	return root, nil
}

func fileNodeForPath(abs, rel string) (FileNode, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return FileNode{}, err
	}
	node := FileNode{
		Name:  filepath.Base(abs),
		Path:  filepath.ToSlash(rel),
		IsDir: info.IsDir(),
	}
	if !info.IsDir() {
		node.Size = info.Size()
	}
	return node, nil
}

func resolveBotWorkspaceFile(rootPath, relPath string) (string, error) {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return "", err
	}
	if layout, ok := loadIDEWorkspaceLayout(absRoot); ok {
		if resolved, ok, err := resolveIDEWorkspaceFile(layout, relPath); ok || err != nil {
			return resolved, err
		}
	}
	return safeResolveInsideRoot(rootPath, relPath)
}

func resolveIDEWorkspaceFile(layout ideWorkspaceLayout, relPath string) (string, bool, error) {
	rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relPath)))
	if rel == "." || rel == "" {
		return "", true, errors.New("path is a directory, not a file")
	}
	if rel == discover.MetaFilename {
		abs, err := safeResolveInsideRoot(layout.RootPath, discover.MetaFilename)
		return abs, true, err
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		return "", false, nil
	}
	switch parts[0] {
	case "agents":
		if len(parts) != 2 {
			return "", true, errors.New("agents 目录只允许访问当前机器人声明的 agent 文件")
		}
		id := strings.TrimSuffix(parts[1], layout.AgentExt)
		if parts[1] != id+layout.AgentExt || !containsString(layout.AgentIDs, id) {
			return "", true, fmt.Errorf("agent %q 不属于当前机器人", parts[1])
		}
		abs, err := safeResolveInsideRoot(filepath.Join(layout.PlatformRoot, "agents"), parts[1])
		return abs, true, err
	case "skills":
		id := parts[1]
		if !containsString(layout.AgentIDs, id) {
			return "", true, fmt.Errorf("skills/%s 不属于当前机器人", id)
		}
		abs, err := safeResolveInsideRoot(filepath.Join(layout.SkillsRoot, id), strings.Join(parts[2:], "/"))
		return abs, true, err
	case "scripts":
		id := parts[1]
		if !containsString(layout.AgentIDs, id) {
			return "", true, fmt.Errorf("scripts/%s 不属于当前机器人", id)
		}
		abs, err := safeResolveInsideRoot(filepath.Join(layout.PlatformRoot, "scripts", id), strings.Join(parts[2:], "/"))
		return abs, true, err
	default:
		return "", false, nil
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// safeResolveInsideRoot 把 relPath 拼到 rootPath 下并展开成绝对路径,
// 校验结果仍在 rootPath 内部(防 ".." 穿越 + symlink 越界)。relPath 接受空字符串(=指 root 本身)。
//
// 关键防御:
//  1. filepath.Clean + Rel 校验避免 ".." 文本路径穿越(老逻辑已防)
//  2. filepath.EvalSymlinks 跟踪 symlink 真实目标,再二次校验仍在 rootPath 真实路径下 ——
//     否则若 workspace 下有 `link → ~/.ssh/id_rsa`,旧逻辑(只 filepath.Abs)
//     校验 link 名仍在 root 下就放行,os.ReadFile 跟链接读到根外敏感文件。
//
// rootPath 自己可能本身就是 symlink(用户用 `~/.openclaw/workspace/<bot>` 装的 bot 通过
// /private 链接拿到),所以先 EvalSymlinks(root)拿到真实根,再比对 EvalSymlinks(abs)。
// 文件还没创建时(WriteBotWorkspaceFile 写新文件)EvalSymlinks 会报 not-exist —— 那就退化到
// EvalSymlinks(filepath.Dir(abs)) + Base 拼出"父目录解析后 + 文件名"做校验,父目录里有 symlink
// 也会被解掉。
func safeResolveInsideRoot(rootPath, relPath string) (string, error) {
	rootAbs, err := filepath.Abs(rootPath)
	if err != nil {
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		// rootPath 不存在等异常 —— 让上层统一报错(BotsPage 已 assertBotWorkspacePath 兜过一道)
		return "", fmt.Errorf("eval symlinks (root): %w", err)
	}
	rel := filepath.Clean(filepath.FromSlash(relPath))
	if rel == "." {
		rel = ""
	}
	abs := filepath.Join(rootReal, rel)
	// EvalSymlinks 对不存在的 path 报错 —— 写新文件场景退化:解父目录 + 拼 base
	resolved, evalErr := filepath.EvalSymlinks(abs)
	if evalErr != nil {
		if !os.IsNotExist(evalErr) {
			return "", fmt.Errorf("eval symlinks: %w", evalErr)
		}
		parent, base := filepath.Split(abs)
		parentReal, perr := filepath.EvalSymlinks(parent)
		if perr != nil {
			// 父目录都不存在 —— 不允许深度自动创建,让 caller 失败
			return "", fmt.Errorf("eval symlinks (parent): %w", perr)
		}
		resolved = filepath.Join(parentReal, base)
	}
	rel2, err := filepath.Rel(rootReal, resolved)
	if err != nil {
		return "", err
	}
	if rel2 == ".." || strings.HasPrefix(rel2, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace root (symlink or ..)")
	}
	return resolved, nil
}

// assertBotWorkspacePath 校验 rootPath 是 discover.Scan 出来的某个机器人部署根。
// 不直接信任前端传值 —— 防止 binding 被当成"读任意目录"的通用 file API 用。
//
// discover.Scan 的结果走 5 分钟 TTL cache —— BotsPage 浏览文件树时用户每点一个文件都会
// 触发 Read/List/Write binding,每次都跑一遍全盘 4-root 扫描会有明显 IO 卡顿。bot 在 5 分钟
// 内被装 / 卸的概率极低,缓存窗口安全;cache miss 或 TTL 过期才重 Scan。
func assertBotWorkspacePath(rootPath string) error {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return errors.New("rootPath required")
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return err
	}
	paths, err := cachedDiscoverBotPaths()
	if err != nil {
		return err
	}
	for _, p := range paths {
		if p == abs {
			return nil
		}
	}
	return fmt.Errorf("rootPath %q 不是已识别的机器人部署目录,只能浏览 BotsPage 上展示的机器人", rootPath)
}

// botPathsCache 给 assertBotWorkspacePath 的 5 分钟 TTL 缓存。Wails 单进程,sync.Mutex 够。
type botPathsCacheT struct {
	mu       sync.Mutex
	paths    []string
	loadedAt time.Time
}

var botPathsCache botPathsCacheT

const botPathsCacheTTL = 5 * time.Minute

// cachedDiscoverBotPaths 拿当前 4 个根下所有 bot 的绝对路径列表。
// TTL 内复用上次结果;TTL 过期 / 首次调用时跑 discover.Scan 重新扫。
// 失败时(权限 / 磁盘异常)直接 propagate err,不更新 cache。
func cachedDiscoverBotPaths() ([]string, error) {
	botPathsCache.mu.Lock()
	defer botPathsCache.mu.Unlock()
	if time.Since(botPathsCache.loadedAt) < botPathsCacheTTL && botPathsCache.paths != nil {
		return botPathsCache.paths, nil
	}
	roots := []string{
		"~/.openclaw/workspace",
		"~/.claude/skills",
		"~/.cursor/skills",
		"~/.codex/skills",
	}
	found, err := discover.Scan(roots)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(found))
	for _, ag := range found {
		agAbs, _ := filepath.Abs(ag.Path)
		out = append(out, agAbs)
	}
	botPathsCache.paths = out
	botPathsCache.loadedAt = time.Now()
	return out, nil
}

// invalidateBotPathsCache 强制下次 assertBotWorkspacePath 重 Scan。
// 部署 / 卸载完后调一次,用户立即看到新机器人(否则要等 TTL 过期才显)。
func invalidateBotPathsCache() {
	botPathsCache.mu.Lock()
	botPathsCache.paths = nil
	botPathsCache.loadedAt = time.Time{}
	botPathsCache.mu.Unlock()
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
