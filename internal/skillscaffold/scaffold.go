package skillscaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type Options struct {
	TemplateRoot string
	Name         string
	Description  string
	WithScripts  bool
	WithRefs     bool
}

// New 在 TemplateRoot/workspace/skills/<name>/ 创建最小可用 skill 模板骨架
// 已存在时返回错误，不覆盖
func New(opts Options) (string, error) {
	if !nameRe.MatchString(opts.Name) {
		return "", fmt.Errorf("skill name must match [a-z][a-z0-9-]*, got %q", opts.Name)
	}
	if opts.TemplateRoot == "" {
		return "", fmt.Errorf("TemplateRoot is required")
	}
	dst := filepath.Join(opts.TemplateRoot, "workspace", "skills", opts.Name)
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("skill dir already exists: %s", dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}

	desc := opts.Description
	if desc == "" {
		desc = fmt.Sprintf("TODO: 一句话描述 %s 能做什么、何时使用、硬约束。此描述会出现在 agent 的 skill 索引里。", opts.Name)
	}
	skillMD := fmt.Sprintf(`---
name: %s
description: %s
---

# %s

> 由 tshoot skill new 生成，请按实际需求补全。

## 执行流程（固定）

1. TODO: 第 1 步
2. TODO: 第 2 步
3. TODO: 第 3 步（输出证据）

## 输入要求

- TODO: 必填字段
- TODO: 可选字段

## 输出要求

- 先结论：是否命中
- 再证据：关键字段/样例/来源
- 最后归因：配置问题 / 数据问题 / 代码问题

## 硬约束

- 仅只读（若涉及数据层）
- 不猜测连接信息；必须来自 routing / config-executor 解析
- 失败时明确阻塞点
`, opts.Name, desc, opts.Name)

	if err := os.WriteFile(filepath.Join(dst, "SKILL.md.tmpl"), []byte(skillMD), 0o644); err != nil {
		return "", err
	}

	if opts.WithScripts {
		if err := os.MkdirAll(filepath.Join(dst, "scripts"), 0o755); err != nil {
			return "", err
		}
		placeholder := fmt.Sprintf("# %s scripts\n\n在此目录放置该 skill 的辅助脚本（.py / .js / .sh 等）。\n"+
			"静态文件会被 tshoot 原样拷贝到生成产物。\n", opts.Name)
		if err := os.WriteFile(filepath.Join(dst, "scripts", "README.md"), []byte(placeholder), 0o644); err != nil {
			return "", err
		}
	}
	if opts.WithRefs {
		if err := os.MkdirAll(filepath.Join(dst, "references"), 0o755); err != nil {
			return "", err
		}
		placeholder := fmt.Sprintf("# %s references\n\n在此目录放置该 skill 使用的参考资料（如 API 笔记、数据字典、示例文本）。\n",
			opts.Name)
		if err := os.WriteFile(filepath.Join(dst, "references", "README.md"), []byte(placeholder), 0o644); err != nil {
			return "", err
		}
	}
	return dst, nil
}
