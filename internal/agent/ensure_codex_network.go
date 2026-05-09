// ensure_codex_network.go —— codex CLI 全局 network_access 配置探测。
//
// 背景:codex 在 sandbox_mode=workspace-write 下默认仍**禁止网络访问**,需要在
// ~/.codex/config.toml 全局加 `[sandbox_workspace_write] network_access = true` 才放行。
// 不放行 → 所有 MCP 服务器(连 Grafana/Nacos/Mongo 等)启动时拿到 ENOTFOUND/EPERM,
// 排障机器人在 codex 平台完全跑不起来,且报错指向 MCP 不指向 sandbox,用户难自己定位。
//
// 我们**不主动改**用户的全局 config.toml(那是用户自己的 file,可能有别的定制 / 多个
// codex agent 共用,改出 bug 一片连带),而是 install 时探测:
//   - 已配 network_access = true → 静默通过
//   - 未配 / =false           → 打 [warn] 给确切的修改指引
//
// 这条信息单独印一段(不跟其它装机日志搅在一起),用户首次装完 codex agent 看到了
// 才不会绕一圈才发现 MCP 全挂。
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CheckCodexNetworkAccess 探测 codex 全局 config.toml 里 sandbox_workspace_write.network_access。
//   - 已显式 true → 返回 nil
//   - 未配 / 显式 false → 返回带修复指引的 error(caller 打 stderr 警告,不阻塞 install)
//
// codexHome 一般是 ~/.codex(InstallTarget.RootDir 拿,省得二次 UserHomeDir)。
//
// 不引第三方 toml 库:扫文本找 `[sandbox_workspace_write]` 段头 + 段内 `network_access = true`
// 即可,误判代价就是多打一条 [warn](用户改下 config 再跑也是同一个文件),不会写脏数据。
func CheckCodexNetworkAccess(codexHome string) error {
	cfgPath := filepath.Join(codexHome, "config.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		// 真读不到(权限问题等)— 先放行,反正下面提示也会让用户去看这个文件
		return nil
	}
	if hasNetworkAccessTrue(string(data)) {
		return nil
	}
	return fmt.Errorf("codex 全局 sandbox 默认禁网,装好的 MCP 在 codex 平台启动时会全部 ENOTFOUND\n%s",
		codexNetworkHint(cfgPath))
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
