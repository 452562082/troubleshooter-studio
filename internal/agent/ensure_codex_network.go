// ensure_codex_network.go —— codex CLI 全局 network_access 配置自动开启。
//
// 背景:codex 在 sandbox_mode=workspace-write 下默认仍**禁止网络访问**,需要在
// ~/.codex/config.toml 全局加 `[sandbox_workspace_write] network_access = true` 才放行。
// 不放行 → 所有 MCP 服务器(连 Grafana/Nacos/Mongo 等)启动时拿到 ENOTFOUND/EPERM,
// 排障机器人在 codex 平台完全跑不起来,且报错指向 MCP 不指向 sandbox,用户难自己定位。
//
// 之前版本只探测 + 打 warn 让用户手抄 toml(怕动用户全局 config 引入 bug)——但每次装机
// 都得抄一遍很烦人。改成**自动 patch**:写入前 backup(.tshoot-bak.<ts>)、3 种缺失场景
// 都覆盖、失败降级到原 warn+hint。数据安全靠 MCP 层 --read-only 等约束,sandbox 这层
// 放网不放写盘 = 排障常态,自动开启 = 用户预期。
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnsureCodexNetworkAccess 保证 codex 全局 config.toml 里
// `[sandbox_workspace_write] network_access = true` 已配置好,缺则自动 patch。
//
// 返回 changed 标志告诉 caller 是否实际改过(用来决定要不要 emit 一条 [ok] 提示):
//   - 已就绪 → changed=false, err=nil(静默通过)
//   - 自动 patch 成功 → changed=true, err=nil(caller emit 一条 [ok])
//   - patch 失败 → changed=false, err=带 hint 的 error(caller 降级到 [warn] + 手抄指引)
//
// codexHome 一般是 ~/.codex(InstallTarget.RootDir 拿,省得二次 UserHomeDir)。
//
// Patch 策略(3 种 toml 状态):
//  1. 文件 / 段都不存在 → 新建文件 / 末尾 append 整段
//  2. 段存在但缺 key → 段定义紧跟着插一行 `network_access = true`
//  3. 段存在且 key 是 false/其它 → 替换那一行为 `network_access = true`
//
// 写之前 backup 原文件到 <path>.tshoot-bak.<timestamp>,改坏可恢复。
func EnsureCodexNetworkAccess(codexHome string) (bool, error) {
	cfgPath := filepath.Join(codexHome, "config.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("读 %s 失败: %w\n%s", cfgPath, err, codexNetworkHint(cfgPath))
	}
	if hasNetworkAccessTrue(string(data)) {
		return false, nil
	}
	newSrc, changed := patchCodexNetworkAccess(string(data))
	if !changed {
		// 理论不可达(hasNetworkAccessTrue=false 必有 patch 改动);兜底防御
		return false, nil
	}
	// 文件已存在才 backup;不存在的话原本没东西要保护
	if len(data) > 0 {
		bak := fmt.Sprintf("%s.tshoot-bak.%s", cfgPath, time.Now().Format("20060102-150405"))
		if err := os.WriteFile(bak, data, 0o644); err != nil {
			// backup 失败不阻塞:用户改坏的概率本就极低,且写新文件本身也有 verify
			fmt.Fprintf(os.Stderr, "[warn] 备份 %s 失败: %v\n", bak, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return false, fmt.Errorf("创建 %s 目录失败: %w\n%s", filepath.Dir(cfgPath), err, codexNetworkHint(cfgPath))
	}
	if err := os.WriteFile(cfgPath, []byte(newSrc), 0o644); err != nil {
		return false, fmt.Errorf("写 %s 失败: %w\n%s", cfgPath, err, codexNetworkHint(cfgPath))
	}
	// verify patch 结果实际能被 parser 识别(防 patchCodexNetworkAccess 边角逻辑漏洞)
	if !hasNetworkAccessTrue(newSrc) {
		return false, fmt.Errorf("自动写完 %s 后 verify 失败(patch 结果 parser 不认),请手动修复\n%s", cfgPath, codexNetworkHint(cfgPath))
	}
	return true, nil
}

// patchCodexNetworkAccess 在原 toml 里加/改 [sandbox_workspace_write].network_access = true。
// 返回新内容和是否实际改过。3 种场景:
//   - 段不存在 → 文件末尾 append 整段
//   - 段在但缺 key → 段定义后插一行
//   - 段在且 key != true → 替换那一行
//
// 不破坏其它行 / 注释 / 段:逐行扫定位段头和 key 行 idx,只动一处。
func patchCodexNetworkAccess(src string) (string, bool) {
	lines := strings.Split(src, "\n")
	sectionLineIdx := -1
	keyLineIdx := -1
	inSection := false
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if strings.HasPrefix(line, "[sandbox_workspace_write]") {
				inSection = true
				sectionLineIdx = i
			} else {
				inSection = false
			}
			continue
		}
		if inSection {
			if k, _, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "network_access" {
				keyLineIdx = i
				break
			}
		}
	}
	switch {
	case sectionLineIdx < 0:
		// 段不存在 → 文件末尾 append 整段,前面留个换行隔开已有内容
		suffix := "[sandbox_workspace_write]\nnetwork_access = true\n"
		if src == "" {
			return suffix, true
		}
		if strings.HasSuffix(src, "\n") {
			return src + "\n" + suffix, true
		}
		return src + "\n\n" + suffix, true
	case keyLineIdx >= 0:
		// 改值(保留原 indent)
		lines[keyLineIdx] = "network_access = true"
		return strings.Join(lines, "\n"), true
	default:
		// 段在但缺 key → 段头后插一行
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:sectionLineIdx+1]...)
		out = append(out, "network_access = true")
		out = append(out, lines[sectionLineIdx+1:]...)
		return strings.Join(out, "\n"), true
	}
}

// hasNetworkAccessTrue 扫 toml 文本找 [sandbox_workspace_write] 段下 network_access = true。
// 简单状态机:进入目标段 → 段内逐行找 `network_access = true`(允许两侧空格、引号布尔不要)。
// 离开目标段(遇到下一个 [section] 或 [[array]])就重置。
func hasNetworkAccessTrue(toml string) bool {
	inSection := false
	for _, raw := range strings.Split(toml, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			// 段头:[name] 或 [[name]];都重置 inSection 状态
			inSection = strings.HasPrefix(line, "[sandbox_workspace_write]")
			continue
		}
		if !inSection {
			continue
		}
		// 段内键值:`network_access = true`(忽略大小写、空格、行尾 `# 注释`)
		if k, v, ok := strings.Cut(line, "="); ok {
			if strings.TrimSpace(k) == "network_access" {
				// 截掉行尾注释:TOML 不支持引号字符串里 # 当注释,这里 value 只接 bool 不会有
				// 引号场景,简单按第一个 # 切就够。
				if hash := strings.Index(v, "#"); hash >= 0 {
					v = v[:hash]
				}
				if strings.TrimSpace(v) == "true" {
					return true
				}
			}
		}
	}
	return false
}

// codexNetworkHint 给用户的修复指引 — 直接 copy-paste 进 ~/.codex/config.toml 就完事。
func codexNetworkHint(cfgPath string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "请在 %s 里加这两行(已有 [sandbox_workspace_write] 段就只加 network_access 那行):\n", cfgPath)
	sb.WriteString("\n  [sandbox_workspace_write]\n")
	sb.WriteString("  network_access = true\n\n")
	sb.WriteString("加完后重启 codex CLI,本系统 MCP 才能连业务侧服务(Grafana/Nacos/Mongo/...)。\n")
	sb.WriteString("(数据安全靠 MCP 层 --read-only 等约束,sandbox 这层放网不放写盘 = 排障常态。)")
	return sb.String()
}
